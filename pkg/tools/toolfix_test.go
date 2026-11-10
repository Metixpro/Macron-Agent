package tools

import (
	"crypto/rand"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

func TestPackFilesKeepsOneArchiveUnlessAskedToSplit(t *testing.T) {
	r := newRegistry(t, true, nil)
	big := make([]byte, 3<<20)
	_, _ = rand.Read(big)
	_ = r.Machine.WriteFile("ar", "big/blob.bin", big)
	out := call(t, r, "ar", ToolPackFiles, map[string]any{"paths": []string{"big"}})
	if strings.Contains(out, "Cut into") || !strings.Contains(out, "Created big.zip: 1 files") {
		t.Fatalf("a 3 MB archive nobody asked to split: %s", out)
	}
	if info, err := r.Machine.Stat("ar", "big.zip"); err != nil || info.Size() < 3<<20 {
		t.Errorf("the archive is not whole: %v", err)
	}
	if _, err := r.Machine.Stat("ar", "big.zip.001"); err == nil {
		t.Error("parts were made")
	}
}

func TestPDFReadGivesPersianInReadingOrder(t *testing.T) {
	r := newRegistry(t, true, nil)
	if !r.Machine.Check("python3", "-c", "import reportlab, fitz, arabic_reshaper, bidi") {
		t.Skip("reportlab, PyMuPDF, arabic-reshaper and python-bidi are needed (the installer sets them up)")
	}
	out := call(t, r, "p", "pdf_tool", map[string]any{"action": "create", "filename": "fa.pdf", "title": "گزارش",
		"text_content": "این یک متن فارسی است با عدد ۱۲۳ و کلمه English در میان آن."})
	if !strings.Contains(out, "PDF created") {
		t.Fatalf("create: %s", out)
	}
	out = call(t, r, "p", "pdf_tool", map[string]any{"action": "read", "filename": "fa.pdf"})
	for _, want := range []string{"فارسی", "۱۲۳", "English", "گزارش"} {
		if !strings.Contains(out, want) {
			t.Errorf("the text read back lacks %q (mirrored or in presentation forms?):\n%s", want, out)
		}
	}
	if strings.Contains(out, "۳۲۱") {
		t.Errorf("digits came back mirrored:\n%s", out)
	}
	for _, r := range out {
		if r >= 0xFB50 && r <= 0xFEFF && r != 0xFEFF {
			t.Fatalf("presentation form %U in the text:\n%s", r, out)
		}
	}
}

func TestCompressVideoDoesNotCallABiggerFileCompressed(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	r := newRegistry(t, true, nil)
	res := r.Machine.Run(t.Context(), "v", machine.RunOptions{Shell: "ffmpeg -v error -f lavfi -i testsrc=duration=3:size=320x240:rate=15 " +
		"-f lavfi -i sine=frequency=440:duration=3 -shortest -pix_fmt yuv420p -crf 40 clip.mp4", Timeout: time.Minute})
	if res.ExitCode != 0 {
		t.Skipf("ffmpeg cannot make the sample: %s", res.Stderr)
	}
	out := call(t, r, "v", "media_convert", map[string]any{"action": "compress_video", "input_file": "clip.mp4", "output_file": "small.mp4"})
	if strings.Contains(out, "Compressed") && strings.Contains(out, "→") {
		before, _ := r.Machine.Stat("v", "clip.mp4")
		after, err := r.Machine.Stat("v", "small.mp4")
		if err == nil && after.Size() >= before.Size() {
			t.Fatalf("%d bytes became %d and it was reported as compressed: %s", before.Size(), after.Size(), out)
		}
		return
	}
	if !strings.Contains(out, "send it as it is") {
		t.Fatalf("unexpected answer: %s", out)
	}
	if _, err := r.Machine.Stat("v", "small.mp4"); err == nil {
		t.Error("the bigger copy was kept")
	}
}
