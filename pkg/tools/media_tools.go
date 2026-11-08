package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const wikimediaUA = "Waze-Agent/1.0 (+https://github.com/Metixpro/macron-agent)"

var (
	imageExts = []string{".jpg", ".jpeg", ".png", ".webp"}
	audioOut  = []string{".mp3", ".m4a", ".wav", ".ogg", ".flac"}
	mediaIn   = []string{".mp3", ".m4a", ".wav", ".ogg", ".flac", ".aac", ".mp4", ".mov", ".mkv", ".webm", ".avi", ".gif"}
	clockRe   = regexp.MustCompile(`^(\d{1,2}:){0,2}\d{1,2}(\.\d+)?$|^\d+(\.\d+)?$`)
)

func (r *Registry) registerMediaTools() {
	r.add("generate_image",
		"Make an AI picture from a detailed English prompt.",
		map[string]any{
			"prompt":       prop("string", ""),
			"style":        prop("string", "default realistic; anime, 3d_render, cyberpunk, oil_painting, pixel, lego, neon… (76)"),
			"aspect_ratio": enumProp("", "1:1", "16:9", "9:16", "4:3", "3:4", "21:9"),
			"count":        prop("integer", "2 = two options"),
			"filename":     prop("string", ""),
		}, []string{"prompt"}, r.imageTool)

	r.add("search_image",
		"Real photos from the web (Bing Images, Openverse, Wikimedia): places, people, products, cars, films, anime, wallpapers.",
		map[string]any{
			"query":    prop("string", "In English, e.g. 'BMW M4 2024 4k wallpaper'"),
			"count":    prop("integer", "≤6"),
			"filename": prop("string", ""),
		}, []string{"query"}, r.searchImage)

	r.add("text_to_speech",
		"Text to natural speech (MP3).",
		map[string]any{
			"text": prop("string", "≤4000 characters"),
			"voice": enumProp("Persian: mana (natural local voice, default), farid (man), dilara (woman); English: christopher, jenny",
				"mana", "farid", "dilara", "christopher", "jenny"),
			"speed":    prop("number", "0.5 slow … 2 fast, default 1"),
			"as_voice": prop("boolean", "A voice message, not an MP3"),
			"filename": prop("string", ""),
		}, []string{"text"}, r.textToSpeech)

	r.add("generate_chart",
		"A chart PNG (Persian works).",
		map[string]any{
			"chart_type": enumProp("", "bar", "hbar", "stacked_bar", "line", "area", "scatter", "pie", "donut"),
			"title":      prop("string", ""),
			"labels":     arrayProp("string", ""),
			"values":     arrayProp("number", "one series"),
			"series": map[string]any{"type": "array", "description": "or several, to compare", "items": map[string]any{"type": "object",
				"properties": map[string]any{"name": prop("string", ""), "values": arrayProp("number", "")}}},
			"x_label":  prop("string", ""),
			"y_label":  prop("string", ""),
			"unit":     prop("string", "%, تومان…"),
			"theme":    enumProp("", "dark", "light"),
			"filename": prop("string", ""),
		}, []string{"title", "labels"}, r.generateChart)

	r.add("generate_qr",
		"A QR code PNG.",
		map[string]any{
			"data":     prop("string", "≤1500 characters"),
			"filename": prop("string", ""),
		}, []string{"data"}, r.generateQR)

	r.add("media_convert",
		"Convert media with ffmpeg: GIF (30 s), audio, trim; to_voice: a voice message; to_video_note: a round video message; "+
			"compress_video: fit target_mb (default 45).",
		map[string]any{
			"input_file":  prop("string", ""),
			"output_file": prop("string", "e.g. 'out.gif', 'sound.mp3'"),
			"action":      enumProp("", "video_to_gif", "extract_audio", "convert_audio", "trim", "to_voice", "to_video_note", "compress_video"),
			"start_time":  prop("string", "trim: '00:00:10'"),
			"duration":    prop("string", "trim: seconds"),
			"target_mb":   prop("integer", ""),
		}, []string{"input_file", "action"}, r.mediaConvert)

	r.add("pdf_tool",
		"PDFs: read (30 pages), create a designed document from Markdown (tables, code, pictures as ![](chart.png)), merge, "+
			"extract_pages, delete_pages, rotate, to_images (JPG), from_images (photos or scans), compress.",
		map[string]any{
			"action":       enumProp("", "read", "create", "merge", "extract_pages", "delete_pages", "rotate", "to_images", "from_images", "compress"),
			"filename":     prop("string", "The PDF (the result of create, merge, from_images)"),
			"files":        arrayProp("string", "Inputs in order"),
			"pages":        prop("string", "e.g. '1-5, 8'"),
			"degrees":      prop("integer", ""),
			"output":       prop("string", ""),
			"text_content": prop("string", "create: the document in Markdown"),
			"title":        prop("string", "create: the cover title (dated)"),
		}, []string{"action", "filename"}, r.pdfTool)
}

