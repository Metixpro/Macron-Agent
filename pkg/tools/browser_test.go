package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/search"
)

func browserRegistry(t *testing.T) *Registry {
	t.Helper()
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatalf("machine: %v", err)
	}
	r := NewRegistry(sb, search.NewEngine(guardOff()), nil, Options{HTTP: guardOff()})
	if r.browser == nil {
		t.Skip("Chromium and Playwright are not installed")
	}
	return r
}

func shopSite(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/":
			visitor := "first visit"
			if c, err := r.Cookie("seen"); err == nil && c.Value == "yes" {
				visitor = "welcome back"
			}
			http.SetCookie(w, &http.Cookie{Name: "seen", Value: "yes", Path: "/"})
			fmt.Fprintf(w, `<html><head><title>Waze Shop</title></head><body><h1>Shop</h1><p id="visitor">%s</p>
				<form action="/search"><input name="q" placeholder="Search products"><button type="submit">Search</button></form>
				<a href="/about">About us</a><div id="late"></div>
				<script>setTimeout(() => document.getElementById("late").textContent = "loaded later", 300)</script></body></html>`, visitor)
		case "/search":
			fmt.Fprintf(w, `<html><head><title>Results</title></head><body><ul><li class="item">%s phone</li><li class="item">%s case</li></ul></body></html>`,
				r.URL.Query().Get("q"), r.URL.Query().Get("q"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBrowserDrivesARealPage(t *testing.T) {
	t.Run("real", func(t *testing.T) {
		r := browserRegistry(t)
		site := shopSite(t)

		out := call(t, r, "u", ToolBrowser, map[string]any{"url": site.URL, "actions": []any{
			map[string]any{"action": "wait", "text": "loaded later"},
			map[string]any{"action": "extract", "selector": "#visitor"},
		}})
		for _, want := range []string{"Page: Waze Shop", `✓ 2. extract #visitor → ["first visit"]`, `input[name="q"] ← input[text] "Search products"`,
			`text="About us"`, "Files: screenshot.png", "loaded later"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
		}
		if info, err := r.Machine.Stat("u", "screenshot.png"); err != nil || info.Size() < 1000 {
			t.Errorf("screenshot: %v", err)
		}

		out = call(t, r, "u", ToolBrowser, map[string]any{"actions": `[{"action":"fill","selector":"input[name=\"q\"]","text":"waze"},
				{"action":"press","key":"Enter"},{"action":"extract","selector":".item"},{"action":"eval","script":"document.title"}]`, "screenshot": false})
		for _, want := range []string{"Page: Results", `["waze phone", "waze case"]`, `→ "Results"`} {
			if !strings.Contains(out, want) {
				t.Errorf("form: missing %q in:\n%s", want, out)
			}
		}

		out = call(t, r, "u", ToolBrowser, map[string]any{"url": site.URL, "actions": []any{map[string]any{"action": "click", "selector": "text=Nowhere"}},
			"screenshot": false})
		if !strings.Contains(out, "welcome back") || !strings.Contains(out, "✗ 1. click text=Nowhere → timed out") {
			t.Errorf("cookies stay and a missing element is reported:\n%s", out)
		}

		_ = r.Machine.WriteFile("u", "card.html", []byte(`<title>My card</title><h1>سلام</h1>`))
		if out = call(t, r, "u", ToolBrowser, map[string]any{"url": "card.html", "screenshot": false}); !strings.Contains(out, "Page: My card") {
			t.Errorf("workspace page: %s", out)
		}
		out = call(t, r, "u", ToolBrowser, map[string]any{"url": site.URL, "actions": []any{map[string]any{"action": "pdf", "name": "shop"}},
			"screenshot": false, "new_session": true})
		if !strings.Contains(out, "first visit") || !strings.Contains(out, "shop.pdf") {
			t.Errorf("new session and pdf: %s", out)
		}
	})

}

func TestBrowserRefusesOddSchemes(t *testing.T) {
	r := browserRegistry(t)
	for _, bad := range []string{"file:///etc/passwd", "javascript:alert(1)"} {
		if out := call(t, r, "g", ToolBrowser, map[string]any{"url": bad}); !strings.HasPrefix(out, "error:") {
			t.Errorf("%s: %s", bad, out)
		}
	}
}

func probeDuration(t *testing.T, path, stream string) string {
	out, _ := exec.Command("ffprobe", "-v", "error", "-select_streams", stream, "-show_entries", "stream=codec_name,width,height,duration",
		"-of", "default=nw=1", path).Output()
	return string(out)
}

func TestMotionGraphicRendersSmoothVideo(t *testing.T) {
	r := browserRegistry(t)
	if !r.downloaderInfo().FFmpeg {
		t.Skip("ffmpeg is needed")
	}
	ws, _ := r.Machine.Workspace("m")
	if err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", filepath.Join(ws, "background.mp3")).Run(); err != nil {
		t.Fatal(err)
	}
	html := `<style>#box{position:absolute;width:120px;height:120px;background:#ff3d71;animation:spin 1s linear infinite}
		@keyframes spin{to{transform:rotate(360deg)}}</style><div id="box"></div><canvas id="c" width="640" height="360"></canvas>
		<script>const c=document.getElementById('c').getContext('2d');
		(function draw(t){c.clearRect(0,0,640,360);c.fillStyle='#4dd0e1';c.fillRect(t/5,200,60,60);requestAnimationFrame(draw)})(0);
		setTimeout(()=>document.body.style.background='#123',1000)</script>`
	out := call(t, r, "m", ToolMotion, map[string]any{"html": html, "duration": 2, "fps": 24, "size": "small", "filename": "intro",
		"audio": "background.mp3"})
	for _, want := range []string{`Rendered "intro.mp4" — 854x480, 2 s at 24 fps (48 frames)`, "work/intro_preview.jpg"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	video := probeDuration(t, filepath.Join(ws, "intro.mp4"), "v:0")
	if !strings.Contains(video, "codec_name=h264") || !strings.Contains(video, "width=854") || !strings.Contains(video, "duration=2.0") {
		t.Errorf("video stream: %s", video)
	}
	if audio := probeDuration(t, filepath.Join(ws, "intro.mp4"), "a:0"); !strings.Contains(audio, "codec_name=aac") {
		t.Errorf("audio stream: %s", audio)
	}
	if _, err := os.Stat(filepath.Join(ws, "work", "intro_preview.jpg")); err != nil {
		t.Error("preview sheet missing")
	}

	out = call(t, r, "m", ToolMotion, map[string]any{"html": `<h1>still</h1><script>undefinedFunction()</script>`, "duration": 1, "fps": 12})
	if !strings.Contains(out, "nothing moved") || !strings.Contains(out, "undefinedFunction") {
		t.Errorf("a still page with an error is reported:\n%s", out)
	}
	for args, want := range map[string]string{`{"duration":2}`: "give the animation", `{"html":"x","size":"huge"}`: "size must be",
		`{"html":"x","duration":60,"fps":60}`: "too many", `{"file":"nope.html"}`: "not a file"} {
		if out, _ := r.Execute(t.Context(), "m", ToolMotion, args); !strings.Contains(out, want) {
			t.Errorf("%s: %s", args, out)
		}
	}
}

func TestDebiansChromiumIsFound(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "chromium")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldDirs, oldBins := chromeDirs, chromeBinaries
	t.Cleanup(func() { chromeDirs, chromeBinaries = oldDirs, oldBins })
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "")
	chromeDirs, chromeBinaries = []string{t.TempDir()}, []string{bin}
	if got := findChrome(); got != bin {
		t.Errorf("found %q, want %q", got, bin)
	}
}

func TestBrowserReadsTheShapesModelsWriteActionsIn(t *testing.T) {
	got := normalizeActions([]any{
		map[string]any{"goto": "https://a.com"},
		map[string]any{"url": "https://b.com"},
		"goto https://c.com",
		map[string]any{"click": map[string]any{"selector": "#ok"}},
		map[string]any{"selector": "input[name=q]", "text": "hi"},
		map[string]any{"action": "press", "key": "Enter"},
		"back",
		map[string]any{"wait": 500.0},
	})
	want := []string{`{"action":"goto","url":"https://a.com"}`, `{"action":"goto","url":"https://b.com"}`, `{"action":"goto","url":"https://c.com"}`,
		`{"action":"click","selector":"#ok"}`, `{"action":"fill","selector":"input[name=q]","text":"hi"}`, `{"action":"press","key":"Enter"}`,
		`{"action":"back"}`, `{"action":"wait","ms":500}`}
	for i, w := range want {
		b, _ := json.Marshal(got[i])
		if string(b) != w {
			t.Errorf("%d: %s, want %s", i, b, w)
		}
	}
}
