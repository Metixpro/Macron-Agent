package cli

import (
	"archive/tar"
	"compress/gzip"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestServicePathsAreLiteral(t *testing.T) {
	home := "/home/owner/a b%u\"\n[Service]\nEnvironment=INJECTED=yes"
	binary := "/opt/agent $HOME%u/macron"
	s := renderSystemd(unit{Home: home, Binary: binary})
	if strings.Contains(s, "\nEnvironment=INJECTED=") || !strings.Contains(s, `%%u`) || !strings.Contains(s, `$$HOME`) || !strings.Contains(s, `UMask=0077`) {
		t.Fatalf("unsafe service rendering: %s", s)
	}
	// Decode the actual XML, including characters invalid without escaping.
	p := renderLaunchd(unit{Home: `/home/a&b<test>`, Binary: `/opt/a&b/macron`})
	d := xml.NewDecoder(strings.NewReader(p))
	found := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if text, ok := tok.(xml.CharData); ok && string(text) == `/opt/a&b/macron` {
			found = true
		}
	}
	if !found {
		t.Fatal("program path did not round trip through XML")
	}
}

func TestBackupInsideHomeExcludesItselfAndKeepsSimilarlyNamedFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MACRON_HOME", home)
	for _, name := range []string{"USER.md", "macron.db-notes", "macron.db.bak"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("keep me"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	output := filepath.Join(home, "backup.tar.gz")
	term, log := testIO("")
	if code := backup(term, []string{output}); code != 0 {
		t.Fatalf("backup: %d %s", code, log)
	}
	f, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[h.Name] = true
	}
	if names["macron/backup.tar.gz"] {
		t.Fatal("archive included itself")
	}
	for _, n := range []string{"USER.md", "macron.db-notes", "macron.db.bak"} {
		if !names["macron/"+n] {
			t.Errorf("lost %s", n)
		}
	}
}

func TestBackupRejectsMissingHome(t *testing.T) {
	t.Setenv("MACRON_HOME", filepath.Join(t.TempDir(), "missing"))
	output := filepath.Join(t.TempDir(), "backup.tar.gz")
	term, _ := testIO("")
	if backup(term, []string{output}) == 0 {
		t.Fatal("reported success for missing data")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("left an archive behind")
	}
}

// WorkingDirectory= must never be quoted: systemd reads a quoted value as a
// relative path and refuses the whole unit ("path is not absolute").
func TestSystemdWorkingDirectoryIsRaw(t *testing.T) {
	for _, home := range []string{"/home/debian/.macron", "/home/debian/my dir/.macron"} {
		s := renderSystemd(unit{Home: home, Binary: "/home/debian/.local/bin/macron"})
		if !strings.Contains(s, "\nWorkingDirectory="+home+"\n") {
			t.Errorf("WorkingDirectory not raw for %q:\n%s", home, s)
		}
	}
	s := renderSystemd(unit{Home: "/home/debian/my dir/.macron", Binary: "/opt/my apps/macron"})
	for _, want := range []string{`Environment="MACRON_HOME=/home/debian/my dir/.macron"`, `ExecStart="/opt/my apps/macron"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s:\n%s", want, s)
		}
	}
}

// The unit systemd will really load: checked by systemd-analyze when the
// machine has it (Linux CI and servers do).
func TestSystemdUnitVerifies(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("no systemd-analyze")
	}
	for _, dir := range []string{"plain", "my dir"} {
		root := filepath.Join(t.TempDir(), dir)
		home := filepath.Join(root, ".macron")
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		bin, _ := exec.LookPath("true")
		u := unit{Home: home, Binary: bin}
		path := filepath.Join(root, unitName)
		if err := os.WriteFile(path, []byte(renderSystemd(u)), 0o644); err != nil {
			t.Fatal(err)
		}
		out, checked, err := verifyUnit(u, path)
		if !checked {
			t.Skipf("systemd-analyze cannot run here: %s", out)
		}
		if err != nil {
			t.Errorf("%s: unit rejected: %v\n%s\n%s", dir, err, out, renderSystemd(u))
		}
	}
	// And the old quoted form really is rejected, so the check can fail.
	root := t.TempDir()
	path := filepath.Join(root, unitName)
	bad := "[Service]\nWorkingDirectory=\"" + root + "\"\nExecStart=/bin/true\n"
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, checked, err := verifyUnit(unit{}, path); checked && err == nil {
		t.Error("a quoted WorkingDirectory passed verification")
	}
}
