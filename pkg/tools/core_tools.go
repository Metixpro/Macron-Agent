package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/netclient"
	"github.com/Metixpro/macron-agent/pkg/search"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

var maxDownloadBytes int64 = 100 << 20

const (
	maxWriteBytes  = 2 << 20
	maxAppendBytes = 20 << 20
	maxReadBytes   = 32 << 10
)

func (r *Registry) registerCoreTools() {
	r.add("web_search",
		"Search the web on several engines: results with the top pages' matching passages, a quick answer; news: dated, newest first.",
		map[string]any{
			"query":    prop("string", ""),
			"type":     enumProp("", "web", "news"),
			"time":     enumProp("", "day", "week", "month", "year"),
			"limit":    prop("integer", "max 10"),
			"read_top": prop("integer", fmt.Sprintf("pages read (default %d)", search.PassagePages)),
		}, []string{"query"}, r.webSearch)

	r.add("web_fetch",
		"A web page's readable text, and the media and download links on it.",
		map[string]any{
			"url":       prop("string", ""),
			"max_chars": prop("integer", "Default 8000, max 14000"),
		}, []string{"url"}, r.webFetch)

	r.add("search_music",
		"Direct MP3 links of a song from Persian music sites (then download_file).",
		map[string]any{"query": prop("string", "Artist and title")},
		[]string{"query"}, r.searchMusic)

	r.add("download_file",
		fmt.Sprintf("Download a file from a direct link into the workspace (≤%d MB).", maxDownloadBytes>>20),
		map[string]any{
			"url":      prop("string", ""),
			"filename": prop("string", "Default: from the link"),
		}, []string{"url"}, oneDownload(r.downloadFile))

	{
		r.add("bash_execute",
			"Run a bash script on this machine, as its user (the work folder is the current directory, also written /workspace): the full machine: any program, any file, any network, services, installs.",
			map[string]any{
				"command":         prop("string", ""),
				"timeout_seconds": prop("integer", "Default 60, max 7200"),
				"background":      prop("boolean", "Keep it running (a dev server, a database, a watcher): later commands and run_code reach it on localhost; see "+ToolShellJobs),
			}, []string{"command"}, r.bashExecute)
		r.add(ToolShellJobs,
			"The background jobs of bash_execute: list them, read a job's latest output, or stop it.",
			map[string]any{
				"action": enumProp("Default list", "list", "output", "kill"),
				"job":    prop("integer", ""),
				"lines":  prop("integer", "Output lines (default 80, max 500)"),
			}, nil, r.shellJobs)
	}

	r.add("write_file",
		"Write a text file in the workspace; a long one in parts (append=true after the first).",
		map[string]any{
			"filename": prop("string", "e.g. 'report.md'"),
			"content":  prop("string", "≤2 MB"),
			"append":   prop("boolean", ""),
		}, []string{"filename", "content"}, r.writeFile)

	r.add("read_file",
		"Read a workspace text file: its start, or numbered lines start_line..end_line (≤2000; a long read ends early and says where to go on).",
		map[string]any{
			"filename":   prop("string", ""),
			"start_line": prop("integer", ""),
			"end_line":   prop("integer", ""),
			"files":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "More files at once"},
		},
		[]string{"filename"}, r.readFile)

	r.add("list_files", "List files and directories in the workspace.", nil, nil, r.listFiles)

	r.add("deep_research",
		"Research a complex topic from many sources into a cited report (saved too). Slow.",
		map[string]any{
			"topic":   prop("string", ""),
			"breadth": prop("integer", "Search angles (default 3, max 5)"),
		}, []string{"topic"}, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			topic := strArg(args, "topic")
			if topic == "" {
				return "error: topic is required", nil
			}
			return r.RunDeepResearch(ctx, sessionID, topic, intArg(args, "breadth", 3, 1, 5))
		})
}

