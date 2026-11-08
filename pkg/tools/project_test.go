package tools

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

func fakeProject(t *testing.T, r *Registry, session string) {
	t.Helper()
	files := map[string]string{
		"proj/main.go": "package main\n\nimport \"fmt\"\n\ntype Server struct{ port int }\n\nfunc (s *Server) Start() error {\n\treturn nil\n}\n\n" +
			"func main() {\n\tfmt.Println(\"hello\") // TODO: flags\n}\n",
		"proj/pkg/util.py":                "import os\n\nclass Cache:\n    def get(self, key):\n        return None\n\nasync def fetch(url):\n    pass\n",
		"proj/web/app.ts":                 "export interface User { id: number }\nexport const load = async (id: number) => {\n  return id\n}\nexport class App {}\n",
		"proj/README.md":                  "# Proj\nTODO: write docs\n",
		"proj/node_modules/lib/index.js":  "function hidden() { /* TODO */ }\n",
		"proj/data.bin":                   "\x00\x01TODO\x00",
		"proj/legacy/old.txt":             "line one\r\nline two\r\nline three\r\n",
		"proj/node_modules/lib/README.md": "TODO in deps",
	}
	for name, content := range files {
		if err := r.Machine.WriteFile(session, name, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectTreeAndSearch(t *testing.T) {
	r := newRegistry(t, true, nil)
	fakeProject(t, r, "p")

	tree := call(t, r, "p", ToolProjectTree, map[string]any{"path": "proj"})
	for _, want := range []string{"proj: 6 files", "not counting proj/node_modules", "Languages: ", "Go 1 files", "Python 1 files",
		"pkg/ (1 files", "  util.py (", "node_modules/ (skipped"} {
		if !strings.Contains(tree, want) {
			t.Errorf("tree misses %q:\n%s", want, tree)
		}
	}
	if strings.Contains(tree, "index.js") {
		t.Errorf("dependencies are not listed:\n%s", tree)
	}

	out := call(t, r, "p", ToolSearchCode, map[string]any{"pattern": "TODO"})
	if !strings.Contains(out, "2 matching lines in 2 files") || !strings.Contains(out, "proj/main.go\n  12: \tfmt.Println") ||
		strings.Contains(out, "node_modules") || strings.Contains(out, "data.bin") {
		t.Errorf("search:\n%s", out)
	}
	out = call(t, r, "p", ToolSearchCode, map[string]any{"pattern": "^func ", "glob": "*.go", "context": 1})
	if !strings.Contains(out, "  7: func (s *Server) Start() error {") || !strings.Contains(out, "  8- \treturn nil") {
		t.Errorf("context:\n%s", out)
	}
	out = call(t, r, "p", ToolSearchCode, map[string]any{"pattern": "c.ACHE", "literal": true})
	if !strings.Contains(out, "No matches") {
		t.Errorf("literal: %s", out)
	}
	if out := call(t, r, "p", ToolSearchCode, map[string]any{"pattern": "class cache", "ignore_case": true, "glob": "proj/**/*.py"}); !strings.Contains(out, "3: class Cache:") {
		t.Errorf("ignore case + path glob: %s", out)
	}
	if out := call(t, r, "p", ToolSearchCode, map[string]any{"pattern": "TODO", "path": "proj/node_modules"}); !strings.Contains(out, "index.js") {
		t.Errorf("searching inside a skipped folder on purpose: %s", out)
	}
	if out := call(t, r, "p", ToolSearchCode, map[string]any{"pattern": "("}); !strings.Contains(out, "invalid regular expression") {
		t.Errorf("bad regexp: %s", out)
	}

	out = call(t, r, "p", ToolFindFiles, map[string]any{"pattern": "**/*.{py,ts}"})
	if !strings.Contains(out, "2 files match") || !strings.Contains(out, "proj/pkg/util.py") || !strings.Contains(out, "proj/web/app.ts") {
		t.Errorf("find:\n%s", out)
	}
	if out := call(t, r, "p", ToolFindFiles, map[string]any{"pattern": "*.md"}); !strings.Contains(out, "1 files match") {
		t.Errorf("find by name skips dependencies: %s", out)
	}
	if out := call(t, r, "p", ToolSearchCode, map[string]any{"pattern": "x", "path": "../etc"}); !strings.Contains(out, "No matches") && !strings.HasPrefix(out, "error") {
		t.Errorf("paths stay in the workspace: %s", out)
	}
}

func TestCodeOutlineAndLineRanges(t *testing.T) {
	r := newRegistry(t, true, nil)
	fakeProject(t, r, "o")
	out := call(t, r, "o", ToolCodeOutline, map[string]any{"path": "proj"})
	for _, want := range []string{"proj/main.go (13 lines)", "5  type Server struct", "7  func (s *Server) Start() error", "11  func main()",
		"3  class Cache:", "4  def get(self, key):", "7  async def fetch(url):", "1  export interface User", "2  export const load = async (id: number) =>",
		"5  export class App"} {
		if !strings.Contains(out, want) {
			t.Errorf("outline misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hidden") {
		t.Error("dependencies are not outlined")
	}

	got := call(t, r, "o", "read_file", map[string]any{"filename": "proj/main.go", "start_line": 7, "end_line": 9})
	want := "proj/main.go (lines 7-9 of 13)\n7│ func (s *Server) Start() error {\n8│ \treturn nil\n9│ }\n… 4 more lines (start_line=10)"
	if got != want {
		t.Errorf("range:\n%q\nwant\n%q", got, want)
	}
	if got := call(t, r, "o", "read_file", map[string]any{"filename": "proj/main.go"}); !strings.HasPrefix(got, "package main") {
		t.Errorf("plain read: %s", got)
	}
}

func TestEditFile(t *testing.T) {
	r := newRegistry(t, true, nil)
	fakeProject(t, r, "e")
	read := func(name string) string {
		data, _, _ := r.Machine.ReadFile("e", name, 1<<20)
		return string(data)
	}
	out := call(t, r, "e", ToolEditFile, map[string]any{"filename": "proj/main.go", "edits": []any{
		map[string]any{"old_text": "func main() {\n\tfmt.Println(\"hello\") // TODO: flags\n}", "new_text": "func main() {\n\tfmt.Println(\"hi\")\n\tos.Exit(0)\n}"},
		map[string]any{"old_text": "import \"fmt\"", "new_text": "import (\n\t\"fmt\"\n\t\"os\"\n)"},
	}})
	if !strings.Contains(out, "Edited proj/main.go: 2 change(s), 13 → 17 lines") || !strings.Contains(out, "@@ line 11 @@") || !strings.Contains(out, "+ \tos.Exit(0)") {
		t.Errorf("edit:\n%s", out)
	}
	if got := read("proj/main.go"); !strings.Contains(got, "\"os\"\n)") || !strings.Contains(got, "os.Exit(0)") {
		t.Errorf("file:\n%s", got)
	}

	before := read("proj/main.go")
	out = call(t, r, "e", ToolEditFile, map[string]any{"filename": "proj/main.go", "edits": []any{
		map[string]any{"old_text": "hi", "new_text": "bye"},
		map[string]any{"old_text": "does not exist", "new_text": "x"},
	}})
	if !strings.Contains(out, "edit 2: old_text was not found") || read("proj/main.go") != before {
		t.Errorf("atomic: %s", out)
	}

	r.Machine.WriteFile("e", "dup.txt", []byte("a = 1\nb = 1\na = 1\n"))
	if out := call(t, r, "e", ToolEditFile, map[string]any{"filename": "dup.txt", "old_text": "a = 1", "new_text": "a = 2"}); !strings.Contains(out, "appears 2 times in dup.txt (lines 1, 3)") {
		t.Errorf("unique: %s", out)
	}
	call(t, r, "e", ToolEditFile, map[string]any{"filename": "dup.txt", "old_text": "a = 1", "new_text": "a = 2", "replace_all": true})
	if got := read("dup.txt"); got != "a = 2\nb = 1\na = 2\n" {
		t.Errorf("replace_all: %q", got)
	}

	if out := call(t, r, "e", ToolEditFile, map[string]any{"filename": "proj/pkg/util.py", "old_text": "def get(self,key):", "new_text": "x"}); !strings.Contains(out, "not found") {
		t.Errorf("near miss: %s", out)
	}
	if out := call(t, r, "e", ToolEditFile, map[string]any{"filename": "proj/pkg/util.py", "old_text": "  class Cache:\n    def put(self):", "new_text": "x"}); !strings.Contains(out, "3: class Cache:") {
		t.Errorf("near miss hint: %s", out)
	}

	call(t, r, "e", ToolEditFile, map[string]any{"filename": "proj/legacy/old.txt", "old_text": "line one\nline two", "new_text": "first\nsecond"})
	if got := read("proj/legacy/old.txt"); got != "first\r\nsecond\r\nline three\r\n" {
		t.Errorf("crlf: %q", got)
	}
	if out := call(t, r, "e", ToolEditFile, map[string]any{"filename": "nope.go", "old_text": "a", "new_text": "b"}); !strings.Contains(out, "does not exist") {
		t.Errorf("missing file: %s", out)
	}
}

func TestEditFileForgivesWhitespace(t *testing.T) {
	r := newRegistry(t, true, nil)
	read := func(name string) string {
		data, _, _ := r.Machine.ReadFile("w", name, 1<<20)
		return string(data)
	}
	_ = r.Machine.WriteFile("w", "app.py", []byte("class A:\n    def run(self):   \n        x = 1\n        return x\n"))
	out := call(t, r, "w", ToolEditFile, map[string]any{"filename": "app.py",
		"old_text": "  def run(self):\n    x = 1\n    return x", "new_text": "  def run(self):\n    x = 2\n    if x:\n      return x"})
	if !strings.Contains(out, "matched ignoring spaces and tabs") {
		t.Fatalf("fuzzy: %s", out)
	}
	if got := read("app.py"); got != "class A:\n    def run(self):\n        x = 2\n        if x:\n            return x\n" {
		t.Errorf("the file's 4-space indentation: %q", got)
	}

	_ = r.Machine.WriteFile("w", "main.go", []byte("func main() {\n\tif ok {\n\t\tgo()\n\t}\n}\n"))
	call(t, r, "w", ToolEditFile, map[string]any{"filename": "main.go",
		"old_text": "    if ok {\n        go()\n    }", "new_text": "    if ok {\n        go()\n        for {\n            wait()\n        }\n    }"})
	if got := read("main.go"); got != "func main() {\n\tif ok {\n\t\tgo()\n\t\tfor {\n\t\t\twait()\n\t\t}\n\t}\n}\n" {
		t.Errorf("tabs stay tabs: %q", got)
	}

	_ = r.Machine.WriteFile("w", "win.txt", []byte("a\r\n  b  \r\nc\r\n"))
	call(t, r, "w", ToolEditFile, map[string]any{"filename": "win.txt", "old_text": "a\nb", "new_text": "A\nB"})
	if got := read("win.txt"); got != "A\r\n  B\r\nc\r\n" {
		t.Errorf("windows line ends: %q", got)
	}

	_ = r.Machine.WriteFile("w", "two.py", []byte("if a:\n    x()\nif a:\n    x()\n"))
	if out := call(t, r, "w", ToolEditFile, map[string]any{"filename": "two.py", "old_text": "if a:\n  x()", "new_text": "y()"}); !strings.Contains(out, "not found") {
		t.Errorf("two places: never a guess: %s", out)
	}
}

func projectZip(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	zw.Close()
	return buf.Bytes()
}

func tgzBytes(t *testing.T, entries []tar.Header, contents map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		h := h
		if h.Typeflag == tar.TypeXGlobalHeader {
			h.PAXRecords = map[string]string{"comment": "abc123"}
			if err := tw.WriteHeader(&h); err != nil {
				t.Fatal(err)
			}
			continue
		}
		body := contents[h.Name]
		h.Size = int64(len(body))
		if h.Typeflag == tar.TypeDir || h.Typeflag == tar.TypeSymlink {
			h.Size = 0
		}
		h.Mode = 0o644
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractArchivesSafely(t *testing.T) {
	r := newRegistry(t, true, nil)

	r.Machine.WriteFile("x", "site.zip", projectZip(t, map[string]string{
		"site-main/index.html": "<h1>hi</h1>", "site-main/js/app.js": "app()", "../evil.txt": "no",
	}))
	out := call(t, r, "x", ToolExtract, map[string]any{"filename": "site.zip"})
	if !strings.Contains(out, "Unpacked 2 files") || !strings.Contains(out, "into site/") || !strings.Contains(out, "1 links, special files or unsafe paths") {
		t.Errorf("zip: %s", out)
	}
	if _, err := r.Machine.Stat("x", "site/js/app.js"); err != nil {
		t.Errorf("flattened top folder: %v", err)
	}
	if _, err := r.Machine.Stat("x", "../evil.txt"); err == nil {
		t.Error("zip slip")
	}

	r.Machine.WriteFile("x", "mono.tar.gz", tgzBytes(t, []tar.Header{
		{Name: "api/", Typeflag: tar.TypeDir}, {Name: "api/main.go", Typeflag: tar.TypeReg},
		{Name: "web/index.ts", Typeflag: tar.TypeReg}, {Name: "web/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
	}, map[string]string{"api/main.go": "package main", "web/index.ts": "x"}))
	out = call(t, r, "x", ToolExtract, map[string]any{"filename": "mono.tar.gz", "dest": "mono"})
	if !strings.Contains(out, "Unpacked 2 files") || !strings.Contains(out, "1 links") {
		t.Errorf("tgz: %s", out)
	}
	for _, f := range []string{"mono/api/main.go", "mono/web/index.ts"} {
		if _, err := r.Machine.Stat("x", f); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if _, err := r.Machine.Stat("x", "mono/web/link"); err == nil {
		t.Error("symlinks must not be created")
	}
	if out := call(t, r, "x", ToolExtract, map[string]any{"filename": "site.dmg"}); !strings.Contains(out, "unsupported archive") {
		t.Errorf("dmg: %s", out)
	}
}

func TestPackAndImport(t *testing.T) {
	r := newRegistry(t, true, nil)
	fakeProject(t, r, "k")
	out := call(t, r, "k", ToolPackFiles, map[string]any{"path": "proj"})
	if !strings.Contains(out, "Created proj.zip: 6 files") || !strings.Contains(out, "1 dependency/build/cache folders left out") {
		t.Fatalf("pack: %s", out)
	}
	data, _, _ := r.Machine.ReadFile("k", "proj.zip", 1<<20)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	joined := strings.Join(names, " ")
	if !strings.Contains(joined, "proj/main.go") || strings.Contains(joined, "node_modules") {
		t.Errorf("zip entries: %v", names)
	}

	tarball := tgzBytes(t, []tar.Header{{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader},
		{Name: "owner-repo-abc123/", Typeflag: tar.TypeDir}, {Name: "owner-repo-abc123/go.mod", Typeflag: tar.TypeReg},
		{Name: "owner-repo-abc123/cmd/main.go", Typeflag: tar.TypeReg}},
		map[string]string{"owner-repo-abc123/go.mod": "module x", "owner-repo-abc123/cmd/main.go": "package main"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/repo.tar.gz" {
			http.NotFound(w, req)
			return
		}
		w.Write(tarball)
	}))
	defer srv.Close()
	out = call(t, r, "k", ToolImportRepo, map[string]any{"url": srv.URL + "/repo.tar.gz"})
	if !strings.Contains(out, "Unpacked 2 files") || !strings.Contains(out, "into repo/") {
		t.Fatalf("import: %s", out)
	}
	if _, err := r.Machine.Stat("k", "repo/cmd/main.go"); err != nil {
		t.Error(err)
	}
	if files, _ := r.Machine.List("k", 1, 100); strings.Contains(strings.Join(entryNames(files), " "), ".import-") {
		t.Error("the downloaded archive is removed")
	}
	if out := call(t, r, "k", ToolImportRepo, map[string]any{"url": srv.URL + "/missing.tar.gz"}); !strings.Contains(out, "not found") {
		t.Errorf("404: %s", out)
	}
	if out := call(t, r, "k", ToolImportRepo, map[string]any{"url": "https://example.com/page.html"}); !strings.Contains(out, "not a repository or an archive") {
		t.Errorf("not an archive: %s", out)
	}
}

func entryNames(list []machine.FileEntry) []string {
	var out []string
	for _, e := range list {
		out = append(out, e.Name)
	}
	return out
}

func TestRepositoryLinks(t *testing.T) {
	for link, want := range map[string][]string{
		"https://github.com/golang/go":                   {"golang", "go", ""},
		"github.com/owner/repo.git":                      {"owner", "repo", ""},
		"https://github.com/owner/my.repo/tree/dev/x":    {"owner", "my.repo", "dev/x"},
		"https://www.github.com/a/b/?tab=readme-ov-file": {"a", "b", ""},
	} {
		m := githubRe.FindStringSubmatch(link)
		if m == nil || m[1] != want[0] || m[2] != want[1] || m[3] != want[2] {
			t.Errorf("%s: %q", link, m)
		}
	}
	if m := gitlabRe.FindStringSubmatch("https://gitlab.com/group/sub/proj/-/tree/main"); m == nil || m[1] != "group/sub/proj" || m[2] != "main" {
		t.Errorf("gitlab: %q", m)
	}
	for glob, cases := range map[string]map[string]bool{
		"*.go":          {"a.go": true, "x/a.go": true, "a.gox": false},
		"src/**/*.ts":   {"src/a.ts": true, "src/x/y/a.ts": true, "lib/a.ts": false},
		"**/Dockerfile": {"Dockerfile": true, "deploy/Dockerfile": true},
		"*.{js,ts}":     {"a.js": true, "a.ts": true, "a.py": false},
		"config?.json":  {"config1.json": true, "config.json": false},
	} {
		m, err := globMatcher(glob)
		if err != nil {
			t.Fatal(err)
		}
		for p, want := range cases {
			if m(p) != want {
				t.Errorf("%s ~ %s: %v", glob, p, !want)
			}
		}
	}
}

func TestProjectBuildAndGit(t *testing.T) {
	r := newRegistry(t, false, nil)
	files := map[string]string{
		"calc/calc/__init__.py":  "def add(a, b):\n    return a - b\n",
		"calc/tests/__init__.py": "",
		"calc/tests/test_add.py": "import unittest\nfrom calc import add\n\nclass T(unittest.TestCase):\n    def test_add(self):\n        self.assertEqual(add(2, 3), 5)\n",
	}
	for n, c := range files {
		if err := r.Machine.WriteFile("g", n, []byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	git := "git -c user.name=Waze -c user.email=waze@local"
	out := call(t, r, "g", "bash_execute", map[string]any{"command": "cd calc && git init -q && " + git + " add -A && " + git + " commit -qm init && python3 -m unittest -q 2>&1; echo EXIT=$?"})
	if !strings.Contains(out, "FAILED") || !strings.Contains(out, "EXIT=1") {
		t.Fatalf("the failing test must show:\n%s", out)
	}
	call(t, r, "g", ToolEditFile, map[string]any{"filename": "calc/calc/__init__.py", "old_text": "return a - b", "new_text": "return a + b"})
	out = call(t, r, "g", "bash_execute", map[string]any{"command": "cd calc && python3 -m unittest -q 2>&1; echo EXIT=$?; git diff --stat; " + git + " commit -qam fix && git log --oneline | wc -l"})
	if !strings.Contains(out, "OK") || !strings.Contains(out, "EXIT=0") || !strings.Contains(out, "1 file changed") || !strings.Contains(out, "2\\n") {
		t.Fatalf("after the fix:\n%s", out)
	}
}
