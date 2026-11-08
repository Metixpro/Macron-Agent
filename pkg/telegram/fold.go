package telegram

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

type FoldOptions struct {
	MinRunes int
	Code     func(lang string, lines int) string
	More     string
}

const (
	foldSectionRunes = 400
	foldCodeLines    = 20
	foldPlainRunes   = 2500
	foldKeepMin      = 700
	foldKeepMax      = 1500
)

var (
	foldHeadingRe = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*\s*$`)
	foldFenceRe   = regexp.MustCompile("^\\s*(```|~~~)\\s*([A-Za-z0-9_+#.-]*)")
)

type foldBlock struct {
	lines   []string
	heading string
	code    string
	fenced  bool
	blank   bool
}

func (b foldBlock) text() string { return strings.Join(b.lines, "\n") }

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
			b := foldBlock{lines: []string{line}}
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

func details(summary, body string, open bool) string {
	attr := ""
	if open {
		attr = " open"
	}
	summary = strings.TrimSpace(strings.NewReplacer("<", "‹", ">", "›").Replace(summary))
	return fmt.Sprintf("<details%s><summary>**%s**</summary>\n\n%s\n\n</details>", attr, strings.Trim(summary, "*"), strings.Trim(body, "\n"))
}

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
	visible := func(bs []foldBlock) {
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

var detailsRe = regexp.MustCompile(`(?s)<details(?:\s+open)?\s*>\s*<summary>(.*?)</summary>(.*?)</details>`)

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

const (
	quoteOpen  = "\x01"
	quoteClose = "\x02"
)

func VisibleRunes(md string) int {
	return utf8.RuneCountInString(detailsRe.ReplaceAllStringFunc(md, func(m string) string { return detailsRe.FindStringSubmatch(m)[1] }))
}
