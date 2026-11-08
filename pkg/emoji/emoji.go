package emoji

import (
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	vs16 = '️'
	zwj  = '‍'
)

type Set struct {
	ids     map[string]string
	lengths []int
}

func New(m map[string]string) *Set {
	s := &Set{ids: map[string]string{}}
	add := func(k, v string) {
		if k == "" || v == "" {
			return
		}
		if _, exists := s.ids[k]; !exists {
			s.ids[k] = v
		}
	}
	for k, v := range m {
		add(k, v)
	}
	for k, v := range m {
		add(strings.ReplaceAll(k, string(vs16), ""), v)
	}
	seen := map[int]bool{}
	for k := range s.ids {
		if !seen[len(k)] {
			seen[len(k)] = true
			s.lengths = append(s.lengths, len(k))
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(s.lengths)))
	return s
}

func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.ids)
}

func (s *Set) ID(e string) (string, bool) {
	if s == nil {
		return "", false
	}
	id, ok := s.ids[e]
	if !ok {
		id, ok = s.ids[strings.ReplaceAll(e, string(vs16), "")]
	}
	return id, ok
}

func (s *Set) match(text string) (string, string, int) {
	for _, l := range s.lengths {
		if l > len(text) {
			continue
		}
		id, ok := s.ids[text[:l]]
		if !ok {
			continue
		}
		n := l
		if r, size := utf8.DecodeRuneInString(text[n:]); r == vs16 {
			n += size
		}

		if r, _ := utf8.DecodeRuneInString(text[n:]); r == zwj || (r >= 0x1F3FB && r <= 0x1F3FF) || r == '⃣' {
			continue
		}
		return text[:n], id, n
	}
	return "", "", 0
}

func (s *Set) replace(text string, fn func(emoji, id string) string) string {
	if s.Len() == 0 {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		if text[i] < 0x80 && text[i] != '#' && text[i] != '*' && (text[i] < '0' || text[i] > '9') {
			b.WriteByte(text[i])
			i++
			continue
		}
		if e, id, n := s.match(text[i:]); n > 0 {
			b.WriteString(fn(e, id))
			i += n
			continue
		}
		_, size := utf8.DecodeRuneInString(text[i:])
		b.WriteString(text[i : i+size])
		i += size
	}
	return b.String()
}

func (s *Set) HTML(src string) string {
	if s.Len() == 0 {
		return src
	}
	var b strings.Builder
	skip := 0
	for len(src) > 0 {
		lt := strings.IndexByte(src, '<')
		if lt < 0 {
			lt = len(src)
		}
		text := src[:lt]
		if skip == 0 {
			text = s.replace(text, func(e, id string) string {
				return fmt.Sprintf(`<tg-emoji emoji-id="%s">%s</tg-emoji>`, id, e)
			})
		}
		b.WriteString(text)
		src = src[lt:]
		if src == "" {
			break
		}
		gt := strings.IndexByte(src, '>')
		if gt < 0 {
			b.WriteString(src)
			break
		}
		tag := src[:gt+1]
		name := strings.ToLower(strings.TrimLeft(strings.Fields(strings.Trim(tag, "<>/"))[0], "/"))
		switch name {
		case "code", "pre", "tg-emoji":
			if strings.HasPrefix(tag, "</") {
				skip = max(skip-1, 0)
			} else if !strings.HasSuffix(tag, "/>") {
				skip++
			}
		}
		b.WriteString(tag)
		src = src[gt+1:]
	}
	return b.String()
}

var mdSkipRe = regexp.MustCompile("(?s)```.*?(?:```|\\z)|~~~.*?(?:~~~|\\z)|`[^`\n]*`|!\\[[^\\]\n]*\\]\\([^)\n]*\\)|\\]\\([^)\n]*\\)|<tg-emoji[^>]*>.*?</tg-emoji>")

