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

func StripHTML(s string) string {
	s = strings.ReplaceAll(s, "<br>", "\n")
	return html.UnescapeString(tagRe.ReplaceAllString(s, ""))
}

func CollapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func OneLine(s string) string {
	return CollapseSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s))
}

var argKeys = []string{"query", "url", "topic", "prompt", "text", "domain", "repo", "symbols", "command", "data", "input_file", "filename"}

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

func HeadTail(s string, max int) string {
	r := []rune(s)
	if max <= 0 || len(r) <= max {
		return s
	}
	const slack = 200
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

var foldMap = map[rune]rune{
	'ي': 'ی', 'ى': 'ی', 'ئ': 'ی', 'ك': 'ک', 'ة': 'ه', 'ۀ': 'ه', 'أ': 'ا', 'إ': 'ا', 'آ': 'ا', 'ٱ': 'ا', 'ؤ': 'و',
}

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

var ezafeRe = regexp.MustCompile(`(\p{Arabic})ه\x{200C}ی([^\p{L}\p{Mn}\x{200C}]|$)`)

func PersianEzafe(md string) string {
	if !strings.Contains(md, "ه‌ی") {
		return md
	}
	blocks := strings.Split(md, "```")
	for i := 0; i < len(blocks); i += 2 {
		spans := strings.Split(blocks[i], "`")
		for j := 0; j < len(spans); j += 2 {
			spans[j] = ezafeRe.ReplaceAllString(spans[j], "${1}ه${2}")
		}
		blocks[i] = strings.Join(spans, "`")
	}
	return strings.Join(blocks, "```")
}
