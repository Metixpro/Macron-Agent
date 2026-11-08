package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"image"
	_ "image/gif"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// search_image finds real photos: Bing Images first (everyday things, cars,
// films, technology, anime, wallpapers), then the main pictures of the
// Wikipedia articles about the query (in its own language: «برج میلاد»
// finds the Persian article), Openverse (openly licensed photos) and
// Wikimedia Commons (places, people, history). A query that is not in
// English is asked again under the English names of those articles, since
// the photo libraries search in English. Pictures are taken best
// resolution first, checked to be real images of a useful size, and saved
// as .jpg or .png (WebP is converted).

const (
	maxImageCount   = 6
	imageMaxBytes   = 15 << 20
	imageMinSide    = 400 // the longer side, in pixels
	imageGoodSide   = 1000
	imageCandidates = 14
	// imageFetchers pictures are downloaded at once, each within imageFetchTimeout.
	imageFetchers     = 4
	imageFetchTimeout = 15 * time.Second
)

var (
	bingImagesURL  = "https://www.bing.com/images/async"
	openverseURL   = "https://api.openverse.org/v1/images/"
	commonsURL     = "https://commons.wikimedia.org/w/api.php"
	wikipediaURL   = func(lang string) string { return "https://" + lang + ".wikipedia.org/w/api.php" }
	nonLatinRe     = regexp.MustCompile(`[\p{Arabic}\p{Cyrillic}\p{Han}\p{Hangul}\p{Hiragana}\p{Katakana}\p{Hebrew}\p{Greek}]`)
	arabicScriptRe = regexp.MustCompile(`\p{Arabic}`)

	bingCardRe = regexp.MustCompile(`\sm="(\{[^"]*\})"`)
	bingSizeRe = regexp.MustCompile(`(\d{2,5})\s*(?:&#215;|×|x)\s*(\d{2,5})`)
)

type imageHit struct {
	url, title, page, source string
	w, h                     int
	score                    int // the query's words it carries
	bonus                    int // a Wikipedia article named as the query is the thing itself
}

func (h imageHit) side() int { return max(h.w, h.h) }

