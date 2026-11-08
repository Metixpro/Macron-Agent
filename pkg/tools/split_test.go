package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func testVideo(t *testing.T, path string, seconds int) {
	t.Helper()
	if exec.Command("ffmpeg", "-version").Run() != nil {
		t.Skip("ffmpeg is needed")
	}
	if out, err := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=30", "-f", "lavfi", "-i",
		"sine=frequency=440", "-t", strconv.Itoa(seconds), "-c:v", "libx264", "-g", "15", "-b:v", "3M", "-c:a", "aac", path).CombinedOutput(); err != nil {
		t.Skipf("cannot make a test video: %v %s", err, out)
	}
}

func length(t *testing.T, path string) float64 {
	out, _ := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path).Output()
	d, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return d
}

func TestSplitVideoByMinutes(t *testing.T) {
	r := newRegistry(t, true, nil)
	fakeDownloader(t, r)
	ws, _ := r.Machine.Workspace("v")
	testVideo(t, filepath.Join(ws, "clip.mp4"), 12)

	out := call(t, r, "v", ToolSplitVideo, map[string]any{"filename": "clip.mp4", "minutes": 0.1})
	if !strings.Contains(out, "into 2 parts without re-encoding") || !strings.Contains(out, `"clip - part 02.mp4"`) || !strings.Contains(out, "part 2 of 2, about 0:06–0:12") {
		t.Fatalf("split: %s", out)
	}
	total := 0.0
	for _, p := range []string{"clip - part 01.mp4", "clip - part 02.mp4"} {
		d := length(t, filepath.Join(ws, p))
		if d < 5 || d > 7 {
			t.Errorf("%s lasts %.1f s", p, d)
		}
		total += d
	}
	if total < 11.5 {
		t.Errorf("nothing is lost: %.1f s", total)
	}
	if _, err := os.Stat(filepath.Join(ws, "clip.mp4")); err != nil {
		t.Error("the tool keeps the original")
	}
	// Parts last at least 5 s.
	for args, want := range map[string]string{`{"filename":"nope.mp4"}`: "not found", `{"filename":"clip.mp4","minutes":0.01}`: "into 3 parts"} {
		if out, _ := r.Execute(t.Context(), "v", ToolSplitVideo, args); !strings.Contains(out, want) {
			t.Errorf("%s: %s", args, out)
		}
	}
	// Split again into fewer parts: the third part of the split before is not counted.
	out = call(t, r, "v", ToolSplitVideo, map[string]any{"filename": "clip.mp4", "minutes": 0.1})
	if !strings.Contains(out, "into 2 parts") || strings.Contains(out, "part 03") {
		t.Errorf("only this split's parts: %s", out)
	}
	if _, err := os.Stat(filepath.Join(ws, "clip - part 03.mp4")); err == nil {
		t.Error("the old part is gone")
	}
}

func TestDownloadSplitsInsteadOfReencoding(t *testing.T) {
	r := newRegistry(t, true, nil)
	fakeDownloader(t, r)
	ws, _ := r.Machine.Workspace("w")
	src := filepath.Join(ws, "big_src.mp4")
	testVideo(t, src, 8)
	info, _ := os.Stat(src)
	oldSend, oldFit := dlSendLimit, dlFitBytes
	dlSendLimit, dlFitBytes = info.Size()*3/5, info.Size()/2
	defer func() { dlSendLimit, dlFitBytes = oldSend, oldFit }()

	out := call(t, r, "w", ToolDownloadMedia, map[string]any{"url": "ytsearch1:big", "format": "video", "split": true})
	if !strings.Contains(out, `Downloaded "Big Clip - part 01.mp4"`) || !strings.Contains(out, "without re-encoding") || strings.Contains(out, "compressed") {
		t.Fatalf("split: %s", out)
	}
	n := strings.Count(out, "- Downloaded ")
	if n < 2 || !strings.Contains(out, "Downloaded "+strconv.Itoa(n)+" file(s)") {
		t.Errorf("parts: %s", out)
	}
	files, _ := r.Machine.ListFiles("w")
	for _, f := range files {
		if strings.HasPrefix(f.Name, "Big Clip") && f.Size > dlSendLimit {
			t.Errorf("%s is %d bytes, over the limit", f.Name, f.Size)
		}
		if f.Name == "Big Clip.mp4" {
			t.Error("the oversized original is removed")
		}
	}
}
