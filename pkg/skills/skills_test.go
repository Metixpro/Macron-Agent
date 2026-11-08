package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel), FileName)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T, root string) *Store {
	t.Helper()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEmptyFolderHasNoIndex(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing", "skills")
	s := open(t, root)
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		t.Fatalf("the folder is created: %v", err)
	}
	if s.Len() != 0 || s.Index() != "" {
		t.Fatalf("empty store: Len %d, Index %q", s.Len(), s.Index())
	}
}

func TestLoadAndIndex(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "web/blocked-page-recovery", "---\nname: blocked-page-recovery\ndescription: \"Use when a fetch fails: 403/429, paywall, WAF.\"\nversion: 1.2.0\n---\n\n# Steps\n1. Retry.")
	writeSkill(t, root, "devops/linux-server-ops", "---\ndescription: Use when diagnosing Linux servers over SSH.\n---\nbody")
	writeSkill(t, root, "devops/broken", "---\nname: broken\n---\nno description here")
	writeSkill(t, root, "flat-one", "---\ndescription: 'Use when it''s flat.'\n---\nflat")
	s := open(t, root)

	want := "<available_skills>\n" +
		"  devops:\n    - linux-server-ops: Use when diagnosing Linux servers over SSH.\n" +
		"  general:\n    - flat-one: Use when it's flat.\n" +
		"  web:\n    - blocked-page-recovery: Use when a fetch fails: 403/429, paywall, WAF.\n" +
		"</available_skills>\nWhen a job of several steps is one a skill covers, call read_skill first and follow it. " + privateNote
	if got := s.Index(); got != want {
		t.Fatalf("index:\n%s\nwant:\n%s", got, want)
	}
	if s.Len() != 3 {
		t.Fatalf("a skill without a description is skipped, the others load: Len %d", s.Len())
	}
	sk, ok := s.Get("linux-server-ops")
	if !ok || sk.Category != "devops" || sk.Version != DefaultVersion || sk.Body != "body" {
		t.Fatalf("the folder names the skill and its category: %+v", sk)
	}
	if sk, _ := s.Get("blocked-page-recovery"); sk.Version != "1.2.0" || !strings.HasPrefix(sk.Body, "# Steps") {
		t.Fatalf("front matter: %+v", sk)
	}
}

func TestReadFileStaysInside(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "web/reader", "---\ndescription: Use when reading.\n---\nRead the checklist.")
	dir := filepath.Join(root, "web", "reader")
	_ = os.MkdirAll(filepath.Join(dir, "references"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "references", "checklist.md"), []byte("- step one"), 0o644)
	secret := filepath.Join(root, "secret.txt")
	_ = os.WriteFile(secret, []byte("top secret"), 0o644)
	if err := os.Symlink(secret, filepath.Join(dir, "references", "link.md")); err != nil {
		t.Fatal(err)
	}
	s := open(t, root)

	sk, _ := s.Get("reader")
	if strings.Join(sk.Files, ",") != "references/checklist.md" {
		t.Fatalf("files (symlinks are not listed): %v", sk.Files)
	}
	if got, err := s.ReadFile("reader", "references/checklist.md"); err != nil || got != "- step one" {
		t.Fatalf("ReadFile: %q %v", got, err)
	}
	for _, bad := range []string{"../../secret.txt", "../reader/../../secret.txt", "/etc/passwd", "..", "references/../../../secret.txt", "references/link.md"} {
		if got, err := s.ReadFile("reader", bad); !errors.Is(err, ErrBadPath) {
			t.Errorf("%q must be refused, got %q %v", bad, got, err)
		}
	}
	if _, err := s.ReadFile("nope", "x.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown skill: %v", err)
	}
}

func TestReadFileCutsLongFiles(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "x/big", "---\ndescription: Use when big.\n---\nb")
	_ = os.WriteFile(filepath.Join(root, "x", "big", "huge.txt"), []byte(strings.Repeat("a", MaxFileRead+500)), 0o644)
	s := open(t, root)
	got, err := s.ReadFile("big", "huge.txt")
	if err != nil || !strings.Contains(got, "[cut:") || len(got) > MaxFileRead+100 {
		t.Fatalf("long file: %d bytes, %v", len(got), err)
	}
}

