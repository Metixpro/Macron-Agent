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

// TranslateDocument translates a file while strictly preserving markdown structure, tables, and code.
func (r *Registry) TranslateDocument(ctx context.Context, sessionID, filename, targetLang, outFile string) (string, error) {
	if r.LLM == nil {
		return "", fmt.Errorf("translation requires a language model")
	}

	data, _, err := r.Machine.ReadFile(sessionID, filename, 256<<10)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", filename, err)
	}

	content := string(data)
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("file %s is empty", filename)
	}

	prompt := fmt.Sprintf("Target Language: %s\n\nDocument to translate:\n%s", targetLang, content)
	req := llm.Request{
		Messages: []llm.Message{
			{Role: "system", Content: translateSystemPrompt},
			{Role: "user", Content: prompt},
		},
	}

	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	resp, err := r.LLM.Complete(cctx, req)
	if err != nil {
		return "", fmt.Errorf("translation completion: %w", err)
	}

	translated := strings.TrimSpace(resp.Message.Content)

	if outFile == "" {
		ext := filepath.Ext(filename)
		base := strings.TrimSuffix(filename, ext)
		langSlug := strings.ToLower(sanitizeBase(targetLang))
		outFile = fmt.Sprintf("%s_%s%s", base, langSlug, ext)
	}

	if err := r.Machine.WriteFile(sessionID, outFile, []byte(translated)); err != nil {
		return "", fmt.Errorf("write translated file: %w", err)
	}

	return fmt.Sprintf("✅ Document translated to %s and saved as `%s` (%d characters).",
		targetLang, outFile, len(translated)), nil
}