func (r *Registry) searchImage(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	query := strings.TrimSpace(strArg(args, "query"))
	if query == "" {
		return "error: query is required", nil
	}
	count := intArg(args, "count", 1, 1, maxImageCount)
	count, limitNote := take(ctx, kindImages, count)
	if count == 0 {
		return "error: " + limitNote, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	// The engines are asked at once; a result counts only when its title or
	// addresses carry the query's words (a search engine that distrusts the
	// caller answers with anything) and nothing marks it as adult. The more
	// of the words a result carries, the sooner it is tried.
	var hits []imageHit
	seen := map[string]bool{}
	keep := func(results [][]imageHit, terms []string, strict bool) {
		for _, list := range results {
			for _, h := range list {
				if h.url == "" || seen[h.url] || adultHit(h) {
					continue
				}
				if h.score = matchedTerms(h, terms); h.score < (len(terms)+1)/2 || (strict && h.score < len(terms)) {
					continue
				}
				h.score += h.bonus
				seen[h.url] = true
				hits = append(hits, h)
			}
		}
	}
	var english []string
	wiki := func(ctx context.Context, q string) []imageHit {
		h, en := r.wikiImages(ctx, q)
		english = en
		return h
	}
	keep(askImageEngines(ctx, query, r.bingImages, wiki, r.openverseImages, r.commonsImages), queryTerms(query), false)
	// «برج میلاد» again as «Milad Tower», whole: the photo libraries search
	// in English. The name comes from Wikipedia, or else from the model.
	if nonLatinRe.MatchString(query) && len(hits) < count+imageCandidates/2 {
		en := ""
		if len(english) > 0 {
			en = english[0]
		} else {
			en = r.englishQuery(ctx, query)
		}
		if en != "" {
			keep(askImageEngines(ctx, en, r.bingImages, r.openverseImages, r.commonsImages), queryTerms(en), true)
			english = append(english, en)
		}
	}
	if len(hits) == 0 {
		giveBack(ctx, kindImages, count)
		return fmt.Sprintf("no image found for %q; try other words (English often finds more)", query), nil
	}
	// The best match first, then the sharpest, keeping each engine's own order among equals.
	sharp := func(h imageHit) int {
		switch {
		case h.side() >= imageGoodSide || h.side() == 0:
			return 2
		case h.side() >= imageMinSide:
			return 1
		}
		return 0
	}
	hits = slices.DeleteFunc(hits, func(h imageHit) bool { return sharp(h) == 0 })
	slices.SortStableFunc(hits, func(a, b imageHit) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return sharp(b) - sharp(a)
	})
	stem := slug(query, 20)
	if stem == "" && len(english) > 0 {
		stem = slug(english[0], 20)
	}
	if stem == "" {
		stem = "image"
	}
	base := outputName(strArg(args, "filename"), "photo_"+stem, imageExts, ".jpg")

	// A few pictures are downloaded at once; a site that does not answer is
	// not asked again.
	var saved []string
	got := map[[32]byte]bool{} // the same picture under two addresses comes once
	deadHost := map[string]bool{}
	cands := hits[:min(len(hits), imageCandidates+count)]
	for i := 0; i < len(cands) && len(saved) < count && ctx.Err() == nil; {
		n := min(len(cands)-i, max(imageFetchers, count-len(saved)+1))
		batch := cands[i : i+n]
		i += n
		data := make([][]byte, n)
		dead := make([]bool, n)
		var wg sync.WaitGroup
		for j, h := range batch {
			if deadHost[hostOf(h.url)] {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				data[j], dead[j] = r.downloadImage(ctx, h.url)
			}()
		}
		wg.Wait()
		for j, h := range batch {
			if dead[j] {
				deadHost[hostOf(h.url)] = true
			}
			if data[j] == nil || len(saved) == count {
				continue
			}
			sum := sha256.Sum256(data[j])
			if got[sum] {
				continue
			}
			got[sum] = true
			name := base
			if count > 1 {
				name = strings.TrimSuffix(base, path.Ext(base)) + fmt.Sprintf("_%d", len(saved)+1) + path.Ext(base)
			}
			file, w, hgt, size, ok := r.saveImage(ctx, sessionID, data[j], name)
			if !ok {
				continue
			}
			line := fmt.Sprintf("- %s (%d×%d, %s) — %s", file, w, hgt, humanSize(size), h.source)
			if h.title != "" {
				line += ": " + strings.TrimSpace(h.title)
			}
			if h.page != "" {
				line += " — " + h.page
			}
			saved = append(saved, line)
		}
	}
	giveBack(ctx, kindImages, count-len(saved))
	if len(saved) == 0 {
		return fmt.Sprintf("found %d results for %q but none could be downloaded as a usable picture; try other words", len(hits), query), nil
	}
	note := ""
	if len(saved) < count {
		note = fmt.Sprintf(" (%d of the %d asked for)", len(saved), count)
	}
	out := fmt.Sprintf("Images downloaded and queued for delivery%s:\n%s", note, strings.Join(saved, "\n"))
	if limitNote != "" {
		out += "\n" + limitNote
	}
	return out, nil
}

// sameName: an article's title is the query (a disambiguation in
// parentheses aside): «حافظ» and «حافظ (شاعر)».
func sameName(title, query string) bool {
	norm := func(s string) string {
		if i := strings.Index(s, " ("); i > 0 {
			s = s[:i]
		}
		return strings.Join(queryTerms(s), " ")
	}
	t := norm(title)
	return t != "" && t == norm(query)
}

// englishQuery asks the model for an image search query in English (nil
// model or no answer: "").
func (r *Registry) englishQuery(ctx context.Context, query string) string {
	if r.LLM == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := r.LLM.Complete(ctx, llm.Request{MaxTokens: 40, Messages: []llm.Message{{Role: "user", Content: "Write this image search query in English, " +
		"as the words an image search needs (names in their usual English spelling). Reply with the query only.\n\n" + query}}})
	if err != nil || resp == nil {
		return ""
	}
	en := strings.Trim(textutil.OneLine(resp.Message.Text()), " \"'«»`.")
	if en == "" || len([]rune(en)) > 80 || nonLatinRe.MatchString(en) {
		return ""
	}
	return en
}

