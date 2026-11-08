package tools

import (
	"context"
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

const (
	VoiceSuffix = ".voice.ogg"
	RoundSuffix = ".round.mp4"

	videoNoteSide    = 480
	videoNoteSeconds = 60
	defaultTargetMB  = 45
)

func NativeKind(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.HasSuffix(l, VoiceSuffix) || strings.HasSuffix(l, ".oga"):
		return "voice"
	case strings.HasSuffix(l, RoundSuffix):
		return "video_note"
	}
	return ""
}

func nativeName(out, in, suffix string) string {
	base := sanitizeBase(out)
	if base == "" {
		base = path.Base(in)
	}
	for _, s := range []string{VoiceSuffix, RoundSuffix} {
		base = strings.TrimSuffix(base, s)
	}
	base = strings.TrimSuffix(base, path.Ext(base))
	if dir := path.Dir(out); out != "" && dir != "." && !strings.HasPrefix(dir, "..") {
		base = path.Join(dir, base)
	}
	return base + suffix
}

func ffmpegArgs(extra ...string) []string {
	return append([]string{"ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y"}, extra...)
}

func (r *Registry) telegramMedia(ctx context.Context, sessionID, in, out, action string, args map[string]any) (string, error) {
	switch action {
	case "to_voice":
		name := nativeName(out, in, VoiceSuffix)
		res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: ffmpegArgs("-i", "./"+in, "-vn", "-map_metadata", "-1", "-ac", "1",
			"-ar", "48000", "-c:a", "libopus", "-b:a", "64k", "-vbr", "on", "-application", "voip", "./"+name),
			Timeout: 6 * time.Minute, Heavy: true})
		if res.ExitCode != 0 {
			return failure("Making the voice message", res), nil
		}
		return r.mediaDone(sessionID, name, "a Telegram voice message (OGG/Opus, sent as a voice message)")
	case "to_video_note":
		name := nativeName(out, in, RoundSuffix)
		side := strconv.Itoa(videoNoteSide)
		vf := "crop='min(iw,ih)':'min(iw,ih)',scale=" + side + ":" + side + ":flags=lanczos,setsar=1,fps=30"
		res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: ffmpegArgs("-i", "./"+in, "-t", strconv.Itoa(videoNoteSeconds),
			"-vf", vf, "-c:v", "libx264", "-preset", "veryfast", "-crf", "26", "-pix_fmt", "yuv420p", "-profile:v", "main",
			"-c:a", "aac", "-b:a", "96k", "-ac", "1", "-movflags", "+faststart", "./"+name),
			Timeout: 6 * time.Minute, Heavy: true})
		if res.ExitCode != 0 {
			return failure("Making the video message", res), nil
		}
		note := "a round Telegram video message (" + side + "×" + side + ", centre crop"
		if r.mediaLength(ctx, sessionID, in) > videoNoteSeconds {
			note += ", the first minute"
		}
		return r.mediaDone(sessionID, name, note+")")
	case "compress_video":
		return r.compressVideo(ctx, sessionID, in, out, intArg(args, "target_mb", defaultTargetMB, 5, 2000))
	}
	return fmt.Sprintf("unsupported action %q", action), nil
}

func (r *Registry) mediaDone(sessionID, name, what string) (string, error) {
	info, err := r.Machine.Stat(sessionID, name)
	if err != nil {
		return "ffmpeg finished but produced no output file", nil
	}
	return fmt.Sprintf("Made %s: %s (%s). %s.", what, name, humanSize(info.Size()), SendNote), nil
}

func (r *Registry) compressVideo(ctx context.Context, sessionID, in, out string, targetMB int) (string, error) {
	info, err := r.Machine.Stat(sessionID, in)
	if err != nil {
		return "error: " + friendlyFSError(err), nil
	}
	dur := r.mediaLength(ctx, sessionID, in)
	if dur <= 0 {
		return fmt.Sprintf("error: %s does not look like a video (its length is unknown)", in), nil
	}
	name := sanitizeBase(out)
	if strings.ToLower(path.Ext(name)) != ".mp4" {
		name = strings.TrimSuffix(path.Base(in), path.Ext(in)) + "_small.mp4"
		if out != "" {
			name = strings.TrimSuffix(sanitizeBase(out), path.Ext(out)) + ".mp4"
		}
	}
	if name == in {
		name = strings.TrimSuffix(name, ".mp4") + "_small.mp4"
	}
	target := float64(targetMB) * 1024 * 1024
	totalKbps := target * 8 / 1000 / dur * 0.94
	audio := 96.0
	if totalKbps < 600 {
		audio = 48
	}
	video := math.Max(totalKbps-audio, 120)
	for attempt := 0; attempt < 2; attempt++ {
		width := 1920
		switch {
		case video < 700:
			width = 854
		case video < 1600:
			width = 1280
		}
		crf := "23"
		if attempt > 0 {
			crf = "28"
		}
		vf := fmt.Sprintf("scale='if(gt(iw,%d),%d,iw)':-2", width, width)
		res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: ffmpegArgs("-i", "./"+in, "-vf", vf, "-c:v", "libx264",
			"-preset", "veryfast", "-crf", crf, "-maxrate", fmt.Sprintf("%.0fk", video), "-bufsize", fmt.Sprintf("%.0fk", 2*video),
			"-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", fmt.Sprintf("%.0fk", audio), "-movflags", "+faststart", "./"+name),
			Timeout: 15 * time.Minute, Heavy: true, Tee: outputTee(ctx)})
		if res.ExitCode != 0 {
			return failure("Compressing the video", res), nil
		}
		got, err := r.Machine.Stat(sessionID, name)
		if err != nil {
			return "ffmpeg finished but produced no output file", nil
		}
		if float64(got.Size()) <= target*1.03 || attempt == 1 {
			note := ""
			if float64(got.Size()) > target*1.03 {
				note = fmt.Sprintf(" — still above %d MB; split_video can cut it into parts", targetMB)
			}
			return fmt.Sprintf("Compressed %s: %s → %s (%s, at most %dp wide)%s. "+SendNote+".", in, humanSize(info.Size()),
				humanSize(got.Size()), name, width, note), nil
		}
		video *= 0.75 * target / float64(got.Size())
	}
	return "", nil
}
