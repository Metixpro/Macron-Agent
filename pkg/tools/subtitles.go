package tools

import (
	"context"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

// Subtitles come with a download in four ways: as SRT files next to the
// video ("file"), alone ("only"), embedded in the MP4 as a track players can
// switch on ("embed"), or burned into the picture ("burn"), which Telegram's
// own player shows but which re-encodes the video.

const (
	burnMaxSeconds = 20 * 60
	burnFile       = ".burn.srt"
)

var subLangsRe = regexp.MustCompile(`^[a-zA-Z]{2,3}(-[a-zA-Z0-9]{2,8})?(,[a-zA-Z]{2,3}(-[a-zA-Z0-9]{2,8})?){0,3}$`)

func isSubtitle(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return ext == ".srt" || ext == ".vtt"
}

// subLang is the language code of "Video.fa.srt".
func subLang(name string) string {
	return strings.TrimPrefix(path.Ext(strings.TrimSuffix(name, path.Ext(name))), ".")
}

// subtitleArgs asks yt-dlp for the subtitles, written or automatic.
func (d *DownloaderInfo) subtitleArgs(q dlRequest) []string {
	if q.subs == "" {
		return nil
	}
	a := []string{"--write-subs", "--write-auto-subs", "--sub-langs", q.subLangs}
	if d.FFmpeg {
		a = append(a, "--convert-subs", "srt")
	}
	switch {
	case q.subs == "only":
		a = append(a, "--skip-download")
	case q.subs == "embed" && d.FFmpeg && !q.audio:
		a = append(a, "--embed-subs")
	}
	return a
}

// subtitleFiles lists the subtitles a download wrote.
func (r *Registry) subtitleFiles(sessionID string, before map[string]string) []dlFile {
	var out []dlFile
	for name, sig := range r.rootSnapshot(sessionID) {
		if before[name] == sig || !isSubtitle(name) || strings.HasPrefix(name, ".") {
			continue
		}
		if info, err := r.Machine.Stat(sessionID, name); err == nil {
			out = append(out, dlFile{name: name, size: info.Size(), about: " — subtitles (" + subLang(name) + ")"})
		}
	}
	slices.SortFunc(out, func(a, b dlFile) int { return strings.Compare(a.name, b.name) })
	return out
}

// burnSubs draws the subtitles of the first wanted language into the video.
func (r *Registry) burnSubs(ctx context.Context, sessionID string, files []dlFile, langs string) ([]dlFile, string) {
	vi := slices.IndexFunc(files, func(f dlFile) bool { return !isSubtitle(f.name) })
	var subs []int
	for i, f := range files {
		if isSubtitle(f.name) {
			subs = append(subs, i)
		}
	}
	if vi < 0 || len(subs) == 0 {
		return files, ""
	}
	pick := subs[0]
	for _, l := range slices.Backward(strings.Split(langs, ",")) {
		for _, i := range subs {
			if strings.EqualFold(subLang(files[i].name), l) {
				pick = i
			}
		}
	}
	video, sub := files[vi], files[pick]
	dur := video.duration
	if dur <= 0 {
		dur = r.mediaLength(ctx, sessionID, video.name)
	}
	if dur > burnMaxSeconds {
		return files, "the video is longer than 20 minutes, too long to burn subtitles in; they stay separate files (a player like VLC shows them)"
	}
	data, _, err := r.Machine.ReadFile(sessionID, sub.name, 8<<20)
	if err != nil || r.Machine.WriteFile(sessionID, burnFile, data) != nil {
		return files, "the subtitles could not be burned in; they stay separate files"
	}
	defer r.Machine.Remove(sessionID, burnFile)
	out := strings.TrimSuffix(video.name, path.Ext(video.name)) + " (subtitled).mp4"
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: []string{"ffmpeg", "-y", "-v", "error", "-i", "./" + video.name,
		"-vf", "subtitles=" + burnFile, "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart", "./" + out}, Timeout: 8 * time.Minute, Heavy: true})
	info, err := r.Machine.Stat(sessionID, out)
	if res.ExitCode != 0 || err != nil || info.Size() == 0 {
		_ = r.Machine.Remove(sessionID, out)
		return files, "the subtitles could not be burned in (" + tail(lastLine(res.Stderr), 120) + "); they stay separate files"
	}
	var kept []dlFile
	for i, f := range files {
		if i == vi || isSubtitle(f.name) {
			_ = r.Machine.Remove(sessionID, f.name)
			continue
		}
		kept = append(kept, f)
	}
	video.name, video.size, video.duration = out, info.Size(), dur
	video.about = " — " + subLang(sub.name) + " subtitles burned into the picture"
	return append([]dlFile{video}, kept...), ""
}
