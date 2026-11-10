package telegram

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

func EscapeHTML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

var (
	fenceRe      = regexp.MustCompile("(?s)```([A-Za-z0-9_+#.-]*)[ \\t]*\\n?(.*?)(?:```|\\z)")
	inlineCodeRe = regexp.MustCompile("`([^`\\n]+)`")
	headerRe     = regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}[ \t]+(.+?)[ \t]*#*[ \t]*$`)
	hrRe         = regexp.MustCompile(`(?m)^[ \t]*(?:(?:-[ \t]*){3,}|(?:\*[ \t]*){3,}|(?:_[ \t]*){3,})$`)
	bulletRe     = regexp.MustCompile(`(?m)^([ \t]*)[-*+][ \t]+`)
	boldRe       = regexp.MustCompile(`\*\*([^\n]+?)\*\*`)
	boldUnderRe  = regexp.MustCompile(`(^|[^\w_])__([^\s_][^\n]*?)__($|[^\w_])`)
	italicRe     = regexp.MustCompile(`(^|[^\w*])\*([^\s*][^*\n]*?)\*($|[^\w*])`)
	strikeRe     = regexp.MustCompile(`~~([^\n]+?)~~`)
	linkRe       = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^\s)]+)\)`)
	langRe       = regexp.MustCompile(`^[A-Za-z0-9_+#.-]{1,30}$`)
	tableSepRe   = regexp.MustCompile(`^\|?[\s:|-]*-[\s:|-]*\|?$`)
	manyNLRe     = regexp.MustCompile(`\n{3,}`)
	htmlTagRe    = regexp.MustCompile(`</?([a-zA-Z][a-zA-Z0-9-]*)(?:\s[^<>]*)?>`)
)

func MarkdownToHTML(md string) string {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	md = classicDetails(md)
	var stash []string
	hold := func(html string) string {
		stash = append(stash, html)
		return fmt.Sprintf("\x00%d\x00", len(stash)-1)
	}

	md = fenceRe.ReplaceAllStringFunc(md, func(m string) string {
		sub := fenceRe.FindStringSubmatch(m)
		code := EscapeHTML(strings.Trim(sub[2], "\n"))
		if langRe.MatchString(sub[1]) {
			return hold(fmt.Sprintf("<pre><code class=\"language-%s\">%s</code></pre>", sub[1], code))
		}
		return hold("<pre>" + code + "</pre>")
	})

	md = replaceTables(md, hold)

	md = inlineCodeRe.ReplaceAllStringFunc(md, func(m string) string {
		return hold("<code>" + EscapeHTML(inlineCodeRe.FindStringSubmatch(m)[1]) + "</code>")
	})

	md = EscapeHTML(md)
	md = hrRe.ReplaceAllString(md, "──────────")
	md = headerRe.ReplaceAllString(md, "<b>$1</b>")
	md = bulletRe.ReplaceAllString(md, "$1• ")
	md = boldRe.ReplaceAllString(md, "<b>$1</b>")
	md = boldUnderRe.ReplaceAllString(md, "$1<b>$2</b>$3")
	md = italicRe.ReplaceAllString(md, "$1<i>$2</i>$3")
	md = strikeRe.ReplaceAllString(md, "<s>$1</s>")
	md = linkRe.ReplaceAllStringFunc(md, func(m string) string {
		sub := linkRe.FindStringSubmatch(m)
		return fmt.Sprintf(`<a href="%s">%s</a>`, strings.ReplaceAll(sub[2], `"`, "%22"), sub[1])
	})
	md = quoteBlocks(md)

	for i := len(stash) - 1; i >= 0; i-- {
		md = strings.ReplaceAll(md, fmt.Sprintf("\x00%d\x00", i), stash[i])
	}
	md = strings.NewReplacer(quoteOpen, "<blockquote expandable>", quoteClose, "</blockquote>").Replace(md)
	return strings.TrimSpace(manyNLRe.ReplaceAllString(md, "\n\n"))
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
			n := utf8.RuneCountInString(t)
			if n <= room {
				cur.WriteString(t)
				visible += n
				return
			}
			cut := byteOffsetOfRune(t, room)
			piece := t[:cut]
			if i := strings.LastIndex(piece, "\n"); i > len(piece)/3 {
				cut = i + 1
			} else if i := strings.LastIndexByte(piece, ' '); i > len(piece)/3 {
				cut = i + 1
			}
			if i := strings.LastIndexByte(t[:cut], '&'); i >= 0 && !strings.Contains(t[i:cut], ";") {
				cut = i
			}
			if cut <= 0 {
				_, size := utf8.DecodeRuneInString(t)
				cut = size
			}
			cur.WriteString(t[:cut])
			visible += utf8.RuneCountInString(t[:cut])
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

func byteOffsetOfRune(s string, n int) int {
	for i := range s {
		if n == 0 {
			return i
		}
		n--
	}
	return len(s)
}

var fenceLineRe = regexp.MustCompile("^\\s*(```|~~~)")

func SplitMarkdown(md string, limit int) []string {
	md = strings.TrimSpace(strings.ReplaceAll(md, "\r\n", "\n"))
	if limit <= 0 || utf8.RuneCountInString(md) <= limit {
		return []string{md}
	}

	var blocks []string
	var cur []string
	inFence, fence := false, ""
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(md, "\n") {
		if m := fenceLineRe.FindStringSubmatch(line); m != nil {
			if !inFence {
				flush()
				inFence, fence = true, line
				cur = append(cur, line)
				continue
			}
			cur = append(cur, line)
			inFence = false
			flush()
			continue
		}
		if !inFence && strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	_ = fence

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
	open, closeFence := "", ""
	if fenceLineRe.MatchString(lines[0]) {
		open = strings.TrimSpace(lines[0])
		closeFence = open[:3]
		lines = lines[1:]
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == closeFence {
			lines = lines[:len(lines)-1]
		}
		limit -= utf8.RuneCountInString(open) + 8
	}
	var parts []string
	var b strings.Builder
	size := 0
	emit := func() {
		if b.Len() == 0 {
			return
		}
		body := strings.TrimRight(b.String(), "\n")
		if open != "" {
			body = open + "\n" + body + "\n" + closeFence
		}
		parts = append(parts, body)
		b.Reset()
		size = 0
	}
	for _, line := range lines {
		for utf8.RuneCountInString(line) > limit {
			r := []rune(line)
			emit()
			b.WriteString(string(r[:limit]))
			emit()
			line = string(r[limit:])
		}
		n := utf8.RuneCountInString(line) + 1
		if size+n > limit {
			emit()
		}
		b.WriteString(line + "\n")
		size += n
	}
	emit()
	return parts
}

func CloseOpenMarkdown(md string) string {
	open := false
	for _, line := range strings.Split(md, "\n") {
		if fenceLineRe.MatchString(line) {
			open = !open
		}
	}
	if open {
		return md + "\n```"
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
