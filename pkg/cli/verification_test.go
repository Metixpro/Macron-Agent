package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnitVerifierDoesNotApproveUnclassifiedFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	bin := filepath.Join(dir, "systemd-analyze")
	for _, tc := range []struct {
		output  string
		checked bool
	}{
		{"unclassified verifier failure", true},
		{"Failed to create directory '/run/systemd/': Read-only file system", false},
	} {

		script := "#!/bin/sh\nprintf '%s\\n' \"" + tc.output + "\"\nexit 1\n"
		if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		_, checked, err := verifyUnit(unit{System: true}, "/tmp/macron.service")
		if checked != tc.checked {
			t.Fatalf("checked=%v, expected %v", checked, tc.checked)
		}
		if tc.checked && err == nil {
			t.Fatal("failed verifier reported success")
		}
	}
}
