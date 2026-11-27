package telegram

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

func EscapeHTML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

var (
	inlineCodeRe = regexp.MustCompile("`([^`\\n]+)`")
	headerRe     = regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}[ \t]+(.+?)[ \t]*#*[ \t]*$`)
	hrRe         = regexp.MustCompile(`(?m)^[ \t]*(?:(?:-[ \t]*){3,}|(?:\*[ \t]*){3,}|(?:_[ \t]*){3,})$`)
	bulletRe     = regexp.MustCompile(`(?m)^([ \t]*)[-*+][ \t]+`)
	boldRe       = regexp.MustCompile(`\*\*([^\n]+?)\*\*`)
	boldUnderRe  = regexp.MustCompile(`(^|[^\w_])__([^\s_][^\n]*?)__($|[^\w_])`)
	italicRe     = regexp.MustCompile(`(^|[^\w*])\*([^\s*][^*\n]*?)\*($|[^\w*])`)
	strikeRe     = regexp.MustCompile(`~~([^\n]+?)~~`)
	spoilerRe    = regexp.MustCompile(`\|\|([^\n]+?)\|\|`)
	linkRe       = regexp.MustCompile(`\[([^\]\n]+)\]\(([^\s<>()]+(?:\([^\s<>()]*\)[^\s<>()]*)*)\)`)
	langRe       = regexp.MustCompile(`^[A-Za-z0-9_+#.-]{1,30}$`)
	tableSepRe   = regexp.MustCompile(`^\|?[\s:|-]*-[\s:|-]*\|?$`)
	manyNLRe     = regexp.MustCompile(`\n{3,}`)
	htmlTagRe    = regexp.MustCompile(`</?([a-zA-Z][a-zA-Z0-9-]*)(?:\s[^<>]*)?>`)
)

func safeLinkURL(raw string) (string, bool) {
	u := strings.TrimSpace(raw)
	low := strings.ToLower(u)
	for _, p := range []string{"http://", "https://", "mailto:", "tg://"} {
		if strings.HasPrefix(low, p) && len(u) > len(p) {
			return u, true
		}
	}
	return "", false
}

func formatInline(s string) string {
	s = EscapeHTML(s)
	s = spoilerRe.ReplaceAllString(s, "<tg-spoiler>$1</tg-spoiler>")
	s = boldRe.ReplaceAllString(s, "<b>$1</b>")
	s = boldUnderRe.ReplaceAllString(s, "$1<b>$2</b>$3")
	s = italicRe.ReplaceAllString(s, "$1<i>$2</i>$3")
	s = strikeRe.ReplaceAllString(s, "<s>$1</s>")
	return s
}

func MarkdownToHTML(md string) string {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	md = classicDetails(md)
	var stash []string
	hold := func(html string) string {
		stash = append(stash, html)
		return fmt.Sprintf("\x00%d\x00", len(stash)-1)
	}

	md = replaceFencedCode(md, hold)
	md = replaceTables(md, hold)

	md = inlineCodeRe.ReplaceAllStringFunc(md, func(m string) string {
		return hold("<code>" + EscapeHTML(inlineCodeRe.FindStringSubmatch(m)[1]) + "</code>")
	})

	md = linkRe.ReplaceAllStringFunc(md, func(m string) string {
		sub := linkRe.FindStringSubmatch(m)
		u, ok := safeLinkURL(sub[2])
		if !ok {
			return m
		}
		href := EscapeHTML(strings.ReplaceAll(u, `"`, "%22"))
		return hold(fmt.Sprintf(`<a href="%s">%s</a>`, href, formatInline(sub[1])))
	})

	md = EscapeHTML(md)
	md = hrRe.ReplaceAllString(md, "──────────")
	md = headerRe.ReplaceAllString(md, "<b>$1</b>")
	md = bulletRe.ReplaceAllString(md, "$1• ")
	md = spoilerRe.ReplaceAllString(md, "<tg-spoiler>$1</tg-spoiler>")
	md = boldRe.ReplaceAllString(md, "<b>$1</b>")
	md = boldUnderRe.ReplaceAllString(md, "$1<b>$2</b>$3")
	md = italicRe.ReplaceAllString(md, "$1<i>$2</i>$3")
	md = strikeRe.ReplaceAllString(md, "<s>$1</s>")
	md = quoteBlocks(md)

	for i := len(stash) - 1; i >= 0; i-- {
		md = strings.ReplaceAll(md, fmt.Sprintf("\x00%d\x00", i), stash[i])
	}
	md = strings.NewReplacer(quoteOpen, "<blockquote expandable>", quoteClose, "</blockquote>").Replace(md)
	return strings.TrimSpace(manyNLRe.ReplaceAllString(md, "\n\n"))
}

