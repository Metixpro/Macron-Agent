package telegram

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Folding a long answer. A rich message has collapsible blocks,
// <details><summary>Title</summary>…</details>, in which Markdown works:
// the title always shows, the content opens and closes with a tap. A long
// answer keeps its opening in view and folds the rest: each long section
// under its heading (the first one open), a long code block, or, in a long
// text without headings, everything after the first paragraphs. Blocks are
// never nested, and an answer that has its own <details> is left as it is.

// FoldOptions say when and how an answer is folded.
type FoldOptions struct {
	MinRunes int                                 // shorter answers stay as they are
	Code     func(lang string, lines int) string // the title of a folded code block
	More     string                              // the title of the rest of a long text without headings
}

const (
	foldSectionRunes = 400  // a section folds when its body is longer than this
	foldCodeLines    = 20   // a code block folds when it has more lines
	foldPlainRunes   = 2500 // a text without headings folds past this
	foldKeepMin      = 700  // …keeping at least this much in view
	foldKeepMax      = 1500 // …and at most this much
)

var (
	foldHeadingRe = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*\s*$`)
	foldFenceRe   = regexp.MustCompile("^\\s*(```|~~~)\\s*([A-Za-z0-9_+#.-]*)")
)

// foldBlock is a run of lines that must stay together: a fenced code block,
// a table, an HTML block, or a paragraph.
type foldBlock struct {
	lines   []string
	heading string // a heading line's text
	code    string // a fenced block's language ("" for none)
	fenced  bool
	blank   bool // an empty line between blocks
}

func (b foldBlock) text() string { return strings.Join(b.lines, "\n") }

// foldBlocks cuts Markdown into blocks, never inside a code block or an HTML block.
func foldBlocks(md string) []foldBlock {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var out []foldBlock
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		switch {
		case trim == "":
			out = append(out, foldBlock{lines: []string{line}, blank: true})
		case foldFenceRe.MatchString(line):
			mark := foldFenceRe.FindStringSubmatch(line)
			b := foldBlock{lines: []string{line}, fenced: true, code: mark[2]}
			for i+1 < len(lines) {
				i++
				b.lines = append(b.lines, lines[i])
				if strings.HasPrefix(strings.TrimSpace(lines[i]), mark[1]) {
					break
				}
			}
			out = append(out, b)
		case strings.HasPrefix(trim, "<tg-") || strings.HasPrefix(trim, "<table") || strings.HasPrefix(trim, "<figure"):
			b := foldBlock{lines: []string{line}} // an HTML block runs to its end tag (or a blank line)
			tag := strings.FieldsFunc(trim[1:], func(r rune) bool { return r == ' ' || r == '>' || r == '/' })[0]
			for !strings.Contains(strings.Join(b.lines, "\n"), "</"+tag+">") && i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" {
				i++
				b.lines = append(b.lines, lines[i])
			}
			out = append(out, b)
		case foldHeadingRe.MatchString(trim):
			out = append(out, foldBlock{lines: []string{line}, heading: foldHeadingRe.FindStringSubmatch(trim)[1]})
		default:
			b := foldBlock{lines: []string{line}}
			for i+1 < len(lines) {
				next := strings.TrimSpace(lines[i+1])
				if next == "" || foldHeadingRe.MatchString(next) || foldFenceRe.MatchString(lines[i+1]) || strings.HasPrefix(next, "<tg-") {
					break
				}
				i++
				b.lines = append(b.lines, lines[i])
			}
			out = append(out, b)
		}
	}
	return out
}

func runeCount(blocks []foldBlock) int {
	n := 0
	for _, b := range blocks {
		n += utf8.RuneCountInString(b.text()) + 1
	}
	return n
}

func joinBlocks(blocks []foldBlock) string {
	parts := make([]string, len(blocks))
	for i, b := range blocks {
		parts[i] = b.text()
	}
	return strings.Trim(strings.Join(parts, "\n"), "\n")
}

