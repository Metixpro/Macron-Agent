package tools

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	ToolMotion       = "create_motion_graphic"
	motionMaxSeconds = 60
	motionMaxFrames  = 1800
	motionTimeout    = 5 * time.Minute
)

var motionSizes = map[string][2]int{"landscape": {1280, 720}, "hd": {1920, 1080}, "square": {1080, 1080}, "vertical": {1080, 1920},
	"portrait": {1080, 1350}, "small": {854, 480}}

const motionPage = `<!doctype html>
<html lang="fa"><head><meta charset="utf-8">
<style>html,body{margin:0;width:100%%;height:100%%;overflow:hidden;background:%s;color:#fff;font-family:Vazirmatn,"Noto Sans Arabic","DejaVu Sans",sans-serif}</style>
</head><body>
%s
</body></html>
`

func (r *Registry) registerMotionTool() {
	libs := "Libraries load from their CDN URLs."
	if names := r.browser.webLibs(); len(names) > 0 {
		libs = "Offline (usual CDN URL or 'lib/<file>'): " + libNames(names) + "; ES modules via an importmap."
	}
	r.add(ToolMotion,
		"Render an animation video (MP4) from HTML/CSS/JS you write (Canvas, SVG, CSS, GSAP, Three.js…). Time is virtual, so "+
			"every frame is exact (rAF, timers, CSS and GSAP start at 0 on load). Fill 100vw×100vh, large type; Persian font Vazirmatn. "+
			libs+" On errors or a still picture, fix and render again (same filename).",
		map[string]any{
			"html":       prop("string", "A full HTML document, or body with its <style> and <script>"),
			"file":       prop("string", "Or a workspace .html"),
			"duration":   prop("number", "Seconds (1–60, default 6)"),
			"fps":        prop("integer", "24, 30 (default) or 60"),
			"size":       enumProp("1280x720 (default), 1920x1080, 1080x1080, 1080x1920 (reels), 1080x1350, 854x480", "landscape", "hd", "square", "vertical", "portrait", "small"),
			"background": prop("string", "For body-only html (default #000)"),
			"audio":      prop("string", "A workspace audio file"),
			"music":      map[string]any{"type": "object", "description": "ai: style tags ('epic orchestral') for real music from the song service; else " + ToolMusic + "'s options (style, bpm, sfx…)"},
			"filename":   prop("string", "Default 'motion'"),
		}, nil, r.motionGraphic)
}

func numArg(args map[string]any, key string, def, lo, hi float64) float64 {
	v, err := strconv.ParseFloat(strArg(args, key), 64)
	if err != nil {
		v = def
	}
	return min(max(v, lo), hi)
}

