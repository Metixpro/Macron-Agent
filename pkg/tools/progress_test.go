package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestYtdlpProgressLines(t *testing.T) {
	for line, want := range map[string]string{
		"[download]   0.5% of    2.86MiB at  968.05KiB/s ETA 00:03":              "0% · 2.86MB · 00:03",
		"[download]  45.3% of ~  27.00MiB at    2.50MiB/s ETA 00:10 (frag 3/20)": "45% · 27.00MB · 00:10",
		"[download] Destination: song.mp3":                                       "",
	} {
		if got := ytdlpStatus(line); got != want {
			t.Errorf("%q → %q, want %q", line, got, want)
		}
	}
	if s := downloadStatus(12<<20, 27<<20); s != "44% · 12.0/27.0 MB" {
		t.Errorf("download status: %s", s)
	}
}

func TestLineReporterKeepsTheLastLine(t *testing.T) {
	var mu sync.Mutex
	var got []string
	w := newLineReporter(func(l string) { mu.Lock(); got = append(got, l); mu.Unlock() })
	io.WriteString(w, "\x1b[32mstep 1\x1b[0m\nstep 2\n")
	time.Sleep(350 * time.Millisecond)
	io.WriteString(w, "progress 10%\rprogress 90%\r")
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "step 2" || got[1] != "progress 90%" {
		t.Errorf("%q", got)
	}
}

func TestRunCodeReportsItsOutputLive(t *testing.T) {
	r := newRegistry(t, false, nil)
	var mu sync.Mutex
	var lines []string
	ctx := WithProgress(context.Background(), Progress{Output: func(l string) { mu.Lock(); lines = append(lines, l); mu.Unlock() }})
	out, err := r.Execute(ctx, "p", ToolRunCode, fmt.Sprint(`{"language":"bash","code":"echo epoch 1; sleep 0.4; echo epoch 2; sleep 0.4"}`))
	if err != nil || !strings.Contains(out, "epoch 2") {
		t.Fatalf("%v %s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) == 0 || lines[len(lines)-1] != "epoch 2" {
		t.Errorf("the last line printed reaches the live view: %q", lines)
	}
}

func TestDownloadFileReportsProgress(t *testing.T) {
	payload := strings.Repeat("x", 3<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		for i := 0; i < len(payload); i += 256 << 10 {
			w.Write([]byte(payload[i:min(i+256<<10, len(payload))]))
			w.(http.Flusher).Flush()
			time.Sleep(150 * time.Millisecond)
		}
	}))
	defer srv.Close()
	r := newRegistry(t, true, nil)
	var mu sync.Mutex
	var states []string
	ctx := WithProgress(context.Background(), Progress{Status: func(s string) { mu.Lock(); states = append(states, s); mu.Unlock() }})
	out, err := r.Execute(ctx, "d", "download_file", fmt.Sprintf(`{"url":%q,"filename":"big.bin"}`, srv.URL+"/big.bin"))
	if err != nil || !strings.Contains(out, "Downloaded") {
		t.Fatalf("%v %s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(states) == 0 || !strings.Contains(states[len(states)-1], "/3.0 MB") || !strings.Contains(states[0], "%") {
		t.Errorf("percent and size while downloading: %q", states)
	}
}
