package tools

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/netclient"
)

// Media downloads from YouTube, Instagram, TikTok, X, Facebook, SoundCloud,
// Aparat, Reddit, Twitch, Vimeo, public Telegram posts and the other sites
// yt-dlp knows, built to end with a file the user can play:
//
//   - formats Telegram plays inline (H.264/AAC MP4; MP3 with its tags and
//     cover), at most 720p by default so the result fits in a message;
//   - when a site pushes back, a ladder of retries (other player clients,
//     any format, IPv4), and gallery-dl for image posts;
//   - a result over Telegram's limit (50 MB, or 2000 MB through a local Bot API server) is split into parts (long videos,
//     or when asked) or re-encoded to fit;
//   - playlists and carousels (up to 10 items) and clips of long videos.

const (
	ToolDownloadMedia = "download_media_ytdlp"

	dlTimeout        = 7 * time.Minute  // the downloads of a tool call
	dlPostTimeout    = 12 * time.Minute // then making the files fit (re-encoding, burning subtitles in): a budget of its own
	dlAttemptTimeout = 4 * time.Minute
	dlMaxItems       = 10
	dlMarker         = "WAZEFILE"
	dlCookiesFile    = ".dl_cookies.txt" // where older versions kept the cookies (cleaned up)
	cookiesName      = "cookies.txt"
)

var (
	dlFitBytes  int64 = 47 << 20 // what an oversized file is squeezed into
	dlSendLimit int64 = 50 << 20 // Telegram's upload limit for bots
)

// DownloaderInfo describes the download tools found on the server.
type DownloaderInfo struct {
	YtDlp     []string // command: a binary, or python3 -m yt_dlp
	Version   string
	JSRuntime string // passed with --js-runtimes when deno is missing
	GalleryDL string
	FFmpeg    bool
	// POTProvider: a PO-token provider (bgutil) answers on 127.0.0.1:4416,
	// which lets YouTube work from servers it would otherwise refuse.
	POTProvider bool
	flags       map[string]bool
}

type downloader struct {
	mu   sync.Mutex
	info *DownloaderInfo
}

func (r *Registry) downloaderInfo() *DownloaderInfo {
	r.dl.mu.Lock()
	defer r.dl.mu.Unlock()
	if r.dl.info == nil {
		r.dl.info = r.detectDownloader()
	}
	return r.dl.info
}

// DownloaderStatus reports the download tools (detected again).
func (r *Registry) DownloaderStatus() DownloaderInfo {
	r.dl.mu.Lock()
	r.dl.info = nil
	r.dl.mu.Unlock()
	return *r.downloaderInfo()
}

func (r *Registry) detectDownloader() *DownloaderInfo {
	d := &DownloaderInfo{flags: map[string]bool{}}
	find := func(name string) string {
		p, err := exec.LookPath(name)
		if err != nil {
			return ""
		}
		return p
	}
	run := func(args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		return string(out)
	}
	if p := find("yt-dlp"); p != "" {
		d.YtDlp = []string{p}
	} else if py := find("python3"); py != "" && run(py, "-c", "import yt_dlp; print('ok')") == "ok\n" {
		d.YtDlp = []string{py, "-m", "yt_dlp"}
	}
	if d.YtDlp != nil {
		d.Version = strings.TrimSpace(firstLine(run(append(d.YtDlp, "--version")...)))
		help := run(append(d.YtDlp, "--help")...)
		for _, f := range []string{"--js-runtimes", "--remote-components"} {
			d.flags[f] = strings.Contains(help, f)
		}
	}
	// Newer yt-dlp needs a JavaScript runtime for YouTube; name it with its
	// path, so it is found here whatever its PATH is.
	if deno := find("deno"); deno != "" {
		d.JSRuntime = "deno:" + deno
	} else if node := find("node"); node != "" {
		d.JSRuntime = "node:" + node
	}
	d.GalleryDL = find("gallery-dl")
	d.FFmpeg = find("ffmpeg") != ""
	if c, err := net.DialTimeout("tcp", "127.0.0.1:4416", 300*time.Millisecond); err == nil {
		c.Close()
		d.POTProvider = true
	}
	return d
}

