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
