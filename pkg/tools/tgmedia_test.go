package tools

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/search"
)

func probe(t *testing.T, file string, entries string) string {
	t.Helper()
	out, _ := exec.Command("ffprobe", "-v", "error", "-show_entries", entries, "-of", "default=nw=1", file).Output()
	return string(out)
}

func TestTelegramMedia(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	r := bigRegistryUnsafe(t)
	ws, _ := r.Machine.Workspace("tg")
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=30", "-f", "lavfi",
		"-i", "sine=frequency=440:sample_rate=44100", "-t", "70", "-c:v", "libx264", "-preset", "ultrafast", "-b:v", "8M", "-c:a", "aac",
		filepath.Join(ws, "clip.mp4")).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}

	out := call(t, r, "tg", "media_convert", map[string]any{"input_file": "clip.mp4", "action": "to_voice"})
	if !strings.Contains(out, "clip.voice.ogg") {
		t.Fatalf("voice: %s", out)
	}
	if p := probe(t, filepath.Join(ws, "clip.voice.ogg"), "stream=codec_name,sample_rate,channels"); !strings.Contains(p, "codec_name=opus") ||
		!strings.Contains(p, "sample_rate=48000") || !strings.Contains(p, "channels=1") {
		t.Errorf("a Telegram voice message is Opus, 48 kHz, mono: %s", p)
	}

	out = call(t, r, "tg", "media_convert", map[string]any{"input_file": "clip.mp4", "action": "to_video_note", "output_file": "hello.mp4"})
	if !strings.Contains(out, "hello.round.mp4") || !strings.Contains(out, "the first minute") {
		t.Fatalf("video note: %s", out)
	}
	p := probe(t, filepath.Join(ws, "hello.round.mp4"), "stream=width,height:format=duration")
	if !strings.Contains(p, "width=480") || !strings.Contains(p, "height=480") || strings.Contains(p, "duration=7") {
		t.Errorf("square, at most a minute: %s", p)
	}

	out = call(t, r, "tg", "media_convert", map[string]any{"input_file": "clip.mp4", "action": "compress_video", "target_mb": 20})
	if !strings.Contains(out, "clip_small.mp4") {
		t.Fatalf("compress: %s", out)
	}
	if info, err := r.Machine.Stat("tg", "clip_small.mp4"); err != nil || info.Size() > 20*1024*1024*103/100 {
		t.Errorf("fits the target: %v %v\n%s", info.Size(), err, out)
	}

	for action, file := range map[string]string{"gif": "clip.gif", "mp3": "clip.mp3", "cut": "clip_cut.mp4"} {
		out := call(t, r, "tg", "media_convert", map[string]any{"input_file": "clip.mp4", "action": action, "duration": "3"})
		if !strings.Contains(out, "File: "+file) {
			t.Errorf("%s: %s", action, out)
		}
	}
	if p := probe(t, filepath.Join(ws, "clip.gif"), "stream=codec_name,width"); !strings.Contains(p, "codec_name=gif") || !strings.Contains(p, "width=480") {
		t.Errorf("gif: %s", p)
	}

	if NativeKind("a.voice.ogg") != "voice" || NativeKind("x.oga") != "voice" || NativeKind("b.round.mp4") != "video_note" || NativeKind("c.mp4") != "" {
		t.Error("kinds")
	}
}

func bigRegistryUnsafe(t *testing.T) *Registry {
	t.Helper()
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return NewRegistry(sb, search.NewEngine(guardOff()), nil, Options{HTTP: guardOff()})
}