func (r *Registry) registerDownloadTools() {
	r.add(ToolDownloadMedia,
		"Download a video, or a song as tagged MP3, from YouTube, Instagram, TikTok, X, SoundCloud, Aparat, Telegram posts and "+
			"~1800 sites, or search: 'ytsearch1:<query>', 'scsearch1:<query>'. Spotify: the same recording with its tags; an album or "+
			"playlist lists its songs: pick with items. Always ≤"+limitText()+".",
		map[string]any{
			"url":       prop("string", "A link, or ytsearch1:/scsearch1:<query>"),
			"format":    enumProp("Default: audio for searches and music sites, else video", "video", "audio"),
			"quality":   enumProp("360p, 480p, 720p (default), 1080p", "low", "medium", "high", "best"),
			"section":   prop("string", "Only a part, e.g. '1:30-2:45'"),
			"items":     prop("string", "Playlist or post items, e.g. '1-5' (max 10; Spotify 5)"),
			"filename":  prop("string", ""),
			"split":     prop("boolean", "Over "+limitText()+": parts in full quality instead of re-encoding"),
			"subtitles": enumProp("file: SRT and video; only: SRT; embed: MP4 track; burn: drawn in (the only way Telegram shows them, ≤20 min)", "file", "only", "embed", "burn"),
			"sub_langs": prop("string", "Default 'fa,en'; up to 4"),
		}, []string{"url"}, r.downloadMedia)
	r.registerSplitTool()
}

var (
	sectionRe = regexp.MustCompile(`^\d{1,2}(:\d{1,2}){0,2}(\.\d+)?-\d{1,2}(:\d{1,2}){0,2}(\.\d+)?$`)
	itemsRe   = regexp.MustCompile(`^\d{1,3}(-\d{1,3})?(,\d{1,3}(-\d{1,3})?)*$`)
	searchRe  = regexp.MustCompile(`^(yt|sc)search([1-5])?:\S`)
)

type dlRequest struct {
	target   string
	audio    bool
	height   int
	section  string
	items    string
	base     string
	tries    int  // attempts of the retry ladder (default 3)
	split    bool // send a file over the limit in parts rather than re-encoded
	subs     string
	subLangs string
}

// countItems counts the items of a range list ("1-5,8" → 6).
func countItems(s string) int {
	n := 0
	for _, part := range strings.Split(s, ",") {
		a, b, ok := strings.Cut(part, "-")
		x, _ := strconv.Atoi(a)
		y := x
		if ok {
			y, _ = strconv.Atoi(b)
		}
		if y < x {
			return dlMaxItems + 1
		}
		n += y - x + 1
	}
	return n
}

func isYouTube(host string) bool {
	return host == "youtu.be" || host == "youtube.com" || strings.HasSuffix(host, ".youtube.com")
}

// youTubeBlocked: YouTube served the page but refused the media (no PO token,
// a bot check, a datacenter IP).
func youTubeBlocked(e string) bool {
	return containsAny(e, "403", "po token", "sign in to confirm", "not a bot", "sabr")
}

var titleNoise = regexp.MustCompile(`(?i)[\(\[](official|lyric|lyrics|audio|video|music video|hd|4k|visualizer|live)[^\)\]]*[\)\]]|\|.*$`)

// mediaTitle reads a video's title (and artist) without downloading it.
func (r *Registry) mediaTitle(ctx context.Context, sessionID string, d *DownloaderInfo, target string) string {
	argv := append(append([]string(nil), d.YtDlp...), "--ignore-config", "--no-colors", "--skip-download", "--no-playlist",
		"--socket-timeout", "20", "--print", "%(artist|)s\t%(track,title|)s", "--", target)
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: argv, Timeout: 90 * time.Second})
	artist, title, _ := strings.Cut(firstLine(res.Stdout), "\t")
	title = strings.Join(strings.Fields(titleNoise.ReplaceAllString(title, " ")), " ")
	if artist = strings.TrimSpace(artist); artist != "" && !strings.Contains(strings.ToLower(title), strings.ToLower(artist)) {
		title = artist + " " + title
	}
	if len([]rune(title)) < 2 {
		return ""
	}
	return title
}

