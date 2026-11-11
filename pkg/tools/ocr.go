package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
)

const ToolOCR = "ocr_image"

const ocrPrompt = "Transcribe all the text in this image exactly as written: keep the original language and script (Persian stays Persian), the line breaks and the reading order; render tables as Markdown tables. Output only the text. If there is no text, output exactly: (no text)"

func (r *Registry) RunOCR(ctx context.Context, sessionID, filename, lang string) (string, error) {
	filename = cleanDocName(filename)
	if filename == "" {
		return "", fmt.Errorf("filename is required")
	}
	if lang == "" {
		lang = "fas+eng"
	}
	out := strings.TrimSuffix(filename, "."+lastExt(filename)) + "_ocr.txt"
	var problems []string
	if r.Machine.Available("tesseract")["tesseract"] {
		res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: []string{"tesseract", filename, "stdout", "-l", lang}, Timeout: 90 * time.Second})
		if text := strings.TrimSpace(res.Stdout); res.ExitCode == 0 && text != "" {
			if err := r.Machine.WriteFile(sessionID, out, []byte(text+"\n")); err != nil {
				return "", err
			}
			return fmt.Sprintf("Text read by tesseract (%s), saved to %s:\n\n%s", lang, out, text), nil
		}
		problems = append(problems, "tesseract: "+firstRunes(strings.TrimSpace(res.Stderr+" "+res.Error), 200))
	} else {
		problems = append(problems, "tesseract is not installed (apt install tesseract-ocr tesseract-ocr-fas)")
	}
	if r.LLM == nil {
		return "", fmt.Errorf("could not read %s: %s", filename, strings.Join(problems, "; "))
	}
	data, _, err := r.Machine.ReadFile(sessionID, filename, 6<<20)
	if err != nil {
		return "", err
	}
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		return "", fmt.Errorf("%s is not an image (%s); for PDFs use document_reader", filename, mime)
	}
	resp, err := r.LLM.Complete(ctx, llm.Request{MaxTokens: 4000, Messages: []llm.Message{{Role: "user", Parts: []llm.Part{
		llm.TextPart(ocrPrompt), llm.ImagePart("data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data))}}}})
	if err != nil {
		return "", fmt.Errorf("could not read %s: %s; the model could not either: %v", filename, strings.Join(problems, "; "), err)
	}
	text := strings.TrimSpace(resp.Message.Content)
	if text == "" {
		return "", fmt.Errorf("the model returned no text for %s", filename)
	}
	if err := r.Machine.WriteFile(sessionID, out, []byte(text+"\n")); err != nil {
		return "", err
	}
	return fmt.Sprintf("Text read by the vision model, saved to %s:\n\n%s", out, text), nil
}

func lastExt(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 && !strings.Contains(name[i:], "/") {
		return name[i+1:]
	}
	return ""
}
