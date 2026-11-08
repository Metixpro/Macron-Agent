package cli

import (
	"archive/tar"
	"compress/gzip"
	"encoding/xml"
	"io"
	"os"
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