func (r *Registry) motionGraphic(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	if !r.downloaderInfo().FFmpeg {
		return "error: rendering video needs ffmpeg, which is not installed", nil
	}
	html, file := rawStrArg(args, "html"), strArg(args, "file")
	if strings.TrimSpace(html) == "" && file == "" {
		return "error: give the animation as html (or an .html file)", nil
	}
	size, ok := motionSizes[strArg(args, "size")]
	if !ok && strArg(args, "size") != "" {
		return "error: size must be landscape, hd, square, vertical, portrait or small", nil
	}
	if !ok {
		size = motionSizes["landscape"]
	}
	dur := numArg(args, "duration", 6, 1, motionMaxSeconds)
	fps := intArg(args, "fps", 30, 12, 60)
	if frames := int(dur*float64(fps) + 0.5); frames > motionMaxFrames {
		return fmt.Sprintf("error: %d frames is too many (max %d): shorten it or lower fps", frames, motionMaxFrames), nil
	}
	stem := strings.TrimSuffix(sanitizeBase(strArg(args, "filename")), path.Ext(strArg(args, "filename")))
	if stem == "" {
		stem = "motion"
	}
	out := stem + ".mp4"
	page := file
	if strings.TrimSpace(html) != "" {
		low := strings.ToLower(html)
		if !strings.Contains(low, "<html") && !strings.Contains(low, "<!doctype") {
			bg := strings.NewReplacer("<", "", ">", "", ";", "", "}", "").Replace(textutil.TruncateRunes(strArg(args, "background"), 120, ""))
			if bg == "" {
				bg = "#000"
			}
			html = fmt.Sprintf(motionPage, bg, html)
		}
		page = "work/" + stem + ".html"
		if err := r.Machine.WriteFile(sessionID, page, []byte(html)); err != nil {
			return "error: " + friendlyFSError(err), nil
		}
	} else if info, err := r.Machine.Stat(sessionID, file); err != nil || !info.Mode().IsRegular() {
		return fmt.Sprintf("error: %q is not a file in the workspace", file), nil
	}

	cfg := map[string]any{"page": page, "width": size[0], "height": size[1], "fps": fps, "duration": dur, "out": out,
		"preview": "work/" + stem + "_preview.jpg"}
	var notes []string
	var (
		ai     <-chan motionMusic
		silent bool
		aiTags string
		aiOpts map[string]any
	)
	mctx, mcancel := context.WithCancel(ctx)
	defer mcancel()
	if a := strArg(args, "audio"); a != "" {
		if info, err := r.Machine.Stat(sessionID, a); err != nil || !info.Mode().IsRegular() {
			return fmt.Sprintf("error: audio %q is not a file in the workspace", a), nil
		}
		cfg["audio"] = a
	} else if m, ok := args["music"].(map[string]any); ok {
		music := "work/" + stem + "_music.mp3"
		if tags := cleanStyleText(strArg(m, "ai")); tags != "" {

			if ai = r.startMotionMusic(mctx, sessionID, tags, stem); ai == nil {
				notes = append(notes, "the song service is not available here (or this request's downloads are used up), so the music is a synthesizer's")
			} else {
				cfg["out"], silent = stem+"_silent.mp4", true
				aiTags, aiOpts = tags, m
			}
		}
		if ai == nil {
			opts := m
			if strArg(m, "ai") != "" && strArg(m, "style") == "" {
				opts = map[string]any{"style": synthStyleFor(strArg(m, "ai"))}
				for k, v := range m {
					if k != "ai" {
						opts[k] = v
					}
				}
			}
			if summary, err := r.synthesize(ctx, sessionID, opts, dur, music); err != nil {
				notes = append(notes, "the music could not be made ("+err.Error()+"), so the video is silent")
			} else {
				cfg["audio"] = music
				notes = append(notes, "music: "+summary)
			}
		}
	}

	var res struct {
		Frames  int      `json:"frames"`
		Seconds float64  `json:"seconds"`
		Still   bool     `json:"still"`
		Errors  []string `json:"errors"`
		Error   string   `json:"error"`
	}
	if err := r.runBrowser(ctx, sessionID, "motion.py", cfg, true, motionTimeout, &res); err != nil {
		return "error: " + err.Error(), nil
	}
	rendered := out
	if silent {
		rendered = stem + "_silent.mp4"
	}
	info, err := r.Machine.Stat(sessionID, rendered)
	if res.Error != "" || err != nil || info.Size() == 0 {
		msg := res.Error
		if msg == "" {
			msg = "no video was made"
		}
		if len(res.Errors) > 0 {
			msg += ". Script errors: " + strings.Join(res.Errors, " | ")
		}
		return "error: " + msg, nil
	}
	if silent {
		notes = append(notes, r.addMotionMusic(ctx, sessionID, ai, aiTags, aiOpts, rendered, out, stem, dur))
		if info, err = r.Machine.Stat(sessionID, out); err != nil {
			return "error: the video was lost while the music was added", nil
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Rendered %q — %dx%d, %s at %d fps (%d frames), %s, in %.0f s. "+SendNote+".\n",
		out, size[0], size[1], strconv.FormatFloat(dur, 'f', -1, 64)+" s", fps, res.Frames, humanSize(info.Size()), res.Seconds)
	fmt.Fprintf(&sb, "Preview of 4 frames: %s (view_image it to check the design).\n", cfg["preview"])
	for _, n := range notes {
		sb.WriteString("Note: " + n + "\n")
	}
	if res.Still {
		sb.WriteString("WARNING: every frame looks the same — nothing moved. Animate with requestAnimationFrame, CSS animations or GSAP and render again.\n")
	}
	if len(res.Errors) > 0 {
		sb.WriteString("Script errors on the page (fix them and render again): " + strings.Join(res.Errors, " | ") + "\n")
	}
	return strings.TrimSpace(sb.String()), nil
}

type motionMusic struct {
	file    string
	seconds float64
	err     error
}

func (r *Registry) startMotionMusic(ctx context.Context, sessionID, tags, stem string) <-chan motionMusic {
	if !r.SongReady() {
		return nil
	}
	if n, _ := take(ctx, kindDownloads, 1); n == 0 {
		return nil
	}
	ch := make(chan motionMusic, 1)
	go func() {
		files, seconds, err := r.makeSong(ctx, sessionID, instrumentalLyrics, styleTitle(tags), instrumentalStyle(tags), "", "work/"+stem+"_ai_music", 1, false)
		if err != nil || len(files) == 0 {
			giveBack(ctx, kindDownloads, 1)
			if err == nil {
				err = errors.New("no music came")
			}
			ch <- motionMusic{err: err}
			return
		}
		ch <- motionMusic{file: files[0].name, seconds: seconds}
	}()
	return ch
}

func (r *Registry) addMotionMusic(ctx context.Context, sessionID string, ai <-chan motionMusic, tags string, opts map[string]any, silent, out, stem string, dur float64) string {
	ReportStatus(ctx, "motion · music")
	audio, said := "", ""
	select {
	case m := <-ai:
		if m.err == nil {
			audio, said = m.file, "music: a real instrumental from the song service ("+tags+"), cut to the video and faded"
			break
		}
		log.Printf("create_motion_graphic: the song service gave no music: %v", m.err)
		said = "the song service gave no music (" + textutil.TruncateRunes(m.err.Error(), 120, "…") + "), so it is a synthesizer's: "
	case <-ctx.Done():
		said = "the music was stopped, so "
	}
	if audio == "" && ctx.Err() == nil {
		synth := map[string]any{"style": synthStyleFor(tags)}
		for k, v := range opts {
			if k != "ai" {
				synth[k] = v
			}
		}
		music := "work/" + stem + "_music.mp3"
		if summary, err := r.synthesize(ctx, sessionID, synth, dur, music); err == nil {
			audio, said = music, said+summary
		} else {
			said += "the synthesizer failed too (" + err.Error() + ")"
		}
	}
	if audio != "" {
		if err := r.muxAudio(ctx, sessionID, silent, audio, out, dur); err == nil {
			_ = r.Machine.RemoveAll(sessionID, silent)
			return said
		} else {
			said = "the music could not be put under the video (" + err.Error() + ")"
		}
	}
	if err := r.Machine.Rename(sessionID, silent, out); err != nil {
		return said + "; and the video could not be saved: " + friendlyFSError(err)
	}
	return said + "; the video is silent"
}

func (r *Registry) muxAudio(ctx context.Context, sessionID, video, audio, out string, dur float64) error {
	fade := min(1.5, dur/4)
	in := min(0.5, dur/8)
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{
		Args: []string{"ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", "./" + video, "-i", "./" + audio,
			"-map", "0:v", "-map", "1:a", "-c:v", "copy",
			"-af", fmt.Sprintf("afade=t=in:st=0:d=%.3f,apad,afade=t=out:st=%.3f:d=%.3f", in, dur-fade, fade),
			"-c:a", "aac", "-b:a", "192k", "-t", fmt.Sprintf("%.3f", dur), "-movflags", "+faststart", "./" + out},
		Timeout: 3 * time.Minute,
	})
	if res.ExitCode != 0 {
		return errors.New(textutil.TruncateRunes(strings.TrimSpace(res.Stderr), 200, "…"))
	}
	if fi, err := r.Machine.Stat(sessionID, out); err != nil || fi.Size() == 0 {
		return errors.New("no file was made")
	}
	return nil
}

