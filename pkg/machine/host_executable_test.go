package machine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResolvedHostHelperIsNotRewrittenAsWorkspace(t *testing.T) {
	m := newTestMachine(t)
	path := filepath.Join(t.TempDir(), "workspace", "helper")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf host-helper-ok"), 0700); err != nil {
		t.Fatal(err)
	}
	if !m.Check(path) {
		t.Fatal("installed host helper could not be probed")
	}
	result := m.Run(context.Background(), "s", RunOptions{Args: []string{path}, HostExecutable: true})
	if result.ExitCode != 0 || result.Stdout != "host-helper-ok" {
		t.Fatalf("host helper failed: %+v", result)
	}
}