// details wraps Markdown in a collapsible block.
func details(summary, body string, open bool) string {
	attr := ""
	if open {
		attr = " open"
	}
	summary = strings.TrimSpace(strings.NewReplacer("<", "‹", ">", "›").Replace(summary))
	return fmt.Sprintf("<details%s><summary>**%s**</summary>\n\n%s\n\n</details>", attr, strings.Trim(summary, "*"), strings.Trim(body, "\n"))
}

// FoldLong folds a long answer (see the top of this file).
func FoldLong(md string, o FoldOptions) string {
	if utf8.RuneCountInString(md) < o.MinRunes || strings.Contains(md, "<details") {
		return md
	}
	blocks := foldBlocks(md)
	headings := 0
	for _, b := range blocks {
		if b.heading != "" {
			headings++
		}
	}
	var out []string
	visible := func(bs []foldBlock) { // what stays in view; long code blocks in it fold
		for _, b := range bs {
			if b.fenced && len(b.lines)-2 > foldCodeLines && o.Code != nil {
				out = append(out, details(o.Code(b.code, len(b.lines)-2), b.text(), false))
				continue
			}
			out = append(out, b.text())
		}
	}
	switch {
	case headings >= 2:
		// The intro stays; each long section folds under its heading, the first one open.
		i := 0
		for i < len(blocks) && blocks[i].heading == "" {
			i++
		}
		visible(blocks[:i])
		opened := false
		for i < len(blocks) {
			head := blocks[i]
			j := i + 1
			for j < len(blocks) && blocks[j].heading == "" {
				j++
			}
			body := blocks[i+1 : j]
			if runeCount(body) > foldSectionRunes {
				out = append(out, details(head.heading, joinBlocks(body), !opened))
				opened = true
			} else {
				visible(blocks[i:j])
			}
			i = j
		}
	case utf8.RuneCountInString(md) > foldPlainRunes && o.More != "":
		// The first paragraphs stay; the rest folds.
		cut, n := -1, 0
		for i, b := range blocks {
			n += utf8.RuneCountInString(b.text()) + 1
			if b.blank && n >= foldKeepMin {
				cut = i
				break
			}
			if n > foldKeepMax {
				break
			}
		}
		if cut < 0 {
			visible(blocks)
			break
		}
		visible(blocks[:cut])
		out = append(out, details(o.More, joinBlocks(blocks[cut+1:]), false))
	default:
		visible(blocks)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// detailsRe finds a collapsible block (not nested) for the classic messages,
// which have none: there it becomes its title and an expandable quotation.
var detailsRe = regexp.MustCompile(`(?s)<details(?:\s+open)?\s*>\s*<summary>(.*?)</summary>(.*?)</details>`)

// classicDetails turns collapsible blocks into what a classic message can
// show: the title in bold, then the content in an expandable quotation (or
// as it is, when it holds code, a table or a quotation, which cannot go in one).
func classicDetails(md string) string {
	return detailsRe.ReplaceAllStringFunc(md, func(m string) string {
		g := detailsRe.FindStringSubmatch(m)
		title := "**" + strings.Trim(strings.TrimSpace(g[1]), "*") + "**"
		body := strings.Trim(g[2], "\n ")
		if strings.Contains(body, "```") || strings.Contains(body, "|") || regexp.MustCompile(`(?m)^\s*>`).MatchString(body) {
			return title + "\n" + body
		}
		return title + "\n" + quoteOpen + body + quoteClose
	})
}

// quoteOpen and quoteClose mark an expandable quotation through the
// conversion (they survive escaping).
const (
	quoteOpen  = "\x01"
	quoteClose = "\x02"
)

// VisibleRunes is how much of a message shows before anything is opened:
// a collapsible block counts as its title.
func VisibleRunes(md string) int {
	return utf8.RuneCountInString(detailsRe.ReplaceAllStringFunc(md, func(m string) string { return detailsRe.FindStringSubmatch(m)[1] }))
}
