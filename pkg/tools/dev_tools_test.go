package tools

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCodeShowsTheRealTerminal(t *testing.T) {
	r := newRegistry(t, false, nil)
	if !r.Has(ToolRunCode) {
		t.Skip("no language available here")
	}
	out := call(t, r, "dev", ToolRunCode, map[string]any{"language": "python",
		"code": "import sys\nprint('one')\nprint('two', file=sys.stderr)\nprint('three')\n1/0"})
	if !strings.HasPrefix(out, "$ python3 main.py\none\ntwo\nthree\nTraceback") {
		t.Errorf("stdout and stderr must keep their order, like a terminal:\n%s", out)
	}
	if !strings.Contains(out, "ZeroDivisionError") || !strings.Contains(out, "[exit 1 · ") {
		t.Errorf("the error and the exit code are shown:\n%s", out)
	}
	if fi, err := r.Machine.ListFiles("dev"); err != nil || len(fi) != 0 {
		t.Errorf("a snippet is not a deliverable: it lives in a hidden folder: %v", fi)
	}

	// A named file is saved (to be delivered) and can be run again later.
	out = call(t, r, "dev", ToolRunCode, map[string]any{"filename": "calc.py", "code": "import sys\nprint(sum(map(int, sys.argv[1:])), input())",
		"args": []string{"2", "40"}, "stdin": "done"})
	if !strings.Contains(out, "$ python3 calc.py 2 40\n42 done\n[exit 0") {
		t.Errorf("args and stdin:\n%s", out)
	}
	if out := call(t, r, "dev", ToolRunCode, map[string]any{"filename": "calc.py", "args": []string{"1"}, "stdin": "x"}); !strings.Contains(out, "\n1 x\n") {
		t.Errorf("running an existing file (language from the extension):\n%s", out)
	}

	if out := call(t, r, "dev", ToolRunCode, map[string]any{"language": "python", "code": "import cv2"}); !strings.Contains(out, "pip install opencv-python-headless") {
		t.Errorf("a missing module comes with an install hint:\n%s", out)
	}
	if out := call(t, r, "dev", ToolRunCode, map[string]any{"language": "python", "code": "while True: pass", "timeout_seconds": 1}); !strings.Contains(out, "[timed out") {
		t.Errorf("timeout:\n%s", out)
	}
	if out := call(t, r, "dev", ToolRunCode, map[string]any{"language": "cobol", "code": "x"}); !strings.Contains(out, "not available") {
		t.Errorf("unknown language: %s", out)
	}
	elsewhere := filepath.Join(t.TempDir(), "outside.py")
	if out := call(t, r, "dev", ToolRunCode, map[string]any{"filename": elsewhere, "code": "print('there')"}); !strings.Contains(out, "there") {
		t.Errorf("a program can live anywhere on the machine: %s", out)
	}
}

func TestTypeScriptPicksTheInstalledRuntime(t *testing.T) {
	r := newRegistry(t, false, nil)
	_, deno := r.langs["deno"]
	_, bun := r.langs["bun"]
	if !deno && !bun {
		t.Skip("neither deno nor bun is installed")
	}
	for _, in := range [][2]string{{"typescript", ""}, {"ts", ""}, {"", "app.ts"}, {"", "app.mts"}} {
		l, err := r.pickLanguage(in[0], in[1])
		if err != nil || (deno && l.Name != "deno") || (!deno && l.Name != "bun") {
			t.Errorf("%v: %s %v", in, l.Name, err)
		}
	}
	out := call(t, r, "ts", ToolRunCode, map[string]any{"filename": "hello.ts", "code": "const who: string = 'مکرون';\nconsole.log(`سلام ${who}`)"})
	if !strings.Contains(out, "سلام مکرون") || !strings.Contains(out, "[exit 0") {
		t.Errorf("typescript: %s", out)
	}
}

func TestRunCodeOtherLanguages(t *testing.T) {
	r := newRegistry(t, false, nil)
	cases := map[string][2]string{
		"bash": {`echo "hi $((6*7))"`, "hi 42"},
		"php":  {`<?php echo "php ", 6*7, PHP_EOL;`, "php 42"},
		"c":    {"#include <stdio.h>\nint main(){printf(\"c %d\\n\", 6*7);return 0;}", "c 42"},
		"cpp":  {"#include <iostream>\nint main(){std::cout << \"cpp \" << 6*7 << std::endl;}", "cpp 42"},
		"node": {"console.log('node', 6*7)", "node 42"},
		"ruby": {"puts \"ruby #{6*7}\"", "ruby 42"},
		"deno": {"const n: number = 6 * 7;\nconsole.log(`deno ${n}`);", "deno 42"},
		"bun":  {"const n: number = 6 * 7;\nconsole.log(`bun ${n}`);", "bun 42"},
	}
	for lang, c := range cases {
		if _, ok := r.langs[lang]; !ok {
			continue
		}
		out := call(t, r, "langs", ToolRunCode, map[string]any{"language": lang, "code": c[0]})
		if !strings.Contains(out, c[1]+"\n[exit 0") {
			t.Errorf("%s:\n%s", lang, out)
		}
	}
}

func TestCodeToolsReportTheFilesTheyMake(t *testing.T) {
	r := newRegistry(t, false, nil)
	out := call(t, r, "files", ToolRunCode, map[string]any{"language": "bash", "code": "mkdir -p work; touch work/frame1.png out.png; echo done"})
	if !strings.Contains(out, "[new or changed files in the workspace: out.png]") {
		t.Errorf("run_code lists the root files it made (not work/):\n%s", out)
	}
	out = call(t, r, "files", "bash_execute", map[string]any{"command": "echo hi > a.txt"})
	if !strings.Contains(out, `"new_or_changed_files": [`) || !strings.Contains(out, `"a.txt"`) {
		t.Errorf("bash_execute lists them too:\n%s", out)
	}
	if out := call(t, r, "files", "bash_execute", map[string]any{"command": "echo hi"}); strings.Contains(out, "new_or_changed") {
		t.Errorf("no note without new files:\n%s", out)
	}
}
