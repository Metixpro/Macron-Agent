package telegram

import (
	"fmt"
	"strings"
	"testing"
)

var testFold = FoldOptions{MinRunes: 2000, Code: func(lang string, n int) string { return fmt.Sprintf("code %s · %d lines", lang, n) }, More: "Read more"}

func para(word string, n int) string { return strings.TrimSpace(strings.Repeat(word+" ", n)) }

func TestFoldSections(t *testing.T) {
	md := "Here is the guide.\n\n## Install\n\n" + para("install", 200) + "\n\n## Use\n\n" + para("use", 200) + "\n\n## Notes\n\nShort note."
	got := FoldLong(md, testFold)
	if !strings.HasPrefix(got, "Here is the guide.") {
		t.Errorf("the intro stays in view:\n%s", got)
	}
	if !strings.Contains(got, "<details open><summary>**Install**</summary>") || !strings.Contains(got, "<details><summary>**Use**</summary>") {
		t.Errorf("long sections fold under their headings, the first open:\n%s", got)
	}
	if !strings.Contains(got, "## Notes\n\nShort note.") || strings.Count(got, "<details") != 2 {
		t.Errorf("a short section stays as it is:\n%s", got)
	}
	if strings.Contains(got, "## Install") {
		t.Error("the heading becomes the title")
	}
}

func TestFoldCodeAndPlainText(t *testing.T) {
	code := "```python\n" + strings.Repeat("print('x')\n", 40) + "```"
	md := para("intro", 300) + "\n\n" + code + "\n\nDone."
	got := FoldLong(md, testFold)
	if !strings.Contains(got, "<details><summary>**code python · 40 lines**</summary>\n\n```python") || !strings.HasSuffix(got, "Done.") {
		t.Errorf("a long code block folds:\n%.300s", got)
	}

	var ps []string
	for i := 0; i < 12; i++ {
		ps = append(ps, para(fmt.Sprintf("p%d", i), 100))
	}
	got = FoldLong(strings.Join(ps, "\n\n"), testFold)
	if !strings.HasPrefix(got, "p0 p0") || !strings.Contains(got, "<details><summary>**Read more**</summary>") || strings.Count(got, "<details") != 1 {
		t.Errorf("long plain text:\n%.400s", got)
	}
	head := got[:strings.Index(got, "<details")]
	if n := len([]rune(head)); n < foldKeepMin || n > foldKeepMax+400 {
		t.Errorf("about a screen stays in view: %d runes", n)
	}

	if FoldLong("short", testFold) != "short" {
		t.Error("short")
	}
	own := "<details><summary>Mine</summary>x</details>\n\n" + para("w", 600)
	if FoldLong(own, testFold) != own {
		t.Error("an answer with its own block is not folded again")
	}

	md = "## A\n\n" + para("a", 600) + "\n\n```go\n\n" + strings.Repeat("x := 1\n\n", 30) + "```\n\n## B\n\n" + para("b", 600)
	got = FoldLong(md, testFold)
	if strings.Count(got, "```") != 2 || strings.Count(got, "<details") != 2 {
		t.Errorf("blocks stay whole and nothing nests:\n%.500s", got)
	}
}

func TestClassicDetails(t *testing.T) {
	got := MarkdownToHTML("Intro\n\n<details open><summary>**More**</summary>\n\nHidden *text* here\n\n</details>")
	if !strings.Contains(got, "<b>More</b>") || !strings.Contains(got, "<blockquote expandable>Hidden <i>text</i> here</blockquote>") || strings.Contains(got, "details") {
		t.Errorf("classic: %q", got)
	}
	if !ValidHTML(got) {
		t.Errorf("valid Telegram HTML: %q", got)
	}
	got = MarkdownToHTML("<details><summary>Code</summary>\n\n```go\nx := 1\n```\n</details>")
	if strings.Contains(got, "blockquote") || !strings.Contains(got, "<pre>") || !strings.Contains(got, "<b>Code</b>") {
		t.Errorf("code cannot go in a quotation: %q", got)
	}
}
