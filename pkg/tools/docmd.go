package tools

import (
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
	"unicode"
)

var (
	mdHeadingRe  = regexp.MustCompile(`^(#{1,6})[ \t]+(.+?)[ \t]*#*$`)
	mdHRRe       = regexp.MustCompile(`^(?:(?:-[ \t]*){3,}|(?:\*[ \t]*){3,}|(?:_[ \t]*){3,})$`)
	mdListRe     = regexp.MustCompile(`^([ \t]*)([-*+]|\d{1,9}[.)])[ \t]+(.*)$`)
	mdTaskRe     = regexp.MustCompile(`^\[([ xX])\][ \t]+`)
	mdFenceRe    = regexp.MustCompile("^[ \t]*(```|~~~)[ \t]*([A-Za-z0-9_+#.-]*)")
	mdTableSepRe = regexp.MustCompile(`^\|?[ \t]*:?-{2,}:?[ \t]*(\|[ \t]*:?-{2,}:?[ \t]*)*\|?[ \t]*$`)
	mdImageRe    = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
	mdLinkRe     = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	mdBoldRe     = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	mdItalicRe   = regexp.MustCompile(`(^|[^\w*])\*([^\s*][^*]*?)\*($|[^\w*])`)
	mdStrikeRe   = regexp.MustCompile(`~~(.+?)~~`)
	mdCodeRe     = regexp.MustCompile("`([^`]+)`")
)

func docHTML(md string) string {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var out strings.Builder
	var para []string
	flush := func() {
		if len(para) > 0 {
			text := strings.Join(para, " ")
			out.WriteString(`<p dir="` + textDir(text) + `">` + mdInline(text) + "</p>\n")
			para = nil
		}
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		switch {
		case trim == "":
			flush()
		case mdFenceRe.MatchString(line):
			flush()
			m := mdFenceRe.FindStringSubmatch(line)
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), m[1]); i++ {
				code = append(code, lines[i])
			}
			class := ""
			if m[2] != "" {
				class = ` class="language-` + html.EscapeString(m[2]) + `"`
			}
			out.WriteString(`<pre dir="ltr"><code` + class + ">" + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>\n")
		case mdHeadingRe.MatchString(trim):
			flush()
			m := mdHeadingRe.FindStringSubmatch(trim)
			fmt.Fprintf(&out, "<h%d dir=\"%s\">%s</h%d>\n", len(m[1]), textDir(m[2]), mdInline(m[2]), len(m[1]))
		case mdHRRe.MatchString(trim):
			flush()
			out.WriteString("<hr>\n")
		case strings.HasPrefix(trim, ">"):
			flush()
			var quote []string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">"); i++ {
				quote = append(quote, strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">"), " "))
			}
			i--
			out.WriteString(`<blockquote dir="` + textDir(strings.Join(quote, " ")) + `">` + docHTML(strings.Join(quote, "\n")) + "</blockquote>\n")
		case strings.Contains(trim, "|") && i+1 < len(lines) && mdTableSepRe.MatchString(strings.TrimSpace(lines[i+1])):
			flush()
			head := mdCells(trim)
			aligns := mdAligns(strings.TrimSpace(lines[i+1]))
			out.WriteString(`<table dir="` + textDir(trim) + `"><thead><tr>`)
			for c, cell := range head {
				out.WriteString("<th" + mdAlign(aligns, c) + ">" + mdInline(cell) + "</th>")
			}
			out.WriteString("</tr></thead><tbody>\n")
			for i += 2; i < len(lines) && strings.Contains(lines[i], "|") && strings.TrimSpace(lines[i]) != ""; i++ {
				out.WriteString("<tr>")
				cells := mdCells(strings.TrimSpace(lines[i]))
				for c := range head {
					cell := ""
					if c < len(cells) {
						cell = cells[c]
					}
					out.WriteString("<td" + mdAlign(aligns, c) + ">" + mdInline(cell) + "</td>")
				}
				out.WriteString("</tr>\n")
			}
			i--
			out.WriteString("</tbody></table>\n")
		case mdListRe.MatchString(line):
			flush()
			start := i
			for i < len(lines) && (mdListRe.MatchString(lines[i]) || (strings.TrimSpace(lines[i]) != "" && (strings.HasPrefix(lines[i], "  ") || strings.HasPrefix(lines[i], "\t")))) {
				i++
			}
			out.WriteString(mdList(lines[start:i]))
			i--
		default:
			para = append(para, trim)
		}
	}
	flush()
	return out.String()
}

func mdList(lines []string) string {
	var out strings.Builder
	type level struct {
		indent int
		tag    string
	}
	var stack []level
	open := false
	for _, l := range lines {
		m := mdListRe.FindStringSubmatch(l)
		if m == nil {
			out.WriteString(" " + mdInline(strings.TrimSpace(l)))
			continue
		}
		indent := len(strings.ReplaceAll(m[1], "\t", "    "))
		tag := "ul"
		if m[2][0] >= '0' && m[2][0] <= '9' {
			tag = "ol"
		}
		for len(stack) > 0 && indent < stack[len(stack)-1].indent {
			out.WriteString("</li></" + stack[len(stack)-1].tag + ">")
			stack = stack[:len(stack)-1]
		}
		if n := len(stack); n > 0 && indent == stack[n-1].indent && tag != stack[n-1].tag {
			out.WriteString("</li></" + stack[n-1].tag + ">")
			stack, open = stack[:n-1], false
		}
		switch {
		case len(stack) == 0 || indent > stack[len(stack)-1].indent:
			stack = append(stack, level{indent, tag})
			out.WriteString(`<` + tag + ` dir="` + textDir(m[3]) + `">`)
		case open:
			out.WriteString("</li>")
		}
		text := m[3]
		box := ""
		if t := mdTaskRe.FindStringSubmatch(text); t != nil {
			box = "☐ "
			if t[1] != " " {
				box = "☑ "
			}
			text = text[len(t[0]):]
		}
		out.WriteString("<li>" + box + mdInline(text))
		open = true
	}
	for len(stack) > 0 {
		out.WriteString("</li></" + stack[len(stack)-1].tag + ">")
		stack = stack[:len(stack)-1]
	}
	return out.String() + "\n"
}

