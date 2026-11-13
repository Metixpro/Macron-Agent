package tui

import (
	"os"
	"strings"
	"unicode"

	"github.com/benoitkugler/textprocessing/fribidi"
	"github.com/charmbracelet/x/ansi"
)

var (
	rtlLogical bool
	rtlSeen    bool
)

func resolveRTL(saved string) (string, bool) {
	for _, v := range []string{os.Getenv("MACRON_TUI_RTL"), saved} {
		switch v = strings.ToLower(strings.TrimSpace(v)); v {
		case "logical", "visual":
			return v, true
		}
	}
	if os.Getenv("KONSOLE_VERSION") != "" || os.Getenv("VTE_VERSION") != "" || strings.Contains(strings.ToLower(os.Getenv("TERM_PROGRAM")), "mlterm") {
		return "logical", false
	}
	return "visual", false
}

func terminalText(text string) string {
	if !strings.ContainsFunc(text, isRTL) {
		return text
	}
	rtlSeen = true
	if rtlLogical {
		return text
	}
	lines := strings.Split(text, "\n")
	prev := false
	for i, line := range lines {
		plain := ansi.Strip(line)
		d := lineDirection(plain)
		force := d.rtl > 0 && !codeLike(plain) && (d.rtlWords > d.ltrWords && d.rtlWords >= 2 || prev)
		lines[i] = visualLine(line, force)
		prev = d.rtl > 0 && (force || d.firstRTL)
	}
	return strings.Join(lines, "\n")
}

func isRTL(r rune) bool { return unicode.Is(unicode.Arabic, r) || unicode.Is(unicode.Hebrew, r) }

type direction struct {
	rtl, rtlWords, ltrWords int
	firstRTL                bool
}

func lineDirection(s string) direction {
	var d direction
	seen := false
	for _, word := range strings.Fields(s) {
		r, l := 0, 0
		for _, c := range word {
			switch {
			case !unicode.IsLetter(c):
				continue
			case isRTL(c):
				r++
			default:
				l++
			}
			if !seen {
				seen, d.firstRTL = true, isRTL(c)
			}
		}
		d.rtl += r
		switch {
		case r > 0 && l == 0:
			d.rtlWords++
		case l > 0 && r == 0:
			d.ltrWords++
		}
	}
	return d
}

func codeLike(s string) bool {
	if strings.ContainsAny(s, "{}[]=;\"`") {
		return true
	}
	prev := ' '
	for _, r := range s {
		if r == '(' && unicode.IsLetter(prev) && !isRTL(prev) {
			return true
		}
		prev = r
	}
	return false
}

func visualLine(line string, rtlBase bool) string {
	var runes []rune
	var styles []string
	var links []string
	link := ""
	style := ""
	var state byte
	for len(line) > 0 {
		seq, _, n, next := ansi.GraphemeWidth.DecodeSequenceInString(line, state, nil)
		if n == 0 {
			break
		}
		state = next
		line = line[n:]
		if strings.HasPrefix(seq, "\x1b") {
			if strings.HasPrefix(seq, "\x1b]8;") {
				link = seq
				if strings.HasSuffix(seq, ";;\x1b\\") || strings.HasSuffix(seq, ";;\a") {
					link = ""
				}
			}
			if strings.HasSuffix(seq, "m") {
				if seq == "\x1b[0m" || seq == "\x1b[m" {
					style = ""
				} else {
					style += seq
				}
			}
			continue
		}
		for _, r := range seq {
			runes = append(runes, r)
			styles = append(styles, style)
			links = append(links, link)
		}
	}
	if len(runes) == 0 {
		return ""
	}
	from, to := chromeEdges(runes)
	order := make([]int, 0, len(runes))
	shown := make([]rune, 0, len(runes))
	for i := 0; i < from; i++ {
		order, shown = append(order, i), append(shown, runes[i])
	}
	if from < to {
		base := fribidi.ParType(fribidi.ON)
		if rtlBase {
			base = fribidi.RTL
		}
		visual, _ := fribidi.LogicalToVisual(fribidi.ShapeMirroring|fribidi.ReorderNSM|fribidi.ShapeArabPres, runes[from:to], &base)
		for i, r := range visual.Str {
			order, shown = append(order, from+visual.VisualToLogical[i]), append(shown, r)
		}
	}
	for i := to; i < len(runes); i++ {
		order, shown = append(order, i), append(shown, runes[i])
	}
	var out strings.Builder
	current := ""
	currentLink := ""
	for i, r := range shown {
		if r == 0xfeff || r >= 0x200b && r <= 0x200f || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			continue
		}
		at := min(max(order[i], 0), len(runes)-1)
		nextLink := links[at]
		if currentLink != nextLink {
			if currentLink != "" {
				out.WriteString("\x1b]8;;\x1b\\")
			}
			out.WriteString(nextLink)
			currentLink = nextLink
		}
		next := styles[at]
		if current != next {
			out.WriteString("\x1b[0m")
			out.WriteString(next)
			current = next
		}
		out.WriteRune(r)
	}
	if currentLink != "" {
		out.WriteString("\x1b]8;;\x1b\\")
	}
	if current != "" {
		out.WriteString("\x1b[0m")
	}
	return out.String()
}

const leadChrome = " \t●❯›•⎿┃│◼☐☒✓✗✻>*-"
const tailChrome = " \t│┃╮╯╰╭"

func chromeEdges(runes []rune) (int, int) {
	from := 0
	for from < len(runes) && strings.ContainsRune(leadChrome, runes[from]) {
		from++
	}
	if from < len(runes) && unicode.IsDigit(runes[from]) {
		j := from
		for j < len(runes) && unicode.IsDigit(runes[j]) {
			j++
		}
		if j+1 < len(runes) && (runes[j] == '.' || runes[j] == ')') && runes[j+1] == ' ' {
			from = j + 2
		}
	}
	to := len(runes)
	for to > from && strings.ContainsRune(tailChrome, runes[to-1]) {
		to--
	}
	return from, to
}