func replaceFencedCode(md string, hold func(string) string) string {
	lines := strings.Split(md, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		sub := fenceLineRe.FindStringSubmatch(lines[i])
		if sub == nil {
			out = append(out, lines[i])
			continue
		}
		start := i + 1
		for i++; i < len(lines) && !closesFence(lines[i], sub[1]); i++ {
		}
		code := EscapeHTML(strings.Join(lines[start:i], "\n"))
		info := strings.Fields(sub[2])
		if len(info) > 0 && langRe.MatchString(info[0]) {
			code = fmt.Sprintf("<code class=\"language-%s\">%s</code>", info[0], code)
		}
		out = append(out, hold("<pre>"+code+"</pre>"))
	}
	return strings.Join(out, "\n")
}

func replaceTables(md string, hold func(string) string) string {
	lines := strings.Split(md, "\n")
	var out []string
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "|") {
			out = append(out, lines[i])
			i++
			continue
		}
		j := i
		for j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), "|") {
			j++
		}
		if j-i < 2 {
			out = append(out, lines[i:j]...)
			i = j
			continue
		}
		var rows []string
		for _, l := range lines[i:j] {
			if t := strings.TrimSpace(l); !tableSepRe.MatchString(t) {
				rows = append(rows, EscapeHTML(t))
			}
		}
		out = append(out, hold("<pre>"+strings.Join(rows, "\n")+"</pre>"))
		i = j
	}
	return strings.Join(out, "\n")
}

func quoteBlocks(md string) string {
	lines := strings.Split(md, "\n")
	var out []string
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(lines[i], "&gt;") {
			out = append(out, lines[i])
			i++
			continue
		}
		var quoted []string
		for i < len(lines) && strings.HasPrefix(lines[i], "&gt;") {
			quoted = append(quoted, strings.TrimPrefix(strings.TrimPrefix(lines[i], "&gt;"), " "))
			i++
		}
		out = append(out, "<blockquote>"+strings.Join(quoted, "\n")+"</blockquote>")
	}
	return strings.Join(out, "\n")
}

func ValidHTML(s string) bool {
	allowed := map[string]bool{"b": true, "i": true, "u": true, "s": true, "code": true, "pre": true,
		"a": true, "blockquote": true, "tg-spoiler": true, "strong": true, "em": true, "ins": true, "del": true, "strike": true}
	var stack []string
	for _, loc := range htmlTagRe.FindAllStringSubmatchIndex(s, -1) {
		name := strings.ToLower(s[loc[2]:loc[3]])
		if !allowed[name] {
			return false
		}
		if s[loc[0]+1] == '/' {
			if len(stack) == 0 || stack[len(stack)-1] != name {
				return false
			}
			stack = stack[:len(stack)-1]
		} else {
			stack = append(stack, name)
		}
	}
	return len(stack) == 0
}

type openTag struct{ name, full string }

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// Entity escapes occupy one text character even when their wire encoding is
// longer. Keep the entire entity together when splitting formatted text.
func htmlTextStep(s string) (size, units int) {
	if s[0] == '&' {
		if end := strings.IndexByte(s[:min(len(s), 16)], ';'); end > 0 {
			escape := s[:end+1]
			decoded := html.UnescapeString(escape)
			if utf8.RuneCountInString(decoded) == 1 {
				return len(escape), utf16Len(decoded)
			}
		}
	}
	r, size := utf8.DecodeRuneInString(s)
	if r > 0xFFFF {
		return size, 2
	}
	return size, 1
}

func htmlTextPrefix(s string, maxUnits int) (size, units int) {
	for size < len(s) {
		n, step := htmlTextStep(s[size:])
		if units+step > maxUnits {
			break
		}
		size += n
		units += step
	}
	return size, units
}

func visibleUTF16Len(s string) int {
	n := 0
	pos := 0
	for _, loc := range htmlTagRe.FindAllStringSubmatchIndex(s, -1) {
		_, visible := htmlTextPrefix(s[pos:loc[0]], loc[0]-pos)
		n += visible
		pos = loc[1]
	}
	_, visible := htmlTextPrefix(s[pos:], len(s)-pos)
	return n + visible
}