func (r *Registry) webSearch(ctx context.Context, _ string, args map[string]any) (string, error) {
	q := strArg(args, "query")
	if q == "" {
		return "error: query is required", nil
	}
	opt := search.SearchOptions{Limit: intArg(args, "limit", 5, 1, 10), Time: strArg(args, "time"), News: strArg(args, "type") == "news", Answer: true}
	rep, err := r.Search.SearchWith(ctx, q, opt)
	if rep == nil || (err != nil && len(rep.Results) == 0) {
		if err == nil {
			err = errors.New("no engine answered")
		}
		return fmt.Sprintf("search error: %v. Try other words, or again in a moment.", err), nil
	}
	if len(rep.Results) < 2 && strings.ContainsAny(q, `"«»`) {
		if loose, _ := r.Search.SearchWith(ctx, strings.NewReplacer(`"`, "", "«", "", "»", "").Replace(q), opt); loose != nil && len(loose.Results) > len(rep.Results) {
			rep = loose
		}
	}
	res := rep.Results
	if len(res) == 0 && rep.Answer == nil {
		hint := "Try different or fewer keywords"
		if opt.Time != "" {
			hint += ", or a longer time range"
		}
		return "No results found. " + hint + ".", nil
	}
	r.Search.AddPassages(ctx, q, res, intArg(args, "read_top", search.PassagePages, 0, 5))
	return formatSearch(rep, time.Now()), nil
}