func (r *Registry) imageTool(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	want := intArg(args, "count", 1, 1, 2)
	got, note := take(ctx, kindImages, want)
	if got == 0 {
		return "error: " + note, nil
	}
	out, made := r.generateImage(ctx, sessionID, args, got)
	giveBack(ctx, kindImages, got-made)
	return out, nil
}

func (r *Registry) generateImage(ctx context.Context, sessionID string, args map[string]any, n int) (string, int) {
	prompt := strArg(args, "prompt")
	switch {
	case prompt == "":
		return "error: prompt is required", 0
	case len([]rune(prompt)) > 1000:
		return "error: prompt is too long (max 1000 characters)", 0
	case len([]rune(strings.TrimSpace(prompt))) < 3:
		return "error: prompt is too short: describe the picture (at least 3 characters)", 0
	}
	styles, shapes := r.studioCatalog(ctx)
	style, ok := matchStyle(strArg(args, "style"), styles)
	if !ok {
		return fmt.Sprintf("error: unknown style %q. Styles: %s. Pick one of them, or leave style out for realistic.",
			strArg(args, "style"), styleHelp(styles)), 0
	}
	ratio := nearestShape(strArg(args, "aspect_ratio"), shapes)
	name := outputName(strArg(args, "filename"), "ai_image_"+slug(prompt, 20), imageExts, ".jpg")
	studio := r.StudioReady()
	if !studio && r.imageAPI != "" {
		noImageKeyOnce.Do(func() {
			log.Printf("generate_image: no key for the image service; set it in the admin panel (settings → AI → image making) or WAZE_IMAGE_API_KEY")
		})
	}

	parent := ctx
	taskEngine := r.imageEditAPI != "" && r.editKey() != ""
	budget := 150 * time.Second
	if taskEngine {
		budget = 330 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	done := func(files []savedImage, engine string) (string, int) {
		var sb strings.Builder
		sb.WriteString("Image created and queued for delivery.")
		for i, f := range files {
			label := "File"
			if i > 0 {
				label = "File (option " + strconv.Itoa(i+1) + ")"
			}
			fmt.Fprintf(&sb, "\n- %s: %s (%s)", label, f.name, humanSize(f.size))
		}
		fmt.Fprintf(&sb, "\n- Prompt: %s", prompt)
		if style != "" {
			fmt.Fprintf(&sb, "\n- Style: %s", style)
		}
		fmt.Fprintf(&sb, "\n- Engine: %s", engine)
		if len(files) > 1 {
			sb.WriteString("\n- Two options were made; both go to the user.")
		}
		return sb.String(), len(files)
	}

	var lastErr string
	if studio {
		for attempt := 1; attempt <= imageAttempts; attempt++ {
			if attempt > 1 {
				ReportStatus(ctx, "image service · retry")
				select {
				case <-time.After(imageRetryWait):
				case <-ctx.Done():
				}
			}
			if ctx.Err() != nil {
				break
			}
			files, err := r.imageViaStudio(ctx, sessionID, prompt, style, ratio, name, n)
			if err == nil {
				return done(files, "AI image service")
			}
			lastErr = err.Error()
			if errors.Is(err, ErrImageKey) {
				lastErr = "the service did not accept its key"
				break
			}
			if errors.Is(err, errStudioRefused) {
				break
			}
		}
		if parent.Err() != nil {
			return "image generation was stopped.", 0
		}
		next := "falling back to Flux"
		if taskEngine {
			next = "trying the Nano Banana task service"
		}
		log.Printf("generate_image: the image service failed (%s), %s", lastErr, next)
	}

	fallbackPrompt := styledPrompt(prompt, style)
	if taskEngine {
		ReportStatus(ctx, "Nano Banana · queue")
		tctx, tcancel := context.WithTimeout(ctx, 200*time.Second)
		size, err := r.nanoTaskImage(tctx, sessionID, fallbackPrompt, nil, taskAspect(ratio), name)
		tcancel()
		if err == nil {
			r.fitImage(sessionID, name, ratio)
			if _, fi, err := r.Machine.Open(sessionID, name); err == nil {
				size = fi.Size()
			}
			return done([]savedImage{{name: name, size: size}}, "Nano Banana")
		}
		if parent.Err() != nil {
			return "image generation was stopped.", 0
		}
		log.Printf("generate_image: the Nano Banana task service failed (%v), falling back to Flux", err)
		if lastErr == "" {
			lastErr = err.Error()
		} else {
			lastErr += "; " + err.Error()
		}
	}

	ReportStatus(ctx, "Flux")
	if strArg(args, "filename") == "" {
		name = strings.TrimSuffix(name, path.Ext(name)) + ".jpg"
	}
	size, err := r.imageViaFlux(ctx, sessionID, fallbackPrompt, ratio, name)
	if err != nil {
		if lastErr != "" {
			return fmt.Sprintf("image generation failed: %v (%s). Tell the user it is temporarily unavailable; do NOT substitute a different image.", err, lastErr), 0
		}
		return fmt.Sprintf("image generation failed: %v. Tell the user it is temporarily unavailable; do NOT substitute a different image.", err), 0
	}
	return fmt.Sprintf("Image created and queued for delivery.\n- File: %s (%s)\n- Prompt: %s", name, humanSize(size), prompt), 1
}

func taskAspect(ratio string) string {
	switch ratio {
	case "1:1", "16:9", "9:16", "4:3", "3:4":
		return ratio
	}
	return ""
}

var noImageKeyOnce sync.Once

const (
	imageAttempts = 2

	nestCallTimeout = 45 * time.Second
)

var (
	imageRetryWait = 2 * time.Second
	fluxEndpoint   = "https://image.pollinations.ai/prompt/"
)

func (r *Registry) cropToRatio(sessionID, name, ratio string) {
	wantW, wantH, ok := normShape(ratio)
	if !ok || wantW == wantH {
		return
	}
	f, _, err := r.Machine.Open(sessionID, name)
	if err != nil {
		return
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	if w <= 0 || h <= 0 || math.Abs(float64(w*wantH-h*wantW)) <= 0.015*float64(h*wantW) {
		return
	}
	var rw, rh int
	if w*wantH > h*wantW {
		rh = h
		rw = h * wantW / wantH
	} else {
		rw = w
		rh = w * wantH / wantW
	}
	x0 := b.Min.X + (w-rw)/2
	y0 := b.Min.Y + (h-rh)/3
	out := image.NewRGBA(image.Rect(0, 0, rw, rh))
	draw.Draw(out, out.Bounds(), img, image.Pt(x0, y0), draw.Src)

	var buf bytes.Buffer
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg":
		err = jpeg.Encode(&buf, out, &jpeg.Options{Quality: 92})
	default:
		err = png.Encode(&buf, out)
	}
	if err != nil {
		return
	}
	w2, err := r.Machine.Create(sessionID, name)
	if err != nil {
		return
	}
	_, err = w2.Write(buf.Bytes())
	if cerr := w2.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		log.Printf("generate_image: cropping %s failed: %v", name, err)
	}
}

