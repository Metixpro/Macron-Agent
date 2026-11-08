package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/netclient"
	"github.com/Metixpro/macron-agent/pkg/textutil"
	"golang.org/x/text/unicode/norm"
)

// Spotify streams are encrypted, so a Spotify link is read for its facts
// (title, artists, album, exact length, cover) and the same recording is
// found on YouTube or SoundCloud: candidates are scored by how well their
// title and artist match and must be within a few seconds of Spotify's
// length. The file then gets Spotify's cover and tags.

const (
	spotifyMaxDelta  = 8.0 // seconds a match may differ from Spotify's length
	spotifyMaxTracks = 5   // album or playlist songs per call
	spotifyTries     = 4   // candidates downloaded before giving up
	spotifyCoverFile = ".spotify_cover.jpg"
	spotifyTagged    = ".spotify_tagged.mp3"
	spotifyUA        = "TelegramBot (like TwitterBot)"
)

var (
	spotifyBase   = "https://open.spotify.com"
	spotifyPathRe = regexp.MustCompile(`^/(?:(?i:intl-[a-z-]+)/)?(track|album|playlist|artist|episode|show)/([A-Za-z0-9]{22})`)
	spotifyLinkRe = regexp.MustCompile(`open\.spotify\.com/(?:(?i:intl-[a-z-]+)/)?(track|album|playlist)/([A-Za-z0-9]{22})`)
	spotifyURIRe  = regexp.MustCompile(`^spotify:(track|album|playlist):([A-Za-z0-9]{22})$`)
	nextDataRe    = regexp.MustCompile(`(?s)<script id="__NEXT_DATA__" type="application/json">(.*?)</script>`)
	metaTagRe     = regexp.MustCompile(`<meta (?:property|name)="([^"]+)" content="([^"]*)"`)
	featRe        = regexp.MustCompile(`(?i)\s*[\(\[](feat\.?|ft\.?|with) [^\)\]]*[\)\]]|\s+-\s+(feat\.?|ft\.?) .*$`)
)

func spotifyHost(host string) bool {
	switch host {
	case "open.spotify.com", "play.spotify.com", "spotify.link", "spotify.app.link":
		return true
	}
	return false
}

type spotifyTrack struct {
	ID, Title, Album, Year, Cover string
	Artists                       []string
	Number                        int
	Duration                      float64 // seconds
}

type spotifyEntity struct {
	Name        string                  `json:"name"`
	Title       string                  `json:"title"`
	Subtitle    string                  `json:"subtitle"`
	Duration    float64                 `json:"duration"` // milliseconds
	Artists     []struct{ Name string } `json:"artists"`
	ReleaseDate *struct {
		ISO string `json:"isoString"`
	} `json:"releaseDate"`
	TrackList []struct {
		URI      string  `json:"uri"`
		Title    string  `json:"title"`
		Subtitle string  `json:"subtitle"`
		Duration float64 `json:"duration"`
	} `json:"trackList"`
	VisualIdentity struct {
		Image []struct {
			URL      string `json:"url"`
			MaxWidth int    `json:"maxWidth"`
		} `json:"image"`
	} `json:"visualIdentity"`
}

func (e *spotifyEntity) name() string {
	if e.Name != "" {
		return e.Name
	}
	return e.Title
}

// cover is the largest picture of the item.
func (e *spotifyEntity) cover() string {
	best, width := "", 0
	for _, im := range e.VisualIdentity.Image {
		if im.MaxWidth > width {
			best, width = im.URL, im.MaxWidth
		}
	}
	return best
}