func formatSearch(rep *search.SearchReport, now time.Time) string {
	var sb strings.Builder
	if a := rep.Answer; a != nil {
		source := a.Source
		if a.Title != "" {
			source += " — " + a.Title
		}
		fmt.Fprintf(&sb, "Quick answer (%s): %s\n", strings.TrimPrefix(source, " — "), a.Text)
		if len(a.Facts) > 0 {
			sb.WriteString("   " + strings.Join(a.Facts, " · ") + "\n")
		}
		if a.URL != "" {
			sb.WriteString("   " + a.URL + "\n")
		}
		sb.WriteString("\n")
	}
	for i, x := range rep.Results {
		meta := x.Source
		if meta == "" {
			meta = siteName(x.URL)
		}
		if t, err := time.Parse(time.RFC3339, x.Date); err == nil {
			meta += " · " + t.UTC().Format("2006-01-02 15:04 UTC") + " (" + ageText(now.Sub(t)) + ")"
		}
		fmt.Fprintf(&sb, "%d. %s — %s\n   %s\n", i+1, x.Title, meta, x.URL)
		if x.Snippet != "" {
			sb.WriteString("   " + textutil.TruncateRunes(x.Snippet, 300, "…") + "\n")
		}
		if len(x.Passages) > 0 {
			sb.WriteString("   Key passages from the page:\n")
			for _, p := range x.Passages {
				sb.WriteString("   › " + p + "\n")
			}
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func siteName(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return strings.TrimPrefix(u.Hostname(), "www.")
	}
	return raw
}

func ageText(d time.Duration) string {
	switch {
	case d < 0:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", max(1, int(d.Minutes())))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

func (r *Registry) webFetch(ctx context.Context, _ string, args map[string]any) (string, error) {
	target := strArg(args, "url")
	if target == "" {
		return "error: url is required", nil
	}
	text, err := r.Search.FetchPage(ctx, target, intArg(args, "max_chars", 8000, 500, readShowRunes))
	if err != nil {
		return fmt.Sprintf("fetch error: %v", err), nil
	}
	return text, nil
}

func (r *Registry) searchMusic(ctx context.Context, _ string, args map[string]any) (string, error) {
	q := strArg(args, "query")
	if q == "" {
		return "error: query is required", nil
	}
	list, err := r.Search.SearchPersianMusic(ctx, q)
	if err != nil {
		return fmt.Sprintf("music search error: %v", err), nil
	}
	if len(list) == 0 {
		return fmt.Sprintf("No direct MP3 found on the Persian music sites for %q. %s with 'ytsearch1:<artist> <title>' "+
			"(or 'scsearch1:…') usually finds and sends the song.", q, ToolDownloadMedia), nil
	}
	return jsonText(list), nil
}

func (r *Registry) downloadFile(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	rawURL := strArg(args, "url")
	if rawURL == "" {
		return "error: url is required", nil
	}
	u, err := netclient.ValidateURL(rawURL)
	if err != nil {
		return "error: " + err.Error(), nil
	}

	free := r.Machine.FreeSpace()
	limit := min(maxDownloadBytes, free)
	if limit <= 0 {
		return "error: the disk is full", nil
	}

	tooBig := func(size int64) string {
		file := "the file"
		if size > 0 {
			file = fmt.Sprintf("the file (%s)", humanSize(size))
		}
		if free < maxDownloadBytes {
			return fmt.Sprintf("error: %s does not fit in the %s left in the user's storage; they can free space with /files", file, humanSize(free))
		}
		return fmt.Sprintf("error: %s is over the %s download limit", file, humanSize(maxDownloadBytes))
	}

	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	req, err := newGet(ctx, u.String())
	if err != nil {
		return fmt.Sprintf("create request failed: %v", err), nil
	}
	req.Header.Set("Range", "bytes=0-")
	resp, err := r.HTTP.Do(req)
	if err == nil && resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		ctx = asDownloadTool(ctx)
		if req, err = newGet(ctx, u.String()); err == nil {
			req.Header.Set("Range", "bytes=0-")
			resp, err = r.HTTP.Do(req)
		}
	}
	if err == nil && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		resp.Body.Close()
		req.Header.Del("Range")
		resp, err = r.HTTP.Do(req)
	}
	if err != nil {
		return fmt.Sprintf("download failed: %v", err), nil
	}
	defer resp.Body.Close()
	total, ranged := resp.ContentLength, false
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusPartialContent:
		start, end, size, ok := contentRange(resp.Header.Get("Content-Range"))
		if !ok || start != 0 {
			return "download failed: the server answered with a broken byte range", nil
		}
		total, ranged = size, size >= dlParallelMin || end < size-1
	default:
		return fmt.Sprintf("download failed with HTTP status: %d", resp.StatusCode), nil
	}
	if total > limit {
		return tooBig(total), nil
	}

	name := sanitizeBase(strArg(args, "filename"))
	if name == "" {
		name = nameFromResponse(resp)
	}
	if name == "" {
		name = fmt.Sprintf("download_%d", time.Now().UnixMilli())
	}

	f, err := r.Machine.Create(sessionID, name)
	if err != nil {
		return fmt.Sprintf("create local file error: %v", err), nil
	}
	var (
		n     int64
		conns int
	)
	ctx, stopTracking := trackDownload(ctx, func() int64 { return total })
	defer stopTracking()
	if ranged {
		if conns, err = r.parallelGet(ctx, f, u.String(), resp, total); err == nil {
			n = total
		} else if errors.Is(err, errNoRanges) && ctx.Err() == nil {
			n, conns, err = r.streamAgain(ctx, f, u.String(), limit)
		}
	} else {
		n, err = copyLimited(counted(ctx, f), resp.Body, limit)
	}
	stopTracking()
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = r.Machine.Remove(sessionID, name)
		if errors.Is(err, ErrTooLarge) {
			return tooBig(0) + " — download aborted", nil
		}
		return fmt.Sprintf("download failed: %v", err), nil
	}

	msg := fmt.Sprintf("Downloaded %q (%s) into the workspace.", name, humanSize(n))
	if conns > 1 {
		msg = fmt.Sprintf("Downloaded %q (%s) into the workspace over %d parallel connections.", name, humanSize(n), conns)
	}
	if ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); ct == "text/html" && !strings.HasSuffix(strings.ToLower(name), ".html") {
		msg += " WARNING: the server returned an HTML page, not the expected file (the link may need a login or have expired)."
	}
	return msg, nil
}

func (r *Registry) streamAgain(ctx context.Context, f *os.File, rawURL string, limit int64) (int64, int, error) {
	if c := counterOf(ctx); c != nil {
		c.Store(0)
	}
	if err := f.Truncate(0); err != nil {
		return 0, 1, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, 1, err
	}
	req, err := newGet(ctx, rawURL)
	if err != nil {
		return 0, 1, err
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return 0, 1, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 1, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	n, err := copyLimited(counted(ctx, f), resp.Body, limit)
	return n, 1, err
}

func nameFromResponse(resp *http.Response) string {
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if n := sanitizeBase(params["filename"]); n != "" {
				return n
			}
		}
	}
	if resp.Request != nil && resp.Request.URL != nil {
		return sanitizeBase(path.Base(resp.Request.URL.Path))
	}
	return ""
}

