package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

func TestALongOutputIsKeptWhole(t *testing.T) {
	r := newRegistry(t, true, nil)
	var lines []string
	for i := 1; i <= 4000; i++ {
		lines = append(lines, fmt.Sprintf("row %04d MIDDLE-%d", i, i))
	}
	big := strings.Join(lines, "\n")
	r.Register(llm.Tool{Type: "function", Function: llm.ToolDef{Name: "big_dump", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		func(context.Context, string, map[string]any) (string, error) { return big, nil })
	out, err := r.Execute(context.Background(), "s1", "big_dump", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "MIDDLE-2000\n") || len([]rune(out)) > MaxToolOutput {
		t.Fatalf("the model gets the head and tail only: %d runes", len([]rune(out)))
	}
	m := regexp.MustCompile(`saved in (\S+): read_file`).FindStringSubmatch(out)
	if m == nil || !strings.HasPrefix(m[1], WholeOutputDir+"/") {
		t.Fatalf("no file named in: …%s", out[len(out)-300:])
	}
	data, _, err := r.Machine.ReadFile("s1", m[1], 8<<20)
	if err != nil || string(data) != big {
		t.Fatalf("the file holds the whole output: %v (%d bytes)", err, len(data))
	}

	for i := 0; i < wholeOutputKeep+5; i++ {
		_, _ = r.Execute(context.Background(), "s1", "big_dump", "{}")
	}
	ws, _ := r.Machine.Workspace("s1")
	entries, _ := os.ReadDir(filepath.Join(ws, WholeOutputDir))
	if len(entries) != wholeOutputKeep {
		t.Fatalf("kept %d files", len(entries))
	}

	r.Register(llm.Tool{Type: "function", Function: llm.ToolDef{Name: "small", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		func(context.Context, string, map[string]any) (string, error) { return "ok", nil })
	if out, _ := r.Execute(context.Background(), "s1", "small", "{}"); out != "ok" {
		t.Fatalf("small: %q", out)
	}
}

func TestManyFilesInOneCall(t *testing.T) {
	r := newRegistry(t, true, nil)
	for i := 1; i <= 5; i++ {
		_ = r.Machine.WriteFile("s2", fmt.Sprintf("src/f%d.go", i), []byte(strings.Repeat("TODO fix\n", i)+"package x\n"))
	}
	out, err := r.Execute(context.Background(), "s2", "read_file", `{"files":["src/f1.go","src/f2.go","src/f3.go"]}`)
	if err != nil || strings.Count(out, "=== src/f") != 3 || !strings.Contains(out, "package x") {
		t.Fatalf("read many: %v\n%s", err, out)
	}
	out, err = r.Execute(context.Background(), "s2", "search_code", `{"pattern":"TODO","count":true}`)
	if err != nil || !strings.HasPrefix(out, "15 matching lines in 5 files") || !strings.Contains(out, "src/f5.go: 5") ||
		strings.Index(out, "f5.go") > strings.Index(out, "f1.go") {
		t.Fatalf("count: %v\n%s", err, out)
	}
	if out, _ := r.Execute(context.Background(), "s2", "read_file", `{}`); !strings.Contains(out, "filename is required") {
		t.Fatalf("no file: %s", out)
	}
}