func fluxSize(ratio string) (int, int) {
	w, h, ok := normShape(ratio)
	if !ok || w == h {
		return 768, 768
	}
	if w > h {
		return 1024, max(64, int(math.Round(1024*float64(h)/float64(w)/8))*8)
	}
	return max(64, int(math.Round(1024*float64(w)/float64(h)/8))*8), 1024
}

func (r *Registry) imageViaFlux(ctx context.Context, sessionID, prompt, ratio, name string) (int64, error) {
	width, height := fluxSize(ratio)
	endpoint := fmt.Sprintf("%s%s?nologo=true&width=%d&height=%d&model=flux", fluxEndpoint, url.PathEscape(prompt), width, height)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("creating the request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("the image service is unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("the service answered HTTP %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		return 0, fmt.Errorf("unexpected content type %q", ct)
	}
	n, err := r.saveStream(sessionID, name, resp.Body, 15<<20)
	if err != nil {
		return 0, fmt.Errorf("saving the image: %w", err)
	}
	return n, nil
}

func (r *Registry) saveStream(sessionID, name string, src io.Reader, max int64) (int64, error) {
	if rem := r.Machine.FreeSpace(); rem < max {
		max = rem
	}
	if max <= 0 {
		return 0, machine.ErrNoSpace
	}
	f, err := r.Machine.Create(sessionID, name)
	if err != nil {
		return 0, err
	}
	n, err := copyLimited(f, src, max)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = r.Machine.Remove(sessionID, name)
		if err == ErrTooLarge {
			return 0, fmt.Errorf("file is larger than %s", humanSize(max))
		}
		return 0, err
	}
	return n, nil
}

