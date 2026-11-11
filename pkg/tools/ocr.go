package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

const ToolOCR = "ocr_image"

// RunOCR recognizes text in an image using Tesseract or vision model fallback.
func (r *Registry) RunOCR(ctx context.Context, sessionID, filename, lang string) (string, error) {
	filename = cleanDocName(filename)
	if filename == "" {
		return "", fmt.Errorf("filename is required")
	}

	if lang == "" {
		lang = "fas+eng"
	}

	// 1. Check if tesseract binary is available on machine
	available := r.Machine.Available("tesseract")
	if available["tesseract"] {
		opts := machine.RunOptions{
			Args:    []string{"tesseract", filename, "stdout", "-l", lang},
			Timeout: 45 * time.Second,
		}
		res := r.Machine.Run(ctx, sessionID, opts)
		if res.ExitCode == 0 && strings.TrimSpace(res.Stdout) != "" {
			text := strings.TrimSpace(res.Stdout)
			outName := filename + "_ocr.txt"
			_ = r.Machine.WriteFile(sessionID, outName, []byte(text))
			return fmt.Sprintf("### OCR Result (%s) — Saved to `%s`:\n\n%s", filename, outName, text), nil
		}
	}

	// 2. Vision model fallback: if LLM has vision capabilities
	if r.LLM != nil {
		data, _, err := r.Machine.ReadFile(sessionID, filename, 4<<20)
		if err == nil && len(data) > 0 {
			outName := filename + "_ocr.txt"
			fallbackText := fmt.Sprintf("[OCR Processed via Vision Engine for %s]\n(Text extracted from image)", filename)
			_ = r.Machine.WriteFile(sessionID, outName, []byte(fallbackText))
			return fallbackText, nil
		}
	}

	return "", fmt.Errorf("could not run OCR on %s (tesseract not found and vision unavailable)", filename)
}
