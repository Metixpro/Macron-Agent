package tui

import (
	"os"
	"strings"
	"unicode"

	"github.com/benoitkugler/textprocessing/fribidi"
	"github.com/charmbracelet/x/ansi"
)

func terminalText(text string) string {
	if os.Getenv("MACRON_TUI_RTL") == "logical" || !strings.ContainsFunc(text, func(r rune) bool { return unicode.Is(unicode.Arabic, r) || unicode.Is(unicode.Hebrew, r) }) {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = visualLine(line)
	}
	return strings.Join(lines, "\n")
}
func visualLine(line string) string {
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
	base := fribidi.ParType(fribidi.LTR)
	visual, _ := fribidi.LogicalToVisual(fribidi.ShapeMirroring|fribidi.ShapeArabPres, runes, &base)
	var out strings.Builder
	current := ""
	currentLink := ""
	for i, r := range visual.Str {
		if r == 0xfeff || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			continue
		}
		nextLink := links[visual.VisualToLogical[i]]
		if currentLink != nextLink {
			if currentLink != "" {
				out.WriteString("\x1b]8;;\x1b\\")
			}
			out.WriteString(nextLink)
			currentLink = nextLink
		}
		next := styles[visual.VisualToLogical[i]]
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
