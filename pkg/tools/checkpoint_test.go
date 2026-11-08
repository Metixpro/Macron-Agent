package tools

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCheckpointDoesNotClaimMissingFilesWereSaved(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar unavailable")
	}
	r := newRegistry(t, true, nil)
	r.CheckpointDir = t.TempDir()
	note := r.checkpoint(context.Background(), "backup", []string{"missing.txt"})
	if !strings.Contains(note, "failed") {
		t.Fatalf("missing file reported as saved: %s", note)
	}
}

func TestCheckpointsKeepSeparatePrivateArchives(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar unavailable")
	}
	r := newRegistry(t, true, nil)
	r.CheckpointDir = t.TempDir()
	if err := r.Machine.WriteFile("backup", "notes.txt", []byte("first")); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if note := r.checkpoint(context.Background(), "backup", []string{"notes.txt"}); !strings.HasPrefix(note, "checkpoint kept") {
			t.Fatal(note)
		}
	}
	entries, err := os.ReadDir(r.CheckpointDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected three retained backups, got %d", len(entries))
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("backup permissions %o", info.Mode().Perm())
		}
	}
}