func splitArtists(s string) []string {
	var out []string
	for _, a := range strings.Split(strings.ReplaceAll(s, " ", " "), ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func (r *Registry) spotifyGet(ctx context.Context, rawURL string, limit int64) (string, *url.URL, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", spotifyUA) // link previews get the full facts
	req.Header.Set("Accept-Language", "en")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("Spotify answered HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return string(body), resp.Request.URL, err
}

// spotifyTarget reads the kind and id of a link, opening short links.
func (r *Registry) spotifyTarget(ctx context.Context, u *url.URL) (kind, id string, err error) {
	if m := spotifyPathRe.FindStringSubmatch(u.Path); m != nil {
		return m[1], m[2], nil
	}
	if strings.HasSuffix(u.Hostname(), "spotify.com") {
		return "", "", errors.New("this Spotify link is not a song, album or playlist")
	}
	body, final, err := r.spotifyGet(ctx, u.String(), 1<<20)
	if err != nil {
		return "", "", fmt.Errorf("the short link could not be opened: %w", err)
	}
	if m := spotifyPathRe.FindStringSubmatch(final.Path); m != nil && final.Hostname() == "open.spotify.com" {
		return m[1], m[2], nil
	}
	if m := spotifyLinkRe.FindStringSubmatch(body); m != nil {
		return m[1], m[2], nil
	}
	return "", "", errors.New("the short link does not lead to a Spotify song, album or playlist")
}

// spotifyEntityOf reads the data of an item from its embed page.
func (r *Registry) spotifyEntityOf(ctx context.Context, kind, id string) (*spotifyEntity, error) {
	page, _, err := r.spotifyGet(ctx, spotifyBase+"/embed/"+kind+"/"+id, 4<<20)
	if err != nil {
		return nil, err
	}
	m := nextDataRe.FindStringSubmatch(page)
	if m == nil {
		return nil, errors.New("Spotify's page has no data for this " + kind)
	}
	var doc struct {
		Props struct {
			PageProps struct {
				State struct {
					Data struct {
						Entity spotifyEntity `json:"entity"`
					} `json:"data"`
				} `json:"state"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal([]byte(m[1]), &doc); err != nil {
		return nil, fmt.Errorf("Spotify's page could not be read: %w", err)
	}
	e := doc.Props.PageProps.State.Data.Entity
	if e.name() == "" {
		return nil, errors.New("this " + kind + " was not found on Spotify")
	}
	return &e, nil
}

// spotifyTrackOf gathers a song's facts: the embed page has the title,
// artists, length and cover; the song page adds the album and track number.
func (r *Registry) spotifyTrackOf(ctx context.Context, id string) (spotifyTrack, error) {
	e, err := r.spotifyEntityOf(ctx, "track", id)
	if err != nil {
		return spotifyTrack{}, err
	}
	t := spotifyTrack{ID: id, Title: e.name(), Duration: e.Duration / 1000, Cover: e.cover()}
	for _, a := range e.Artists {
		if a.Name != "" {
			t.Artists = append(t.Artists, a.Name)
		}
	}
	if len(t.Artists) == 0 {
		t.Artists = splitArtists(e.Subtitle)
	}
	if e.ReleaseDate != nil && len(e.ReleaseDate.ISO) >= 4 {
		t.Year = e.ReleaseDate.ISO[:4]
	}
	if page, _, err := r.spotifyGet(ctx, spotifyBase+"/track/"+id, 3<<20); err == nil {
		meta := map[string]string{}
		for _, m := range metaTagRe.FindAllStringSubmatch(page, -1) {
			if _, ok := meta[m[1]]; !ok {
				meta[m[1]] = html.UnescapeString(m[2])
			}
		}
		// "Ed Sheeran · ÷ (Deluxe) · Song · 2017"
		parts := strings.Split(meta["og:description"], " · ")
		for i := 2; i < len(parts); i++ {
			if parts[i] == "Song" {
				t.Album = parts[i-1]
				if t.Year == "" && i+1 < len(parts) {
					t.Year = parts[i+1]
				}
				break
			}
		}
		t.Number, _ = strconv.Atoi(meta["music:album:track"])
	}
	if len(t.Artists) == 0 {
		return t, errors.New("Spotify does not name the artist of this song")
	}
	return t, nil
}

type songCandidate struct {
	url, title, channel, site string
	duration, delta, score    float64
}

// songWords splits a title into folded words without accents.
func songWords(s string) []string {
	var b strings.Builder
	for _, r := range norm.NFD.String(textutil.Fold(s)) {
		switch {
		case unicode.Is(unicode.Mn, r):
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

var fillerWords = map[string]bool{"the": true, "a": true, "and": true, "of": true, "feat": true, "ft": true, "x": true, "with": true}

// versionWords mark another recording of a song; a candidate holding one the
// Spotify title lacks is not the song.
var versionWords = []string{"live", "cover", "remix", "karaoke", "instrumental", "slowed", "sped up", "speed up", "8d", "nightcore",
	"reverb", "acoustic", "mashup", "bootleg", "flip", "extended", "1 hour", "loop", "reaction", "tutorial", "piano version",
	"کاور", "ریمیکس", "بی کلام", "بیکلام", "اجرای زنده", "لایو"}

func otherVersion(got, want []string) bool {
	g, w := " "+strings.Join(got, " ")+" ", " "+strings.Join(want, " ")+" "
	for _, v := range versionWords {
		if strings.Contains(g, " "+v+" ") && !strings.Contains(w, " "+v+" ") {
			return true
		}
	}
	return false
}

func coverage(want []string, have map[string]bool) float64 {
	n, hit := 0, 0
	for _, w := range want {
		if fillerWords[w] {
			continue
		}
		n++
		if have[w] {
			hit++
		}
	}
	if n == 0 {
		return 0
	}
	return float64(hit) / float64(n)
}

// noiseWords say nothing about which recording a title is.
var noiseWords = map[string]bool{"official": true, "video": true, "audio": true, "lyric": true, "lyrics": true, "music": true, "hd": true,
	"hq": true, "4k": true, "visualizer": true, "mv": true, "clip": true, "topic": true, "vevo": true, "remastered": true, "remaster": true,
	"explicit": true, "song": true, "new": true, "آهنگ": true, "جدید": true, "موزیک": true, "ویدیو": true, "رسمی": true, "متن": true}

// precision is the share of a candidate's title words that belong to the
// song (its title and artists); a mashup or another song scores low. Words
// in another script than the song's are translations and not counted.
func precision(title string, song map[string]bool) float64 {
	latin := func(w string) bool { return w[0] < utf8.RuneSelf }
	scripts := map[bool]bool{}
	for w := range song {
		scripts[latin(w)] = true
	}
	n, hit := 0, 0
	for _, w := range songWords(title) {
		if fillerWords[w] || noiseWords[w] || !scripts[latin(w)] || strings.Trim(w, "0123456789") == "" {
			continue
		}
		n++
		if song[w] {
			hit++
		}
	}
	if n == 0 {
		return 1
	}
	return float64(hit) / float64(n)
}

// rate scores how surely a candidate is the Spotify recording; false means
// it is not (another song, or its length is off by more than spotifyMaxDelta).
func (c *songCandidate) rate(t spotifyTrack) bool {
	words := songWords(c.title + " " + c.channel)
	have := wordSet(words)
	titleCov := coverage(songWords(featRe.ReplaceAllString(t.Title, "")), have)
	artistCov := coverage(songWords(t.Artists[0]), have)
	prec := precision(c.title, wordSet(songWords(t.Title+" "+strings.Join(t.Artists, " "))))
	if titleCov < 0.5 || prec < 0.6 || otherVersion(words, songWords(t.Title)) {
		return false
	}
	c.delta = -1
	if c.duration > 0 && t.Duration > 0 {
		if c.delta = math.Abs(c.duration - t.Duration); c.delta > spotifyMaxDelta {
			return false
		}
	}
	c.score = 0.6*titleCov + 0.2*artistCov + 0.2*prec
	if c.delta >= 0 {
		c.score += 0.3 * (1 - c.delta/spotifyMaxDelta)
	}
	ch := strings.ToLower(c.channel)
	switch {
	case strings.HasSuffix(ch, " - topic"):
		c.score += 0.15
	case strings.Contains(ch, "vevo"):
		c.score += 0.1
	case artistCov == 1 && coverage(songWords(c.channel), wordSet(songWords(t.Artists[0]))) == 1:
		c.score += 0.1
	}
	return true
}

func wordSet(words []string) map[string]bool {
	m := map[string]bool{}
	for _, w := range words {
		m[w] = true
	}
	return m
}

// songCandidates searches YouTube and SoundCloud at once and keeps the
// results that match, best first.
func (r *Registry) songCandidates(ctx context.Context, sessionID string, d *DownloaderInfo, t spotifyTrack) (cands []songCandidate, seen int) {
	title := strings.TrimSpace(featRe.ReplaceAllString(t.Title, ""))
	queries := []string{"ytsearch6:" + t.Artists[0] + " - " + title, "scsearch8:" + t.Artists[0] + " " + title}
	found := make([][]songCandidate, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Go(func() { found[i] = r.searchSongs(ctx, sessionID, d, q) })
	}
	wg.Wait()
	for _, list := range found {
		for _, c := range list {
			seen++
			if c.rate(t) {
				cands = append(cands, c)
			}
		}
	}
	slices.SortStableFunc(cands, func(a, b songCandidate) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		}
		return 0
	})
	return cands, seen
}

func (r *Registry) searchSongs(ctx context.Context, sessionID string, d *DownloaderInfo, query string) []songCandidate {
	site := "YouTube"
	if strings.HasPrefix(query, "scsearch") {
		site = "SoundCloud"
	}
	argv := append(append([]string(nil), d.YtDlp...), "--ignore-config", "--no-colors", "--flat-playlist", "--socket-timeout", "20",
		"--print", "%(url)s\t%(title|)s\t%(duration|0)s\t%(channel,uploader|)s", "--", query)
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: argv, Timeout: 90 * time.Second})
	var out []songCandidate
	for _, line := range strings.Split(res.Stdout, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 4 || !strings.HasPrefix(f[0], "https://") || clean(f[1]) == "" {
			continue
		}
		dur, _ := strconv.ParseFloat(f[2], 64)
		out = append(out, songCandidate{url: f[0], title: clean(f[1]), duration: dur, channel: clean(f[3]), site: site})
	}
	return out
}

// songName is a file name for "Artist - Title" that is safe for yt-dlp's template.
func songName(t spotifyTrack) string {
	var b strings.Builder
	for _, r := range t.Artists[0] + " - " + t.Title {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r), strings.ContainsRune(" -_.,()'&!+", r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	name := strings.Trim(strings.Join(strings.Fields(b.String()), " "), " .-")
	return textutil.TruncateRunes(name, 90, "")
}

// spotifySong finds, downloads and tags one Spotify song.
func (r *Registry) spotifySong(ctx context.Context, sessionID string, d *DownloaderInfo, t spotifyTrack) (dlFile, []string, error) {
	cands, seen := r.songCandidates(ctx, sessionID, d, t)
	if len(cands) == 0 {
		return dlFile{}, nil, fmt.Errorf("none of the %d results on YouTube and SoundCloud is this song within %d s of Spotify's length (%s)",
			seen, int(spotifyMaxDelta), clock(t.Duration))
	}
	var (
		notes    []string
		lastErr  string
		ytRefuse bool
		tried    int
	)
	for _, c := range cands {
		if tried == spotifyTries || ctx.Err() != nil {
			break
		}
		u, err := netclient.ValidateURL(c.url)
		if err != nil || (ytRefuse && c.site == "YouTube") {
			continue
		}
		tried++
		res := r.fetch(ctx, sessionID, dlRequest{target: u.String(), audio: true, base: songName(t), tries: 2}, d, strings.ToLower(u.Hostname()))
		notes = append(notes, res.notes...)
		if len(res.files) == 0 {
			lastErr = res.err
			ytRefuse = ytRefuse || (c.site == "YouTube" && youTubeBlocked(res.err))
			continue
		}
		f := res.files[0]
		for _, extra := range res.files[1:] {
			_ = r.Machine.Remove(sessionID, extra.name)
		}
		f.title, f.uploader, f.duration = t.Title, strings.Join(t.Artists, ", "), t.Duration
		var about []string
		if t.Album != "" {
			about = append(about, "album «"+t.Album+"»")
		}
		if t.Year != "" {
			about = append(about, t.Year)
		}
		match := fmt.Sprintf("matched on %s: «%s» by %s", c.site, c.title, c.channel)
		if c.delta >= 0 {
			match += fmt.Sprintf(", %.0f s from Spotify's length", c.delta)
		}
		about = append(about, match)
		var cover bool
		if f, cover = r.tagSong(ctx, sessionID, f, t); cover {
			about = append(about, "Spotify's cover and tags added")
		}
		f.about = " — " + strings.Join(about, "; ")
		return f, notes, nil
	}
	reason := "no match could be downloaded"
	switch {
	case ytRefuse:
		reason = "YouTube refused this server and no other match could be downloaded"
	case lastErr != "":
		reason += " (" + tail(lastLine(lastErr), 200) + ")"
	}
	return dlFile{}, notes, errors.New(reason)
}

// tagSong writes Spotify's tags and cover into an MP3 (the stream is copied).
func (r *Registry) tagSong(ctx context.Context, sessionID string, f dlFile, t spotifyTrack) (dlFile, bool) {
	if !strings.EqualFold(path.Ext(f.name), ".mp3") {
		return f, false
	}
	cover := r.saveCover(ctx, sessionID, t.Cover)
	defer r.Machine.Remove(sessionID, spotifyCoverFile)
	argv := []string{"ffmpeg", "-y", "-v", "error", "-i", "./" + f.name}
	if cover {
		argv = append(argv, "-i", "./"+spotifyCoverFile, "-map", "0:a", "-map", "1:0")
	} else {
		argv = append(argv, "-map", "0:a")
	}
	argv = append(argv, "-c", "copy", "-id3v2_version", "3", "-map_metadata", "-1",
		"-metadata", "title="+t.Title, "-metadata", "artist="+strings.Join(t.Artists, ", "), "-metadata", "album_artist="+t.Artists[0])
	if t.Album != "" {
		argv = append(argv, "-metadata", "album="+t.Album)
	}
	if t.Year != "" {
		argv = append(argv, "-metadata", "date="+t.Year)
	}
	if t.Number > 0 {
		argv = append(argv, "-metadata", "track="+strconv.Itoa(t.Number))
	}
	if cover {
		argv = append(argv, "-metadata:s:v", "title=Album cover", "-metadata:s:v", "comment=Cover (front)", "-disposition:v", "attached_pic")
	}
	argv = append(argv, "./"+spotifyTagged)
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: argv, Timeout: time.Minute})
	info, err := r.Machine.Stat(sessionID, spotifyTagged)
	if res.ExitCode != 0 || err != nil || info.Size() == 0 || r.Machine.Rename(sessionID, spotifyTagged, f.name) != nil {
		_ = r.Machine.Remove(sessionID, spotifyTagged)
		return f, false
	}
	f.size = info.Size()
	return f, cover
}

// saveCover downloads Spotify's cover next to the song.
func (r *Registry) saveCover(ctx context.Context, sessionID, rawURL string) bool {
	u, err := netclient.ValidateURL(rawURL)
	if rawURL == "" || err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", browserUA)
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	return err == nil && len(data) > 0 && r.Machine.WriteFile(sessionID, spotifyCoverFile, data) == nil
}

// expandItems lists the numbers of a range list ("1-3,5" → 1 2 3 5).
func expandItems(s string) []int {
	var out []int
	for _, part := range strings.Split(s, ",") {
		a, b, ok := strings.Cut(part, "-")
		x, _ := strconv.Atoi(a)
		y := x
		if ok {
			y, _ = strconv.Atoi(b)
		}
		for i := x; i <= y && len(out) < dlMaxItems; i++ {
			if !slices.Contains(out, i) {
				out = append(out, i)
			}
		}
	}
	return out
}

// spotify downloads a Spotify song, or chosen songs of an album or playlist.
func (r *Registry) spotify(ctx context.Context, sessionID string, u *url.URL, q dlRequest, d *DownloaderInfo) string {
	kind, id, err := r.spotifyTarget(ctx, u)
	if err != nil {
		return "error: " + err.Error()
	}
	var ids []string
	switch kind {
	case "track":
		ids = []string{id}
	case "album", "playlist":
		e, err := r.spotifyEntityOf(ctx, kind, id)
		if err != nil {
			return "error: " + err.Error()
		}
		if q.items == "" {
			return spotifyList(kind, e)
		}
		picked := expandItems(q.items)
		if len(picked) > spotifyMaxTracks {
			return fmt.Sprintf("error: at most %d songs per call; split items into several calls", spotifyMaxTracks)
		}
		for _, n := range picked {
			if n >= 1 && n <= len(e.TrackList) {
				if tid, ok := strings.CutPrefix(e.TrackList[n-1].URI, "spotify:track:"); ok {
					ids = append(ids, tid)
				}
			}
		}
		if len(ids) == 0 {
			return fmt.Sprintf("error: the %s has %d songs; items must point at them", kind, len(e.TrackList))
		}
	default:
		return "error: Spotify " + kind + " links cannot be downloaded (only songs, albums and playlists). Ask the user for a song link or its name."
	}

	var res dlResult
	for _, tid := range ids {
		if ctx.Err() != nil {
			res.notes = append(res.notes, "time ran out before every song was done; ask for the rest in another call")
			break
		}
		t, err := r.spotifyTrackOf(ctx, tid)
		if err != nil {
			res.notes = append(res.notes, "song "+tid+": "+err.Error())
			continue
		}
		f, notes, err := r.spotifySong(ctx, sessionID, d, t)
		res.notes = append(res.notes, notes...)
		if err != nil {
			res.notes = append(res.notes, fmt.Sprintf("«%s» by %s: %s", t.Title, t.Artists[0], err))
			continue
		}
		res.files = append(res.files, f)
	}
	if len(res.files) == 0 {
		return "error: nothing could be downloaded: " + strings.Join(res.notes, "; ") +
			". Tell the user simply; you may offer a search (ytsearch1:/scsearch1: with the artist and title)."
	}
	return r.report(ctx, sessionID, dlRequest{audio: true}, d, res)
}

// spotifyList shows the songs of an album or playlist so the model can pick.
func spotifyList(kind string, e *spotifyEntity) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Spotify %s «%s»", kind, e.name())
	if e.Subtitle != "" {
		sb.WriteString(" by " + strings.ReplaceAll(e.Subtitle, " ", " "))
	}
	fmt.Fprintf(&sb, " — %d songs:\n", len(e.TrackList))
	for i, tr := range e.TrackList {
		if i == 50 {
			fmt.Fprintf(&sb, "… and %d more\n", len(e.TrackList)-50)
			break
		}
		fmt.Fprintf(&sb, "%d. %s — %s · %s\n", i+1, tr.Title, strings.Join(splitArtists(tr.Subtitle), ", "), clock(tr.Duration/1000))
	}
	fmt.Fprintf(&sb, "Nothing was downloaded yet. Download the songs the user wants by calling this tool again with the same link and items "+
		"(e.g. items='1-%d'; at most %d songs per call). If the user did not say which, ask.", min(spotifyMaxTracks, max(len(e.TrackList), 1)), spotifyMaxTracks)
	return sb.String()
}