func musicSite(host string) bool {
	for _, h := range []string{"music.youtube.com", "soundcloud.com", "bandcamp.com", "deezer.com", "spotify.com", "music.apple.com", "audiomack.com"} {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

func (r *Registry) downloadMedia(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, dlTimeout)
	defer cancel()
	q := dlRequest{target: strArg(args, "url"), height: 720, section: strArg(args, "section"), items: strings.ReplaceAll(strArg(args, "items"), " ", ""),
		split: strArg(args, "split") == "true"}
	if q.target == "" {
		return "error: url is required", nil
	}
	host := ""
	if m := spotifyURIRe.FindStringSubmatch(q.target); m != nil {
		q.target = "https://open.spotify.com/" + m[1] + "/" + m[2]
	}
	// Only real http(s) URLs or search pseudo-URLs; never anything that could be read as an option.
	if searchRe.MatchString(q.target) {
		q.audio = strArg(args, "format") != "video"
	} else {
		u, err := netclient.ValidateURL(q.target)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		q.target, host = u.String(), strings.ToLower(u.Hostname())
		switch strArg(args, "format") {
		case "audio":
			q.audio = true
		case "":
			q.audio = musicSite(host)
		}
	}
	switch strArg(args, "quality") {
	case "low":
		q.height = 360
	case "medium":
		q.height = 480
	case "best":
		q.height = 1080
	}
	if q.section != "" && !sectionRe.MatchString(q.section) {
		return "error: section must look like '1:30-2:45'", nil
	}
	if q.items != "" && (!itemsRe.MatchString(q.items) || countItems(q.items) > dlMaxItems) {
		return fmt.Sprintf("error: items must look like '1-5' or '2,4', at most %d", dlMaxItems), nil
	}
	if f := strArg(args, "filename"); f != "" {
		q.base = strings.TrimSuffix(sanitizeBase(f), path.Ext(f))
	}
	if q.subs = strArg(args, "subtitles"); q.subs != "" {
		if !slices.Contains([]string{"file", "only", "embed", "burn"}, q.subs) {
			return "error: subtitles must be file, only, embed or burn", nil
		}
		if q.subLangs = strings.ReplaceAll(strArg(args, "sub_langs"), " ", ""); q.subLangs == "" {
			q.subLangs = "fa,en"
		}
		if !subLangsRe.MatchString(q.subLangs) {
			return "error: sub_langs must be language codes like 'fa,en'", nil
		}
		if q.subs != "only" {
			q.audio = false
		}
	}

	// A request gets as many files as its plan allows: a playlist's items count one by one.
	want := 1
	if q.items != "" {
		want = countItems(q.items)
	}
	granted, limitNote := take(parent, kindDownloads, want)
	if granted == 0 {
		return "error: " + limitNote, nil
	}
	if granted < want {
		q.items = firstItems(q.items, granted)
	}
	// What did not download gives its share back; the limit's note goes with what did.
	kept := false
	defer func() {
		if !kept {
			giveBack(parent, kindDownloads, granted)
		}
	}()
	withNote := func(s string) string {
		if strings.HasPrefix(s, "error") {
			return s
		}
		kept = true
		if limitNote != "" {
			s += "\n" + limitNote
		}
		return s
	}

	d := r.downloaderInfo()
	if d.YtDlp == nil {
		if d.GalleryDL != "" && host != "" {
			out, err := r.galleryDL(ctx, sessionID, q, d)
			return withNote(out), err
		}
		return "error: yt-dlp is not installed on this server, so media links cannot be downloaded. Tell the user; for a direct file link use download_file.", nil
	}
	if q.section != "" && !d.FFmpeg {
		return "error: cutting a section needs ffmpeg, which is not installed; download the whole file instead", nil
	}
	if (q.subs == "embed" || q.subs == "burn") && !d.FFmpeg {
		return "error: putting subtitles into the video needs ffmpeg, which is not installed; use subtitles='file' instead", nil
	}
	if spotifyHost(host) {
		u, _ := url.Parse(q.target)
		return withNote(r.spotify(ctx, sessionID, u, q, d)), nil
	}

	res := r.fetch(ctx, sessionID, q, d, host)
	if len(res.files) == 0 && q.audio && isYouTube(host) && youTubeBlocked(res.err) && ctx.Err() == nil {
		// YouTube refuses many servers; the same song is usually on SoundCloud.
		if title := r.mediaTitle(ctx, sessionID, d, q.target); title != "" {
			alt := q
			alt.target, alt.tries = "scsearch1:"+title, 1
			if sc := r.fetch(ctx, sessionID, alt, d, ""); len(sc.files) > 0 {
				res.files = sc.files
				res.notes = append(res.notes, "YouTube refused this server, so the song was found by its title on SoundCloud ("+title+"); check it is the right one")
			}
		}
	}
	if len(res.files) == 0 { // nothing came: its share goes back (see kept)
		if q.subs == "only" && !containsAny(res.err, "error:") {
			return "error: this video has no " + q.subLangs + " subtitles, not even automatic ones. Tell the user; other languages can be asked with sub_langs.", nil
		}
		if d.GalleryDL != "" && host != "" && imagePostError(res.err) {
			out, err := r.galleryDL(ctx, sessionID, q, d)
			return withNote(out), err
		}
		return explainDownloadError(res.err, res.cookies), nil
	}
	// The files are here: what follows gets its own time, so a long
	// download never leaves the re-encoding a context that already ran out.
	ctx, cancelPost := context.WithTimeout(parent, dlPostTimeout)
	defer cancelPost()
	if q.subs == "file" || q.subs == "burn" {
		if !slices.ContainsFunc(res.files, func(f dlFile) bool { return isSubtitle(f.name) }) {
			res.notes = append(res.notes, "this video has no "+q.subLangs+" subtitles, so only the video was downloaded")
		} else if q.subs == "burn" {
			var note string
			if res.files, note = r.burnSubs(ctx, sessionID, res.files, q.subLangs); note != "" {
				res.notes = append(res.notes, note)
			}
		}
	}
	return withNote(r.report(ctx, sessionID, q, d, res)), nil
}

// dlResult is what one yt-dlp download produced.
type dlResult struct {
	files   []dlFile
	err     string
	notes   []string
	cookies bool // some cookie fits the site
}

// fetch runs yt-dlp with the ladder of retries, rotating the cookies of the
// site when it blocks one.
func (r *Registry) fetch(ctx context.Context, sessionID string, q dlRequest, d *DownloaderInfo, host string) dlResult {
	before := r.rootSnapshot(sessionID)
	cookies := r.cookieSlot(sessionID, cookieHost(q.target, host))
	defer cookies.clear()
	tries := q.tries
	if tries <= 0 {
		tries = 3
	}
	var out dlResult
	for attempt := 0; attempt < tries && ctx.Err() == nil; {
		actx, acancel := context.WithTimeout(ctx, dlAttemptTimeout)
		argv := d.args(q, attempt, cookies.path())
		// The report of each file is collected on its own: progress lines may
		// fill the captured output, never push the report out of it.
		reports := &lineCollector{prefix: dlMarker + "\t"}
		tee := io.Writer(reports)
		if hasProgress(ctx) {
			argv = append(argv[:len(argv)-2:len(argv)-2], append([]string{"--progress", "--newline"}, argv[len(argv)-2:]...)...)
			tee = io.MultiWriter(reports, newLineReporter(func(line string) {
				if s := ytdlpStatus(line); s != "" {
					ReportStatus(ctx, s)
				}
			}))
		}
		res := r.Machine.Run(actx, sessionID, machine.RunOptions{Args: argv, Timeout: dlAttemptTimeout, Tee: tee, Heavy: true})
		acancel()
		out.files = r.parseDownloads(sessionID, res.Stdout+"\n"+reports.String())
		if q.subs == "only" || (len(out.files) > 0 && q.subs != "" && q.subs != "embed") {
			out.files = append(out.files, r.subtitleFiles(sessionID, before)...)
		}
		if len(out.files) > 0 {
			cookies.good()
			if res.ExitCode != 0 {
				out.notes = append(out.notes, "some items failed: "+tail(lastLine(res.Stderr), 200))
			}
			break
		}
		out.err = strings.TrimSpace(res.Stderr + "\n" + res.Error)
		if cookies.rotate(out.err) {
			continue
		}
		if !retryable(out.err) {
			break
		}
		attempt++
	}
	out.notes = append(out.notes, cookies.notes...)
	out.cookies = cookies.any
	r.dropLeftovers(sessionID, before, out.files)
	return out
}

// dropLeftovers removes what a download left besides its files (thumbnails,
// parts of a failed attempt), so only the result reaches the user.
func (r *Registry) dropLeftovers(sessionID string, before map[string]string, files []dlFile) {
	keep := map[string]bool{}
	for _, f := range files {
		keep[f.name] = true
	}
	for name := range r.rootSnapshot(sessionID) {
		if _, existed := before[name]; !existed && !keep[name] && !strings.HasPrefix(name, ".") {
			_ = r.Machine.Remove(sessionID, name)
		}
	}
}

// report fits the files for Telegram and describes them to the model.
func (r *Registry) report(ctx context.Context, sessionID string, q dlRequest, d *DownloaderInfo, res dlResult) string {
	fitted := make([][]dlFile, len(res.files))
	n := 0
	for i, f := range res.files {
		fitted[i] = r.fitForTelegram(ctx, sessionID, f, d, q.split)
		n += len(fitted[i])
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Downloaded %d file(s); %s:\n", n, SendNotePlural)
	for _, f := range slices.Concat(fitted...) {
		fmt.Fprintf(&sb, "- Downloaded %q (%s)", f.name, humanSize(f.size))
		var meta []string
		if f.title != "" {
			meta = append(meta, "«"+f.title+"»")
		}
		if f.uploader != "" {
			meta = append(meta, "by "+f.uploader)
		}
		if f.duration > 0 {
			meta = append(meta, clock(f.duration))
		}
		if f.height > 0 && !q.audio {
			meta = append(meta, fmt.Sprintf("%dp", f.height))
		}
		if len(meta) > 0 {
			sb.WriteString(" — " + strings.Join(meta, " · "))
		}
		if f.note != "" {
			sb.WriteString(" (" + f.note + ")")
		}
		sb.WriteString(f.about)
		sb.WriteString("\n")
	}
	for _, n := range res.notes {
		sb.WriteString("Note: " + n + "\n")
	}
	return strings.TrimSpace(sb.String())
}

// args builds the yt-dlp command of one attempt; later attempts are more lenient.
func (d *DownloaderInfo) args(q dlRequest, attempt int, cookies string) []string {
	a := append([]string(nil), d.YtDlp...)
	a = append(a, "--ignore-config", "--no-colors", "--no-mtime", "--no-cache-dir",
		"--socket-timeout", "30", "--retries", "5", "--fragment-retries", "10", "--extractor-retries", "3",
		"--concurrent-fragments", strconv.Itoa(min(2, runtime.NumCPU())), "--max-filesize", "300M",
		"--print", "after_move:"+dlMarker+"\t%(filepath)s\t%(title|)s\t%(uploader,channel,creator,artist|)s\t%(duration|0)s\t%(height|0)s")
	name := q.base
	if name == "" {
		name = "%(title).80B"
	}
	number := "%(playlist_autonumber& - {}|)s" // items of a playlist or a carousel
	if searchRe.MatchString(q.target) {
		number = ""
	}
	a = append(a, "-o", "./"+name+number+".%(ext)s")
	if q.items != "" {
		a = append(a, "--yes-playlist", "--playlist-items", q.items)
	} else {
		a = append(a, "--no-playlist", "--playlist-items", "1-"+strconv.Itoa(dlMaxItems))
	}
	switch {
	case q.audio && d.FFmpeg:
		a = append(a, "-f", "ba/b", "-x", "--audio-format", "mp3", "--audio-quality", "192K",
			"--embed-metadata", "--embed-thumbnail", "--convert-thumbnails", "jpg")
	case q.audio:
		a = append(a, "-f", "ba[ext=m4a]/ba/b")
	case !d.FFmpeg:
		a = append(a, "-f", fmt.Sprintf("b[height<=%d][ext=mp4]/b[ext=mp4]/b", q.height))
	case attempt == 0:
		a = append(a, "-f", "bv*+ba/b", "-S", fmt.Sprintf("res:%d,vcodec:h264,acodec:aac,ext:mp4:m4a", q.height),
			"--merge-output-format", "mp4", "--embed-metadata")
	default:
		a = append(a, "-f", "b/bv*+ba", "-S", fmt.Sprintf("res:%d", q.height), "--merge-output-format", "mp4")
	}
	if q.section != "" {
		a = append(a, "--download-sections", "*"+q.section)
	}
	a = append(a, d.subtitleArgs(q)...)
	// Playlists and channels: skip the login check of YouTube's tab pages.
	a = append(a, "--extractor-args", "youtubetab:skip=authcheck")
	switch attempt {
	case 1:
		a = append(a, "--extractor-args", "youtube:player_client=default,tv_simple,web_safari,mweb")
	case 2:
		a = append(a, "--extractor-args", "youtube:player_client=android_vr,ios,web_embedded", "--force-ipv4")
	}
	if d.JSRuntime != "" && d.flags["--js-runtimes"] {
		a = append(a, "--js-runtimes", d.JSRuntime)
	}
	if d.flags["--remote-components"] {
		a = append(a, "--remote-components", "ejs:github")
	}
	if cookies != "" {
		a = append(a, "--cookies", cookies)
	}
	return append(a, "--", q.target)
}

type dlFile struct {
	name, title, uploader string
	duration              float64
	height                int
	size                  int64
	note                  string
	about                 string // more details for the model
}

// parseDownloads reads the files yt-dlp reported (one marker line each).
func (r *Registry) parseDownloads(sessionID, stdout string) []dlFile {
	var out []dlFile
	seen := map[string]bool{}
	for _, line := range strings.Split(stdout, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 6 || f[0] != dlMarker {
			continue
		}
		name := path.Base(f[1]) // every file is written to the workspace root
		if seen[name] {
			continue
		}
		info, err := r.Machine.Stat(sessionID, name)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		seen[name] = true
		dur, _ := strconv.ParseFloat(f[4], 64)
		h, _ := strconv.Atoi(f[5])
		out = append(out, dlFile{name: name, title: clean(f[2]), uploader: clean(f[3]), duration: dur, height: h, size: info.Size()})
	}
	return out
}

func clean(s string) string {
	if s == "NA" {
		return ""
	}
	return strings.TrimSpace(s)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func clock(seconds float64) string {
	s := int(seconds + 0.5)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func containsAny(s string, words ...string) bool {
	l := strings.ToLower(s)
	for _, w := range words {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// retryable: the site pushed back or the network hiccuped, not "this does not exist".
func retryable(e string) bool {
	if geoBlocked(e) {
		return false
	}
	if containsAny(e, "sign in to confirm", "not a bot") {
		return true
	}
	return !containsAny(e, "unsupported url", "private video", "video unavailable", "has been removed", "does not exist", "copyright",
		"no video formats found", "is not a valid url", "larger than max-filesize", "drm protected", "no subtitles")
}

func imagePostError(e string) bool {
	return containsAny(e, "unsupported url", "no video formats found", "no video could be found", "there's no video", "there is no video")
}

// ytdlpBoilerRe is yt-dlp's request to report a bug, which only takes room.
var ytdlpBoilerRe = regexp.MustCompile(`(?is);?\s*please report this issue on\s.*$`)

// explainDownloadError turns yt-dlp's error into advice for the model.
func explainDownloadError(e string, haveCookies bool) string {
	short := tail(strings.TrimSpace(ytdlpBoilerRe.ReplaceAllString(lastLine(e), "")), 300)
	switch {
	case geoBlocked(e):
		return "Download failed: " + short + ". The video is not available in the server's country; no account or retry changes that. " +
			"Look for another upload of it (ytsearch1:<title>) or another source, and tell the user if there is none."
	case e == "":
		return "error: the download produced no file"
	case containsAny(e, "sign in to confirm", "not a bot", "login required", "log in", "cookies", "rate-limit", "rate limit", "429"):
		msg := "error: the site asks for a login or limits downloads (" + short + ")."
		if !haveCookies {
			msg += " The bot's admin can add cookies of a logged-in account in the admin panel (Settings → Downloader)."
		}
		msg += " The user can also add their own cookies.txt of the site with /cookies (exported from a browser where they are logged in); it is tried first for their downloads."
		return msg + " Tell the user simply; you may try a search (ytsearch1:<title>) or another link."
	case containsAny(e, "403", "po token", "sabr"):
		return "error: YouTube refused to send the media to this server (HTTP 403 / PO token). Tell the user simply. For a song, try " +
			"scsearch1:<artist and title>. The bot's admin can fix YouTube for good with a PO-token provider or cookies (see the README)."
	case containsAny(e, "unsupported url"):
		return "error: this site is not supported for media downloads. If it is a direct link to a file, use download_file."
	case containsAny(e, "private video", "private account", "video unavailable", "has been removed", "does not exist", "copyright"):
		return "error: the media is private, removed or unavailable (" + short + ")."
	case containsAny(e, "larger than max-filesize"):
		return "error: the media is very large. Ask for a section (e.g. the first minutes), lower quality, or audio only."
	case containsAny(e, "ssl", "unexpected_eof", "connection reset", "connection refused", "network is unreachable",
		"name or service not known", "temporary failure in name resolution", "no route to host", "remote end closed"):
		return "error: the server could not reach the site (" + short + "). It is the connection, not the link: try once more " +
			"in a little while, or another source (scsearch1:<artist and title> for a song)."
	case containsAny(e, "timed out", "deadline"):
		return "error: the download took too long. Try lower quality, audio only, or a section."
	}
	return "error: the download failed: " + short
}

// fitForTelegram makes a file over Telegram's limit sendable. Long videos
// (and any file when the user asked for parts) are split with the streams
// copied; shorter ones are re-encoded to fit. Each way falls back on the other.
func (r *Registry) fitForTelegram(ctx context.Context, sessionID string, f dlFile, d *DownloaderInfo, split bool) []dlFile {
	if f.size <= dlSendLimit || !d.FFmpeg {
		if f.size > dlSendLimit {
			f.note = "too big for Telegram (" + limitText() + ") and ffmpeg is missing: ask for a section, lower quality or audio"
		}
		return []dlFile{f}
	}
	if f.duration <= 0 {
		f.duration = r.mediaLength(ctx, sessionID, f.name)
	}
	if f.duration <= 0 {
		f.note = "larger than " + limitText() + " and its length is unknown: ask for a section or lower quality"
		return []dlFile{f}
	}
	cut := func() []dlFile {
		parts, err := r.splitMedia(ctx, sessionID, f, dlFitBytes, 0)
		if err != nil {
			return nil
		}
		_ = r.Machine.Remove(sessionID, f.name)
		for i := range parts {
			parts[i].note = "split from " + humanSize(f.size) + " without re-encoding"
		}
		return parts
	}
	if split || f.duration >= splitLongVideo {
		if parts := cut(); parts != nil {
			return parts
		}
	}
	c, ok := r.compress(ctx, sessionID, f)
	if !ok && !split && f.duration < splitLongVideo {
		if parts := cut(); parts != nil {
			return parts
		}
	}
	return []dlFile{c}
}

// compress re-encodes a file to fit Telegram.
func (r *Registry) compress(ctx context.Context, sessionID string, f dlFile) (dlFile, bool) {
	dur := f.duration
	totalKbps := int(float64(dlFitBytes) * 8 / dur / 1000)
	ext := strings.ToLower(path.Ext(f.name))
	stem := strings.TrimSuffix(f.name, path.Ext(f.name))
	audio := ext == ".mp3" || ext == ".m4a" || ext == ".opus" || ext == ".ogg"
	var argv []string
	out := stem + " (compressed)" + ext
	switch {
	case audio:
		kbps := min(max(totalKbps-8, 32), 160)
		if totalKbps < 40 {
			f.note = fmt.Sprintf("%s long, too long to fit %s even as low-quality audio: ask for a section", clock(dur), limitText())
			return f, false
		}
		out = stem + " (compressed).mp3"
		argv = []string{"ffmpeg", "-y", "-v", "error", "-i", "./" + f.name, "-map", "0:a", "-c:a", "libmp3lame", "-b:a", strconv.Itoa(kbps) + "k",
			"-map_metadata", "0", "./" + out}
	default:
		videoKbps := totalKbps - 96
		if videoKbps < 150 {
			f.note = fmt.Sprintf("%s long, too long to fit %s as video: ask for a section, or offer audio only", clock(dur), limitText())
			return f, false
		}
		height := 720
		switch {
		case videoKbps < 400:
			height = 360
		case videoKbps < 900:
			height = 480
		}
		out = stem + " (compressed).mp4"
		argv = []string{"ffmpeg", "-y", "-v", "error", "-i", "./" + f.name, "-vf", fmt.Sprintf("scale=-2:'min(%d,ih)'", height),
			"-c:v", "libx264", "-preset", "veryfast", "-b:v", strconv.Itoa(videoKbps) + "k", "-maxrate", strconv.Itoa(videoKbps*5/4) + "k",
			"-bufsize", strconv.Itoa(videoKbps*2) + "k", "-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", "./" + out}
	}
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: argv, Timeout: compressTime(f.size), Heavy: true})
	info, err := r.Machine.Stat(sessionID, out)
	if res.ExitCode != 0 || err != nil || info.Size() == 0 || info.Size() > dlSendLimit {
		_ = r.Machine.Remove(sessionID, out)
		f.note = "larger than " + limitText() + " and could not be compressed enough: ask for a section, lower quality or audio"
		return f, false
	}
	_ = r.Machine.Remove(sessionID, f.name)
	f.note = fmt.Sprintf("re-encoded from %s to fit Telegram", humanSize(f.size))
	f.name, f.size = out, info.Size()
	return f, true
}

// galleryDL downloads the photos (and videos) of a post with gallery-dl.
func (r *Registry) galleryDL(ctx context.Context, sessionID string, q dlRequest, d *DownloaderInfo) (string, error) {
	items := q.items
	if items == "" {
		items = "1-" + strconv.Itoa(dlMaxItems)
	}
	before := r.rootSnapshot(sessionID)
	host := ""
	if u, err := netclient.ValidateURL(q.target); err == nil {
		host = u.Hostname()
	}
	cookies := r.cookieSlot(sessionID, cookieHost(q.target, host))
	defer cookies.clear()
	var (
		files []string
		res   *machine.ExecutionResult
	)
	for try := 0; try < 4 && ctx.Err() == nil; try++ {
		argv := []string{d.GalleryDL, "--range", items, "-D", ".", "-f", "{filename}.{extension}", "--no-mtime"}
		if p := cookies.path(); p != "" {
			argv = append(argv, "--cookies", p)
		}
		argv = append(argv, "--", q.target)
		res = r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: argv, Timeout: dlAttemptTimeout, Heavy: true})
		if files = r.changedFiles(sessionID, before); len(files) > 0 {
			cookies.good()
			break
		}
		if e := res.Stderr + "\n" + res.Error; !cookies.rotate(e) && !cookies.withoutCookies(e) {
			break
		}
	}
	if len(files) == 0 {
		return explainDownloadError(strings.TrimSpace(res.Stderr+"\n"+res.Error), cookies.any), nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Downloaded %d file(s) from the post; %s:\n", len(files), SendNotePlural)
	for _, name := range files {
		if info, err := r.Machine.Stat(sessionID, name); err == nil {
			fmt.Fprintf(&sb, "- Downloaded %q (%s)\n", name, humanSize(info.Size()))
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

// cookieSlot is the cookie a download uses, written to a private folder
// outside the workspace (the user's code and files never reach it) while it
// runs, and replaced by the next one when the site blocks it.
//
// The order follows tg-ytdlp-bot: the user's own cookies first, then the
// shared pool (least recently used first; a blocked one rests), then none
// (a bad cookie can cause the login wall itself). One user's downloads go
// through at most cookieRotationsPerHour cookies an hour.
type cookieSlot struct {
	r        *Registry
	session  string
	host     string
	user     []Cookie // the user's own cookies for the site
	cookie   Cookie
	refused  int // 403s met: another account is tried once
	have     bool
	any      bool // some cookie fits this site (used or resting)
	dropped  bool // the cookies failed: the last try goes without any
	tried    map[string]bool
	rotation int
	notes    []string
	secret   *secretDir
}

const maxCookieRotations = 4

func (r *Registry) cookieSlot(sessionID, host string) *cookieSlot {
	s := &cookieSlot{r: r, session: sessionID, host: host, tried: map[string]bool{}}
	if r.userCookies != nil && host != "" {
		now := time.Now()
		for _, c := range r.userCookies(machine.ParentSession(sessionID)) { // a sub-agent downloads for its user
			if CookieFits(c, host) && !CookieExpired(c, host, now) {
				s.user = append(s.user, c)
			}
		}
	}
	s.next()
	s.any = s.have
	return s
}

// next takes the next ready cookie for the site and writes it out.
func (s *cookieSlot) next() bool {
	s.have = false
	if s.host == "" {
		return false
	}
	var (
		c  Cookie
		ok bool
	)
	for _, u := range s.user {
		if !s.tried[u.ID] {
			c, ok = u, true
			break
		}
	}
	if !ok && s.r.cookiePool != nil {
		c, ok = s.r.cookiePool.Pick(s.host, s.tried)
	}
	if !ok {
		s.drop()
		return false
	}
	if s.secret == nil {
		sd, err := newSecretDir()
		if err != nil {
			return false
		}
		s.secret = sd
	}
	if err := s.secret.write(cookiesName, []byte(c.Data)); err != nil {
		return false
	}
	s.cookie, s.have = c, true
	return true
}

func (s *cookieSlot) active() bool { return s.have }

// path is the cookie file as the command sees it ("" without a cookie).
func (s *cookieSlot) path() string {
	if !s.have || s.secret == nil {
		return ""
	}
	return s.secret.path(s.r.Machine, cookiesName)
}

// drop stops using a cookie: the file is gone before the next command runs.
func (s *cookieSlot) drop() {
	s.have = false
	if s.secret != nil {
		s.secret.remove(cookiesName)
	}
}

func (s *cookieSlot) mine() bool { return strings.HasPrefix(s.cookie.ID, UserCookiePrefix) }

func (s *cookieSlot) good() {
	if s.have && !s.mine() && s.r.cookiePool != nil {
		s.r.cookiePool.Good(s.cookie.ID)
	}
}

// rotate reacts to a failure: a blocked account rests and the next one is
// tried; a private item is tried with the next account. It reports whether
// the download should be retried at once.
func (s *cookieSlot) rotate(e string) bool {
	if !s.have || geoBlocked(e) {
		return false
	}
	blocked, private := cookieBlocked(e), privateForAccount(e)
	if !blocked && !private && forbidden(e) && s.refused == 0 {
		s.refused++
		private = true // another account, once; a second 403 is not the cookie's
	}
	if !blocked && !private {
		return false
	}
	s.tried[s.cookie.ID] = true
	switch {
	case blocked && s.mine():
		s.notes = append(s.notes, "the user's own cookie for this site was refused (logged out or blocked); they can replace it with /cookies")
	case blocked:
		rest := cookieRest()
		s.r.cookiePool.Rest(s.cookie.ID, rest, tail(lastLine(e), 200))
		s.notes = append(s.notes, fmt.Sprintf("cookie %q was blocked and rests for %d minutes", s.cookie.Label, int(rest.Minutes())))
	}
	if s.rotation >= maxCookieRotations || !s.r.rotations.allow(machine.ParentSession(s.session), time.Now()) {
		s.drop()
		return false
	}
	s.rotation++
	return s.next()
}

// withoutCookies is gallery-dl's last try after the cookies hit a login
// wall: none at all (yt-dlp's ladder goes on without them by itself).
func (s *cookieSlot) withoutCookies(e string) bool {
	if s.dropped || !s.any || !cookieBlocked(e) {
		return false
	}
	s.dropped = true
	s.drop()
	s.notes = append(s.notes, "tried once more without cookies")
	return true
}

func (s *cookieSlot) clear() {
	s.drop()
	if s.secret != nil {
		s.secret.close()
	}
	_ = s.r.Machine.Remove(s.session, dlCookiesFile) // left by an older version
}