func (r *Registry) generateChart(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	labelsRaw := anyList(args["labels"])
	if len(labelsRaw) == 0 {
		return "error: labels must not be empty", nil
	}
	if len(labelsRaw) > 60 {
		return "error: at most 60 labels are supported; group or sample the data", nil
	}
	labels := make([]string, len(labelsRaw))
	for i, l := range labelsRaw {
		labels[i] = textutil.TruncateRunes(l, 60, "…")
	}
	numbers := func(what string, raw []string) ([]float64, string) {
		if len(raw) != len(labels) {
			return nil, fmt.Sprintf("error: labels (%d) and %s (%d) must have the same length", len(labels), what, len(raw))
		}
		out := make([]float64, len(raw))
		for i, v := range raw {
			f, ok := parseNumber(v)
			if !ok {
				return nil, fmt.Sprintf("error: %s[%d] (%q) is not a number", what, i, v)
			}
			out[i] = f
		}
		return out, ""
	}
	type series struct {
		Name   string    `json:"name"`
		Values []float64 `json:"values"`
	}
	var all []series
	if list, ok := args["series"].([]any); ok && len(list) > 0 {
		if len(list) > 10 {
			return "error: at most 10 series are supported", nil
		}
		for i, x := range list {
			m, _ := x.(map[string]any)
			name := textutil.TruncateRunes(strings.TrimSpace(strArg(m, "name")), 40, "…")
			if name == "" {
				name = fmt.Sprintf("%d", i+1)
			}
			vals, bad := numbers(fmt.Sprintf("series[%d].values", i), anyList(m["values"]))
			if bad != "" {
				return bad, nil
			}
			all = append(all, series{name, vals})
		}
	} else {
		vals, bad := numbers("values", anyList(args["values"]))
		if bad != "" {
			return bad, nil
		}
		all = []series{{"", vals}}
	}
	kind := strArg(args, "chart_type")
	if kind == "" {
		kind = "bar"
		if len(all) > 1 && len(labels) > 12 {
			kind = "line"
		}
	}
	if kind == "pie" || kind == "donut" {
		if len(all) > 1 {
			return "error: a " + kind + " chart shows one series; use bar or stacked_bar to compare several", nil
		}
		for _, v := range all[0].Values {
			if v < 0 {
				return "error: pie charts cannot have negative values", nil
			}
		}
	}
	name := outputName(strArg(args, "filename"), "chart", []string{".png"}, ".png")
	in := map[string]any{
		"type": kind, "title": strArg(args, "title"), "labels": labels, "values": all[0].Values, "out": "./" + name,
		"x_label": strArg(args, "x_label"), "y_label": strArg(args, "y_label"),
		"unit": textutil.TruncateRunes(strArg(args, "unit"), 20, ""), "theme": strArg(args, "theme"),
	}
	if len(all) > 1 {
		in["series"] = all
	}
	res, err := r.runScript(ctx, sessionID, "chart.py", in, 90*time.Second)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return failure("chart generation", res), nil
	}
	return fmt.Sprintf("Chart created and queued for delivery.\n- File: %s\n- Type: %s, %d labels × %d series", name, kind, len(labels), len(all)), nil
}

func (r *Registry) generateQR(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	data := rawStrArg(args, "data")
	if strings.TrimSpace(data) == "" {
		return "error: data is required", nil
	}
	if len([]rune(data)) > 1500 {
		return "error: data is too long for a QR code (max 1500 characters)", nil
	}
	name := outputName(strArg(args, "filename"), "qr", []string{".png"}, ".png")
	res, err := r.runScript(ctx, sessionID, "qr.py", map[string]any{"data": data, "out": "./" + name}, 60*time.Second)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return failure("QR generation", res), nil
	}
	return fmt.Sprintf("QR code created and queued for delivery.\n- File: %s\n- Encoded: %s", name, textutil.TruncateRunes(textutil.OneLine(data), 120, "…")), nil
}

var convertAliases = map[string]string{"gif": "video_to_gif", "to_gif": "video_to_gif", "audio": "extract_audio", "mp3": "extract_audio",
	"to_mp3": "extract_audio", "voice": "to_voice", "voice_message": "to_voice", "cut": "trim", "clip": "trim", "round": "to_video_note",
	"video_note": "to_video_note", "compress": "compress_video"}