func (r *Registry) bashExecute(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	cmd := rawStrArg(args, "command")
	if strings.TrimSpace(cmd) == "" {
		return "error: command is required", nil
	}
	if refused := r.gateCommand(ctx, sessionID, cmd); refused != "" {
		return refused, nil
	}
	if boolArg(args, "background") {
		return r.startBackground(ctx, sessionID, cmd), nil
	}
	timeout := time.Duration(intArg(args, "timeout_seconds", 60, 1, maxBashSeconds)) * time.Second
	before := r.rootSnapshot(sessionID)
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Shell: cmd, Timeout: timeout, Tee: outputTee(ctx), InSession: true})
	return jsonText(struct {
		*machine.ExecutionResult
		NewFiles []string `json:"new_or_changed_files,omitempty"`
	}{res, r.changedFiles(sessionID, before)}), nil
}

func (r *Registry) writeFile(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	name := strArg(args, "filename")
	if name == "" {
		return "error: filename is required", nil
	}
	content := textArg(args, "content")
	if len(content) > maxWriteBytes {
		return fmt.Sprintf("error: content is %s, larger than the 2 MB limit", humanSize(int64(len(content)))), nil
	}
	if refused := r.gatePath(ctx, sessionID, "write_file", name); refused != "" {
		return refused, nil
	}
	data := []byte(content)
	if boolArg(args, "append") {
		old, truncated, err := r.Machine.ReadFile(sessionID, name, maxAppendBytes)
		switch {
		case truncated:
			return fmt.Sprintf("error: %q is over %s; start a new file", name, humanSize(maxAppendBytes)), nil
		case err == nil:
			data = append(old, data...)
		case !errors.Is(err, fs.ErrNotExist):
			return "write error: " + friendlyFSError(err), nil
		}
	}
	if err := r.Machine.WriteFile(sessionID, name, data); err != nil {
		return "write error: " + friendlyFSError(err), nil
	}
	note := r.syntaxNote(ctx, sessionID, name)
	if strings.HasPrefix(note, "\n⚠") {
		note += "\n(fine if more parts of the file are still coming)"
	}
	if len(data) > len(content) {
		return fmt.Sprintf("Added %s to %q (now %s)", humanSize(int64(len(content))), name, humanSize(int64(len(data)))) + note, nil
	}
	return fmt.Sprintf("File %q saved (%s)", name, humanSize(int64(len(content)))) + note, nil
}

const readManyMax = 20

func (r *Registry) readFile(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	name := strArg(args, "filename")
	if list, ok := args["files"].([]any); ok && len(list) > 0 {
		return r.readMany(ctx, sessionID, name, list), nil
	}
	if name == "" {
		return "error: filename is required", nil
	}
	if refused := r.gateRead(ctx, sessionID, "read_file", name); refused != "" {
		return refused, nil
	}
	if from, to := intArg(args, "start_line", 0, 0, 1<<30), intArg(args, "end_line", 0, 0, 1<<30); from > 0 || to > 0 {
		return r.readLines(sessionID, name, max(from, 1), to), nil
	}
	data, truncated, err := r.Machine.ReadFile(sessionID, name, maxReadBytes)
	if err != nil {
		return "read error: " + friendlyFSError(err), nil
	}
	if hint := binaryHint(name, data); hint != "" {
		return hint, nil
	}
	text, cut := fitText(strings.ToValidUTF8(string(data), "�"), readShowRunes)
	if truncated || cut {

		text = strings.TrimRight(text, "\n") + fmt.Sprintf("\n... [truncated: lines 1-%d shown — read on with start_line=%d, or find things with search_code]",
			max(strings.Count(text, "\n"), 1), strings.Count(text, "\n")+1)
	}
	return text, nil
}

