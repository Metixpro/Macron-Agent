package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactVersioningAndRollback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "macron-ver-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	vm := NewVersionManager(tmpDir)

	// Step 1: Create initial file
	f1 := filepath.Join(tmpDir, "app.py")
	if err := os.WriteFile(f1, []byte("print('v1')"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap1, err := vm.Snapshot(1, "initial file")
	if err != nil || snap1.FileCount != 1 {
		t.Fatalf("snap1: err=%v, count=%d", err, snap1.FileCount)
	}

	// Step 2: Modify file and add new file
	if err := os.WriteFile(f1, []byte("print('v2 corrupted')"), 0o644); err != nil {
		t.Fatal(err)
	}
	f2 := filepath.Join(tmpDir, "extra.txt")
	if err := os.WriteFile(f2, []byte("extra data"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap2, err := vm.Snapshot(2, "v2 modified")
	if err != nil || snap2.FileCount != 2 {
		t.Fatalf("snap2: err=%v, count=%d", err, snap2.FileCount)
	}

	// List snapshots
	snaps := vm.ListSnapshots()
	if len(snaps) != 2 {
		t.Fatalf("snapshots count = %d, want 2", len(snaps))
	}

	// Rollback to Step 1
	restored, err := vm.Rollback(1)
	if err != nil {
		t.Fatalf("Rollback(1): %v", err)
	}
	if restored != 1 {
		t.Errorf("restored count = %d, want 1", restored)
	}

	// Verify file content is back to v1
	b, err := os.ReadFile(f1)
	if err != nil || string(b) != "print('v1')" {
		t.Errorf("f1 content after rollback = %q, want print('v1')", string(b))
	}
}
