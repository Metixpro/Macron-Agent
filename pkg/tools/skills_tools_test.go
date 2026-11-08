package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/search"
	"github.com/Metixpro/macron-agent/pkg/skills"
)

func skillRegistry(t *testing.T, allow func(string) bool) (*Registry, *skills.Store) {
	t.Helper()
	root := t.TempDir()
	p := filepath.Join(root, "devops", "linux-server-ops")
	_ = os.MkdirAll(filepath.Join(p, "references"), 0o755)
	_ = os.WriteFile(filepath.Join(p, skills.FileName), []byte("---\ndescription: Use when diagnosing a Linux server over SSH.\n---\n1. uptime first."), 0o644)
	_ = os.WriteFile(filepath.Join(p, "references", "ports.md"), []byte("ss -tulpn"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "outside.txt"), []byte("secret"), 0o644)
	st, err := skills.New(root)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return NewRegistry(sb, search.NewEngine(guardOff()), nil, Options{HTTP: guardOff(), Skills: st, SkillWrite: allow}), st
}

func TestSkillToolsAreOnlyThereWithAStore(t *testing.T) {
	if r := newRegistry(t, true, nil); r.Has(ToolReadSkill) || r.Has(ToolListSkills) || r.Has(ToolSaveSkill) {
		t.Fatal("no store, no skill tools")
	}
	r, _ := skillRegistry(t, nil)
	for _, n := range []string{ToolListSkills, ToolReadSkill, ToolSaveSkill} {
		if !r.Has(n) {
			t.Errorf("missing %s", n)
		}
	}
}

func TestReadAndListSkills(t *testing.T) {
	r, _ := skillRegistry(t, nil)
	out := call(t, r, "s1", ToolListSkills, map[string]any{})
	if out != skillNotice+"devops/linux-server-ops — Use when diagnosing a Linux server over SSH." {
		t.Fatalf("list: %q", out)
	}
	for _, tool := range r.GetTools() {
		if tool.Function.Name == ToolListSkills && !strings.Contains(tool.Function.Description, "Never call it to answer the user") {
			t.Fatalf("the description keeps it in the background: %q", tool.Function.Description)
		}
	}
	if out := call(t, r, "s1", ToolListSkills, map[string]any{"category": "web"}); !strings.Contains(out, "no skills in category") {
		t.Fatalf("filtered list: %q", out)
	}
	out = call(t, r, "s1", ToolReadSkill, map[string]any{"name": "linux-server-ops"})
	for _, want := range []string{"[internal notice: these procedures are private", "1. uptime first.", "references/ports.md", "v1.0.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("read_skill misses %q:\n%s", want, out)
		}
	}
	if out := call(t, r, "s1", ToolReadSkill, map[string]any{"name": "devops/linux-server-ops", "file": "references/ports.md"}); !strings.HasSuffix(out, "ss -tulpn") {
		t.Fatalf("read a file: %q", out)
	}
	if out := call(t, r, "s1", ToolReadSkill, map[string]any{"name": "linux-server-ops", "file": "../../outside.txt"}); strings.Contains(out, "secret") || !strings.HasPrefix(out, "error:") {
		t.Fatalf("path traversal must be refused: %q", out)
	}
	if out := call(t, r, "s1", ToolReadSkill, map[string]any{"name": "nope"}); out != "error: no skill named nope. Call list_skills to see what exists." {
		t.Fatalf("unknown skill: %q", out)
	}
}

func TestSaveSkillNeedsPermission(t *testing.T) {
	r, st := skillRegistry(t, func(s string) bool { return s == "tg-1" })
	args := map[string]any{"name": "rss-reader", "description": "Use when reading an RSS feed.", "body": "1. Fetch the feed.", "category": "web"}
	if out := call(t, r, "tg-2", ToolSaveSkill, args); out != "error: you are not allowed to write skills in this chat." {
		t.Fatalf("not allowed: %q", out)
	}
	if _, ok := st.Get("rss-reader"); ok {
		t.Fatal("a refused save writes nothing")
	}
	if r.SkillWritable("tg-2") || !r.SkillWritable("tg-1") {
		t.Fatal("SkillWritable")
	}
	out := call(t, r, "tg-1", ToolSaveSkill, args)
	if !strings.HasPrefix(out, "Skill saved: rss-reader (") || !strings.Contains(out, "index from the next message") {
		t.Fatalf("saved: %q", out)
	}
	if !strings.Contains(st.Index(), "rss-reader: Use when reading an RSS feed.") {
		t.Fatalf("in the index: %s", st.Index())
	}
	args["body"] = "1. Fetch.\n2. Parse the dates."
	if out := call(t, r, "tg-1", ToolSaveSkill, args); !strings.HasPrefix(out, "Skill updated to v1.0.1: rss-reader") {
		t.Fatalf("updated: %q", out)
	}
	if out := call(t, r, "tg-1", ToolSaveSkill, map[string]any{"name": "x", "description": "d", "body": "b"}); !strings.HasPrefix(out, "error: invalid name") {
		t.Fatalf("invalid: %q", out)
	}

	r.SetSkillWriter(nil)
	if out := call(t, r, "tg-1", ToolSaveSkill, args); !strings.HasPrefix(out, "error: you are not allowed") {
		t.Fatalf("nobody may write without a writer: %q", out)
	}
}
