package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	ToolBookWriter = "book_writer"
	bookDir        = "book"
)

type BookMeta struct {
	Title       string          `json:"title"`
	Author      string          `json:"author,omitempty"`
	Overview    string          `json:"overview,omitempty"`
	Chapters    []ChapterHeader `json:"chapters"`
	TotalWords  int             `json:"total_words"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type ChapterHeader struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Words  int    `json:"words"`
	Done   bool   `json:"done"`
}

// ManageBook handles multi-chapter book writing, outlining, and manuscript assembly.
func (r *Registry) ManageBook(ctx context.Context, sessionID, action, title string, chNum int, chTitle, content string) (string, error) {
	metaPath := bookDir + "/meta.json"
	meta := BookMeta{Title: title, UpdatedAt: time.Now()}

	data, _, err := r.Machine.ReadFile(sessionID, metaPath, 32<<10)
	if err == nil {
		_ = json.Unmarshal(data, &meta)
	}
	if title != "" {
		meta.Title = title
	}

	switch action {
	case "outline":
		if content == "" {
			return "error: outline content or chapter list required", nil
		}
		// Parse outline lines: "1. Chapter Title"
		var headers []ChapterHeader
		lines := strings.Split(content, "\n")
		num := 1
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l != "" && (strings.HasPrefix(l, "#") || strings.Contains(l, ".")) {
				cleanTitle := strings.TrimLeft(l, "#1234567890. ")
				headers = append(headers, ChapterHeader{
					Number: num,
					Title:  cleanTitle,
				})
				num++
			}
		}
		if len(headers) == 0 {
			headers = []ChapterHeader{
				{Number: 1, Title: "Introduction"},
				{Number: 2, Title: "Core Concepts"},
				{Number: 3, Title: "Advanced Topics"},
				{Number: 4, Title: "Conclusion"},
			}
		}
		meta.Chapters = headers
		metaData, _ := json.MarshalIndent(meta, "", "  ")
		_ = r.Machine.WriteFile(sessionID, metaPath, metaData)
		return fmt.Sprintf("📖 Book outline saved with %d chapters for «%s».", len(headers), meta.Title), nil

	case "write_chapter":
		if chNum <= 0 {
			return "error: valid chapter_num is required (>= 1)", nil
		}
		if content == "" {
			return "error: chapter content is required", nil
		}
		chFile := fmt.Sprintf("%s/chapter_%02d.md", bookDir, chNum)
		if chTitle == "" {
			chTitle = fmt.Sprintf("Chapter %d", chNum)
		}
		chapterDoc := fmt.Sprintf("# Chapter %d: %s\n\n%s\n", chNum, chTitle, content)
		if err := r.Machine.WriteFile(sessionID, chFile, []byte(chapterDoc)); err != nil {
			return "", err
		}

		// Update metadata
		words := len(strings.Fields(content))
		found := false
		for i := range meta.Chapters {
			if meta.Chapters[i].Number == chNum {
				meta.Chapters[i].Title = chTitle
				meta.Chapters[i].Words = words
				meta.Chapters[i].Done = true
				found = true
				break
			}
		}
		if !found {
			meta.Chapters = append(meta.Chapters, ChapterHeader{
				Number: chNum,
				Title:  chTitle,
				Words:  words,
				Done:   true,
			})
		}
		sort.Slice(meta.Chapters, func(i, j int) bool {
			return meta.Chapters[i].Number < meta.Chapters[j].Number
		})

		metaData, _ := json.MarshalIndent(meta, "", "  ")
		_ = r.Machine.WriteFile(sessionID, metaPath, metaData)
		return fmt.Sprintf("✍️ Chapter %d («%s») written and saved to `%s` (%d words).", chNum, chTitle, chFile, words), nil

	case "assemble":
		files, err := r.Machine.ListFiles(sessionID)
		if err != nil {
			return "", err
		}
		var chFiles []string
		for _, f := range files {
			if strings.HasPrefix(f.Name, bookDir+"/chapter_") && strings.HasSuffix(f.Name, ".md") {
				chFiles = append(chFiles, f.Name)
			}
		}
		sort.Strings(chFiles)

		if len(chFiles) == 0 {
			return "error: no chapters found to assemble in book/ directory", nil
		}

		var manuscript strings.Builder
		manuscript.WriteString(fmt.Sprintf("# %s\n\n", meta.Title))
		manuscript.WriteString("## Table of Contents\n\n")
		for _, ch := range meta.Chapters {
			manuscript.WriteString(fmt.Sprintf("- Chapter %d: %s\n", ch.Number, ch.Title))
		}
		manuscript.WriteString("\n---\n\n")

		totalWords := 0
		for _, cf := range chFiles {
			data, _, err := r.Machine.ReadFile(sessionID, cf, 512<<10)
			if err == nil {
				text := string(data)
				totalWords += len(strings.Fields(text))
				manuscript.WriteString(text + "\n\n---\n\n")
			}
		}

		outPath := bookDir + "/manuscript.md"
		_ = r.Machine.WriteFile(sessionID, outPath, []byte(manuscript.String()))
		return fmt.Sprintf("📚 Full manuscript assembled at `%s` (%d chapters, %d total words).",
			outPath, len(chFiles), totalWords), nil

	default: // status
		totalWords := 0
		doneCount := 0
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("📖 **Book Project: %s**\n", meta.Title))
		for _, ch := range meta.Chapters {
			st := "[ ]"
			if ch.Done {
				st = "[x]"
				doneCount++
				totalWords += ch.Words
			}
			sb.WriteString(fmt.Sprintf("%s Chapter %d: %s (%d words)\n", st, ch.Number, ch.Title, ch.Words))
		}
		sb.WriteString(fmt.Sprintf("\nTotal completed: %d/%d chapters | %d words\n", doneCount, len(meta.Chapters), totalWords))
		return strings.TrimSpace(sb.String()), nil
	}
}