func (s *Set) Markdown(md string) string {
	if s.Len() == 0 {
		return md
	}
	var b strings.Builder
	last := 0
	for _, loc := range mdSkipRe.FindAllStringIndex(md, -1) {
		b.WriteString(s.replace(md[last:loc[0]], mdEmoji))
		b.WriteString(md[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(s.replace(md[last:], mdEmoji))
	return b.String()
}

func mdEmoji(e, id string) string { return "![" + e + "](tg://emoji?id=" + id + ")" }

func (s *Set) Button(label string) (string, string) {
	if s.Len() == 0 {
		return label, ""
	}
	t := strings.TrimSpace(label)
	if _, id, n := s.match(t); n > 0 {
		rest := strings.TrimSpace(t[n:])
		if rest == "" {

			return "\u200c", id
		}
		return rest, id
	}

	for i := len(t) - 1; i > 0; i-- {
		if !utf8.RuneStart(t[i]) {
			continue
		}
		if _, id, n := s.match(t[i:]); n > 0 && i+n == len(t) {
			if rest := strings.TrimSpace(t[:i]); rest != "" {
				return rest, id
			}
			return "\u200c", id
		}
		if len(t)-i > 32 {
			break
		}
	}
	return label, ""
}

type Entity struct {
	Offset, Length int
	CustomEmojiID  string
}

type Pair struct {
	Emoji string
	ID    string
}

var idRe = regexp.MustCompile(`^\d{5,25}$`)

func Key(e string) string { return strings.ReplaceAll(e, string(vs16), "") }

func Dedupe(pairs []Pair) (kept, extra []Pair) {
	emojis := map[string]bool{}
	for _, p := range pairs {
		if emojis[Key(p.Emoji)] {
			extra = append(extra, p)
			continue
		}
		emojis[Key(p.Emoji)] = true
		kept = append(kept, p)
	}
	return kept, extra
}

type Reject struct {
	Pair
	Taken   bool
	UsedFor string
	Twice   bool
}

func Filter(existing map[string]string, pairs []Pair) (add []Pair, rejected []Reject) {
	have, used := map[string]bool{}, map[string]string{}
	for e, id := range existing {
		have[Key(e)] = true
		if prev, ok := used[id]; !ok || e < prev {
			used[id] = e
		}
	}
	batch := map[string]bool{}
	for _, p := range pairs {
		k := Key(p.Emoji)
		switch {
		case batch[k]:
			rejected = append(rejected, Reject{Pair: p, Twice: true})
		case have[k]:
			rejected = append(rejected, Reject{Pair: p, Taken: true})
		case used[p.ID] != "":
			rejected = append(rejected, Reject{Pair: p, UsedFor: used[p.ID]})
		default:
			add = append(add, p)
			used[p.ID] = p.Emoji
		}
		batch[k] = true
	}
	return add, rejected
}

type Problem struct {
	Line      string
	MissingID bool
}

func ParseMappings(text string, entities []Entity) (pairs []Pair, problems []Problem) {

	u16ToByte := map[int]int{}
	u16 := 0
	for i, r := range text {
		u16ToByte[u16] = i
		u16 += len(utf16.Encode([]rune{r}))
	}
	u16ToByte[u16] = len(text)
	type span struct {
		start, end int
		id         string
	}
	var spans []span
	for _, e := range entities {
		if e.CustomEmojiID == "" {
			continue
		}
		start, ok1 := u16ToByte[e.Offset]
		end, ok2 := u16ToByte[e.Offset+e.Length]
		if ok1 && ok2 {
			spans = append(spans, span{start, end, e.CustomEmojiID})
		}
	}

	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		lineStart := offset
		offset += len(line)
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		first := fields[0]
		if !IsEmoji(first) {
			problems = append(problems, Problem{Line: truncate(trimmed, 30)})
			continue
		}

		firstEnd := lineStart + strings.Index(line, first) + len(first)
		var id string
		for _, sp := range spans {
			if sp.start >= firstEnd && sp.end <= lineStart+len(line) {
				id = sp.id
				break
			}
		}
		if id == "" && len(fields) > 1 && idRe.MatchString(fields[1]) {
			id = fields[1]
		}
		if id == "" {
			problems = append(problems, Problem{Line: truncate(trimmed, 30), MissingID: true})
			continue
		}
		pairs = append(pairs, Pair{Emoji: first, ID: id})
	}
	return pairs, problems
}

func IsEmoji(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > 12 {
		return false
	}
	hasPictograph := false
	for _, r := range s {
		switch {
		case r == vs16 || r == zwj || r == '⃣' || (r >= 0x1F3FB && r <= 0x1F3FF) || (r >= 0xE0020 && r <= 0xE007F):
		case r >= 0x1F000 || (r >= 0x2190 && r <= 0x2BFF) || (r >= 0x2300 && r <= 0x23FF) || r == 0x00A9 || r == 0x00AE || r == 0x3030 || r == 0x303D || r == 0x3297 || r == 0x3299,
			r >= 0x203C && r <= 0x2049, r == 0x2139:
			hasPictograph = true
		case unicode.Is(unicode.So, r):
			hasPictograph = true
		case r == '#' || r == '*' || (r >= '0' && r <= '9'):

		default:
			return false
		}
	}
	return hasPictograph || strings.ContainsRune(s, '⃣')
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func Preview(m map[string]string, limit int) (list string, more int) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i == limit {
			return b.String(), len(keys) - limit
		}
		fmt.Fprintf(&b, "%s → <tg-emoji emoji-id=\"%s\">%s</tg-emoji> <code>%s</code>\n", k, m[k], k, html.EscapeString(m[k]))
	}
	return b.String(), 0
}
