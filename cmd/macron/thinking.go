package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/textutil"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

const (
	thinkShown = 6
	thinkKept  = 40
	thinkWidth = 56
)

type thinkStep struct {
	id     int
	sub    int
	name   string
	text   string
	detail string
	since  time.Time
	took   time.Duration
	active bool
	failed bool
	agent  bool
}

type thinkLog struct {
	steps []thinkStep
	next  int
}

func (t *thinkLog) add(s thinkStep) *thinkStep {
	t.next++
	s.id = t.next
	t.steps = append(t.steps, s)
	if len(t.steps) > thinkKept {
		kept := t.steps[:0]
		drop := len(t.steps) - thinkKept/2
		for _, o := range t.steps {
			if drop > 0 && !o.active {
				drop--
				continue
			}
			kept = append(kept, o)
		}
		t.steps = kept
	}
	return &t.steps[len(t.steps)-1]
}

func (t *thinkLog) start(sub int, name, text string, now time.Time) {
	t.add(thinkStep{sub: sub, name: name, text: text, since: now, active: true})
}

func (t *thinkLog) subStart(text string, now time.Time) int {
	return t.add(thinkStep{name: agent.ToolDelegate, text: text, since: now, active: true, agent: true}).id
}

func (t *thinkLog) end(sub int, name string, failed bool, now time.Time) {
	for i := range t.steps {
		if s := &t.steps[i]; s.active && !s.agent && s.sub == sub && s.name == name {
			s.active, s.failed, s.took, s.detail = false, failed, now.Sub(s.since), ""
			return
		}
	}
}

func (t *thinkLog) subEnd(id int, failed bool, now time.Time) {
	for i := range t.steps {
		if s := &t.steps[i]; s.id == id && s.agent {
			s.active, s.failed, s.took, s.detail = false, failed, now.Sub(s.since), ""
			return
		}
	}
}

func (t *thinkLog) progress(sub int, name, text string) {
	for i := len(t.steps) - 1; i >= 0; i-- {
		if s := &t.steps[i]; s.active && !s.agent && s.sub == sub && s.name == name {
			s.detail = text
			return
		}
	}
}

func (t *thinkLog) busy() bool {
	for _, s := range t.steps {
		if s.active {
			return true
		}
	}
	return false
}

func (t *thinkLog) current() (thinkStep, bool) {
	for i := len(t.steps) - 1; i >= 0; i-- {
		if t.steps[i].active && !t.steps[i].agent {
			return t.steps[i], true
		}
	}
	return thinkStep{}, false
}

func (t *thinkLog) markdown(l lang, now, started time.Time, plan string) string {
	var sb strings.Builder
	head := thinkingStatus(l)
	if cur, ok := t.current(); ok {
		if st := statusFor(l, cur.name); st != "" {
			head = st
		}
	}
	sb.WriteString(escapeMarkdown(head))
	if total := now.Sub(started); total >= time.Second {
		fmt.Fprintf(&sb, " `%d:%02d`", int(total.Minutes()), int(total.Seconds())%60)
	}
	if plan != "" {
		sb.WriteString("\n\n" + plan)
	}
	steps := t.steps
	if len(steps) == 0 {
		return sb.String()
	}
	if len(steps) > thinkShown {
		sb.WriteString("\n\n" + escapeMarkdown(l.f("و %s کار قبلی…", l.num(len(steps)-thinkShown))))
		steps = steps[len(steps)-thinkShown:]
	}
	sb.WriteString("\n")
	for _, s := range steps {
		sb.WriteString("\n" + t.line(l, s, now))
	}
	return sb.String()
}

func (t *thinkLog) line(l lang, s thinkStep, now time.Time) string {
	icon := statusIcon(s.name)
	if s.agent {
		icon = "🤖"
	}
	text := escapeMarkdown(s.text)
	switch {
	case s.active:
		out := "- " + icon + " **" + text + "** ⏳"
		if d := now.Sub(s.since); d >= 3*time.Second {
			out += fmt.Sprintf(" `%ds`", int(d.Seconds()))
		}
		if s.detail != "" {
			out += " · " + escapeMarkdown(textutil.TruncateRunes(s.detail, 40, "…"))
		}
		return out
	case s.failed:
		return "- " + icon + " " + text + " ❌"
	}
	out := "- " + icon + " " + text + " ✅"
	if s.took >= 1500*time.Millisecond {
		out += fmt.Sprintf(" `%ds`", int(s.took.Round(time.Second).Seconds()))
	}
	return out
}

type thinkKind struct {
	about string
	plain string
	keys  []string
	link  bool
}

