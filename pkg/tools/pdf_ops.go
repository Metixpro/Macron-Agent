package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"
)

// The office jobs of pdf_tool: merging, extracting, deleting and rotating
// pages, pages to pictures, pictures (scans, phone photos) to one PDF, and
// making a PDF smaller. They run here (pdf_ops.py: pypdf, and
// PyMuPDF or pdftoppm for pictures).

const maxPDFImages = 20

// pageSpec reads page lists as people write them: «۱ تا ۵، ۸» is "1-5, 8".
func pageSpec(s string) string {
	s = strings.NewReplacer("،", ",", "؛", ",", " تا ", "-", "تا", "-", "–", "-", "—", "-").Replace(s)
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		switch {
		case r >= '۰' && r <= '۹':
			return '0' + (r - '۰')
		case r >= '٠' && r <= '٩':
			return '0' + (r - '٠')
		}
		return r
	}, s))
}

func (r *Registry) pdfOps(ctx context.Context, sessionID, action, name string, args map[string]any) (string, error) {
	exists := func(f string) bool {
		info, err := r.Machine.Stat(sessionID, f)
		return err == nil && info.Mode().IsRegular()
	}
	stem := strings.TrimSuffix(name, path.Ext(name))
	output := strings.TrimPrefix(strings.TrimSpace(strArg(args, "output")), "/workspace/")
	if output != "" && action != "to_images" {
		output = outputName(output, "document", []string{".pdf"}, ".pdf")
	}
	in := map[string]any{"action": action}
	var files []string
	for _, f := range anyList(args["files"]) {
		f = strings.TrimPrefix(strings.TrimSpace(f), "/workspace/")
		if !exists(f) {
			return fmt.Sprintf("error: %q is not a file in the workspace", f), nil
		}
		files = append(files, "./"+f)
	}
	result := name
	switch action {
	case "merge", "from_images":
		if len(files) < 1 || (action == "merge" && len(files) < 2) {
			return "error: files lists the inputs in order (at least two PDFs for merge)", nil
		}
		if len(files) > 200 {
			return "error: at most 200 files", nil
		}
		if output != "" {
			result = output
		}
		in["files"], in["out"] = files, "./"+result
	default:
		if !exists(name) {
			return fmt.Sprintf("error: file %q not found in the workspace", name), nil
		}
		in["file"], in["pages"] = "./"+name, pageSpec(strArg(args, "pages"))
		switch action {
		case "to_images":
			if output == "" {
				output = stem
			}
			in["stem"], in["dpi"], in["fmt"], in["max_images"] = "./"+strings.TrimSuffix(output, path.Ext(output)), 130, "jpg", maxPDFImages
		case "rotate":
			in["degrees"] = intArg(args, "degrees", 90, 90, 270)
			fallthrough
		default:
			suffix := map[string]string{"extract_pages": "_pages", "delete_pages": "_edited", "rotate": "_rotated", "compress": "_small"}[action]
			if suffix == "" {
				return fmt.Sprintf("unsupported action %q (read, create, merge, extract_pages, delete_pages, rotate, to_images, from_images or compress)", action), nil
			}
			result = stem + suffix + ".pdf"
			if output != "" {
				result = output
			}
			in["out"] = "./" + result
		}
	}
	res, err := r.runScript(ctx, sessionID, "pdf_ops.py", in, 5*time.Minute)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return failure("pdf_tool "+action, res), nil
	}
	var out struct {
		Pages  int      `json:"pages"`
		Of     int      `json:"of"`
		Images []string `json:"images"`
		Before int64    `json:"before"`
		After  int64    `json:"after"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &out) != nil {
		return failure("pdf_tool "+action, res), nil
	}
	switch action {
	case "to_images":
		var names []string
		for _, n := range out.Images {
			names = append(names, strings.TrimPrefix(n, "./"))
		}
		more := ""
		if out.Of > len(names) {
			more = fmt.Sprintf(" (the file has %d pages; ask for others with pages)", out.Of)
		}
		return fmt.Sprintf("Pages as pictures (%s): %s%s", SendNotePlural, strings.Join(names, ", "), more), nil
	case "compress":
		if out.After >= out.Before {
			return fmt.Sprintf("%s is already as small as it gets (%s); %s is a copy.", name, humanSize(out.Before), result), nil
		}
		return fmt.Sprintf("Compressed %s: %s → %s (%s smaller). File: %s. "+SendNote+".", name, humanSize(out.Before), humanSize(out.After),
			fmt.Sprintf("%d%%", 100-out.After*100/out.Before), result), nil
	}
	of := ""
	if out.Of > 0 {
		of = fmt.Sprintf(" of %d", out.Of)
	}
	return fmt.Sprintf("Done: %s (%d pages%s). %s.", result, out.Pages, of, SendNote), nil
}