// SafeHTMLCaption truncates HTML so visible text never exceeds maxUTF16 UTF-16 code units,
// without splitting inside HTML tags, entities, or UTF-16 surrogate pairs.
func SafeHTMLCaption(s string, maxUTF16 int) string {
	if s == "" || maxUTF16 <= 0 {
		return ""
	}
	if !ValidHTML(s) {
		s = EscapeHTML(textutil.StripHTML(s))
	}
	if visibleUTF16Len(s) <= maxUTF16 {
		return s
	}
	if maxUTF16 == 1 {
		return "…"
	}
	head := SplitHTML(s, maxUTF16-1)[0]
	if visibleUTF16Len(head) > maxUTF16-1 {
		return "…"
	}
	return head + "…"
}

func SplitHTML(s string, limit int) []string {
	if limit <= 0 {
		limit = 3800
	}
	var (
		chunks  []string
		cur     strings.Builder
		open    []openTag
		visible int
	)
	flush := func() {
		if visible == 0 {
			return
		}
		out := cur.String()
		for i := len(open) - 1; i >= 0; i-- {
			out += "</" + open[i].name + ">"
		}
		chunks = append(chunks, out)
		cur.Reset()
		visible = 0
		for _, o := range open {
			cur.WriteString(o.full)
		}
	}
	addText := func(t string) {
		for len(t) > 0 {
			room := limit - visible
			if room <= 0 {
				flush()
				room = limit
			}
			cut, n := htmlTextPrefix(t, room)
			if cut == len(t) {
				cur.WriteString(t)
				visible += n
				return
			}
			if cut > 0 {
				piece := t[:cut]
				if i := strings.LastIndex(piece, "\n"); i > len(piece)/3 {
					cut = i + 1
				} else if i := strings.LastIndexByte(piece, ' '); i > len(piece)/3 {
					cut = i + 1
				}
				if cut != len(piece) {
					_, n = htmlTextPrefix(t[:cut], room)
				}
			}
			if cut == 0 {
				if visible > 0 {
					flush()
					continue
				}
				cut, n = htmlTextStep(t)
			}
			cur.WriteString(t[:cut])
			visible += n
			t = t[cut:]
			flush()
		}
	}

	pos := 0
	for _, loc := range htmlTagRe.FindAllStringSubmatchIndex(s, -1) {
		addText(s[pos:loc[0]])
		tag := s[loc[0]:loc[1]]
		name := strings.ToLower(s[loc[2]:loc[3]])
		if tag[1] == '/' {
			for i := len(open) - 1; i >= 0; i-- {
				if open[i].name == name {
					open = open[:i]
					break
				}
			}
		} else {
			open = append(open, openTag{name, tag})
		}
		cur.WriteString(tag)
		pos = loc[1]
	}
	addText(s[pos:])
	flush()
	if len(chunks) == 0 {
		return []string{""}
	}
	return chunks
}

var fenceLineRe = regexp.MustCompile("^[ \\t]*(`{3,}|~{3,})(.*)$")

func fenceMarker(line string) string {
	if m := fenceLineRe.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	return ""
}

func closesFence(line, marker string) bool {
	sub := fenceLineRe.FindStringSubmatch(line)
	return sub != nil && len(sub[1]) >= len(marker) && sub[1][0] == marker[0] && strings.TrimSpace(sub[2]) == ""
}

