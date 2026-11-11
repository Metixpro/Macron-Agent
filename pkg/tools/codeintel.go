package tools

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
)

const ToolCodeIntel = "code_intelligence"

type CodeSymbol struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // "function", "class", "type", "const"
	Line int    `json:"line"`
	File string `json:"file"`
}

var (
	goDeclRe   = regexp.MustCompile(`(?m)^(?:func\s+(?:\([^)]+\)\s+)?([A-Za-z0-9_]+)|type\s+([A-Za-z0-9_]+)\s+(?:struct|interface))\b`)
	pyDeclRe   = regexp.MustCompile(`(?m)^\s*(?:def|class)\s+([A-Za-z0-9_]+)\b`)
	jsDeclRe   = regexp.MustCompile(`(?m)^\s*(?:function|class|const|let|var)\s+([A-Za-z0-9_]+)\s*(?:=\s*(?:function|\([^)]*\)\s*=>)|\()`)
	rustDeclRe = regexp.MustCompile(`(?m)^\s*(?:fn|struct|enum|trait)\s+([A-Za-z0-9_]+)\b`)
)

// ExtractFileSymbols parses definitions in source code.
func ExtractFileSymbols(filename, content string) []CodeSymbol {
	ext := strings.ToLower(filepath.Ext(filename))
	lines := strings.Split(content, "\n")
	var symbols []CodeSymbol

	for lineIdx, line := range lines {
		lNum := lineIdx + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}

		switch ext {
		case ".go":
			if m := goDeclRe.FindStringSubmatch(line); len(m) > 0 {
				name := m[1]
				kind := "function"
				if name == "" && len(m) > 2 {
					name = m[2]
					kind = "type"
				}
				if name != "" {
					symbols = append(symbols, CodeSymbol{Name: name, Kind: kind, Line: lNum, File: filename})
				}
			}
		case ".py":
			if m := pyDeclRe.FindStringSubmatch(line); len(m) > 1 {
				kind := "function"
				if strings.HasPrefix(trimmed, "class ") {
					kind = "class"
				}
				symbols = append(symbols, CodeSymbol{Name: m[1], Kind: kind, Line: lNum, File: filename})
			}
		case ".js", ".ts", ".tsx", ".jsx":
			if m := jsDeclRe.FindStringSubmatch(line); len(m) > 1 {
				symbols = append(symbols, CodeSymbol{Name: m[1], Kind: "definition", Line: lNum, File: filename})
			}
		case ".rs":
			if m := rustDeclRe.FindStringSubmatch(line); len(m) > 1 {
				symbols = append(symbols, CodeSymbol{Name: m[1], Kind: "rust_item", Line: lNum, File: filename})
			}
		}
	}
	return symbols
}

// FindDefinition scans workspace files to locate where a symbol was defined.
func (r *Registry) FindDefinition(ctx context.Context, sessionID, symbol string) ([]CodeSymbol, error) {
	files, err := r.Machine.ListFiles(sessionID)
	if err != nil {
		return nil, err
	}

	var matches []CodeSymbol
	for _, f := range files {
		if ctx.Err() != nil {
			break
		}
		ext := strings.ToLower(filepath.Ext(f.Name))
		if ext != ".go" && ext != ".py" && ext != ".js" && ext != ".ts" && ext != ".rs" {
			continue
		}
		data, _, err := r.Machine.ReadFile(sessionID, f.Name, 128<<10)
		if err == nil {
			syms := ExtractFileSymbols(f.Name, string(data))
			for _, s := range syms {
				if s.Name == symbol {
					matches = append(matches, s)
				}
			}
		}
	}
	return matches, nil
}
