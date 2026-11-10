package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/search"
)

func TestPiperVoiceSetup(t *testing.T) {
	if PiperFiles("") != nil || PiperFiles("voices/fa.onnx") != nil {
		t.Error("no voice, or a relative path, mounts nothing")
	}
	if got := PiperFiles("/opt/piper-voices/fa_IR-mantatts-par.onnx"); len(got) != 2 || got[1] != "/opt/piper-voices/fa_IR-mantatts-par.onnx.json" {
		t.Errorf("files: %v", got)
	}
	if n := (&piperVoice{model: "/opt/piper-voices/fa_IR-mantatts-par.onnx"}).name(); n != "mantatts" {
		t.Errorf("name %q", n)
	}
	if n := (&piperVoice{model: "/x/voice.onnx"}).name(); n != "voice" {
		t.Errorf("name %q", n)
	}

	model := filepath.Join(t.TempDir(), "fa_IR-test-medium.onnx")
	p := &piperVoice{model: model}
	if p.ready() || (&piperVoice{}).ready() {
		t.Fatal("ready without a model")
	}
	_ = os.WriteFile(model, []byte("onnx"), 0o644)
	if p.ready() {
		t.Fatal("ready without the model's config")
	}
	_ = os.WriteFile(model+".json", []byte("{}"), 0o644)
	if !p.ready() {
		t.Fatal("not ready with both files")
	}
	p.offUntil.Store(time.Now().Add(time.Minute).Unix())
	if p.ready() {
		t.Fatal("ready while Piper is known to be missing")
	}
}

func leftovers(t *testing.T, r *Registry, session string) []string {
	ws, _ := r.Machine.Workspace(session)
	m, _ := filepath.Glob(filepath.Join(ws, ".tts-*"))
	return m
}

func TestTextToSpeechWithPiper(t *testing.T) {
	model := os.Getenv("MACRON_PIPER_MODEL")
	if model == "" {
		model = "/opt/piper-voices/fa_IR-mana-medium.onnx"
	}
	if !(&piperVoice{model: model}).ready() {
		t.Skip("no Piper voice on this machine (the installer puts one in /opt/piper-voices)")
	}
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatalf("machine: %v", err)
	}
	r := NewRegistry(sb, search.NewEngine(guardOff()), nil, Options{HTTP: guardOff(), PiperModel: model})
	if !r.check("python3", "-I", "-c", "import piper") {
		t.Skip("piper-tts is not installed")
	}

	voice := "Piper " + r.piper.name() + " (local)"
	out := call(t, r, "p", "text_to_speech", map[string]any{"text": "سلام! من مکرون هستم.\nامروز هوا خیلی خوبه.", "filename": "hello"})
	if !strings.Contains(out, "Voice: "+voice) || !strings.Contains(out, "hello.mp3") {
		t.Fatalf("Persian with no voice: %s", out)
	}
	if data, _, err := r.Machine.ReadFile("p", "hello.mp3", 16); err != nil || !(strings.HasPrefix(string(data), "ID3") || data[0] == 0xFF) {
		t.Fatalf("not an MP3: %q %v", data, err)
	}

	out = call(t, r, "p", "text_to_speech", map[string]any{"text": "این یک پیام صوتی است.", "voice": "mana", "as_voice": true, "filename": "note", "speed": 1.25})
	if !strings.Contains(out, "Voice message created") || !strings.Contains(out, voice) || !strings.Contains(out, "note.voice.ogg") {
		t.Fatalf("a voice message: %s", out)
	}
	if data, _, err := r.Machine.ReadFile("p", "note.voice.ogg", 8); err != nil || !strings.HasPrefix(string(data), "OggS") {
		t.Fatalf("not an Ogg voice: %q %v", data, err)
	}
	if left := leftovers(t, r, "p"); len(left) > 0 {
		t.Errorf("the WAV was left behind: %v", left)
	}

	for _, args := range []map[string]any{{"text": "Hello, this is Macron."}, {"text": "سلام", "voice": "jenny"}, {"text": "سلام", "voice": "dilara"}} {
		if out := call(t, r, "p", "text_to_speech", args); strings.Contains(out, "Piper") {
			t.Errorf("%v went to Piper: %s", args, out)
		}
	}
}

func TestTextToSpeechFallsBackToEdge(t *testing.T) {
	model := filepath.Join(t.TempDir(), "fa_IR-broken-medium.onnx")
	_ = os.WriteFile(model, []byte("not a model"), 0o644)
	_ = os.WriteFile(model+".json", []byte("{}"), 0o644)
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(sb, search.NewEngine(guardOff()), nil, Options{HTTP: guardOff(), PiperModel: model})
	out := call(t, r, "f", "text_to_speech", map[string]any{"text": "سلام دنیا"})
	if strings.Contains(out, "Piper") || !(strings.Contains(out, "Farid Neural (edge)") || strings.Contains(out, "text-to-speech failed")) {
		t.Fatalf("a broken voice must fall back to Edge TTS: %s", out)
	}
	if left := leftovers(t, r, "f"); len(left) > 0 {
		t.Errorf("the WAV was left behind: %v", left)
	}
	if !r.check("python3", "-I", "-c", "import piper") && r.piper.ready() {
		t.Error("a server without Piper must not try it on every request")
	}
}

func TestSpeechSpeed(t *testing.T) {
	for in, want := range map[any]float64{nil: 1, 1.5: 1.5, 3.0: 2, 0.1: 0.5, "۱٫۲۵": 1.25, "fast": 1, -1.0: 1} {
		args := map[string]any{}
		if in != nil {
			args["speed"] = in
		}
		if got := speechSpeed(args); got != want {
			t.Errorf("%v: %v, want %v", in, got, want)
		}
	}
}