func SplitMarkdown(md string, limit int) []string {
	md = strings.TrimSpace(strings.ReplaceAll(md, "\r\n", "\n"))
	if limit <= 0 || utf8.RuneCountInString(md) <= limit {
		return []string{md}
	}

	var blocks []string
	var cur []string
	fence := ""
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(md, "\n") {
		if fence != "" {
			cur = append(cur, line)
			if closesFence(line, fence) {
				fence = ""
				flush()
			}
			continue
		}
		if marker := fenceMarker(line); marker != "" {
			flush()
			fence = marker
			cur = append(cur, line)
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()

	var parts []string
	var b strings.Builder
	size := 0
	emit := func() {
		if t := strings.TrimSpace(b.String()); t != "" {
			parts = append(parts, t)
		}
		b.Reset()
		size = 0
	}
	for _, blk := range blocks {
		n := utf8.RuneCountInString(blk)
		if n > limit {
			emit()
			parts = append(parts, splitBigBlock(blk, limit)...)
			continue
		}
		if size > 0 && size+2+n > limit {
			emit()
		}
		if size > 0 {
			b.WriteString("\n\n")
			size += 2
		}
		b.WriteString(blk)
		size += n
	}
	emit()
	return parts
}

func splitBigBlock(blk string, limit int) []string {
	lines := strings.Split(blk, "\n")
	open, closeFence := "", fenceMarker(lines[0])
	if closeFence != "" {
		if 2*len(closeFence)+2 < limit {
			open = strings.TrimSpace(lines[0])
			if utf8.RuneCountInString(open)+len(closeFence)+2 >= limit {
				open = closeFence
			}
			limit -= utf8.RuneCountInString(open) + len(closeFence) + 2
			lines = lines[1:]
			if len(lines) > 0 && closesFence(lines[len(lines)-1], closeFence) {
				lines = lines[:len(lines)-1]
			}
		}
	}
	var parts []string
	var b strings.Builder
	size := 0
	emit := func() {
		if b.Len() == 0 {
			return
		}
		body := strings.TrimSuffix(b.String(), "\n")
		if open != "" {
			body = open + "\n" + body + "\n" + closeFence
		}
		parts = append(parts, body)
		b.Reset()
		size = 0
	}
	for _, line := range lines {
		runes := []rune(line)
		for len(runes) > limit {
			emit()
			b.WriteString(string(runes[:limit]))
			emit()
			runes = runes[limit:]
		}
		n := len(runes) + 1
		if size+n-1 > limit {
			emit()
		}
		b.WriteString(string(runes) + "\n")
		size += n
	}
	emit()
	return parts
}

func CloseOpenMarkdown(md string) string {
	fence := ""
	for _, line := range strings.Split(md, "\n") {
		if fence == "" {
			fence = fenceMarker(line)
		} else if closesFence(line, fence) {
			fence = ""
		}
	}
	if fence != "" {
		return md + "\n" + fence
	}
	return md
}

var (
	setextRe = regexp.MustCompile(`^[ \t]{0,3}(?:=+|-+)[ \t]*$`)

	mediaLineRe = regexp.MustCompile(`^!\[[^\]\n]*\]\((?:tg://(?:photo|video|audio|document)|https?://)[^)\n]*\)[ \t]*$`)
)

func HardBreaks(md string) string {
	if !strings.Contains(md, "\n") {
		return md
	}
	lines := strings.Split(md, "\n")
	fence := ""
	gallery := ""
	for i, line := range lines {
		trim := strings.TrimLeft(line, " \t")
		if gallery != "" {
			if strings.Contains(line, gallery) {
				gallery = ""
			}
			continue
		}
		for _, tag := range []string{"tg-slideshow", "tg-collage"} {
			if strings.HasPrefix(trim, "<"+tag) && !strings.Contains(line, "</"+tag+">") {
				gallery = "</" + tag + ">"
			}
		}
		if gallery != "" {
			continue
		}
		if fence != "" {
			if strings.HasPrefix(trim, fence) && strings.Trim(strings.TrimSpace(trim), fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if m := fenceOpen(trim); m != "" {
			fence = m
			continue
		}
		if i == len(lines)-1 || strings.TrimSpace(line) == "" || strings.HasSuffix(line, "  ") || strings.HasSuffix(line, "\\") {
			continue
		}
		next := strings.TrimLeft(lines[i+1], " \t")
		switch {
		case strings.TrimSpace(next) == "", fenceOpen(next) != "", setextRe.MatchString(lines[i+1]):
			continue
		case strings.HasPrefix(trim, "|"), strings.HasPrefix(trim, "#"), strings.HasPrefix(trim, "<"):
			continue
		case mediaLineRe.MatchString(trim), mediaLineRe.MatchString(next):
			continue
		}
		lines[i] = line + "  "
	}
	return strings.Join(lines, "\n")
}

func fenceOpen(trim string) string {
	for _, c := range []string{"`", "~"} {
		n := len(trim) - len(strings.TrimLeft(trim, c))
		if n >= 3 {
			return strings.Repeat(c, n)
		}
	}
	return ""
}

func IsMostlyRTL(s string) bool {
	rtl, ltr := 0, 0
	for _, r := range s {
		switch {
		case r >= 0x0590 && r <= 0x08FF, r >= 0xFB1D && r <= 0xFDFF, r >= 0xFE70 && r <= 0xFEFF:
			rtl++
		case r < 0x0250 && ((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r >= 0xC0):
			ltr++
		}
	}
	return rtl > ltr
}
