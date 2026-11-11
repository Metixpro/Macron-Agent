package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	ToolProcessInbox = "process_inbox"
	inboxDir         = "inbox"
	outboxDir        = "outbox"
)

type ProcessedItem struct {
	OriginalFile string `json:"original_file"`
	Action       string `json:"action"`
	OutputFile   string `json:"output_file"`
	Summary      string `json:"summary"`
}

// ProcessInboxFolder inspects the workspace inbox/ folder and auto-processes all dropped documents and media.
func (r *Registry) ProcessInboxFolder(ctx context.Context, sessionID string) ([]ProcessedItem, error) {
	files, err := r.Machine.ListFiles(sessionID)
	if err != nil {
		return nil, err
	}

	var inboxFiles []string
	for _, f := range files {
		if strings.HasPrefix(f.Name, inboxDir+"/") && !strings.HasPrefix(f.Name, inboxDir+"/processed/") {
			rel := strings.TrimPrefix(f.Name, inboxDir+"/")
			if !strings.Contains(rel, "/") && rel != "" {
				inboxFiles = append(inboxFiles, f.Name)
			}
		}
	}

	if len(inboxFiles) == 0 {
		return nil, nil
	}

	var processed []ProcessedItem
	for _, fPath := range inboxFiles {
		if ctx.Err() != nil {
			break
		}
		ext := strings.ToLower(filepath.Ext(fPath))
		baseName := filepath.Base(fPath)
		stem := strings.TrimSuffix(baseName, ext)

		var item ProcessedItem
		item.OriginalFile = fPath

		switch ext {
		case ".pdf", ".docx", ".txt", ".md":
			item.Action = "Document Summarization"
			outFile := fmt.Sprintf("%s/%s_summary.md", outboxDir, stem)
			content, _, err := r.Machine.ReadFile(sessionID, fPath, 64<<10)
			if err == nil {
				summary := fmt.Sprintf("# Summary of %s\n\nFile size: %d bytes.\n\nKey Content:\n%s\n",
					baseName, len(content), string(content))
				_ = r.Machine.WriteFile(sessionID, outFile, []byte(summary))
				item.OutputFile = outFile
				item.Summary = fmt.Sprintf("Processed document (%d bytes)", len(content))
			}

		case ".png", ".jpg", ".jpeg", ".webp":
			item.Action = "OCR Text Extraction"
			outFile := fmt.Sprintf("%s/%s_ocr.txt", outboxDir, stem)
			ocrText, err := r.RunOCR(ctx, sessionID, fPath, "fas+eng")
			if err == nil {
				_ = r.Machine.WriteFile(sessionID, outFile, []byte(ocrText))
				item.OutputFile = outFile
				item.Summary = "Extracted text via OCR"
			}

		case ".csv", ".json":
			item.Action = "Data Profiling"
			outFile := fmt.Sprintf("%s/%s_analysis.md", outboxDir, stem)
			data, _, err := r.Machine.ReadFile(sessionID, fPath, 64<<10)
			if err == nil {
				lines := strings.Split(string(data), "\n")
				analysis := fmt.Sprintf("# Data Analysis: %s\n- Total Rows: %d\n- Size: %d bytes\n",
					baseName, len(lines), len(data))
				_ = r.Machine.WriteFile(sessionID, outFile, []byte(analysis))
				item.OutputFile = outFile
				item.Summary = fmt.Sprintf("Profiled %d rows", len(lines))
			}
		default:
			item.Action = "Archived"
			item.Summary = "File moved to processed queue"
		}

		// Move to processed subfolder
		destProcessed := fmt.Sprintf("%s/processed/%s_%d%s", inboxDir, stem, time.Now().Unix()%1000, ext)
		_ = r.Machine.Rename(sessionID, fPath, destProcessed)

		processed = append(processed, item)
	}

	return processed, nil
}
