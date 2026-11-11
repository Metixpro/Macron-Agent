package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

const ToolTranslateDoc = "translate_document"

const translateSystemPrompt = `You are a professional document translator.
Translate the text to the target language with high fidelity and natural phrasing.
CRITICAL FORMAT-PRESERVATION RULES:
1. Preserve all Markdown elements: headings (#, ##), bold (**), italic (*), lists (- or 1.), and blockquotes (>).
2. NEVER modify or translate code blocks, inline code ('` + "`" + `'), variable placeholders ({{...}}), or URLs.
3. Keep Markdown tables aligned and keep table headers intact.
4. Output ONLY the translated document with no preamble or commentary.`

var translatable = map[string]bool{".md": true, ".markdown": true, ".txt": true, ".html": true, ".htm": true, ".srt": true,
	".vtt": true, ".csv": true, ".tsv": true, ".json": true, ".yaml": true, ".yml": true, ".po": true, ".xml": true, ".tex": true, ".rst": true, "": true}

const translateChunkRunes = 6000

func translateChunks(text string) []string {
	var out []string
	var cur strings.Builder
	inCode := false
	flush := func() {
		if strings.TrimSpace(cur.String()) != "" {
			out = append(out, cur.String())
		}
		cur.Reset()
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
		}
		if !inCode && strings.TrimSpace(line) == "" && cur.Len() > 0 && len([]rune(cur.String())) >= translateChunkRunes {
			flush()
		}
		cur.WriteString(line)
		if !inCode && len([]rune(cur.String())) >= translateChunkRunes*3/2 {
			flush()
		}
	}
	flush()
	return out
}

func (r *Registry) TranslateDocument(ctx context.Context, sessionID, filename, targetLang, outFile string) (string, error) {
	if r.LLM == nil {
		return "", fmt.Errorf("translation needs a model")
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if !translatable[ext] {
		return "", fmt.Errorf("%s is not a text format; extract its text first (document_reader, or convert it to Markdown) and translate that", filename)
	}
	data, truncated, err := r.Machine.ReadFile(sessionID, filename, 1<<20)
	if err != nil {
		return "", err
	}
	if truncated {
		return "", fmt.Errorf("%s is larger than 1 MB; split it first", filename)
	}
	chunks := translateChunks(string(data))
	if len(chunks) == 0 {
		return "", fmt.Errorf("%s is empty", filename)
	}
	var out strings.Builder
	cut := 0
	for i, c := range chunks {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		resp, err := r.LLM.Complete(cctx, llm.Request{MaxTokens: 16000, Messages: []llm.Message{
			{Role: "system", Content: translateSystemPrompt},
			{Role: "user", Content: fmt.Sprintf("Target language: %s\nPart %d of %d of %s. Translate only this part:\n\n%s", targetLang, i+1, len(chunks), filename, c)},
		}})
		cancel()
		if err != nil {
			return "", fmt.Errorf("part %d of %d: %w", i+1, len(chunks), err)
		}
		if resp.FinishReason == "length" {
			cut++
		}
		part := strings.TrimSpace(resp.Message.Content)
		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString(part)
	}
	if outFile == "" {
		outFile = fmt.Sprintf("%s_%s%s", strings.TrimSuffix(filename, filepath.Ext(filename)), strings.ToLower(sanitizeBase(targetLang)), filepath.Ext(filename))
	}
	if err := r.Machine.WriteFile(sessionID, outFile, []byte(out.String()+"\n")); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Translated to %s in %d parts and saved as %s.", targetLang, len(chunks), outFile)
	if cut > 0 {
		msg += fmt.Sprintf(" Warning: %d part(s) hit the model's output limit and may be cut; check the end of each part.", cut)
	}
	return msg, nil
}
