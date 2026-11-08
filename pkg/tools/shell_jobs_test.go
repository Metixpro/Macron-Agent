package tools

import (
	"strings"
	"testing"
)

// The agent starts a server in the background, reaches it from its next
// commands, reads its log and stops it.
func TestBackgroundServerFromTheTools(t *testing.T) {
	r := newRegistry(t, false, nil)
	if !r.Has(ToolShellJobs) {
		t.Fatal("shell_jobs comes with bash_execute")
	}
	_ = r.Machine.WriteFile("bg", "app/index.html", []byte("<h1>ok</h1>"))
	out := call(t, r, "bg", "bash_execute", map[string]any{"command": "cd app && python3 -m http.server 8766 --bind 127.0.0.1", "background": true})
	if !strings.Contains(out, "Started background job 1") || !strings.Contains(out, "localhost") {
		t.Fatalf("started: %s", out)
	}
	out = call(t, r, "bg", "bash_execute", map[string]any{"command": "for i in $(seq 1 50); do curl -s localhost:8766/ && exit 0; sleep 0.1; done"})
	if !strings.Contains(out, "<h1>ok</h1>") {
		t.Fatalf("the next command reaches it: %s", out)
	}
	out = call(t, r, "bg", ToolRunCode, map[string]any{"language": "python", "code": "import urllib.request\nprint(urllib.request.urlopen('http://127.0.0.1:8766/').read().decode())"})
	if !strings.Contains(out, "<h1>ok</h1>") {
		t.Errorf("run_code reaches it too: %s", out)
	}
	if out := call(t, r, "bg", ToolShellJobs, map[string]any{}); !strings.Contains(out, "job 1 · running") {
		t.Errorf("list: %s", out)
	}
	if out := call(t, r, "bg", ToolShellJobs, map[string]any{"action": "output", "job": 1}); !strings.Contains(out, "GET / HTTP") {
		t.Errorf("its log: %s", out)
	}
	if out := call(t, r, "bg", ToolShellJobs, map[string]any{"action": "kill", "job": 1}); !strings.Contains(out, "Stopped job 1") {
		t.Errorf("kill: %s", out)
	}
	if out := call(t, r, "bg", ToolShellJobs, map[string]any{"action": "output", "job": 7}); !strings.HasPrefix(out, "error:") {
		t.Errorf("an unknown job: %s", out)
	}
	out = call(t, r, "bg", "bash_execute", map[string]any{"command": "echo boom; exit 3", "background": true})
	if !strings.Contains(out, "ended at once (exit 3)") || !strings.Contains(out, "boom") {
		t.Errorf("a job that dies at once says so: %s", out)
	}
	r.Machine.StopShell("bg")
}

func TestSyntaxCheckAfterEdits(t *testing.T) {
	r := newRegistry(t, false, nil)
	out := call(t, r, "syn", "write_file", map[string]any{"filename": "app.py", "content": "def f(:\n    return 1\n"})
	if !strings.Contains(out, "⚠ syntax error (python)") || !strings.Contains(out, "line 1") {
		t.Errorf("a broken Python file: %s", out)
	}
	out = call(t, r, "syn", "edit_file", map[string]any{"filename": "app.py", "old_text": "def f(:", "new_text": "def f():"})
	if !strings.Contains(out, "✓ syntax ok (python)") {
		t.Errorf("fixed: %s", out)
	}
	out = call(t, r, "syn", "write_file", map[string]any{"filename": "conf.json", "content": `{"a": 1,}`})
	if !strings.Contains(out, "⚠ syntax error (json)") {
		t.Errorf("JSON: %s", out)
	}
	out = call(t, r, "syn", "write_file", map[string]any{"filename": "run.sh", "content": "if true; then echo x\n"})
	if !strings.Contains(out, "⚠ syntax error (bash -n)") {
		t.Errorf("bash: %s", out)
	}
	if out := call(t, r, "syn", "write_file", map[string]any{"filename": "notes.md", "content": "# hi (\n"}); strings.Contains(out, "syntax") {
		t.Errorf("only code is checked: %s", out)
	}
	if out := call(t, r, "syn", "write_file", map[string]any{"filename": "evil.py", "content": "import os\nos.system('touch pwned')\n"}); !strings.Contains(out, "✓ syntax ok") {
		t.Errorf("checked: %s", out)
	}
	if _, err := r.Machine.Stat("syn", "pwned"); err == nil {
		t.Error("a syntax check never runs the code")
	}
}

