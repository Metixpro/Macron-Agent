package tools

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const piperRetry = 10 * time.Minute

type piperVoice struct {
	model    string
	offUntil atomic.Int64
}

func PiperFiles(model string) []string {
	if model == "" || !filepath.IsAbs(model) {
		return nil
	}
	return []string{model, model + ".json"}
}

func (p *piperVoice) ready() bool {
	if p.model == "" || time.Now().Unix() < p.offUntil.Load() {
		return false
	}
	files := PiperFiles(p.model)
	for _, f := range files {
		if fi, err := os.Stat(f); err != nil || fi.Size() == 0 {
			return false
		}
	}
	return len(files) > 0
}

func (p *piperVoice) name() string {
	base := strings.TrimSuffix(filepath.Base(p.model), ".onnx")
	if parts := strings.Split(base, "-"); len(parts) >= 2 && parts[1] != "" {
		return parts[1]
	}
	return base
}

var edgeVoices = map[string]struct{ id, name string }{
	"farid":       {"fa-IR-FaridNeural", "Farid Neural"},
	"dilara":      {"fa-IR-DilaraNeural", "Dilara Neural"},
	"christopher": {"en-US-ChristopherNeural", "Christopher Neural"},
	"jenny":       {"en-US-JennyNeural", "Jenny Neural"},
}

func hasPersian(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Arabic, r) && unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func (r *Registry) textToSpeech(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	text := rawStrArg(args, "text")
	if strings.TrimSpace(text) == "" {
		return "error: text is required", nil
	}
	if len([]rune(text)) > 4000 {
		return "error: text is too long (max 4000 characters) — split it into parts", nil
	}
	persian := hasPersian(text)
	voice := strings.ToLower(strings.TrimSpace(strArg(args, "voice")))

	local := voice == "" || voice == "mana" || voice == "amir" || voice == "mantatts" || voice == "local"
	if _, ok := edgeVoices[voice]; !ok {
		voice = "farid"
		if !persian {
			voice = "christopher"
		}
	}
	name := outputName(strArg(args, "filename"), "speech", []string{".mp3"}, ".mp3")
	asVoice := boolArg(args, "as_voice")
	speed := speechSpeed(args)

	if persian && local && r.piper.ready() {
		out, err := r.piperSpeech(ctx, sessionID, text, name, asVoice, speed)
		if err == nil {
			return out, nil
		}
		log.Printf("text_to_speech: the local voice failed (%s); Edge TTS reads it", tail(textutil.OneLine(err.Error()), 300))
	}
	return r.edgeSpeech(ctx, sessionID, text, name, voice, asVoice, speed), nil
}

func speechSpeed(args map[string]any) float64 {
	s := 1.0
	switch v := args["speed"].(type) {
	case float64:
		s = v
	case string:
		if f, ok := parseNumber(v); ok {
			s = f
		}
	}
	if s <= 0 {
		return 1
	}
	return min(max(s, 0.5), 2)
}

func (r *Registry) piperSpeech(ctx context.Context, sessionID, text, name string, asVoice bool, speed float64) (string, error) {
	wav := ".tts-" + strconv.FormatInt(time.Now().UnixNano(), 36) + ".wav"
	defer func() { _ = r.Machine.Remove(sessionID, wav) }()

	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{

		Args: []string{"python3", "-I", "-m", "piper", "-m", r.piper.model, "--length_scale", strconv.FormatFloat(1/speed, 'f', 3, 64),
			"--sentence_silence", "0.25", "-f", "./" + wav},
		Stdin:   []byte(text),
		Timeout: 3 * time.Minute,
	})
	if res.ExitCode != 0 {
		if strings.Contains(res.Stderr, "No module named piper") {
			r.piper.offUntil.Store(time.Now().Add(piperRetry).Unix())
		}
		return "", errors.New(failure("piper", res))
	}
	if info, err := r.Machine.Stat(sessionID, wav); err != nil || info.Size() <= 44 {
		return "", errors.New("piper produced no audio")
	}
	out, kind := name, "Audio"
	conv := ffmpegArgs("-i", "./"+wav, "-ac", "1", "-c:a", "libmp3lame", "-b:a", "64k", "./"+out)
	if asVoice {
		out, kind = nativeName("", name, VoiceSuffix), "Voice message"
		conv = ffmpegArgs("-i", "./"+wav, "-ac", "1", "-ar", "48000", "-c:a", "libopus", "-b:a", "48k", "-vbr", "on", "-application", "voip", "./"+out)
	}
	if c := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: conv, Timeout: 2 * time.Minute}); c.ExitCode != 0 {
		_ = r.Machine.Remove(sessionID, out)
		return "", errors.New(failure("converting the speech", c))
	}
	info, err := r.Machine.Stat(sessionID, out)
	if err != nil || info.Size() == 0 {
		return "", errors.New("the converted speech is empty")
	}
	return speechResult(kind, out, info.Size(), "Piper "+r.piper.name()+" (local)"), nil
}

func (r *Registry) edgeSpeech(ctx context.Context, sessionID, text, name, voice string, asVoice bool, speed float64) string {
	v := edgeVoices[voice]
	rate := fmt.Sprintf("%+d%%", int((speed-1)*100+0.5*sign(speed-1)))
	res, err := r.runScript(ctx, sessionID, "edge_speech.py", map[string]any{"text": text, "voice": v.id, "rate": rate, "filename": "./" + name}, 120*time.Second)
	if err != nil {
		return "error: text-to-speech: " + err.Error()
	}

	if res.ExitCode != 0 {
		return failure("text-to-speech", res)
	}
	info, err := r.Machine.Stat(sessionID, name)
	if err != nil || info.Size() == 0 {
		return "text-to-speech produced no audio"
	}
	label := v.name + " (edge)"
	if asVoice {
		ogg := nativeName("", name, VoiceSuffix)
		conv := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: ffmpegArgs("-i", "./"+name, "-ac", "1", "-ar", "48000", "-c:a", "libopus",
			"-b:a", "48k", "-vbr", "on", "-application", "voip", "./"+ogg), Timeout: 60 * time.Second})
		if conv.ExitCode == 0 {
			_ = r.Machine.Remove(sessionID, name)
			if vi, err := r.Machine.Stat(sessionID, ogg); err == nil {
				return speechResult("Voice message", ogg, vi.Size(), label)
			}
		}
	}
	return speechResult("Audio", name, info.Size(), label)
}

func speechResult(kind, file string, size int64, voice string) string {
	return fmt.Sprintf("%s created and queued for delivery.\n- File: %s (%s)\n- Voice: %s", kind, file, humanSize(size), voice)
}

func sign(x float64) float64 {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	}
	return 0
}
