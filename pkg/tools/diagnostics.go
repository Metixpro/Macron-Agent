package tools

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

type syntaxChecker struct {
	program, label string
	argv           func(file string) []string
}

var syntaxCheckers = map[string]syntaxChecker{
	".py":   {"python3", "python", pyCheck(`import ast,sys; ast.parse(open(sys.argv[1],'rb').read(), sys.argv[1])`)},
	".json": {"python3", "json", pyCheck(`import json,sys; json.load(open(sys.argv[1],'rb'))`)},
	".toml": {"python3", "toml", pyCheck(`import sys,tomllib; tomllib.load(open(sys.argv[1],'rb'))`)},
	".yaml": {"python3", "yaml", pyCheck(yamlCheck)},
	".yml":  {"python3", "yaml", pyCheck(yamlCheck)},
	".js":   {"node", "node --check", func(f string) []string { return []string{"node", "--check", f} }},
	".mjs":  {"node", "node --check", func(f string) []string { return []string{"node", "--check", f} }},
	".cjs":  {"node", "node --check", func(f string) []string { return []string{"node", "--check", f} }},
	".php":  {"php", "php -l", func(f string) []string { return []string{"php", "-l", f} }},
	".sh":   {"bash", "bash -n", func(f string) []string { return []string{"bash", "-n", f} }},
	".bash": {"bash", "bash -n", func(f string) []string { return []string{"bash", "-n", f} }},
	".rb":   {"ruby", "ruby -c", func(f string) []string { return []string{"ruby", "-c", f} }},
	".go":   {"gofmt", "gofmt", func(f string) []string { return []string{"bash", "-c", `gofmt -e "$1" >/dev/null`, "gofmt", f} }},
}

const yamlCheck = "import sys\ntry:\n import yaml\nexcept ImportError:\n sys.exit(0)\nlist(yaml.safe_load_all(open(sys.argv[1],'rb')))"

func pyCheck(code string) func(string) []string {
	return func(f string) []string { return []string{"python3", "-I", "-c", code, f} }
}

func (r *Registry) syntaxNote(ctx context.Context, sessionID, name string) string {
	c, ok := syntaxCheckers[strings.ToLower(path.Ext(name))]
	if !ok || !r.syntax[c.program] {
		return ""
	}
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: c.argv(argFile(name)), Timeout: 20 * time.Second, MergeOutput: true})
	if res.ExitCode == 0 {
		return "\n✓ syntax ok (" + c.label + ")"
	}
	if res.ExitCode < 0 || res.ExitCode == 124 {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	return "\n⚠ syntax error (" + c.label + "):\n" + strings.Join(lines, "\n")
}

const (
	SyntaxUnknown = 0
	SyntaxOK      = 1
	SyntaxBroken  = -1
)

func (r *Registry) CheckSyntax(ctx context.Context, sessionID, name string, content []byte) (int, string) {
	ext := strings.ToLower(path.Ext(name))
	c, ok := syntaxCheckers[ext]
	if !ok {
		return SyntaxUnknown, ""
	}
	r.syntaxOnce.Do(func() {
		if r.syntax == nil {
			r.syntax = r.Machine.Available("python3", "bash", "node", "php", "ruby", "gofmt")
		}
	})
	if !r.syntax[c.program] {
		return SyntaxUnknown, ""
	}
	tmp := fmt.Sprintf(".syntax-check-%d%s", time.Now().UnixNano(), ext)
	if err := r.Machine.WriteFile(sessionID, tmp, content); err != nil {
		return SyntaxUnknown, ""
	}
	defer r.Machine.Remove(sessionID, tmp)
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: c.argv(argFile(tmp)), Timeout: 20 * time.Second, MergeOutput: true})
	switch {
	case res.ExitCode == 0:
		return SyntaxOK, c.label
	case res.ExitCode < 0 || res.ExitCode == 124:
		return SyntaxUnknown, ""
	}
	out := strings.ReplaceAll(strings.TrimSpace(res.Stdout), tmp, name)
	lines := strings.Split(out, "\n")
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	return SyntaxBroken, c.label + ": " + strings.Join(lines, "\n")
}
