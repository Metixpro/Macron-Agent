// Package textutil has small helpers for handling multi-byte (Persian, emoji) text safely.
package textutil

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// TruncateRunes cuts s to at most max runes (never in the middle of a character)
// and appends suffix when something was removed.
func TruncateRunes(s string, max int, suffix string) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + suffix
}

// TruncateBytes cuts s to at most max bytes on a character boundary.
func TruncateBytes(s string, max int, suffix string) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

// StripHTML removes tags and decodes entities (used for plain-text fallbacks).
func StripHTML(s string) string {
	s = strings.ReplaceAll(s, "<br>", "\n")
	return html.UnescapeString(tagRe.ReplaceAllString(s, ""))
}

// CollapseSpace replaces runs of whitespace with a single space.
func CollapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// OneLine collapses whitespace and control characters into a single line.
func OneLine(s string) string {
	return CollapseSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s))
}

var argKeys = []string{"query", "url", "topic", "prompt", "text", "domain", "repo", "symbols", "command", "data", "input_file", "filename"}

// ArgPreview extracts the most telling argument of a tool call (given as JSON)
// for display in progress messages and logs.
func ArgPreview(argsJSON string, max int) string {
	var args map[string]any
	if json.Unmarshal([]byte(argsJSON), &args) != nil {
		return ""
	}
	for _, k := range argKeys {
		if v, ok := args[k].(string); ok && strings.TrimSpace(v) != "" {
			return TruncateRunes(OneLine(v), max, "…")
		}
	}
	return ""
}

// HeadTail shortens s to about max runes by keeping its beginning and its end
// (where errors and summaries usually are) and saying how much was left out:
//
//	…head…
//	... [output truncated, 48210 chars omitted] ...
//	…tail…
//
// Cuts move to a nearby line break so lines stay whole.
func HeadTail(s string, max int) string {
	r := []rune(s)
	if max <= 0 || len(r) <= max {
		return s
	}
	const slack = 200 // how far a cut may move to reach a line break
	budget := max - 60
	if budget < 20 {
		budget = max
	}
	head, tail := budget*2/5, budget-budget*2/5
	cutHead, cutTail := head, len(r)-tail
	for i := head; i > head-slack && i > 0; i-- {
		if r[i-1] == '\n' {
			cutHead = i
			break
		}
	}
	for i := cutTail; i < cutTail+slack && i < len(r); i++ {
		if r[i] == '\n' {
			cutTail = i + 1
			break
		}
	}
	if cutTail <= cutHead {
		cutTail = cutHead
	}
	omitted := cutTail - cutHead
	return string(r[:cutHead]) + fmt.Sprintf("\n... [output truncated, %d chars omitted] ...\n", omitted) + string(r[cutTail:])
}

// foldMap merges spelling variants: Arabic and Persian letters, hamza forms.
var foldMap = map[rune]rune{
	'ي': 'ی', 'ى': 'ی', 'ئ': 'ی', 'ك': 'ک', 'ة': 'ه', 'ۀ': 'ه', 'أ': 'ا', 'إ': 'ا', 'آ': 'ا', 'ٱ': 'ا', 'ؤ': 'و',
}

// Fold prepares text for matching: NFKC, lower case, one spelling for
// Arabic/Persian variants, no diacritics, tatweel or zero-width joiners, and
// ASCII digits.
func Fold(s string) string {
	s = norm.NFKC.String(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\u200c' || r == '\u200d' || r == '\u00ad' || r == 'ـ':
			continue
		case r >= '\u064b' && r <= '\u065f', r == '\u0670':
			continue
		case r >= '۰' && r <= '۹':
			r = '0' + (r - '۰')
		case r >= '٠' && r <= '٩':
			r = '0' + (r - '٠')
		}
		if f, ok := foldMap[r]; ok {
			r = f
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// ezafeRe is an ezafe written as «‌ی» after a final ه: «همه‌ی فایل‌ها».
var ezafeRe = regexp.MustCompile(`(\p{Arabic})ه\x{200C}ی([^\p{L}\p{Mn}\x{200C}]|$)`)

// PersianEzafe writes the ezafe after a final ه the way the bot's Persian
// does, without «‌ی» («همه فایل‌ها», «دکمه توقف»), in Markdown prose;
// code (fenced or inline) is left as it is.
func PersianEzafe(md string) string {
	if !strings.Contains(md, "ه‌ی") {
		return md
	}
	blocks := strings.Split(md, "```")
	for i := 0; i < len(blocks); i += 2 { // the even ones are outside fenced code
		spans := strings.Split(blocks[i], "`")
		for j := 0; j < len(spans); j += 2 { // and outside inline code
			spans[j] = ezafeRe.ReplaceAllString(spans[j], "${1}ه${2}")
		}
		blocks[i] = strings.Join(spans, "`")
	}
	return strings.Join(blocks, "```")
}
