package telegram

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	rlm = "‏" // right-to-left mark
	lrm = "‎" // left-to-right mark
)

// MarkRTL lays a right-to-left HTML message out the way it reads. Telegram
// takes a line's direction from its first letter, so a Persian line that
// opens with a Latin word ("VIP تا ۳۰ آبان") or holds no letter at all
// ("💰 ۲۰ ⭐") would sit on the left and read backwards: such a line starts
// with a right-to-left mark. A command, a mention or a tag inside Persian
// ("لغو با /reminders") would show its sign on the wrong side ("reminders/"):
// it gets a left-to-right mark that keeps the sign with its word. Lines with
// Latin letters and no right-to-left ones (a link, a code, an English
// sentence) keep their direction, and <pre> blocks are left alone.
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

// preAfter reports whether a <pre> block is open after a line.
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

// markLine marks one line (see MarkRTL).
func markLine(line string) string {
	var (
		first   rune // the first strong direction: 'R', 'L', or 0 for none yet
		rtl     bool // a right-to-left letter
		shows   bool // anything visible
		head    []rune
		signs   []int // where a left-to-right mark keeps a sign with its word
		prev    rune  // the rune before
		lastEnd int   // the byte after it: a mark goes there, before any tags
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
			first = 'X' // the line already says its direction
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

// UnmarkRTL removes the marks MarkRTL adds, from a message read back.
func UnmarkRTL(s string) string { return marks.Replace(s) }
