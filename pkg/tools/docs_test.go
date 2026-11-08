package tools

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

func TestFoldAndTerms(t *testing.T) {
	if got := textutil.Fold("كتاب‌هاي عربي ۱۲۳ آزمونِ ـمـ"); got != "کتابهای عربی 123 ازمون م" {
		t.Errorf("fold: %q", got)
	}
	got := strings.Join(terms(textutil.Fold("کتاب‌های قدیمی در کتابخانه")), ",")
	if got != "کتابهای,کتاب,قدیمی,کتابخانه" {
		t.Errorf("terms: %s", got)
	}
	for in, want := range map[string]string{"libraries": "library", "running": "runn", "boxes": "box", "cats": "cat", "class": "class", "virus": "virus"} {
		if s := stem(in); s != want {
			t.Errorf("stem(%s) = %s, want %s", in, s, want)
		}
	}
}

func TestChunkTextOverlapsAndCoversEverything(t *testing.T) {
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, fmt.Sprintf("line %02d %s", i, strings.Repeat("word ", 8)))
	}
	chunks := chunkText(strings.Join(lines, "\n"))
	if len(chunks) < 3 {
		t.Fatalf("chunks: %d", len(chunks))
	}
	all := strings.Join(chunks, "\n")
	for i := 0; i < 60; i++ {
		if !strings.Contains(all, fmt.Sprintf("line %02d ", i)) {
			t.Errorf("line %d lost", i)
		}
	}
	for i := 1; i < len(chunks); i++ {
		prev := strings.Split(chunks[i-1], "\n")
		if !strings.HasPrefix(chunks[i], prev[len(prev)-1]) {
			t.Errorf("chunk %d does not overlap with the previous one", i)
		}
	}
	if got := chunkText(strings.Repeat("x", 5000)); len(got) < 4 {
		t.Errorf("a long line is cut: %d chunks", len(got))
	}
}