func (r *Registry) mediaConvert(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	in, action := strArg(args, "input_file"), strArg(args, "action")
	if in == "" || action == "" {
		return "error: input_file and action are required", nil
	}
	info, err := r.Machine.Stat(sessionID, in)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Sprintf("error: input file %q not found in the workspace", in), nil
	}
	if a, ok := convertAliases[strings.ToLower(action)]; ok {
		action = a
	}
	switch action {
	case "to_voice", "to_video_note", "compress_video":
		return r.telegramMedia(ctx, sessionID, in, strArg(args, "output_file"), action, args)
	}
	stem := strings.TrimSuffix(in, path.Ext(in))
	output := strArg(args, "output_file")
	if output == "" {
		output = map[string]string{"video_to_gif": stem + ".gif", "extract_audio": stem + ".mp3", "convert_audio": stem + ".mp3",
			"trim": stem + "_cut" + path.Ext(in)}[action]
		if output == in {
			output = stem + "_converted" + path.Ext(in)
		}
	}
	args["output_file"] = output

	var allowed []string
	var def string
	switch action {
	case "video_to_gif":
		allowed, def = []string{".gif"}, ".gif"
	case "extract_audio", "convert_audio":
		allowed, def = audioOut, ".mp3"
	case "trim":
		allowed, def = mediaIn, ""
	default:
		return fmt.Sprintf("unsupported action %q (use video_to_gif, extract_audio, convert_audio, trim, to_voice, to_video_note or compress_video)", action), nil
	}
	out := outputName(strArg(args, "output_file"), "converted", allowed, def)
	if def == "" && !strings.Contains(out, ".") {
		out += ".mp4"
	}
	if out == in {
		return "error: output_file must differ from input_file", nil
	}

	argv := []string{"ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y"}
	switch action {
	case "video_to_gif":
		argv = append(argv, "-i", "./"+in, "-t", "30", "-filter_complex",
			"fps=12,scale='min(480,iw)':-2:flags=lanczos,split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=4", "-loop", "0")
	case "extract_audio":
		argv = append(argv, "-i", "./"+in, "-vn", "-q:a", "2")
	case "convert_audio":
		argv = append(argv, "-i", "./"+in, "-vn")
	case "trim":
		start, dur := strArg(args, "start_time"), strArg(args, "duration")
		if start == "" {
			start = "0"
		}
		if !clockRe.MatchString(start) || (dur != "" && !clockRe.MatchString(dur)) {
			return "error: start_time/duration must look like '00:01:30' or '90'", nil
		}
		argv = append(argv, "-ss", start)
		if dur != "" {
			argv = append(argv, "-t", dur)
		}
		argv = append(argv, "-i", "./"+in, "-c", "copy")
	}
	argv = append(argv, "./"+out)

	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: argv, Timeout: max(180*time.Second, compressTime(info.Size())/2)})
	if res.ExitCode != 0 {
		return failure("ffmpeg conversion", res), nil
	}
	outInfo, err := r.Machine.Stat(sessionID, out)
	if err != nil {
		return "ffmpeg finished but produced no output file", nil
	}
	return fmt.Sprintf("Media converted.\n- File: %s (%s)\n- Action: %s", out, humanSize(outInfo.Size()), action), nil
}

func (r *Registry) pdfTool(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	action, fname := strArg(args, "action"), strArg(args, "filename")
	if action == "" || fname == "" {
		return "error: action and filename are required", nil
	}
	name := outputName(fname, "document", []string{".pdf"}, ".pdf")

	switch action {
	case "read":
		if info, err := r.Machine.Stat(sessionID, name); err != nil || !info.Mode().IsRegular() {
			return fmt.Sprintf("error: file %q not found in the workspace", name), nil
		}
		from, to := pageRange(strArg(args, "pages"))
		res, err := r.runScript(ctx, sessionID, "pdf_read.py", map[string]any{"path": "./" + name, "max_pages": 30, "start": from, "end": to}, 90*time.Second)
		if err != nil {
			return "", err
		}
		if res.ExitCode != 0 {
			return failure("PDF read", res), nil
		}
		return fitPages(strings.TrimSpace(res.Stdout)), nil

	case "create":
		content := textArg(args, "text_content")
		if strings.TrimSpace(content) == "" {
			return "error: text_content is required for the create action", nil
		}

		if r.browser != nil {
			if err := r.htmlPDF(ctx, sessionID, content, strArg(args, "title"), name); err == nil {
				return fmt.Sprintf("PDF created and queued for delivery.\n- File: %s", name), nil
			} else {
				log.Printf("pdf_tool: the browser could not print, plain PDF instead: %v", err)
			}
		}
		res, err := r.runScript(ctx, sessionID, "pdf_create.py", map[string]any{
			"text": content, "title": strArg(args, "title"), "out": "./" + name,
		}, 90*time.Second)
		if err != nil {
			return "", err
		}
		if res.ExitCode != 0 {
			return failure("PDF creation", res), nil
		}
		return fmt.Sprintf("PDF created and queued for delivery.\n- File: %s", name), nil
	}
	return r.pdfOps(ctx, sessionID, action, name, args)
}
