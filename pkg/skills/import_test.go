package skills

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestImportZipValidAndOversizedReject(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("demo/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("---\nname: demo-skill\ndescription: A valid test skill\n---\nRun echo ok.\n"))
	extra, err := zw.Create("demo/helper.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = extra.Write([]byte("helper content\n"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	res, err := st.ImportZip(buf.Bytes())
	if err != nil {
		t.Fatalf("ImportZip valid failed: %v", err)
	}
	if len(res.Imported) != 1 || res.Imported[0] != "demo-skill" || len(res.Skipped) != 0 {
		t.Fatalf("unexpected import result: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(st.Dir(), DefaultCategory, "demo-skill", "helper.txt")); err != nil {
		t.Fatalf("imported helper file missing: %v", err)
	}

	// Build a zip with 16 x 2MB entries (> 30MB maxZipBytes) to ensure unpacked cap triggers.
	var big bytes.Buffer
	bzw := zip.NewWriter(&big)
	mw, _ := bzw.Create("huge/SKILL.md")
	_, _ = mw.Write([]byte("---\nname: huge-skill\ndescription: Oversized\n---\nBody\n"))
	payload := bytes.Repeat([]byte("a"), 2<<20)
	for i := 0; i < 16; i++ {
		ew, _ := bzw.Create(filepath.ToSlash(filepath.Join("huge", string(rune('a'+i))+".bin")))
		_, _ = ew.Write(payload)
	}
	_ = bzw.Close()

	if _, err := st.ImportZip(big.Bytes()); err == nil {
		t.Fatal("ImportZip accepted archive exceeding maxZipBytes")
	}
}
