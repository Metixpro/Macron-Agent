package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadSubtitles(t *testing.T) {
	r := newRegistry(t, true, nil)
	fakeDownloader(t, r)
	ws, _ := r.Machine.Workspace("t")

	out := call(t, r, "t", ToolDownloadMedia, map[string]any{"url": "https://93.184.216.34/watch", "subtitles": "only"})
	if !strings.Contains(out, `Downloaded "Test Video.en.srt"`) || !strings.Contains(out, `"Test Video.fa.srt" (`) || strings.Contains(out, ".mp4") {
		t.Fatalf("only: %s", out)
	}
	a := args(t, r, "t", 1)
	for _, want := range []string{"--write-subs", "--write-auto-subs", "--sub-langs\nfa,en", "--convert-subs\nsrt", "--skip-download"} {
		if !strings.Contains(a, want) {
			t.Errorf("only: missing %q", want)
		}
	}

	_ = r.Machine.Remove("t", ".attempts")
	out = call(t, r, "t", ToolDownloadMedia, map[string]any{"url": "https://93.184.216.34/watch2", "subtitles": "file", "sub_langs": "fa"})
	if !strings.Contains(out, `"Test Video.mp4"`) || !strings.Contains(out, "subtitles (fa)") {
		t.Errorf("file: %s", out)
	}
	if a := args(t, r, "t", 1); !strings.Contains(a, "--sub-langs\nfa\n") || strings.Contains(a, "--skip-download") || strings.Contains(a, "--embed-subs") {
		t.Errorf("file args: %s", a)
	}

	_ = r.Machine.Remove("t", ".attempts")
	call(t, r, "t", ToolDownloadMedia, map[string]any{"url": "https://93.184.216.34/watch3", "subtitles": "embed"})
	if a := args(t, r, "t", 1); !strings.Contains(a, "--embed-subs") {
		t.Errorf("embed args: %s", a)
	}

	out = call(t, r, "t", ToolDownloadMedia, map[string]any{"url": "https://93.184.216.34/nosubs", "subtitles": "only"})
	if !strings.Contains(out, "has no fa,en subtitles") {
		t.Errorf("none: %s", out)
	}
	out = call(t, r, "t", ToolDownloadMedia, map[string]any{"url": "https://93.184.216.34/nosubs2", "subtitles": "file"})
	if !strings.Contains(out, `"Test Video.mp4"`) || !strings.Contains(out, "only the video was downloaded") {
		t.Errorf("file without subtitles: %s", out)
	}
	for _, bad := range []map[string]any{{"subtitles": "yes"}, {"subtitles": "file", "sub_langs": "fa;rm -rf"}, {"subtitles": "file", "sub_langs": "a,b,c,d,e"}} {
		bad["url"] = "https://93.184.216.34/x"
		if out := call(t, r, "t", ToolDownloadMedia, bad); !strings.HasPrefix(out, "error:") {
			t.Errorf("%v: %s", bad, out)
		}
	}

	if f, _ := exec.Command("ffmpeg", "-hide_banner", "-filters").Output(); !strings.Contains(string(f), " subtitles ") {
		t.Skip("ffmpeg without libass")
	}
	testVideo(t, filepath.Join(ws, "sub_src.mp4"), 4)
	out = call(t, r, "t", ToolDownloadMedia, map[string]any{"url": "https://93.184.216.34/subclip", "subtitles": "burn", "sub_langs": "fa,en"})
	if !strings.Contains(out, `Downloaded "Sub Clip (subtitled).mp4"`) || !strings.Contains(out, "fa subtitles burned into the picture") || strings.Contains(out, ".srt") {
		t.Fatalf("burn: %s", out)
	}
	for _, gone := range []string{"Sub Clip.mp4", "Sub Clip.fa.srt", "Sub Clip.en.srt", burnFile} {
		if _, err := os.Stat(filepath.Join(ws, gone)); err == nil {
			t.Errorf("%s is left", gone)
		}
	}
	if d := length(t, filepath.Join(ws, "Sub Clip (subtitled).mp4")); d < 3.5 {
		t.Errorf("burned video lasts %.1f s", d)
	}
}