// askImageEngines asks the engines at once; each gives its own list.
func askImageEngines(ctx context.Context, query string, engines ...func(context.Context, string) []imageHit) [][]imageHit {
	results := make([][]imageHit, len(engines))
	var wg sync.WaitGroup
	for i, engine := range engines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = engine(ctx, query)
		}()
	}
	wg.Wait()
	return results
}

// wikiPage is an article a Wikipedia search found.
type wikiPage struct {
	Index     int    `json:"index"`
	Title     string `json:"title"`
	FullURL   string `json:"fullurl"`
	Thumbnail struct {
		Source string `json:"source"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"thumbnail"`
	LangLinks []struct {
		Title string `json:"title"`
	} `json:"langlinks"`
}

// wikiImages takes the main pictures (≤2048 px) of the Wikipedia articles
// a search for the query finds, in the query's own language (Persian and
// Arabic script: the Persian Wikipedia), with the English titles of those
// articles when the query is not in English.
func (r *Registry) wikiImages(ctx context.Context, query string) (hits []imageHit, english []string) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	lang := "en"
	if arabicScriptRe.MatchString(query) {
		lang = "fa"
	}
	api := wikipediaURL(lang) + "?action=query&format=json&formatversion=2&redirects=1&generator=search&gsrlimit=4" +
		"&prop=pageimages|langlinks|info&piprop=thumbnail&pithumbsize=2048&pilicense=any&lllang=en&inprop=url&gsrsearch=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return nil, nil
	}
	req.Header.Set("User-Agent", wikimediaUA)
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()
	var data struct {
		Query struct {
			Pages []wikiPage `json:"pages"`
		} `json:"query"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&data) != nil {
		return nil, nil
	}
	pages := data.Query.Pages
	slices.SortFunc(pages, func(a, b wikiPage) int { return a.Index - b.Index }) // the search's own order
	for _, p := range pages {
		title := p.Title
		if lang != "en" && len(p.LangLinks) > 0 && p.LangLinks[0].Title != "" {
			english = append(english, p.LangLinks[0].Title)
			title += " (" + p.LangLinks[0].Title + ")"
		}
		low := strings.ToLower(p.Thumbnail.Source)
		if p.Thumbnail.Source == "" || strings.HasSuffix(low, ".svg.png") || strings.Contains(low, "/flag_of") {
			continue // a logo, map or flag drawn as SVG is not a photo
		}
		h := imageHit{url: p.Thumbnail.Source, title: title, page: p.FullURL, source: "Wikipedia", w: p.Thumbnail.Width, h: p.Thumbnail.Height}
		if sameName(p.Title, query) {
			h.bonus = 1
		}
		hits = append(hits, h)
	}
	return hits, english
}

var (
	termSplitRe = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	imageStop   = map[string]bool{"wallpaper": true, "wallpapers": true, "hd": true, "4k": true, "8k": true, "photo": true, "photos": true,
		"image": true, "images": true, "picture": true, "pictures": true, "pic": true, "high": true, "quality": true, "resolution": true,
		"background": true, "the": true, "and": true, "for": true, "with": true, "of": true, "عکس": true, "تصویر": true, "والپیپر": true,
		"پس‌زمینه": true, "باکیفیت": true, "کیفیت": true, "با": true, "از": true, "و": true, "های": true}
	adultWords = []string{"porn", "nude", "naked", "nsfw", "xxx", "sex", "erotic", "hentai", "onlyfans", "masturbat", "fetish", "lingerie", "playboy"}
)

// queryTerms are the words of a query that should show in a matching result.
func queryTerms(q string) []string {
	var terms []string
	for _, w := range termSplitRe.Split(strings.ToLower(q), -1) {
		if w == "" || imageStop[w] || (len([]rune(w)) < 3 && !strings.ContainsAny(w, "0123456789")) {
			continue
		}
		terms = append(terms, w)
	}
	return terms
}

// relevantHit: at least half the terms (one of one or two) in the title or the addresses.
func relevantHit(h imageHit, terms []string) bool {
	return matchedTerms(h, terms) >= (len(terms)+1)/2
}

// matchedTerms counts the terms in a result's title or addresses.
func matchedTerms(h imageHit, terms []string) int {
	if len(terms) == 0 {
		return 0
	}
	hay := strings.ToLower(h.title + " " + h.page + " " + h.url)
	if u, err := url.PathUnescape(hay); err == nil {
		hay = u
	}
	words := map[string]bool{}
	for _, w := range termSplitRe.Split(hay, -1) {
		words[w] = true
	}
	n := 0
	for _, t := range terms {
		if words[t] || (len([]rune(t)) >= 5 && strings.Contains(hay, t)) { // short words and numbers only whole
			n++
		}
	}
	return n
}

func adultHit(h imageHit) bool {
	hay := strings.ToLower(h.title + " " + h.page + " " + h.url)
	for _, w := range adultWords {
		if strings.Contains(hay, w) {
			return true
		}
	}
	return false
}

// downloadImage fetches a picture: nil when it is not one (an error page,
// too small or too big); dead when the site did not answer at all.
func (r *Registry) downloadImage(ctx context.Context, src string) (data []byte, dead bool) {
	ctx, cancel := context.WithTimeout(ctx, imageFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "image/avif,image/webp,image/png,image/jpeg,*/*;q=0.8")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, true
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, imageMaxBytes+1))
	if err != nil || len(data) > imageMaxBytes || len(data) < 2048 {
		return nil, false
	}
	return data, false
}

// saveImage keeps a downloaded picture only when it is a real JPEG, PNG or
// WebP of a useful size, and saves it as .jpg or .png.
func (r *Registry) saveImage(ctx context.Context, sessionID string, data []byte, name string) (string, int, int, int64, bool) {
	stem := strings.TrimSuffix(name, path.Ext(name))
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		name = stem + ".jpg"
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		name = stem + ".png"
	case len(data) > 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return r.webpToJPEG(ctx, sessionID, data, stem+".jpg")
	default:
		return "", 0, 0, 0, false // HTML, SVG, GIF, an error page…
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || max(cfg.Width, cfg.Height) < imageMinSide {
		return "", 0, 0, 0, false
	}
	if int64(len(data)) > r.Machine.FreeSpace() || r.Machine.WriteFile(sessionID, name, data) != nil {
		return "", 0, 0, 0, false
	}
	return name, cfg.Width, cfg.Height, int64(len(data)), true
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return raw
}

// webpToJPEG converts a WebP picture with ffmpeg here.
func (r *Registry) webpToJPEG(ctx context.Context, sessionID string, data []byte, name string) (string, int, int, int64, bool) {
	tmp := scratchDir + "/" + strings.TrimSuffix(path.Base(name), ".jpg") + ".webp"
	if r.Machine.WriteFile(sessionID, tmp, data) != nil {
		return "", 0, 0, 0, false
	}
	defer r.Machine.RemoveAll(sessionID, tmp)
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{
		Args:    []string{"ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", "./" + tmp, "-q:v", "2", "./" + name},
		Timeout: 30 * time.Second,
	})
	if res.ExitCode != 0 {
		return "", 0, 0, 0, false
	}
	out, _, err := r.Machine.ReadFile(sessionID, name, imageMaxBytes)
	if err != nil {
		return "", 0, 0, 0, false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || max(cfg.Width, cfg.Height) < imageMinSide {
		_ = r.Machine.RemoveAll(sessionID, name)
		return "", 0, 0, 0, false
	}
	return name, cfg.Width, cfg.Height, int64(len(out)), true
}

// bingImages reads Bing's image results page (no key needed), safe search on.
func (r *Registry) bingImages(ctx context.Context, query string) []imageHit {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	u := bingImagesURL + "?q=" + url.QueryEscape(query) + "&first=0&count=35&mmasync=1&adlt=strict"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cookie", "SRCHHPGUSR=ADLT=STRICT") // safe search, as the site keeps it
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	page := string(body)
	cards := bingCardRe.FindAllStringSubmatchIndex(page, -1)
	var hits []imageHit
	for i, c := range cards {
		var m struct {
			MURL string `json:"murl"`
			PURL string `json:"purl"`
			T    string `json:"t"`
		}
		if json.Unmarshal([]byte(html.UnescapeString(page[c[2]:c[3]])), &m) != nil || !strings.HasPrefix(m.MURL, "http") {
			continue
		}
		end := len(page)
		if i+1 < len(cards) {
			end = cards[i+1][0]
		}
		h := imageHit{url: m.MURL, title: strings.NewReplacer("\ue000", "", "\ue001", "").Replace(m.T), page: m.PURL, source: "Bing"}
		if s := bingSizeRe.FindStringSubmatch(page[c[1]:end]); s != nil {
			h.w, _ = strconv.Atoi(s[1])
			h.h, _ = strconv.Atoi(s[2])
		}
		hits = append(hits, h)
	}
	return hits
}

// openverseImages asks Openverse (openly licensed photos, no key needed).
func (r *Registry) openverseImages(ctx context.Context, query string) []imageHit {
	var data struct {
		Results []struct {
			URL     string `json:"url"`
			Title   string `json:"title"`
			Landing string `json:"foreign_landing_url"`
			Width   int    `json:"width"`
			Height  int    `json:"height"`
			License string `json:"license"`
		} `json:"results"`
	}
	if r.getJSONWithin(ctx, 12*time.Second, openverseURL+"?page_size=20&mature=false&q="+url.QueryEscape(query), &data) != nil {
		return nil
	}
	var hits []imageHit
	for _, x := range data.Results {
		hits = append(hits, imageHit{url: x.URL, title: x.Title, page: x.Landing, source: "Openverse (CC " + strings.ToUpper(x.License) + ")",
			w: x.Width, h: x.Height})
	}
	return hits
}

// commonsImages searches Wikimedia Commons, taking renditions of at most 2048 px.
func (r *Registry) commonsImages(ctx context.Context, query string) []imageHit {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	api := commonsURL + "?action=query&format=json&generator=search&gsrnamespace=6&gsrlimit=10" +
		"&prop=imageinfo&iiprop=url|size|mime&iiurlwidth=2048&gsrsearch=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", wikimediaUA)
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var data struct {
		Query struct {
			Pages map[string]struct {
				Index     int    `json:"index"`
				Title     string `json:"title"`
				ImageInfo []struct {
					URL         string `json:"url"`
					ThumbURL    string `json:"thumburl"`
					ThumbWidth  int    `json:"thumbwidth"`
					ThumbHeight int    `json:"thumbheight"`
					Mime        string `json:"mime"`
					PageURL     string `json:"descriptionurl"`
				} `json:"imageinfo"`
			} `json:"pages"`
		} `json:"query"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&data) != nil {
		return nil
	}
	ordered := make([]imageHit, 0, len(data.Query.Pages))
	index := map[string]int{}
	for _, p := range data.Query.Pages {
		if len(p.ImageInfo) == 0 {
			continue
		}
		ii := p.ImageInfo[0]
		if ii.Mime != "image/jpeg" && ii.Mime != "image/png" && ii.Mime != "image/webp" {
			continue
		}
		src := ii.ThumbURL
		if src == "" {
			src = ii.URL
		}
		h := imageHit{url: src, title: strings.TrimPrefix(p.Title, "File:"), page: ii.PageURL, source: "Wikimedia Commons", w: ii.ThumbWidth, h: ii.ThumbHeight}
		index[h.url] = p.Index
		ordered = append(ordered, h)
	}
	for i := 1; i < len(ordered); i++ { // the search's own order
		for j := i; j > 0 && index[ordered[j].url] < index[ordered[j-1].url]; j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	return ordered
}
