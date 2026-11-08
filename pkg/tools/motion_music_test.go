package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSynthStyleForPicksTheClosestStyle(t *testing.T) {
	for tags, want := range map[string]string{
		"cinematic orchestral, epic": "cinematic", "lo-fi hip hop, mellow piano": "lofi", "Persian santur and daf, calm": "persian",
		"upbeat EDM, house": "edm", "synthwave, retro synths": "synthwave", "8-bit chiptune game": "chiptune", "solo piano, calm": "piano",
		"dark trap beat": "trap", "ambient, atmospheric": "ambient", "something unknown": "lofi",
	} {
		if got := synthStyleFor(tags); got != want {
			t.Errorf("%q → %q, want %q", tags, got, want)
		}
	}
}

// muxAudio puts a track under a silent video, cut and faded, with the picture
// copied as it is.
func TestMuxAudioPutsMusicUnderTheVideo(t *testing.T) {
	r := songRegistry(t, newFakeSongService(t))
	if !r.downloaderInfo().FFmpeg {
		t.Skip("ffmpeg is needed")
	}
	ws, _ := r.Machine.Workspace("mux")
	if err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=blue:s=160x120:d=2:r=12", "-pix_fmt", "yuv420p",
		filepath.Join(ws, "silent.mp4")).Run(); err != nil {
		t.Skipf("no test video: %v", err)
	}
	if err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=330:duration=10",
		filepath.Join(ws, "long.mp3")).Run(); err != nil {
		t.Skipf("no test audio: %v", err)
	}
	if err := r.muxAudio(t.Context(), "mux", "silent.mp4", "long.mp3", "final.mp4", 2); err != nil {
		t.Fatal(err)
	}
	if v := probeDuration(t, filepath.Join(ws, "final.mp4"), "v:0"); !strings.Contains(v, "codec_name=h264") || !strings.Contains(v, "width=160") {
		t.Errorf("the picture is kept: %s", v)
	}
	if a := probeDuration(t, filepath.Join(ws, "final.mp4"), "a:0"); !strings.Contains(a, "codec_name=aac") {
		t.Errorf("the music is under it: %s", a)
	}
	out, _ := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", filepath.Join(ws, "final.mp4")).Output()
	if d := strings.TrimSpace(string(out)); !strings.HasPrefix(d, "2.0") {
		t.Errorf("a ten-second track is cut to the video's two seconds: %q", d)
	}
	if err := r.muxAudio(t.Context(), "mux", "silent.mp4", "missing.mp3", "bad.mp4", 2); err == nil {
		t.Error("a missing track is an error, not a silent success")
	}
}

// music.ai: real instrumental music from the song service goes under the
// video, made while it renders; when the service cannot, a synthesizer's does;
// the video is never lost.
func TestMotionGraphicWithAIMusic(t *testing.T) {
	f := newFakeSongService(t)
	r := browserRegistry(t)
	if !r.downloaderInfo().FFmpeg {
		t.Skip("ffmpeg is needed")
	}
	r.SetMusicAPI(f.URL + "/music/index.php")
	r.SetMusicKey(func() string { return songTestKey })
	r.registerSongTool()
	ws, _ := r.Machine.Workspace("mm")
	html := `<style>#b{position:absolute;width:80px;height:80px;background:#f36;animation:s 1s linear infinite}@keyframes s{to{transform:rotate(360deg)}}</style><div id="b"></div>`
	args := func(music map[string]any, name string) map[string]any {
		return map[string]any{"html": html, "duration": 2, "fps": 12, "size": "small", "filename": name, "music": music}
	}

	out := call(t, r, "mm", ToolMotion, args(map[string]any{"ai": "cinematic orchestral, epic strings"}, "epic"))
	for _, want := range []string{`Rendered "epic.mp4"`, "a real instrumental from the song service (cinematic orchestral, epic strings)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	b := f.body()
	if st, _ := b["style"].(string); !strings.HasPrefix(st, "instrumental, no vocals, no singing") || !strings.Contains(st, "epic strings") || b["lyrics"] != instrumentalLyrics {
		t.Errorf("the service was asked for an instrumental: %v", b)
	}
	if a := probeDuration(t, filepath.Join(ws, "epic.mp4"), "a:0"); !strings.Contains(a, "codec_name=aac") {
		t.Errorf("the music is under the video: %s", a)
	}
	for _, gone := range []string{"epic_silent.mp4"} {
		if _, err := os.Stat(filepath.Join(ws, gone)); err == nil {
			t.Errorf("%s is a leftover", gone)
		}
	}
	if strings.Contains(out, "/music/generated") || strings.Contains(out, songTestKey) {
		t.Errorf("no link or key reaches the model:\n%s", out)
	}

	// the service fails: the synthesizer's music, in the style the tags point to
	f.failWith = "no credit"
	f.polls.Store(0)
	out = call(t, r, "mm", ToolMotion, args(map[string]any{"ai": "chiptune game"}, "chip"))
	if !strings.Contains(out, `Rendered "chip.mp4"`) || !strings.Contains(out, "the song service gave no music") {
		t.Errorf("a failing service does not lose the video:\n%s", out)
	}
	if r.Has(ToolMusic) {
		if !strings.Contains(out, "chiptune") {
			t.Errorf("the synthesizer takes over in the style of the tags:\n%s", out)
		}
		if a := probeDuration(t, filepath.Join(ws, "chip.mp4"), "a:0"); !strings.Contains(a, "codec_name=aac") {
			t.Errorf("the fallback music is under the video: %s", a)
		}
	}

	// no key: nothing is asked of the service, and it is said
	r.SetMusicKey(func() string { return "" })
	before := f.created.Load()
	out = call(t, r, "mm", ToolMotion, args(map[string]any{"ai": "lofi chill"}, "nokey"))
	if f.created.Load() != before || !strings.Contains(out, "the song service is not available here") || !strings.Contains(out, `Rendered "nokey.mp4"`) {
		t.Errorf("without a key:\n%s", out)
	}
}
