package tools

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

// A zip made on a Persian Windows keeps its names in Windows-1256: the files
// are unpacked under their Persian names, not dropped.
func TestZipWithWindowsNames(t *testing.T) {
	r := newRegistry(t, true, nil)
	persian, _ := charmap.Windows1256.NewEncoder().String("گزارش.txt")
	latin, _ := charmap.CodePage437.NewEncoder().String("café.txt")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{persian: "سلام", latin: "hi", "plain.txt": "ok"} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, NonUTF8: name != "plain.txt"})
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	if err := r.Machine.WriteFile("z", "old.zip", buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	out := call(t, r, "z", "extract_archive", map[string]any{"filename": "old.zip", "dest": "out"})
	if !strings.Contains(out, "Unpacked 3 files") {
		t.Fatalf("all three files: %s", out)
	}
	if data, _, err := r.Machine.ReadFile("z", "out/گزارش.txt", 1<<10); err != nil || string(data) != "سلام" {
		t.Errorf("the Persian name is read: %q %v", data, err)
	}
	if _, err := r.Machine.Stat("z", "out/café.txt"); err != nil {
		t.Errorf("the CP437 name is read: %v", err)
	}
	if list := call(t, r, "z", "extract_archive", map[string]any{"filename": "old.zip", "list": true}); !strings.Contains(list, "گزارش.txt") {
		t.Errorf("the listing shows the name: %s", list)
	}
}