func mdCells(row string) []string {
	row = strings.TrimSuffix(strings.TrimPrefix(row, "|"), "|")
	cells := strings.Split(row, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func mdAligns(sep string) []string {
	var out []string
	for _, c := range mdCells(sep) {
		switch {
		case strings.HasPrefix(c, ":") && strings.HasSuffix(c, ":"):
			out = append(out, "center")
		case strings.HasSuffix(c, ":"):
			out = append(out, "right")
		case strings.HasPrefix(c, ":"):
			out = append(out, "left")
		default:
			out = append(out, "")
		}
	}
	return out
}

func mdAlign(aligns []string, i int) string {
	if i < len(aligns) && aligns[i] != "" {
		return ` style="text-align:` + aligns[i] + `"`
	}
	return ""
}

func mdInline(s string) string {
	var stash []string
	hold := func(h string) string {
		stash = append(stash, h)
		return fmt.Sprintf("\x00%d\x00", len(stash)-1)
	}
	s = mdCodeRe.ReplaceAllStringFunc(s, func(m string) string {
		return hold("<code>" + html.EscapeString(mdCodeRe.FindStringSubmatch(m)[1]) + "</code>")
	})
	s = mdImageRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdImageRe.FindStringSubmatch(m)
		src := sub[2]
		if !strings.HasPrefix(src, "https://") && !strings.HasPrefix(src, "http://") {
			src = "https://waze.local/" + strings.TrimLeft(strings.TrimPrefix(src, "/workspace"), "/")
		}
		return hold(`<img src="` + html.EscapeString(src) + `" alt="` + html.EscapeString(sub[1]) + `">`)
	})
	s = mdLinkRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdLinkRe.FindStringSubmatch(m)
		return hold(`<a href="` + html.EscapeString(sub[2]) + `">` + html.EscapeString(sub[1]) + "</a>")
	})
	s = html.EscapeString(s)
	s = mdBoldRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdBoldRe.FindStringSubmatch(m)
		return "<strong>" + sub[1] + sub[2] + "</strong>"
	})
	s = mdItalicRe.ReplaceAllString(s, "$1<em>$2</em>$3")
	s = mdStrikeRe.ReplaceAllString(s, "<del>$1</del>")
	for i, h := range stash {
		s = strings.Replace(s, fmt.Sprintf("\x00%d\x00", i), h, 1)
	}
	return s
}

func (r *Registry) htmlPDF(ctx context.Context, sessionID, md, title, name string) error {
	body := docHTML(md)
	rtl := persianShare(title+" "+md) >= 0.3
	cfg := map[string]any{"html": body, "title": title, "out": name, "rtl": rtl}
	if title != "" {
		cfg["date"] = time.Now().Format("2 January 2006")
		if rtl {
			cfg["date"] = persianDate(time.Now())
		}
	}
	var res struct {
		OK     bool     `json:"ok"`
		Error  string   `json:"error"`
		Errors []string `json:"errors"`
	}
	remote := strings.Contains(body, `<img src="http`)
	if err := r.runBrowser(ctx, sessionID, "pdf_html.py", cfg, remote, 2*time.Minute, &res); err != nil {
		return err
	}
	if !res.OK {
		return errors.New(res.Error)
	}
	if info, err := r.Machine.Stat(sessionID, name); err != nil || info.Size() == 0 {
		return errors.New("no PDF was made")
	}
	return nil
}

func textDir(s string) string {
	for _, c := range s {
		switch {
		case unicode.Is(unicode.Arabic, c) || unicode.Is(unicode.Hebrew, c):
			return "rtl"
		case unicode.IsLetter(c):
			return "ltr"
		}
	}
	return "auto"
}

func jalali(t time.Time) (y, m, d int) {
	gy, gm, gd := t.Year(), int(t.Month()), t.Day()
	before := [...]int{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334}
	gy2 := gy
	if gm > 2 {
		gy2 = gy + 1
	}
	days := 355666 + 365*gy + (gy2+3)/4 - (gy2+99)/100 + (gy2+399)/400 + gd + before[gm-1]
	y = -1595 + 33*(days/12053)
	days %= 12053
	y += 4 * (days / 1461)
	days %= 1461
	if days > 365 {
		y += (days - 1) / 365
		days = (days - 1) % 365
	}
	if days < 186 {
		return y, 1 + days/31, 1 + days%31
	}
	return y, 7 + (days-186)/30, 1 + (days-186)%30
}

var jalaliMonths = [...]string{"فروردین", "اردیبهشت", "خرداد", "تیر", "مرداد", "شهریور", "مهر", "آبان", "آذر", "دی", "بهمن", "اسفند"}

func persianDate(t time.Time) string {
	y, m, d := jalali(t)
	digits := strings.NewReplacer("0", "۰", "1", "۱", "2", "۲", "3", "۳", "4", "۴", "5", "۵", "6", "۶", "7", "۷", "8", "۸", "9", "۹")
	return digits.Replace(fmt.Sprintf("%d %s %d", d, jalaliMonths[m-1], y))
}

func persianShare(s string) float64 {
	var fa, all int
	for _, c := range s {
		if unicode.IsLetter(c) {
			all++
			if unicode.Is(unicode.Arabic, c) {
				fa++
			}
		}
	}
	if all == 0 {
		return 0
	}
	return float64(fa) / float64(all)
}
