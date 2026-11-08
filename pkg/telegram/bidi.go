package telegram

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	rlm = "‏"
	lrm = "‎"
)

func MarkRTL(html string) string {
	lines := strings.Split(html, "\n")
	pre := false
	for i, line := range lines {
		skip := pre || strings.Contains(line, "<pre")
		pre = preAfter(line, pre)
		if !skip {
			lines[i] = markLine(line)
		}
	}
	return strings.Join(lines, "\n")
}

func preAfter(line string, open bool) bool {
	o, c := strings.LastIndex(line, "<pre"), strings.LastIndex(line, "</pre>")
	switch {
	case o > c:
		return true
	case c >= 0:
		return false
	}
	return open
}

func markLine(line string) string {
	var (
		first   rune
		rtl     bool
		shows   bool
		head    []rune
		signs   []int
		prev    rune
		lastEnd int
	)
	for i := 0; i < len(line); {
		switch line[i] {
		case '<':
			if j := strings.IndexByte(line[i:], '>'); j >= 0 {
				i += j + 1
				continue
			}
		case '&':
			if j := strings.IndexByte(line[i:], ';'); j > 1 && j <= 10 {
				i += j + 1
				shows, prev, lastEnd = true, '&', i
				continue
			}
		}
		r, n := utf8.DecodeRuneInString(line[i:])
		if strings.ContainsRune("/@#", r) && (prev == 0 || unicode.IsSpace(prev) || strings.ContainsRune("(«[", prev)) &&
			i+n < len(line) && isASCIILetter(line[i+n]) {
			signs = append(signs, lastEnd)
		}
		i += n
		prev, lastEnd = r, i
		if unicode.IsSpace(r) {
			continue
		}
		shows = true
		if len(head) < 8 {
			head = append(head, r)
		}
		switch {
		case first == 0 && isBidiMark(r):
			first = 'X'
		case isRTLLetter(r):
			rtl = true
			if first == 0 {
				first = 'R'
			}
		case unicode.IsLetter(r) && first == 0:
			first = 'L'
		}
	}
	if !shows || first == 'X' || !rtl && first == 'L' || isLink(string(head)) {
		return line
	}
	var sb strings.Builder
	if first != 'R' {
		sb.WriteString(rlm)
	}
	at := 0
	for _, p := range signs {
		sb.WriteString(line[at:p])
		sb.WriteString(lrm)
		at = p
	}
	sb.WriteString(line[at:])
	return sb.String()
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isRTLLetter(r rune) bool {
	return unicode.IsLetter(r) && (r >= 0x0590 && r <= 0x08FF || r >= 0xFB1D && r <= 0xFDFF || r >= 0xFE70 && r <= 0xFEFF)
}

func isBidiMark(r rune) bool {
	return r == 0x200E || r == 0x200F || r == 0x061C || r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069
}

func isLink(s string) bool {
	for _, p := range []string{"http://", "https://", "tg://"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

var marks = strings.NewReplacer(rlm, "", lrm, "")

func UnmarkRTL(s string) string { return marks.Replace(s) }