func TestSaveUpdateBumpsVersionAndIsAtomic(t *testing.T) {
	root := t.TempDir()
	s := open(t, root)
	if err := s.Save("Telegram Live Progress", "Use when a Telegram bot must stream live progress.", "1. Edit one message.", "ops"); err != nil {
		t.Fatal(err)
	}
	sk, ok := s.Get("telegram-live-progress")
	if !ok || sk.Version != "1.0.0" || sk.Category != "ops" || s.Len() != 1 {
		t.Fatalf("saved: %+v", sk)
	}
	if !strings.Contains(s.Index(), "telegram-live-progress: Use when a Telegram bot") {
		t.Fatalf("in the index right away: %s", s.Index())
	}
	if err := s.Save("telegram-live-progress", "Use when streaming progress.", "---\nname: x\n---\n2. Throttle edits.", ""); err != nil {
		t.Fatal(err)
	}
	sk, _ = s.Get("telegram-live-progress")
	if sk.Version != "1.0.1" || sk.Body != "2. Throttle edits." || sk.Category != "ops" {
		t.Fatalf("updated: %+v", sk)
	}
	entries, _ := os.ReadDir(sk.Dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("a temporary file was left: %s", e.Name())
		}
	}
	if err := s.Save("telegram-live-progress", "Use when streaming progress.", "3.", "telegram"); err != nil {
		t.Fatal(err)
	}
	if sk, _ = s.Get("telegram-live-progress"); sk.Category != "telegram" || !strings.HasSuffix(sk.Dir, filepath.Join("telegram", "telegram-live-progress")) {
		t.Fatalf("a new category moves the folder: %+v", sk)
	}
	if _, err := os.Stat(filepath.Join(root, "ops")); !os.IsNotExist(err) {
		t.Fatal("the empty old category folder is removed")
	}

	for _, c := range []struct {
		name, desc, body, cat string
		want                  error
	}{
		{"x", "d", "b", "", ErrBadName},
		{"../evil", "d", "b", "", ErrBadName},
		{"ok-name", "", "b", "", ErrNoDescription},
		{"ok-name", strings.Repeat("d", MaxDescription+1), "b", "", ErrLongDescription},
		{"ok-name", "d", "  ", "", ErrEmptyBody},
		{"ok-name", "d", strings.Repeat("b", MaxBody), "", ErrBigBody},
		{"ok-name", "d", "b", "../x", ErrBadCategory},
	} {
		if err := s.Save(c.name, c.desc, c.body, c.cat); !errors.Is(err, c.want) {
			t.Errorf("Save(%q, …): %v, want %v", c.name, err, c.want)
		}
	}
	if _, ok := s.Get("ok-name"); ok {
		t.Fatal("a refused skill is never written")
	}
}

func TestSetEnabledKeepsTheFile(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "web/aa", "---\ndescription: Use when a.\nauthor: me\n---\nA")
	writeSkill(t, root, "web/bb", "---\ndescription: Use when b.\n---\nB")
	s := open(t, root)
	if err := s.SetEnabled("aa", false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.Index(), "- aa:") || !strings.Contains(s.Index(), "- bb:") || s.Len() != 1 {
		t.Fatalf("a disabled skill leaves the index: %s", s.Index())
	}
	raw, err := os.ReadFile(filepath.Join(root, "web", "aa", FileName))
	if err != nil || !strings.Contains(string(raw), "enabled: false") || !strings.Contains(string(raw), "author: me") || !strings.HasSuffix(string(raw), "A\n") {
		t.Fatalf("the file stays, with enabled: false and its other keys: %q %v", raw, err)
	}
	if sk, ok := s.Get("aa"); !ok || sk.Enabled {
		t.Fatal("Get still finds the disabled skill")
	}
	if err := s.SetEnabled("aa", true); err != nil || s.Len() != 2 {
		t.Fatalf("on again: %v, Len %d", err, s.Len())
	}
	if err := s.Delete("aa"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "web", "aa")); !os.IsNotExist(err) {
		t.Fatal("Delete removes the folder")
	}
	if _, err := os.Stat(filepath.Join(root, "web", "bb")); err != nil {
		t.Fatal("Delete leaves the other skills")
	}
}

func TestBigIndexListsCategories(t *testing.T) {
	root := t.TempDir()
	for i := range 90 {
		writeSkill(t, root, fmt.Sprintf("cat%d/skill-%02d", i%3, i), fmt.Sprintf("---\ndescription: Use when job number %d needs doing.\n---\nx", i))
	}
	s := open(t, root)
	idx := s.Index()
	if s.Len() != 90 || strings.Count(idx, "\n") > 10 {
		t.Fatalf("90 skills: Len %d, index of %d lines:\n%s", s.Len(), strings.Count(idx, "\n"), idx)
	}
	for _, want := range []string{"cat0: 30 skills", "cat2: 30 skills", "list_skills", "read_skill"} {
		if !strings.Contains(idx, want) {
			t.Errorf("index misses %q:\n%s", want, idx)
		}
	}
}