func binaryHint(name string, data []byte) string {
	head := data[:min(len(data), 8192)]
	invalid := len(head) - len(bytes.ToValidUTF8(head, nil))
	if !bytes.Contains(head, []byte{0}) && invalid*20 <= len(head) && !looksBinary(head) {
		return ""
	}
	ext := strings.ToLower(path.Ext(name))
	kind := http.DetectContentType(head)
	use := "bash_execute (file, xxd | head, or a script) to look inside it"
	switch {
	case ext == ".pdf":
		use = "pdf_tool action=read, or " + ToolDocumentReader + " for a long one"
	case ext == ".xlsx" || ext == ".xls" || ext == ".ods":
		use = ToolQueryData + " (SQL over its sheets) or " + ToolDocumentReader
	case ext == ".docx" || ext == ".pptx" || ext == ".epub" || ext == ".odt" || ext == ".doc" || ext == ".rtf":
		use = ToolDocumentReader
	case ext == ".db" || ext == ".sqlite" || ext == ".sqlite3":
		use = ToolQueryData
	case strings.HasPrefix(kind, "image/"):
		use = "view_image to inspect it; media_convert or code for file processing"
	case strings.HasPrefix(kind, "audio/") || strings.HasPrefix(kind, "video/") || slices.Contains([]string{".mp3", ".m4a", ".ogg", ".oga", ".opus", ".wav", ".mp4", ".mkv", ".mov", ".webm"}, ext):
		use = "media_convert to convert, cut or trim it; speech recognition is not supported"
	case slices.Contains([]string{".zip", ".rar", ".7z", ".tar", ".gz", ".tgz", ".xz", ".bz2", ".iso"}, ext) || kind == "application/zip" || kind == "application/x-gzip":
		use = ToolExtract
	}
	return fmt.Sprintf("%s is not a text file (%s, %s); read_file shows text only. Use %s.", name, kind, humanSize(int64(len(data))), use)
}

func looksBinary(b []byte) bool {
	ctrl := 0
	for _, c := range b {
		if c < 0x09 || (c > 0x0d && c < 0x20) {
			ctrl++
		}
	}
	return len(b) > 0 && ctrl*20 > len(b)
}

func (r *Registry) listFiles(_ context.Context, sessionID string, _ map[string]any) (string, error) {
	entries, err := r.Machine.List(sessionID, 3, 200)
	if err != nil {
		return fmt.Sprintf("list error: %v", err), nil
	}
	if len(entries) == 0 {
		return "Workspace is empty.", nil
	}
	var sb strings.Builder
	for _, e := range entries {
		switch {
		case e.Symlink:
			fmt.Fprintf(&sb, "%s (symlink)\n", e.Name)
		case e.IsDir:
			fmt.Fprintf(&sb, "%s/ (dir)\n", e.Name)
		default:
			fmt.Fprintf(&sb, "%s (%s)\n", e.Name, humanSize(e.Size))
		}
	}
	return sb.String(), nil
}

func friendlyFSError(err error) string {
	switch {
	case errors.Is(err, machine.ErrOutsideWorkspace):
		return "not a usable path"
	case errors.Is(err, machine.ErrNoSpace):
		return "the disk is full"
	case errors.Is(err, machine.ErrNotRegular):
		return "not a regular file"
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return textutil.OneLine(err.Error())
}

func (r *Registry) readMany(ctx context.Context, sessionID, first string, list []any) string {
	var names []string
	seen := map[string]bool{}
	for _, v := range append([]any{first}, list...) {
		if s, _ := v.(string); strings.TrimSpace(s) != "" && !seen[s] {
			seen[s] = true
			names = append(names, strings.TrimSpace(s))
		}
	}
	more := ""
	if len(names) > readManyMax {
		more = fmt.Sprintf("\n\n(%d more files not read: ask for them in another call)", len(names)-readManyMax)
		names = names[:readManyMax]
	}
	share := (MaxToolOutput - 400) / max(len(names), 1)
	var sb strings.Builder
	for _, n := range names {
		out, _ := r.readFile(ctx, sessionID, map[string]any{"filename": n})
		fmt.Fprintf(&sb, "=== %s ===\n%s\n\n", n, textutil.HeadTail(out, share))
	}
	return strings.TrimSpace(sb.String()) + more
}