func TestApplyPatch(t *testing.T) {
	r := newRegistry(t, false, nil)
	if !r.Has(ToolApplyPatch) {
		t.Skip("no git here")
	}
	_ = r.Machine.WriteFile("pt", "proj/main.py", []byte("def hello():\n    return 'hi'\n\nprint(hello())\n"))
	_ = r.Machine.WriteFile("pt", "proj/old.txt", []byte("bye\n"))
	patch := `--- a/main.py
+++ b/main.py
@@ -1,4 +1,4 @@
 def hello():
-    return 'hi'
+    return 'hello'

 print(hello())
--- /dev/null
+++ b/util.py
@@ -0,0 +1,2 @@
+def twice(x):
+    return 2 * x
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-bye
`
	out := call(t, r, "pt", ToolApplyPatch, map[string]any{"patch": patch, "path": "proj"})
	if !strings.Contains(out, "Applied to 3 files") || !strings.Contains(out, "✓ syntax ok (python)") {
		t.Fatalf("applied: %s", out)
	}
	if b, _, _ := r.Machine.ReadFile("pt", "proj/main.py", 1000); !strings.Contains(string(b), "'hello'") {
		t.Errorf("changed: %s", b)
	}
	if _, err := r.Machine.Stat("pt", "proj/util.py"); err != nil {
		t.Error("a new file")
	}
	if _, err := r.Machine.Stat("pt", "proj/old.txt"); err == nil {
		t.Error("a deleted file")
	}
	// A hunk that does not match changes nothing, even in the other files.
	bad := "--- a/main.py\n+++ b/main.py\n@@ -1 +1 @@\n-def nope():\n+def x():\n--- a/util.py\n+++ b/util.py\n@@ -1 +1 @@\n-def twice(x):\n+def thrice(x):\n"
	out = call(t, r, "pt", ToolApplyPatch, map[string]any{"patch": bad, "path": "proj"})
	if !strings.Contains(out, "Nothing was changed") {
		t.Errorf("refused: %s", out)
	}
	if b, _, _ := r.Machine.ReadFile("pt", "proj/util.py", 1000); !strings.Contains(string(b), "twice") {
		t.Errorf("all or nothing: %s", b)
	}
	// Paths without a/ and b/, and nothing outside the workspace.
	out = call(t, r, "pt", ToolApplyPatch, map[string]any{"patch": "--- proj/util.py\n+++ proj/util.py\n@@ -1 +1 @@\n-def twice(x):\n+def double(x):\n"})
	if !strings.Contains(out, "Applied to 1 files") {
		t.Errorf("plain paths: %s", out)
	}
	if out := call(t, r, "pt", ToolApplyPatch, map[string]any{"patch": "--- a/x\n+++ b/x\n", "path": "../.."}); !strings.HasPrefix(out, "error:") {
		t.Errorf("outside: %s", out)
	}
}

// TestDevToolsGitJavaGoModules: git commits work here (it has a
// name to commit with), run_code runs Java source files, and a Go file in a
// module runs as its whole package.
func TestDevToolsGitJavaGoModules(t *testing.T) {
	r := newRegistry(t, false, nil)
	s := "devfix"
	out := call(t, r, s, "bash_execute", map[string]any{"command": "mkdir p && cd p && echo hi > a.txt && git init -q && git add -A && git commit -qm first && git log --format='%an %s'"})
	if !strings.Contains(out, "Macron first") {
		t.Errorf("git commit: %s", out)
	}
	if _, ok := r.langs["java"]; ok {
		out = call(t, r, s, ToolRunCode, map[string]any{"language": "java", "code": "public class Main { public static void main(String[] a) { System.out.println(\"java \" + (a.length > 0 ? a[0] : \"-\")); } }", "args": []any{"ok"}})
		if !strings.Contains(out, "java ok") || !strings.Contains(out, "$ java main.java ok") {
			t.Errorf("java: %s", out)
		}
	} else {
		t.Log("no java here")
	}
	if _, ok := r.langs["go"]; ok {
		_ = r.Machine.WriteFile(s, "gm/go.mod", []byte("module gm\n\ngo 1.21\n"))
		_ = r.Machine.WriteFile(s, "gm/cmd/app/main.go", []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(greet()) }\n"))
		_ = r.Machine.WriteFile(s, "gm/cmd/app/greet.go", []byte("package main\n\nfunc greet() string { return \"two files\" }\n"))
		out = call(t, r, s, ToolRunCode, map[string]any{"filename": "gm/cmd/app/main.go"})
		if !strings.Contains(out, "two files") || !strings.Contains(out, "go run .") {
			t.Errorf("a Go module runs as its package: %s", out)
		}
	}
}
