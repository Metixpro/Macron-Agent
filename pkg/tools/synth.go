package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

const ToolMusic = "generate_music_synth"

var (
	musicStyles = []string{"lofi", "ambient", "edm", "cinematic", "chiptune", "synthwave", "trap", "pop", "piano", "persian", "none"}
	musicScales = []string{"major", "minor", "dorian", "phrygian", "lydian", "mixolydian", "harmonic_minor", "pentatonic_major",
		"pentatonic_minor", "blues", "hijaz", "mahur", "shur", "homayoun", "chahargah", "segah", "isfahan"}
	musicKeys = []string{"style", "bpm", "key", "scale", "chords", "melody", "drums", "bass", "lead", "pad", "arp", "energy", "meter", "sfx",
		"seed", "fade_in", "fade_out", "title"}
)

func (r *Registry) registerMusicTool() {
	if !r.check("python3", "-I", "-c", "import numpy") {
		return
	}
	r.add(ToolMusic,
		"Compose music or sound effects as an MP3: a style sets tempo, instruments and mix; set any of BPM, key, scale (Persian "+
			"dastgahs too), chords, melody, instruments and timed sound effects.",
		map[string]any{
			"style":    enumProp("Default lofi; none: effects only", musicStyles...),
			"duration": prop("number", "Seconds (2–180, default 30)"),
			"bpm":      prop("integer", "40–220"),
			"key":      prop("string", "e.g. 'A', 'F#', 'Bb'"),
			"scale":    enumProp("", musicScales...),
			"chords":   prop("string", "'Am F C G' (m, 7, maj7, m7, sus2, sus4, add9, dim, 5) or 'i VI III VII'; 'Am:2' sets beats"),
			"melody":   prop("string", "'E5:1 D5:0.5 R:1 Ek5:1' (note+octave:beats; R rest; # b, k koron, s sori); repeats; default: from the chords"),
			"drums":    enumProp("", "four_on_floor", "boom_bap", "pop", "synthwave", "trap", "breakbeat", "half_time", "chip", "epic", "daf", "none"),
			"bass":     enumProp("", "lofi", "offbeat", "eighths", "pop", "chip", "long", "piano", "808", "drone", "none"),
			"lead": enumProp("Melody instrument", "epiano", "piano", "bell", "pluck", "santur", "flute", "square", "saw", "supersaw",
				"strings", "sine", "triangle", "none"),
			"pad":    enumProp("Chord instrument", "warm", "air", "strings", "supersaw", "dark", "piano", "none"),
			"arp":    enumProp("", "up", "updown", "none"),
			"energy": enumProp("", "low", "medium", "high"),
			"meter":  prop("integer", "3, 4 or 6 (6/8)"),
			"sfx": map[string]any{"type": "array", "description": "{type: whoosh|swoosh|riser|impact|hit|boom|click|pop|ding|glitch|typing, " +
				"at: seconds, duration?, volume?: 0–1.5}", "items": map[string]any{"type": "object"}},
			"seed":     prop("integer", "Another variation"),
			"format":   enumProp("", "mp3", "wav"),
			"filename": prop("string", ""),
			"title":    prop("string", "MP3 title tag"),
		}, nil, r.musicSynth)
}

func (r *Registry) musicSynth(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	style := strArg(args, "style")
	if style == "" {
		style = "lofi"
	}
	ext := ".mp3"
	if strArg(args, "format") == "wav" || !r.downloaderInfo().FFmpeg {
		ext = ".wav"
	}
	stem := strings.TrimSuffix(sanitizeBase(strArg(args, "filename")), path.Ext(strArg(args, "filename")))
	if stem == "" {

		name := strings.Title(style)
		if t := cleanTitle(strArg(args, "title")); t != "" {
			name = t
		}
		stem = "Macron_AI_-_" + sanitizeBase(name)
	}
	out := stem + ext
	summary, err := r.synthesize(ctx, sessionID, args, numArg(args, "duration", 30, 2, 180), out)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	info, err := r.Machine.Stat(sessionID, out)
	if err != nil {
		return "error: no audio was made", nil
	}
	return fmt.Sprintf("Made %q (%s): %s. %s.", out, humanSize(info.Size()), summary, SendNote), nil
}

func (r *Registry) synthesize(ctx context.Context, sessionID string, opts map[string]any, duration float64, out string) (string, error) {
	cfg := map[string]any{"duration": duration, "out": out}
	for _, k := range musicKeys {
		if v, ok := opts[k]; ok && v != nil {
			cfg[k] = v
		}
	}
	res, err := r.runScript(ctx, sessionID, "synth.py", cfg, 2*time.Minute)
	if err != nil {
		return "", err
	}
	var info struct {
		Style   string   `json:"style"`
		BPM     int      `json:"bpm"`
		Key     string   `json:"key"`
		Scale   string   `json:"scale"`
		Meter   string   `json:"meter"`
		Chords  []string `json:"chords"`
		Bars    int      `json:"bars"`
		Seconds float64  `json:"seconds"`
		Layers  []string `json:"layers"`
		SFX     int      `json:"sfx"`
		Error   string   `json:"error"`
	}
	if res.ExitCode != 0 || json.Unmarshal([]byte(lastLine(res.Stdout)), &info) != nil {
		return "", errors.New(failure("synthesis", res))
	}
	if info.Error != "" {
		return "", errors.New(info.Error)
	}
	s := fmt.Sprintf("%s, %d BPM, %s %s, %s, %.0f s", info.Style, info.BPM, info.Key, strings.ReplaceAll(info.Scale, "_", " "), info.Meter, info.Seconds)
	if len(info.Chords) > 0 && info.Style != "none" {
		s += ", chords " + strings.Join(info.Chords, " ")
	}
	if len(info.Layers) > 0 {
		s += ", with " + strings.Join(info.Layers, ", ")
	}
	if info.SFX > 0 {
		s += fmt.Sprintf(" and %d sound effects", info.SFX)
	}
	return s, nil
}
