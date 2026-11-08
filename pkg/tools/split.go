package tools

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

const (
	ToolSplitVideo = "split_video"

	splitMaxParts  = 20
	splitLongVideo = 10 * 60
)

func (r *Registry) registerSplitTool() {
	r.add(ToolSplitVideo,
		"Split a workspace video or audio into parts that fit Telegram (≤"+limitText()+"), fast and without quality loss.",
		map[string]any{
			"filename": prop("string", ""),
			"part_mb":  prop("integer", fmt.Sprintf("5–%d, default %d", partMaxMB(), telegramPartBytes>>20)),
			"minutes":  prop("number", "Or minutes per part"),
		}, []string{"filename"}, r.splitVideo)
}

func (r *Registry) splitVideo(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	name := strArg(args, "filename")
	info, err := r.Machine.Stat(sessionID, name)
	if name == "" || err != nil || !info.Mode().IsRegular() {
		return fmt.Sprintf("error: file %q not found in the workspace", name), nil
	}
	if !r.downloaderInfo().FFmpeg {
		return "error: splitting needs ffmpeg, which is not installed", nil
	}
	limit := int64(intArg(args, "part_mb", int(telegramPartBytes>>20), 5, partMaxMB())) << 20
	var every float64
	if m, err := strconv.ParseFloat(strArg(args, "minutes"), 64); err == nil && m > 0 {
		every = max(m*60, 5)
		limit = dlSendLimit
	}
	f := dlFile{name: name, size: info.Size()}
	parts, err := r.splitMedia(ctx, sessionID, f, limit, every)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Split %q (%s) into %d parts without re-encoding; %s:\n", name, humanSize(f.size), len(parts), SendNotePlural)
	for _, p := range parts {
		fmt.Fprintf(&sb, "- %q (%s)%s\n", p.name, humanSize(p.size), p.about)
	}
	return strings.TrimSpace(sb.String()), nil
}

func (r *Registry) mediaLength(ctx context.Context, sessionID, name string) float64 {
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: []string{"ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "csv=p=0", "./" + name}, Timeout: 30 * time.Second})
	dur, _ := strconv.ParseFloat(strings.TrimSpace(res.Stdout), 64)
	return dur
}

func (r *Registry) splitMedia(ctx context.Context, sessionID string, f dlFile, limit int64, every float64) ([]dlFile, error) {
	dur := f.duration
	if dur <= 0 {
		dur = r.mediaLength(ctx, sessionID, f.name)
	}
	if dur <= 0 {
		return nil, fmt.Errorf("the length of %q is unknown, so it cannot be split", f.name)
	}
	seg := every
	if seg <= 0 {
		seg = dur * float64(limit) * 0.9 / float64(max(f.size, 1))
	}
	seg = max(seg, 5)
	if int(dur/seg)+1 > splitMaxParts {
		return nil, fmt.Errorf("it would make more than %d parts; ask for a section, lower quality or audio", splitMaxParts)
	}
	ext := path.Ext(f.name)
	stem := strings.TrimSuffix(f.name, ext)
	pattern := strings.ReplaceAll(stem, "%", "%%") + " - part %02d" + ext
	partName := func(i int) string { return fmt.Sprintf("%s - part %02d%s", stem, i, ext) }
	for try := 0; try < 3 && ctx.Err() == nil; try++ {

		for i := 1; i <= splitMaxParts+1; i++ {
			_ = r.Machine.Remove(sessionID, partName(i))
		}
		argv := []string{"ffmpeg", "-y", "-v", "error", "-i", "./" + f.name, "-map", "0:v?", "-map", "0:a?", "-map", "0:s?", "-c", "copy",
			"-f", "segment", "-segment_time", strconv.FormatFloat(seg, 'f', 2, 64), "-segment_start_number", "1", "-reset_timestamps", "1"}
		if strings.EqualFold(ext, ".mp4") || strings.EqualFold(ext, ".m4a") || strings.EqualFold(ext, ".mov") {
			argv = append(argv, "-segment_format_options", "movflags=+faststart")
		}
		argv = append(argv, "./"+pattern)
		res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: argv, Timeout: 5 * time.Minute})
		var parts []dlFile
		tooBig := false
		for i := 1; i <= splitMaxParts+1; i++ {
			name := partName(i)
			info, err := r.Machine.Stat(sessionID, name)
			if err != nil {
				break
			}
			tooBig = tooBig || info.Size() > limit
			parts = append(parts, dlFile{name: name, size: info.Size(), title: f.title, uploader: f.uploader})
		}
		if res.ExitCode == 0 && len(parts) > 0 && !tooBig {
			start := 0.0
			for i := range parts {
				end := min(start+seg, dur)
				if i == len(parts)-1 {
					end = dur
				}
				parts[i].about = fmt.Sprintf(" — part %d of %d, about %s–%s", i+1, len(parts), clock(start), clock(end))
				start = end
			}
			return parts, nil
		}
		for _, p := range parts {
			_ = r.Machine.Remove(sessionID, p.name)
		}
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("ffmpeg could not split it: %s", tail(lastLine(res.Stderr), 200))
		}
		if every > 0 {
			return nil, fmt.Errorf("parts of %s are larger than %s; ask for shorter parts", clock(every), limitText())
		}
		seg *= 0.7
	}
	return nil, fmt.Errorf("could not make parts under %s", humanSize(limit))
}
