package tools

import (
	"context"
	"fmt"
	"log"
	"path"
	"regexp"
	"strings"
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