func synthStyleFor(tags string) string {
	low := strings.ToLower(tags)
	for _, c := range []struct{ style, words string }{
		{"persian", "persian santur tar daf kamancheh dastgah iranian"},
		{"synthwave", "synthwave retrowave outrun 80s"},
		{"chiptune", "chiptune 8-bit 8bit chip retro-game"},
		{"cinematic", "cinematic orchestral epic trailer film score"},
		{"lofi", "lo-fi lofi chill study"},
		{"trap", "trap drill hip-hop hiphop"},
		{"edm", "edm house techno dance electro club"},
		{"ambient", "ambient atmospheric drone meditation relax"},
		{"piano", "piano"},
		{"pop", "pop"},
	} {
		for _, w := range strings.Fields(c.words) {
			if strings.Contains(low, w) {
				return c.style
			}
		}
	}
	return "lofi"
}

func libNames(files []string) string {
	var names []string
	plugins := 0
	for _, f := range files {
		base := strings.TrimSuffix(strings.TrimSuffix(f, ".js"), ".mjs")
		base = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(base, ".min"), ".umd"), ".browser")
		switch {
		case strings.HasSuffix(base, "Plugin") || base == "CustomEase" || base == "SplitText":
			plugins++
		case strings.HasSuffix(base, ".module"):
		default:
			names = append(names, base)
		}
	}
	for i, n := range names {
		if n == "gsap" && plugins > 0 {
			names[i] = fmt.Sprintf("gsap (+%d plugins)", plugins)
		}
	}
	return strings.Join(names, ", ")
}
