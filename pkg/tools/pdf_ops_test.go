package tools

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func pdfPages(t *testing.T, file string) string {
	t.Helper()
	out, _ := exec.Command("python3", "-c", "import pypdf,sys; r=pypdf.PdfReader(sys.argv[1]); print(len(r.pages), [p.get('/Rotate',0) for p in r.pages])", file).Output()
	return strings.TrimSpace(string(out))
}

func TestPDFOps(t *testing.T) {
	if exec.Command("python3", "-c", "import pypdf, PIL").Run() != nil {
		t.Skip("pypdf or Pillow is missing")
	}
	r := newRegistry(t, true, nil)
	ws, _ := r.Machine.Workspace("p")
	mk := `import pypdf,sys
for name, n in (("a.pdf", 3), ("b.pdf", 2)):
    w = pypdf.PdfWriter()
    for i in range(n): w.add_blank_page(width=300, height=400)
    w.write(sys.argv[1] + "/" + name)`
	if out, err := exec.Command("python3", "-c", mk, ws).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	if out := call(t, r, "p", "pdf_tool", map[string]any{"action": "merge", "filename": "all.pdf", "files": []string{"a.pdf", "b.pdf"}}); !strings.Contains(out, "all.pdf (5 pages") {
		t.Fatalf("merge: %s", out)
	}
	if out := call(t, r, "p", "pdf_tool", map[string]any{"action": "extract_pages", "filename": "all.pdf", "pages": "۱ تا ۲، ۵"}); !strings.Contains(out, "all_pages.pdf (3 pages of 5)") {
		t.Errorf("extract with Persian digits: %s", out)
	}
	if out := call(t, r, "p", "pdf_tool", map[string]any{"action": "delete_pages", "filename": "all.pdf", "pages": "1", "output": "four.pdf"}); !strings.Contains(out, "four.pdf (4 pages of 5)") {
		t.Errorf("delete: %s", out)
	}
	call(t, r, "p", "pdf_tool", map[string]any{"action": "rotate", "filename": "a.pdf", "pages": "2", "degrees": 90})
	if got := pdfPages(t, filepath.Join(ws, "a_rotated.pdf")); got != "3 [0, 90, 0]" {
		t.Errorf("rotate: %s", got)
	}
	if out := call(t, r, "p", "pdf_tool", map[string]any{"action": "extract_pages", "filename": "a.pdf", "pages": "9"}); !strings.Contains(out, "page 9 does not exist (the file has 3 pages)") {
		t.Errorf("a missing page: %s", out)
	}
	_ = r.Machine.WriteFile("p", "scan1.jpg", testJPEG(t, 600, 800))
	_ = r.Machine.WriteFile("p", "scan2.jpg", testJPEG(t, 800, 600))
	if out := call(t, r, "p", "pdf_tool", map[string]any{"action": "from_images", "filename": "scans", "files": []string{"scan1.jpg", "scan2.jpg"}}); !strings.Contains(out, "scans.pdf (2 pages") {
		t.Errorf("from images: %s", out)
	}
	if out := call(t, r, "p", "pdf_tool", map[string]any{"action": "compress", "filename": "scans.pdf"}); !strings.Contains(out, "scans_small.pdf") {
		t.Errorf("compress: %s", out)
	}
	if exec.Command("python3", "-c", "import fitz").Run() != nil {
		if _, err := exec.LookPath("pdftoppm"); err != nil {
			t.Skip("neither PyMuPDF nor pdftoppm")
		}
	}
	out := call(t, r, "p", "pdf_tool", map[string]any{"action": "to_images", "filename": "scans.pdf"})
	if !strings.Contains(out, "scans_p1.jpg, scans_p2.jpg") {
		t.Fatalf("to images: %s", out)
	}
	if info, err := r.Machine.Stat("p", "scans_p2.jpg"); err != nil || info.Size() < 1000 {
		t.Error("the page pictures exist")
	}
}