func TestIndexIsNotRebuiltPerCall(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "web/aa", "---\ndescription: Use when a.\n---\nA")
	s := open(t, root)
	before := s.Index()
	writeSkill(t, root, "web/new", "---\ndescription: Use when new.\n---\nN")
	if s.Index() != before {
		t.Fatal("the index comes from the cache until Reload")
	}
	if err := s.Reload(); err != nil || !strings.Contains(s.Index(), "- new:") {
		t.Fatalf("Reload reads the disk: %v\n%s", err, s.Index())
	}
}

func TestImportAndTouch(t *testing.T) {
	root := t.TempDir()
	s := open(t, root)
	name, err := s.Import("\uFEFF---\ndescription: Use when importing.\ncategory: tools\n---\n# Import\nDo it.", "Uploaded File")
	if err != nil || name != "uploaded-file" {
		t.Fatalf("Import: %q %v", name, err)
	}
	if _, err := s.Import("no front matter at all", "x"); !errors.Is(err, ErrBadName) {
		t.Fatalf("a bad fallback name: %v", err)
	}
	s.Touch(name)
	s.Touch(name)
	if sk, _ := s.Get(name); sk.Uses != 2 {
		t.Fatalf("uses: %d", sk.Uses)
	}
	if sk, _ := open(t, root).Get(name); sk.Uses != 2 {
		t.Fatal("the use counts survive a restart")
	}
}

func TestSeedOnlyOnce(t *testing.T) {
	root := t.TempDir()
	s := open(t, root)
	added, err := s.Seed()
	if err != nil || len(added) != len(s.Snapshot()) || len(added) < 10 {
		t.Fatalf("seed: %v %v", added, err)
	}
	for _, sk := range s.Snapshot() {
		if !strings.HasPrefix(sk.Description, "Use when") || len([]rune(sk.Description)) > MaxDescription || !sk.Enabled ||
			len(sk.Body) < 300 || sk.Category == DefaultCategory {
			t.Errorf("built-in skill %s/%s: %q (%d chars)", sk.Category, sk.Name, sk.Description, len(sk.Body))
		}
	}
	if !strings.Contains(s.Index(), "systematic-debugging") || strings.Count(s.Index(), "\n") > 40 {
		t.Fatalf("index:\n%s", s.Index())
	}
	for _, n := range []string{"linux-server-ops", "blocked-page-recovery", "downloader-pitfalls"} {
		sk, ok := s.Get(n)
		if !ok || !sk.Enabled || sk.Description == "" || sk.Body == "" {
			t.Fatalf("seed skill %s: %+v", n, sk)
		}
	}
	if err := s.Delete("linux-server-ops"); err != nil {
		t.Fatal(err)
	}
	if added, err := s.Seed(); err != nil || len(added) != 0 {
		t.Fatalf("a deleted built-in skill stays deleted: %v %v", added, err)
	}
	if _, ok := s.Get("linux-server-ops"); ok {
		t.Fatal("not seeded again")
	}
}

func TestParseAndRender(t *testing.T) {
	meta, body := Parse("---\r\nname: x\r\ndescription: \"a \\\"quoted\\\" one: yes\"\r\n  nested: ignored\r\n---\r\nBody\r\n")
	if meta.get("name") != "x" || meta.get("description") != `a "quoted" one: yes` || meta.get("nested") != "" || body != "Body" {
		t.Fatalf("parse: %v %q", meta, body)
	}
	again, b2 := Parse(Render(meta, body))
	if again.get("description") != meta.get("description") || b2 != body {
		t.Fatalf("round trip: %v %q", again, b2)
	}
	if m, b := Parse("---\nno end"); m != nil || b != "---\nno end" {
		t.Fatal("an unclosed front matter is just text")
	}
	if bumpPatch("1.2.9") != "1.2.10" || bumpPatch("2") != "2.0.1" || bumpPatch("x.y.z") != DefaultVersion {
		t.Fatal("bumpPatch")
	}
}

// A skill written by hand (the owner's editor, or the agent through a shell)
// is found without a restart.
func TestHandWrittenSkillsAreFound(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get("by-hand"); ok {
		t.Fatal("not there yet")
	}
	folder := filepath.Join(dir, "misc", "by-hand")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: by-hand\ndescription: \"Use when testing.\"\n---\n# By hand\nDo the thing.\n"
	if err := os.WriteFile(filepath.Join(folder, FileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	st.checked.Store(0) // the folder is looked at every few seconds; skip the wait
	if _, ok := st.Get("by-hand"); !ok {
		t.Fatal("a skill written by hand must appear")
	}
	if !strings.Contains(st.Index(), "by-hand") {
		t.Error("and be in the index")
	}
}
