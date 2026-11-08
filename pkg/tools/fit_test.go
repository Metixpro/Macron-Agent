package tools

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// A long read stays under the output limit (which would cut its middle out)
// and says where to go on: the next read starts at the first line not shown.
func TestLongReadsFitAndGoOn(t *testing.T) {
	r := newRegistry(t, true, nil)
	var sb strings.Builder
	for i := 1; i <= 1500; i++ {
		fmt.Fprintf(&sb, "line %04d %s\n", i, strings.Repeat("x", 30))
	}
	if err := r.Machine.WriteFile("f", "big.txt", []byte(sb.String())); err != nil {
		t.Fatal(err)
	}
	out := call(t, r, "f", "read_file", map[string]any{"filename": "big.txt"})
	if n := utf8.RuneCountInString(out); n > MaxToolOutput {
		t.Fatalf("a read fits the limit: %d", n)
	}
	var shown, next int
	if _, err := fmt.Sscanf(out[strings.LastIndex(out, "[truncated: lines"):], "[truncated: lines 1-%d shown — read on with start_line=%d", &shown, &next); err != nil {
		t.Fatalf("the read says where to go on: %v\n%s", err, out[len(out)-200:])
	}
	if !strings.Contains(out, fmt.Sprintf("line %04d ", shown)) || strings.Contains(out, fmt.Sprintf("line %04d ", next)) || next != shown+1 {
		t.Errorf("the last line shown is %d and the next read starts at %d", shown, next)
	}

	out = call(t, r, "f", "read_file", map[string]any{"filename": "big.txt", "start_line": next, "end_line": 1500})
	if n := utf8.RuneCountInString(out); n > MaxToolOutput {
		t.Fatalf("a range fits the limit: %d", n)
	}
	if !strings.Contains(out, fmt.Sprintf("line %04d ", next)) || !strings.Contains(out, "more lines (start_line=") {
		t.Errorf("the range starts where the read stopped and says where to go on:\n%s", out[:200])
	}

	// One huge line (a minified file) is shortened, not the whole read.
	if err := r.Machine.WriteFile("f", "min.js", []byte("var a=1;"+strings.Repeat("b()", 20000)+"\nend\n")); err != nil {
		t.Fatal(err)
	}
	out = call(t, r, "f", "read_file", map[string]any{"filename": "min.js", "start_line": 1})
	if !strings.Contains(out, "line cut") || !strings.Contains(out, "2│ end") {
		t.Errorf("a long line is shortened and the next one shows: %s", out[len(out)-300:])
	}
}

func TestPDFTextKeepsWholePages(t *testing.T) {
	var sb strings.Builder
	for p := 1; p <= 8; p++ {
		fmt.Fprintf(&sb, "--- Page %d/8 ---\n%s\n", p, strings.Repeat("کلمه ", 600))
	}
	out := fitPages(strings.TrimSpace(sb.String()))
	if n := utf8.RuneCountInString(out); n > MaxToolOutput {
		t.Fatalf("fits: %d", n)
	}
	last := strings.Count(out, "--- Page ")
	if !strings.Contains(out, fmt.Sprintf(`read on with pages="%d-"`, last+1)) {
		t.Errorf("whole pages 1-%d shown, the next read starts at page %d: %s", last, last+1, out[len(out)-120:])
	}
	if from, to := pageRange(" 5-9, 12"); from != 5 || to != 9 {
		t.Errorf("pages: %d-%d", from, to)
	}
	if from, to := pageRange("12-"); from != 12 || to != 0 {
		t.Errorf("open range: %d-%d", from, to)
	}
	if from, to := pageRange("3"); from != 3 || to != 3 {
		t.Errorf("one page: %d-%d", from, to)
	}
}

// A model that sends a file's content as JSON gets that JSON written.
func TestWriteFileWithJSONContent(t *testing.T) {
	r := newRegistry(t, true, nil)
	call(t, r, "w", "write_file", map[string]any{"filename": "data.json", "content": map[string]any{"name": "Ali", "tags": []string{"a&b"}}})
	data, _, err := r.Machine.ReadFile("w", "data.json", 1<<20)
	if err != nil || !strings.Contains(string(data), `"name": "Ali"`) {
		t.Errorf("the object is written as JSON: %q %v", data, err)
	}
	call(t, r, "w", "write_file", map[string]any{"filename": "n.txt", "content": 42})
	if data, _, _ := r.Machine.ReadFile("w", "n.txt", 1<<20); string(data) != "42" {
		t.Errorf("a number: %q", data)
	}
}

func TestCommandOutputKeepsSymbols(t *testing.T) {
	r := newRegistry(t, false, nil)
	out := call(t, r, "s", "bash_execute", map[string]any{"command": "echo 'a && b <tag>'"})
	if !strings.Contains(out, "a && b <tag>") {
		t.Errorf("& < > are shown as they are: %s", out)
	}
}

func TestListsAndNumbersAsTheModelSendsThem(t *testing.T) {
	for in, want := range map[any]string{
		`["a.pdf","my file.pdf"]`: "a.pdf|my file.pdf",
		"a.pdf, my file.pdf":      "a.pdf|my file.pdf",
		"one\ntwo":                "one|two",
		"x y":                     "x|y",
	} {
		if got := strings.Join(anyList(in), "|"); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if got := strings.Join(argList("--format=csv,json -v"), "|"); got != "--format=csv,json|-v" {
		t.Errorf("program arguments keep their commas: %q", got)
	}
	for in, want := range map[string]float64{"12": 12, "۱۲٫۵": 12.5, "1,250": 1250, "40%": 40, " 3 ": 3, "1.5e+06": 1.5e6} {
		if got, ok := parseNumber(in); !ok || got != want {
			t.Errorf("%q: %v %v", in, got, ok)
		}
	}
	if _, ok := parseNumber("abc"); ok {
		t.Error("not a number")
	}
}
