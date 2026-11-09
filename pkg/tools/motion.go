package tools

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

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
	if a := strArg(args, "audio"); a != "" {
		if info, err := r.Machine.Stat(sessionID, a); err != nil || !info.Mode().IsRegular() {
			return fmt.Sprintf("error: audio %q is not a file in the workspace", a), nil
		}
		cfg["audio"] = a
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
	var sb strings.Builder
	fmt.Fprintf(&sb, "Rendered %q — %dx%d, %s at %d fps (%d frames), %s, in %.0f s. "+SendNote+".\n",
		out, size[0], size[1], strconv.FormatFloat(dur, 'f', -1, 64)+" s", fps, res.Frames, humanSize(info.Size()), res.Seconds)
	fmt.Fprintf(&sb, "Preview of 4 frames: %s (view_image it to check the design).\n", cfg["preview"])
	if res.Still {
		sb.WriteString("WARNING: every frame looks the same — nothing moved. Animate with requestAnimationFrame, CSS animations or GSAP and render again.\n")
	}
	if len(res.Errors) > 0 {
		sb.WriteString("Script errors on the page (fix them and render again): " + strings.Join(res.Errors, " | ") + "\n")
	}
	return strings.TrimSpace(sb.String()), nil
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
