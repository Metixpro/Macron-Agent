package tools

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const ToolRAGSearch = "rag_search"

type RAGChunk struct {
	File      string  `json:"file"`
	LineStart int     `json:"line_start"`
	LineEnd   int     `json:"line_end"`
	Text      string  `json:"text"`
	Score     float64 `json:"score"`
}

var ragSupportedExts = map[string]bool{
	".txt":  true,
	".md":   true,
	".go":   true,
	".py":   true,
	".sh":   true,
	".js":   true,
	".ts":   true,
	".json": true,
	".csv":  true,
	".sql":  true,
	".html": true,
	".yaml": true,
	".yml":  true,
}

// SearchWorkspaceRAG indexes documents in the workspace and retrieves top-k relevant chunks.
func (r *Registry) SearchWorkspaceRAG(ctx context.Context, sessionID, query string, topK int) ([]RAGChunk, error) {
	if topK <= 0 {
		topK = 5
	}
	ws, err := r.Machine.Workspace(sessionID)
	if err != nil {
		return nil, err
	}

	var chunks []RAGChunk
	err = filepath.Walk(ws, func(path string, fi os.FileInfo, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		rel, _ := filepath.Rel(ws, path)
		if strings.HasPrefix(rel, ".") || strings.Contains(rel, string(filepath.Separator)+".") {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !fi.Mode().IsRegular() || fi.Size() > 2<<20 { // skip files > 2MB
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !ragSupportedExts[ext] {
			return nil
		}

		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		fileChunks := chunkDocument(rel, string(b), 800, 150)
		chunks = append(chunks, fileChunks...)
		return nil
	})
	if err != nil {
		return nil, err
	}

	if len(chunks) == 0 {
		return nil, nil
	}

	qVec, qMag := buildVectorMap(query)
	for i := range chunks {
		cVec, cMag := buildVectorMap(chunks[i].Text)
		chunks[i].Score = cosineSim(qVec, qMag, cVec, cMag)
	}

	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].Score > chunks[j].Score
	})

	var result []RAGChunk
	for _, c := range chunks {
		if c.Score > 0.05 {
			result = append(result, c)
			if len(result) >= topK {
				break
			}
		}
	}
	return result, nil
}

func chunkDocument(relPath, text string, chunkSize, overlap int) []RAGChunk {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return nil
	}

	var chunks []RAGChunk
	lineStart := 1
	var curBlock strings.Builder

	for i, l := range lines {
		curBlock.WriteString(l)
		curBlock.WriteByte('\n')

		if curBlock.Len() >= chunkSize || i == len(lines)-1 {
			chunks = append(chunks, RAGChunk{
				File:      relPath,
				LineStart: lineStart,
				LineEnd:   i + 1,
				Text:      strings.TrimSpace(curBlock.String()),
			})
			// shift overlap
			lineStart = max(1, i-3)
			curBlock.Reset()
			for j := lineStart - 1; j <= i && j < len(lines); j++ {
				curBlock.WriteString(lines[j])
				curBlock.WriteByte('\n')
			}
		}
	}
	return chunks
}

func buildVectorMap(s string) (map[string]float64, float64) {
	vec := make(map[string]float64)
	var cur strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else {
			if cur.Len() > 0 {
				vec[cur.String()] += 1.0
				cur.Reset()
			}
		}
	}
	if cur.Len() > 0 {
		vec[cur.String()] += 1.0
	}

	runes := []rune(strings.ToLower(s))
	for i := 0; i+3 <= len(runes); i++ {
		vec["3g:"+string(runes[i:i+3])] += 0.5
	}

	mag := 0.0
	for _, v := range vec {
		mag += v * v
	}
	return vec, math.Sqrt(mag)
}

func cosineSim(v1 map[string]float64, mag1 float64, v2 map[string]float64, mag2 float64) float64 {
	if mag1 == 0 || mag2 == 0 {
		return 0
	}
	dot := 0.0
	for k, val1 := range v1 {
		if val2, ok := v2[k]; ok {
			dot += val1 * val2
		}
	}
	return dot / (mag1 * mag2)
}
