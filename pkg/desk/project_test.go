package desk

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type projectTestHost struct {
	Host
	dir   string
	paths []string
}

func (h projectTestHost) DataDir() string        { return h.dir }
func (h projectTestHost) Projects() []string     { return h.paths }
func (h projectTestHost) DefaultProject() string { return "" }
func (h projectTestHost) Servers() []ServerSpec  { return nil }

func TestProjectPathCaseAndHistoryMigration(t *testing.T) {
	root := t.TempDir()
	upper, lower := filepath.Join(root, "CaseProject"), filepath.Join(root, "caseproject")
	if runtime.GOOS == "windows" {
		if projectID(upper) != projectID(lower) {
			t.Fatal("Windows aliases split")
		}
		return
	}
	if projectID(upper) == projectID(lower) {
		t.Fatal("distinct case-sensitive paths collide")
	}
	data := t.TempDir()
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(upper))))
	old := filepath.Join(data, "desktop", hex.EncodeToString(sum[:6])+".jsonl")
	if err := os.MkdirAll(filepath.Dir(old), 0700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("{\"kind\":\"assistant\",\"text\":\"saved history\",\"at\":1}\n")
	if err := os.WriteFile(old, payload, 0600); err != nil {
		t.Fatal(err)
	}
	srv := New(projectTestHost{dir: data, paths: []string{upper}})
	defer srv.Close()
	a, b := srv.addProject(upper), srv.addProject(lower)
	if a == b || a.path != upper || b.path != lower {
		t.Fatal("wrong project selected")
	}
	a.loadLocked()
	b.loadLocked()
	if len(a.blocks) != 1 || a.blocks[0].Text != "saved history" || len(b.blocks) != 0 {
		t.Fatal("history lost or shared across projects")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("legacy log still eligible for another project", err)
	}
	migrateProjectLog(data, upper)
	if got, err := os.ReadFile(a.file()); err != nil || string(got) != string(payload) {
		t.Fatal("migration changed history", err)
	}
}

func TestStripInternalNotes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "deepseek_work_notes_suffix",
			in:   "تحلیل انجام شد.\n\n<work_notes>\n$ Get-Item zip -> exit 0\nread_file README.md\n</work_notes>\n",
			want: "تحلیل انجام شد.",
		},
		{
			name: "unclosed_streaming_work_notes",
			in:   "Done.\n<work_notes>\n$ ls -la\nstill streaming...",
			want: "Done.",
		},
		{
			name: "partial_opening_tag",
			in:   "Result ready.\n<work_notes",
			want: "Result ready.",
		},
		{
			name: "files_sent_and_orphan_close",
			in:   "Sent the report.\n<files_sent>report.pdf, summary.csv</files_sent>\n</work_notes>",
			want: "Sent the report.",
		},
		{
			name: "preserve_inside_fenced_code",
			in:   "Example:\n```xml\n<work_notes>keep me</work_notes>\n```\n<work_notes>drop me</work_notes>",
			want: "Example:\n```xml\n<work_notes>keep me</work_notes>\n```",
		},
		{
			name: "preserve_inside_inline_code",
			in:   "Tag `<work_notes>` is internal.\n<work_notes>secret</work_notes>",
			want: "Tag `<work_notes>` is internal.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripInternalNotes(tc.in); got != tc.want {
				t.Fatalf("stripInternalNotes(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	rendered := renderMarkdown("Hello world.\n\n<work_notes>\ninternal step\n</work_notes>")
	if strings.Contains(rendered, "work_notes") || strings.Contains(rendered, "internal step") {
		t.Fatalf("renderMarkdown leaked work_notes: %s", rendered)
	}
	if !strings.Contains(rendered, "Hello world.") {
		t.Fatalf("renderMarkdown dropped body: %s", rendered)
	}
}
