package tools

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDownloadFileInParallelParts(t *testing.T) {
	data := make([]byte, 3<<20+12345)
	rand.Read(data)
	mod := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var ranges, plain atomic.Int32
	var broke atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rg := r.Header.Get("Range")
		if rg == "" {
			plain.Add(1)
		} else {
			ranges.Add(1)
		}
		switch r.URL.Path {
		case "/big.bin":
			var start, end int
			if _, err := fmt.Sscanf(rg, "bytes=%d-%d", &start, &end); err == nil && start > 0 && !broke.Swap(true) {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
				w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(data[start : start+(end-start+1)/2])
				panic(http.ErrAbortHandler) // the connection drops halfway
			}
			http.ServeContent(w, r, "big.bin", mod, bytes.NewReader(data))
		case "/first-only.bin": // honours the first range, then sends the whole file
			if rg == "bytes=0-" {
				http.ServeContent(w, r, "x.bin", mod, bytes.NewReader(data))
				return
			}
			w.Write(data)
		case "/empty.txt":
			if rg != "" {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
		}
	}))
	defer srv.Close()
	oldMin, oldPart := dlParallelMin, dlPartBytes
	dlParallelMin, dlPartBytes = 1<<20, 512<<10
	defer func() { dlParallelMin, dlPartBytes = oldMin, oldPart }()
	r := newRegistry(t, true, nil)
	ws, _ := r.Machine.Workspace("p")
	same := func(name string) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(ws, name))
		if err != nil || !bytes.Equal(got, data) {
			t.Errorf("%s differs from the original (%d bytes, %v)", name, len(got), err)
		}
	}

	out := call(t, r, "p", "download_file", map[string]any{"url": srv.URL + "/big.bin"})
	if !strings.Contains(out, `Downloaded "big.bin" (3.0 MB) into the workspace over 6 parallel connections`) {
		t.Fatalf("parallel: %s", out)
	}
	same("big.bin")
	if !broke.Load() || ranges.Load() < 7 || plain.Load() != 0 {
		t.Errorf("6 parts plus one resumed: %d ranges, %d plain, broke %v", ranges.Load(), plain.Load(), broke.Load())
	}

	out = call(t, r, "p", "download_file", map[string]any{"url": srv.URL + "/first-only.bin"})
	if !strings.Contains(out, `Downloaded "first-only.bin" (3.0 MB) into the workspace.`) {
		t.Fatalf("fallback: %s", out)
	}
	same("first-only.bin")

	if out = call(t, r, "p", "download_file", map[string]any{"url": srv.URL + "/empty.txt"}); !strings.Contains(out, `"empty.txt" (0 B)`) {
		t.Errorf("empty: %s", out)
	}
}