func TestDocumentReaderOnText(t *testing.T) {
	r := newRegistry(t, true, nil)
	var sb strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&sb, "بخش %d درباره موضوعات عمومی و تکراری است. %s\n\n", i, strings.Repeat("متن پرکننده برای حجم. ", 40))
		if i == 27 {
			sb.WriteString("در این بخش درباره فتوسنتز گیاهان و نقش کلروفیل در جذب نور خورشید توضیح داده شده است.\n\n")
		}
	}
	_ = r.Machine.WriteFile("d", "notes.txt", []byte(sb.String()))
	out := call(t, r, "d", ToolDocumentReader, map[string]any{"action": "search", "filename": "/workspace/notes.txt", "query": "فتوسنتز چیست و كلروفيل چه نقشي دارد؟"})
	if !strings.Contains(out, "(indexed now:") || !strings.Contains(out, "[1] part") || !strings.Contains(out, "کلروفیل در جذب نور") {
		t.Fatalf("search: %s", out)
	}
	first := out[strings.Index(out, "[1]"):]
	if strings.Contains(first[:strings.Index(first, "\n")], "part 1 ") {
		t.Errorf("the right passage must rank first: %s", first[:200])
	}
	if again := call(t, r, "d", ToolDocumentReader, map[string]any{"action": "search", "filename": "notes.txt", "query": "فتوسنتز"}); strings.Contains(again, "indexed now") {
		t.Error("the index is reused")
	}
	if out := call(t, r, "d", ToolDocumentReader, map[string]any{"action": "search", "filename": "notes.txt", "query": "quantum chromodynamics"}); !strings.Contains(out, "Nothing matches") {
		t.Errorf("no match: %s", out)
	}
	out = call(t, r, "d", ToolDocumentReader, map[string]any{"action": "read", "filename": "notes.txt", "from_page": 2, "to_page": 50})
	if !strings.Contains(out, "--- part 2 ---") || strings.Contains(out, "--- part 12 ---") {
		t.Errorf("read is limited to 10 parts: %s", out[:300])
	}
	if _, err := r.Machine.Stat("d", docIndexName("notes.txt")); err != nil {
		t.Error("the extracted text is kept next to the file")
	}

	_ = r.Machine.WriteFile("d", "notes.txt", []byte("کاملاً متن تازه درباره ستاره‌ها"))
	if out := call(t, r, "d", ToolDocumentReader, map[string]any{"action": "search", "filename": "notes.txt", "query": "ستاره‌ها"}); !strings.Contains(out, "indexed now") || !strings.Contains(out, "ستاره") {
		t.Errorf("reindex: %s", out)
	}
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "search", "filename": "notes.txt"}, "query is required"},
		{map[string]any{"action": "x", "filename": "notes.txt"}, "action must be"},
		{map[string]any{"action": "outline", "filename": "missing.pdf"}, "not a file"},
		{map[string]any{"action": "outline", "filename": "../../etc/passwd"}, "not a file"},
	} {
		if out := call(t, r, "d", ToolDocumentReader, c.args); !strings.Contains(out, c.want) {
			t.Errorf("%v: %s", c.args, out)
		}
	}
	_ = r.Machine.WriteFile("d", "a.png", []byte("x"))
	if out := call(t, r, "d", ToolDocumentReader, map[string]any{"action": "outline", "filename": "a.png"}); !strings.Contains(out, "not supported") {
		t.Errorf("format: %s", out)
	}
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDocumentReaderOnDocxAndEpub(t *testing.T) {
	r := newRegistry(t, true, nil)
	para := func(style, text string) string {
		s := ""
		if style != "" {
			s = `<w:pPr><w:pStyle w:val="` + style + `"/></w:pPr>`
		}
		return `<w:p>` + s + `<w:r><w:t>` + text + `</w:t></w:r></w:p>`
	}
	body := para("Heading1", "Introduction") + para("", strings.Repeat("General text. ", 300)) +
		para("Heading2", "Budget plan") + para("", "The budget for 2027 is 4.2 billion and covers new libraries.")
	docx := zipBytes(t, map[string]string{"word/document.xml": `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body + `</w:body></w:document>`})
	_ = r.Machine.WriteFile("e", "report.docx", docx)
	out := call(t, r, "e", ToolDocumentReader, map[string]any{"action": "outline", "filename": "report.docx"})
	if !strings.Contains(out, "Introduction — part 1") || !strings.Contains(out, "  Budget plan — part 2") {
		t.Errorf("docx outline: %s", out)
	}
	if out := call(t, r, "e", ToolDocumentReader, map[string]any{"action": "search", "filename": "report.docx", "query": "library budget"}); !strings.Contains(out, "4.2 billion") {
		t.Errorf("docx search (libraries ~ library): %s", out)
	}

	epub := zipBytes(t, map[string]string{
		"META-INF/container.xml": `<container><rootfiles><rootfile full-path="OEBPS/content.opf"/></rootfiles></container>`,
		"OEBPS/content.opf": `<package><metadata><dc:title xmlns:dc="http://purl.org/dc/elements/1.1/">کتاب نمونه</dc:title></metadata>
			<manifest><item id="c1" href="ch1.xhtml"/><item id="c2" href="text/ch2.xhtml"/></manifest>
			<spine><itemref idref="c1"/><itemref idref="c2"/></spine></package>`,
		"OEBPS/ch1.xhtml":      `<html><head><title>فصل اول</title></head><body><p>شروع داستان در تهران.</p></body></html>`,
		"OEBPS/text/ch2.xhtml": `<html><head><title>فصل دوم</title></head><body><p>سفر به اصفهان و دیدن سی‌وسه‌پل.</p></body></html>`,
	})
	_ = r.Machine.WriteFile("e", "book.epub", epub)
	out = call(t, r, "e", ToolDocumentReader, map[string]any{"action": "outline", "filename": "book.epub"})
	if !strings.Contains(out, `title "کتاب نمونه"`) || !strings.Contains(out, "فصل اول — part 1") || !strings.Contains(out, "فصل دوم") {
		t.Errorf("epub outline: %s", out)
	}
	if out := call(t, r, "e", ToolDocumentReader, map[string]any{"action": "search", "filename": "book.epub", "query": "سی و سه پل اصفهان"}); !strings.Contains(out, "سفر به اصفهان") {
		t.Errorf("epub search: %s", out)
	}
}

func TestDocumentReaderOnALongPDF(t *testing.T) {
	if exec.Command("python3", "-c", "import pypdf, reportlab").Run() != nil {
		t.Skip("pypdf and reportlab are needed")
	}
	r := newRegistry(t, true, nil)

	script := `python3 - <<'EOF'
from reportlab.pdfgen import canvas
c = canvas.Canvas("big.pdf")
for p in range(1, 61):
    if p % 20 == 1:
        c.bookmarkPage("ch%d" % p)
        c.addOutlineEntry("Chapter %d" % (p // 20 + 1), "ch%d" % p, level=0)
    c.drawString(72, 760, "Page %d of the handbook." % p)
    c.drawString(72, 740, "Routine maintenance notes for general equipment.")
    if p == 43:
        c.drawString(72, 720, "The emergency shutdown code for the reactor is 7341.")
    c.showPage()
c.save()
EOF`
	if res := r.Machine.Run(t.Context(), "p", scriptRun(script)); res.ExitCode != 0 {
		t.Fatalf("making the PDF: %s %s", res.Stdout, res.Stderr)
	}
	out := call(t, r, "p", ToolDocumentReader, map[string]any{"action": "outline", "filename": "big.pdf"})
	if !strings.Contains(out, "big.pdf — 60 pages") || !strings.Contains(out, "Chapter 3 — page 41") {
		t.Fatalf("outline: %s", out)
	}
	out = call(t, r, "p", ToolDocumentReader, map[string]any{"action": "search", "filename": "big.pdf", "query": "reactor shutdown code", "max_results": 3})
	if !strings.Contains(out, "[1] page 43") || !strings.Contains(out, "7341") {
		t.Fatalf("search: %s", out)
	}
	out = call(t, r, "p", ToolDocumentReader, map[string]any{"action": "read", "filename": "big.pdf", "from_page": 42, "to_page": 44})
	if !strings.Contains(out, "--- page 42 ---") || !strings.Contains(out, "--- page 44 ---") || !strings.Contains(out, "7341") {
		t.Errorf("read: %s", out)
	}
	if out := call(t, r, "p", "pdf_tool", map[string]any{"action": "read", "filename": "big.pdf"}); !strings.Contains(out, "Showing pages 1-30 of 60") {
		t.Errorf("pdf_tool points to document_reader for long PDFs: %s", out[len(out)-200:])
	}
}

func scriptRun(script string) machine.RunOptions {
	return machine.RunOptions{Args: []string{"bash", "-c", script}, Timeout: 60 * time.Second}
}
