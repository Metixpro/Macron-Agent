package tools

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Live progress: a tool tells whoever is watching how far it is, while it
// runs. A download reports its percent and size, a program the last line it
// printed. The agent puts a reporter in the context of each call (see
// WithProgress); without one, reports go nowhere.

// Progress receives the reports of one tool call.
type Progress struct {
	Status func(text string) // a short state: "45% · 12.3/27 MB"
	Output func(line string) // the last line a program printed
}

type progressKey struct{}

// WithProgress gives the tool calls under ctx a reporter.
func WithProgress(ctx context.Context, p Progress) context.Context {
	return context.WithValue(ctx, progressKey{}, p)
}

func progressOf(ctx context.Context) Progress {
	p, _ := ctx.Value(progressKey{}).(Progress)
	return p
}

// ReportStatus tells how far the tool is.
func ReportStatus(ctx context.Context, text string) {
	if p := progressOf(ctx); p.Status != nil {
		p.Status(text)
	}
}

// ReportOutput passes on the last line a program printed.
func ReportOutput(ctx context.Context, line string) {
	if p := progressOf(ctx); p.Output != nil {
		p.Output(line)
	}
}

// LiveOutput is a writer whose last line goes to whoever watches the call
// (nil when nobody does): a long remote command shows where it is.
func LiveOutput(ctx context.Context) io.Writer {
	p := progressOf(ctx)
	if p.Output == nil {
		return nil
	}
	return newLineReporter(p.Output)
}

// hasProgress reports whether anyone listens.
func hasProgress(ctx context.Context) bool {
	p := progressOf(ctx)
	return p.Status != nil || p.Output != nil
}

// ---------------------------------------------------------------- program output

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07`)

// lineReporter is an io.Writer that reports the last non-empty line written
// (a progress bar that redraws itself with \r counts as a line), at most a
// few times a second.
type lineReporter struct {
	mu      sync.Mutex
	partial []byte
	last    time.Time
	report  func(string)
}

func newLineReporter(report func(string)) *lineReporter { return &lineReporter{report: report} }

func (w *lineReporter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = append(w.partial, p...)
	line := ""
	for {
		i := strings.IndexAny(string(w.partial), "\r\n")
		if i < 0 {
			break
		}
		if l := cleanLine(string(w.partial[:i])); l != "" {
			line = l
		}
		w.partial = w.partial[i+1:]
	}
	if len(w.partial) > 4096 { // a line without end: keep its tail
		w.partial = w.partial[len(w.partial)-1024:]
	}
	if line != "" && time.Since(w.last) >= 300*time.Millisecond {
		w.last = time.Now()
		w.report(line)
	}
	return len(p), nil
}

// cleanLine drops colors and control characters.
func cleanLine(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// outputTee reports the last line a command prints, when someone listens.
func outputTee(ctx context.Context) io.Writer {
	if progressOf(ctx).Output == nil {
		return nil
	}
	return newLineReporter(func(line string) { ReportOutput(ctx, line) })
}

// lineCollector keeps the lines that start with a prefix (yt-dlp's report of
// each file), however much else the command prints.
type lineCollector struct {
	mu      sync.Mutex
	prefix  string
	partial []byte
	lines   []string
}

func (c *lineCollector) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.partial = append(c.partial, p...)
	for {
		i := strings.IndexByte(string(c.partial), '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimRight(string(c.partial[:i]), "\r"); strings.HasPrefix(line, c.prefix) {
			c.lines = append(c.lines, line)
		}
		c.partial = c.partial[i+1:]
	}
	if len(c.partial) > 1<<16 && !strings.HasPrefix(string(c.partial), c.prefix) {
		c.partial = nil
	}
	return len(p), nil
}

func (c *lineCollector) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := strings.Join(c.lines, "\n")
	if strings.HasPrefix(string(c.partial), c.prefix) {
		out += "\n" + string(c.partial)
	}
	return out
}

// ---------------------------------------------------------------- downloads

// A download counts its bytes through the context, wherever they are written
// (one stream or parallel parts), and a ticker reports them.

type counterKey struct{}

func withCounter(ctx context.Context, c *atomic.Int64) context.Context {
	return context.WithValue(ctx, counterKey{}, c)
}

func counterOf(ctx context.Context) *atomic.Int64 {
	c, _ := ctx.Value(counterKey{}).(*atomic.Int64)
	return c
}

// countingWriter adds what it writes to a counter.
type countingWriter struct {
	w io.Writer
	c *atomic.Int64
}

func (cw countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.c.Add(int64(n))
	return n, err
}

// counted wraps w so the download's bytes are counted (when someone listens).
func counted(ctx context.Context, w io.Writer) io.Writer {
	if c := counterOf(ctx); c != nil {
		return countingWriter{w: w, c: c}
	}
	return w
}

// trackDownload reports a download's progress until stop is called.
func trackDownload(ctx context.Context, total func() int64) (context.Context, func()) {
	if progressOf(ctx).Status == nil {
		return ctx, func() {}
	}
	var done atomic.Int64
	ctx = withCounter(ctx, &done)
	quit := make(chan struct{})
	go func() {
		t := time.NewTicker(700 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if n := done.Load(); n > 0 {
					ReportStatus(ctx, downloadStatus(n, total()))
				}
			}
		}
	}()
	var once sync.Once
	return ctx, func() { once.Do(func() { close(quit) }) }
}

// sizeMB formats bytes for a progress line.
func sizeMB(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", n>>10)
}

// downloadStatus is "45% · 12.3/27.0 MB" (or "12.3 MB" when the size is unknown).
func downloadStatus(done, total int64) string {
	if total > 0 {
		return fmt.Sprintf("%d%% · %s/%s", min(done*100/total, 100), strings.TrimSuffix(sizeMB(done), " MB"), sizeMB(total))
	}
	return sizeMB(done)
}

// ytdlpProgressRe reads yt-dlp's "[download]  45.3% of ~ 27.00MiB at 2.50MiB/s ETA 00:10".
var ytdlpProgressRe = regexp.MustCompile(`\[download\]\s+([\d.]+)%\s+of\s+~?\s*([\d.]+\s*[KMGT]i?B)(?:.*?ETA\s+([\d:]+))?`)

// ytdlpStatus turns a yt-dlp progress line into a status ("" if it is not one).
func ytdlpStatus(line string) string {
	m := ytdlpProgressRe.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	pct := m[1]
	if i := strings.IndexByte(pct, '.'); i >= 0 {
		pct = pct[:i]
	}
	s := pct + "% · " + strings.ReplaceAll(strings.ReplaceAll(m[2], "iB", "B"), " ", "")
	if m[3] != "" {
		s += " · " + m[3]
	}
	return s
}