var thinkKinds = map[string]thinkKind{
	"web_search":                {"جستجو «%s»", "جستجو توی اینترنت", []string{"query", "queries", "q"}, false},
	"web_fetch":                 {"خوندن %s", "خوندن یه صفحه", []string{"url"}, true},
	"deep_research":             {"تحقیق عمیق «%s»", "تحقیق عمیق", []string{"topic", "query"}, false},
	"search_music":              {"پیدا کردن آهنگ «%s»", "پیدا کردن آهنگ", []string{"query", "song"}, false},
	"download_file":             {"دانلود از %s", "دانلود فایل", []string{"url"}, true},
	tools.ToolDownloadMedia:     {"دانلود از %s", "دانلود از شبکه‌های اجتماعی", []string{"url"}, true},
	"generate_image":            {"ساخت عکس «%s»", "ساخت عکس", []string{"prompt"}, false},
	tools.ToolEditImage:         {"ویرایش عکس «%s»", "ویرایش عکس", []string{"prompt"}, false},
	"search_image":              {"پیدا کردن عکس «%s»", "پیدا کردن عکس", []string{"query"}, false},
	"text_to_speech":            {"", "ساخت صدا", nil, false},
	"generate_chart":            {"کشیدن نمودار «%s»", "کشیدن نمودار", []string{"title"}, false},
	"generate_qr":               {"", "ساخت QR", nil, false},
	"crypto_currency_price":     {"", "چک کردن قیمت‌ها", nil, false},
	tools.ToolMarketPrice:       {"", "چک کردن قیمت‌ها", nil, false},
	"whois_lookup":              {"بررسی دامنه %s", "بررسی یه دامنه", []string{"domain"}, false},
	"github_inspect":            {"نگاه به ریپوی %s", "نگاه به یه ریپو", []string{"repo", "url"}, false},
	"media_convert":             {"", "تبدیل فایل", nil, false},
	tools.ToolSplitVideo:        {"", "چندتکه کردن ویدیو", nil, false},
	tools.ToolBrowser:           {"کار توی مرورگر: %s", "کار توی مرورگر", []string{"url"}, true},
	tools.ToolBrowserAgent:      {"ایجنت مرورگر: «%s»", "ایجنت مرورگر", []string{"task"}, false},
	tools.ToolMotion:            {"", "رندر موشن‌گرافیک", nil, false},
	tools.ToolMusic:             {"", "ساخت آهنگ", nil, false},
	tools.ToolSong:              {"ساخت آهنگ «%s»", "ساخت آهنگ", []string{"title"}, false},
	tools.ToolDocumentReader:    {"خوندن سند «%s»", "خوندن سند", []string{"filename", "file"}, true},
	tools.ToolRunCode:           {"اجرای کد %s", "اجرای کد", []string{"language"}, false},
	"bash_execute":              {"", "اجرای دستور", nil, false},
	"pdf_tool":                  {"کار روی PDF (%s)", "کار روی PDF", []string{"action"}, false},
	tools.ToolQueryData:         {"", "کار روی داده‌ها", nil, false},
	tools.ToolCreateSpreadsheet: {"", "کار روی داده‌ها", nil, false},
	"write_file":                {"نوشتن «%s»", "نوشتن فایل", []string{"filename", "path"}, true},
	tools.ToolEditFile:          {"نوشتن «%s»", "نوشتن فایل", []string{"filename", "path"}, true},
	tools.ToolApplyPatch:        {"نوشتن «%s»", "نوشتن فایل", []string{"filename", "path"}, true},
	"read_file":                 {"خوندن «%s»", "نگاه به فایل‌ها", []string{"filename", "path"}, true},
	"list_files":                {"خوندن «%s»", "نگاه به فایل‌ها", []string{"filename", "path"}, true},
	toolSendFile:                {"", "آماده کردن فایل برای ارسال", nil, false},
	toolAskUser:                 {"", "پرسیدن یه سؤال", nil, false},
}

func thinkText(l lang, name, argsJSON string) string {
	kind, ok := thinkKinds[name]
	if !ok {
		return toolLabel(name)
	}
	if kind.about != "" {
		var a map[string]any
		if json.Unmarshal([]byte(argsJSON), &a) == nil {
			for _, k := range kind.keys {
				s := firstText(a[k])
				if kind.link {
					s = addressAbout(s)
				}
				if s = cleanAbout(s); s != "" {
					return l.f(kind.about, s)
				}
			}
		}
	}
	return l.t(kind.plain)
}

func firstText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		if len(x) > 0 {
			if s, ok := x[0].(string); ok {
				return s
			}
		}
	}
	return ""
}

func cleanAbout(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.NewReplacer("`", "'", "«", "", "»", "").Replace(s)
	return textutil.TruncateRunes(strings.TrimSpace(s), thinkWidth, "…")
}

func addressAbout(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		if strings.ContainsAny(raw, "/\\") {
			return path.Base(strings.ReplaceAll(raw, "\\", "/"))
		}
		return raw
	}
	host := strings.TrimPrefix(u.Hostname(), "www.")
	p := strings.TrimSuffix(u.Path, "/")
	if p == "" {
		return host
	}
	return textutil.TruncateRunes(host+p, thinkWidth, "…")
}
