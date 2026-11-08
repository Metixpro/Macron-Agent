package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalServerGuess(t *testing.T) {
	for base, want := range map[string]bool{
		"http://127.0.0.1:8081":           true,
		"http://localhost:8081/":          true,
		"http://10.0.0.5:8081":            true,
		"http://telegram-bot-api:8081":    true, // a container
		"http://[::1]:8081":               true,
		"https://api.telegram.org":        false,
		"https://tg.my-proxy.workers.dev": false, // a proxy of api.telegram.org keeps its limits
		"https://8.8.8.8":                 false,
		"":                                false,
	} {
		if got := LocalServer(base); got != want {
			t.Errorf("LocalServer(%q) = %v", base, got)
		}
	}
}

// Through a local server the limits are 2000 MB, and a received file is read
// from the server's disk (getFile names it there) instead of over HTTP.
func TestLocalModeReadsFilesFromDisk(t *testing.T) {
	dir := t.TempDir()
	onDisk := filepath.Join(dir, "123:SECRET", "videos", "file_7.mp4")
	_ = os.MkdirAll(filepath.Dir(onDisk), 0o755)
	_ = os.WriteFile(onDisk, []byte("big video bytes"), 0o644)
	httpHits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			path := onDisk
			if strings.Contains(r.FormValue("file_id")+readBody(r), "remote") {
				path = "/elsewhere/file_8.mp4" // another machine's disk
			}
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"x","file_size":15,"file_path":%q}}`, path)
		default:
			httpHits++
			fmt.Fprint(w, "over http")
		}
	}))
	defer srv.Close()

	b := NewBotWithBase("123:SECRET", srv.URL)
	if b.UploadLimit() != MaxUploadBytes || b.DownloadLimit() != MaxDownloadBytes {
		t.Fatal("Telegram's own limits by default")
	}
	b.SetLocal(true)
	if b.UploadLimit() != LocalMaxBytes || b.DownloadLimit() != LocalMaxBytes {
		t.Fatal("2000 MB through a local server")
	}
	if tr := b.UploadClient.Transport.(*http.Transport); tr.ResponseHeaderTimeout < 10*time.Minute {
		t.Errorf("an upload waits long for the local server's answer: %v", tr.ResponseHeaderTimeout)
	}
	var buf bytes.Buffer
	n, err := b.Download(context.Background(), "local", &buf, 1<<20)
	if err != nil || n != 15 || buf.String() != "big video bytes" || httpHits != 0 {
		t.Fatalf("read from disk: %d %q %v (http %d)", n, buf.String(), err, httpHits)
	}
	if _, err := b.Download(context.Background(), "local", &bytes.Buffer{}, 4); !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("the limit holds on disk too: %v", err)
	}
	buf.Reset()
	if _, err := b.Download(context.Background(), "remote", &buf, 1<<20); err != nil || buf.String() != "over http" {
		t.Errorf("a path that is not here is downloaded over HTTP: %q %v", buf.String(), err)
	}
}

func readBody(r *http.Request) string {
	var b bytes.Buffer
	_, _ = b.ReadFrom(r.Body)
	return b.String()
}
