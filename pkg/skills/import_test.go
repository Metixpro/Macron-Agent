package skills

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImportZipWithAttachments(t *testing.T) {
	root := t.TempDir()
	s := open(t, root)
	data := makeZip(t, map[string]string{
		"repo-main/skills/research/arxiv/SKILL.md":          "---\nname: arxiv\ndescription: \"Search arXiv papers.\"\nversion: 1.2.0\nauthor: Someone\nmetadata:\n  hermes:\n    tags: [x]\n---\n# arXiv\nUse the API.",
		"repo-main/skills/research/arxiv/references/api.md": "query syntax",
		"repo-main/skills/research/arxiv/scripts/search.py": "print('hi')",
		"repo-main/skills/research/arxiv/.secret":           "hidden",
		"repo-main/skills/research/arxiv/__pycache__/x.pyc": "junk",
		"repo-main/skills/media/gif-search/SKILL.md":        "---\ndescription: Find GIFs.\n---\nSearch Tenor.",
		"repo-main/skills/broken/no-desc/SKILL.md":          "---\nname: no-desc\n---\nbody",
		"repo-main/README.md":                               "not a skill",
	})
	res, err := s.ImportZip(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Imported, ",") != "gif-search,arxiv" || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "no-desc") {
		t.Fatalf("result: %+v", res)
	}
	sk, ok := s.Get("arxiv")
	if !ok || sk.Category != "research" || sk.Version != "1.2.0" || strings.Join(sk.Files, ",") != "references/api.md,scripts/search.py" {
		t.Fatalf("arxiv: %+v", sk)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "research", "arxiv", FileName))
	if !strings.Contains(string(raw), "author: Someone") || !strings.Contains(string(raw), "category: research") {
		t.Fatalf("the front matter keeps its keys and gains a category:\n%s", raw)
	}
	if sk, _ := s.Get("gif-search"); sk.Category != "media" {
		t.Fatalf("the folder above names the category: %+v", sk)
	}
	if got, err := s.ReadFile("arxiv", "scripts/search.py"); err != nil || got != "print('hi')" {
		t.Fatalf("attachment: %q %v", got, err)
	}

	res, err = s.ImportZip(makeZip(t, map[string]string{"arxiv/SKILL.md": "---\ndescription: Search arXiv, v2.\ncategory: science\n---\nnew"}))
	if err != nil || len(res.Imported) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if sk, _ := s.Get("arxiv"); sk.Category != "science" || sk.Body != "new" || len(sk.Files) != 0 {
		t.Fatalf("replaced: %+v", sk)
	}
	if _, err := os.Stat(filepath.Join(root, "research")); !os.IsNotExist(err) {
		t.Fatal("the old folder is gone")
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".import-") || strings.HasPrefix(e.Name(), ".trash-") {
			t.Fatalf("left behind: %s", e.Name())
		}
	}
}

func TestImportZipStaysInside(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "skills")
	s := open(t, root)
	data := makeZip(t, map[string]string{
		"../../evil/SKILL.md":      "---\ndescription: Use when evil.\n---\nx",
		"ok/SKILL.md":              "---\ndescription: Use when ok.\n---\nx",
		"ok/../../../outside.txt":  "escaped",
		"ok/references/../../x.md": "folded",
		"/abs/SKILL.md":            "---\ndescription: Use when abs.\n---\nx",
	})
	if _, err := s.ImportZip(data); err != nil {
		t.Fatal(err)
	}
	filepath.Walk(parent, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && !strings.HasPrefix(p, root+string(filepath.Separator)) {
			t.Errorf("written outside the skills folder: %s", p)
		}
		return nil
	})
	if _, err := s.ImportZip([]byte("not a zip")); err == nil {
		t.Fatal("not a zip")
	}
	if _, err := s.ImportZip(makeZip(t, map[string]string{"a.txt": "x"})); err == nil {
		t.Fatal("no SKILL.md")
	}
}

func makeTarGz(t *testing.T, files map[string]string, gz bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.Writer = &buf
	var zw *gzip.Writer
	if gz {
		zw = gzip.NewWriter(&buf)
		w = zw
	}
	tw := tar.NewWriter(w)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(files[name])), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(files[name]))
	}
	_ = tw.WriteHeader(&tar.Header{Name: "pack/dev/windows-binary-reverse-engineering/link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if zw != nil {
		_ = zw.Close()
	}
	return buf.Bytes()
}

func TestImportTarGzOfSeveralSkills(t *testing.T) {
	root := t.TempDir()
	s := open(t, root)
	files := map[string]string{
		"pack/dev/windows-binary-reverse-engineering/SKILL.md":            "---\nname: windows-binary-reverse-engineering\ndescription: Use when taking apart a Windows PE file.\n---\nOpen it in Ghidra.",
		"pack/dev/windows-binary-reverse-engineering/scripts/pe_info.py":  "import pefile",
		"pack/dev/windows-binary-reverse-engineering/templates/report.md": "# Report",
		"pack/dev/unpacking/SKILL.md":                                     "---\ndescription: Use when a binary is packed.\n---\nFind the OEP.",
		"pack/README.md":                                                  "not a skill",
	}
	for _, gz := range []bool{true, false} {
		data := makeTarGz(t, files, gz)
		if !IsArchive(data) {
			t.Fatal("recognized from its bytes")
		}
		res, err := s.ImportArchive(data)
		if err != nil || strings.Join(res.Imported, ",") != "unpacking,windows-binary-reverse-engineering" || len(res.Skipped) != 0 {
			t.Fatalf("gz=%v: %+v %v", gz, res, err)
		}
	}
	sk, ok := s.Get("windows-binary-reverse-engineering")
	if !ok || sk.Category != "dev" || strings.Join(sk.Files, ",") != "scripts/pe_info.py,templates/report.md" {
		t.Fatalf("%+v", sk)
	}
	if got, _ := s.ReadFile(sk.Name, "scripts/pe_info.py"); got != "import pefile" {
		t.Fatalf("the script: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(root, "dev", "windows-binary-reverse-engineering", "link")); err == nil {
		t.Fatal("links are left out")
	}
	if !strings.Contains(s.Index(), "unpacking") {
		t.Fatal("in the index right away")
	}
	if _, err := s.ImportArchive([]byte("\x1f\x8bnot really")); err == nil {
		t.Fatal("a broken archive is an error")
	}
	if _, err := s.ImportArchive(makeTarGz(t, map[string]string{"a.txt": "x"}, true)); err == nil {
		t.Fatal("no SKILL.md, no skill")
	}
}
