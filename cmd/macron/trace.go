package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

const (
	traceTitle    = "💭 MACRON ⟫ live"
	traceBgTitle  = "🔄 MACRON ⟫ background · "
	traceRule     = "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
	traceRunning  = "◆ "
	traceDone     = "✓ "
	traceFailed   = "✗ "
	traceEvent    = "» "
	traceSpinner  = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
	traceMaxLines = 24
)

var toolLabels = map[string]string{
	toolReact:                   "",
	toolEffect:                  "",
	"web_search":                "Search",
	"web_fetch":                 "Read Page",
	"deep_research":             "Deep Research",
	"search_music":              "Music Search",
	"download_file":             "Download",
	tools.ToolDownloadMedia:     "Download",
	tools.ToolSplitVideo:        "Split Video",
	tools.ToolBrowser:           "Browser",
	tools.ToolBrowserAgent:      "Browser Agent",
	tools.ToolMotion:            "Motion Graphic",
	tools.ToolMusic:             "Music Synth",
	tools.ToolSong:              "Song",
	"generate_image":            "Generate Image",
	tools.ToolEditImage:         "Edit Image",
	"search_image":              "Image Search",
	"generate_chart":            "Chart",
	"generate_qr":               "QR Code",
	"text_to_speech":            "Text to Speech",
	"pdf_tool":                  "PDF",
	"media_convert":             "Convert",
	"crypto_currency_price":     "Crypto Prices",
	tools.ToolMarketPrice:       "Gold & Currency",
	toolGroupAdmin:              "Group Admin",
	"whois_lookup":              "Whois",
	"github_inspect":            "GitHub",
	"bash_execute":              "Terminal",
	tools.ToolRunCode:           "Run Code",
	tools.ToolDocumentReader:    "Document Reader",
	tools.ToolQueryData:         "Data Query",
	tools.ToolCreateSpreadsheet: "Spreadsheet",
	"write_file":                "Write File",
	tools.ToolEditFile:          "Edit File",
	tools.ToolApplyPatch:        "Apply Patch",
	tools.ToolShellJobs:         "Background Jobs",
	tools.ToolProjectTree:       "Project Map",
	tools.ToolSearchCode:        "Search Code",
	tools.ToolFindFiles:         "Find Files",
	tools.ToolCodeOutline:       "Code Outline",
	tools.ToolExtract:           "Extract",
	tools.ToolImportRepo:        "Import Repo",
	tools.ToolPackFiles:         "Zip",
	"read_file":                 "Read File",
	"list_files":                "Files",
	toolViewImage:               "View Image",
	toolProfilePhoto:            "Profile Photo",
	toolSendFile:                "Attach File",
	toolAskUser:                 "Ask",
	toolSchedule:                "Schedule",
	toolListSchedules:           "Schedules",
	toolCancelSchedule:          "Cancel Schedule",
	toolGroupHistory:            "Group History",
	toolRemember:                "Remember",
	toolForget:                  "Forget",
	toolDice:                    "Dice",
	toolLocation:                "Location",
	toolCreatePoll:              "Poll",
	toolChatInfo:                "Chat Info",
	toolLookup:                  "Lookup",
	tools.ToolListSkills:        "Skills",
	tools.ToolReadSkill:         "Skill",
	tools.ToolSaveSkill:         "Learn Skill",
	agent.ToolDelegate:          "",
	agent.ToolPlan:              "",
	agent.ToolMoreTools:         "",
}

func toolLabel(name string) string {
	if l, ok := toolLabels[name]; ok {
		return l
	}
	words := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' || r == '.' })
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

type traceStep struct {
	id     int
	parent int
	label  string
	count  int
	active int
	failed bool
	event  bool
	mark   string
	sub    bool
	calls  int
	since  time.Time
	detail string
	output string
}

type trace struct {
	steps     []traceStep
	lastID    int
	calls     int
	ctxUsed   int
	ctxWindow int
	note      string
	title     string
	plan      []agent.PlanItem
}

func (t *trace) add(s traceStep) *traceStep {
	t.lastID++
	s.id = t.lastID
	t.steps = append(t.steps, s)
	if len(t.steps) > 4*traceMaxLines {
		t.prune()
	}
	return &t.steps[len(t.steps)-1]
}

func (t *trace) prune() {
	drop := len(t.steps) - 2*traceMaxLines
	kept := t.steps[:0]
	for _, s := range t.steps {
		if drop > 0 && s.active == 0 {
			drop--
			continue
		}
		kept = append(kept, s)
	}
	t.steps = kept
}

func (t *trace) byID(id int) *traceStep {
	for i := range t.steps {
		if t.steps[i].id == id {
			return &t.steps[i]
		}
	}
	return nil
}

func (t *trace) start(parent int, name string, now time.Time) bool {
	label := toolLabel(name)
	if label == "" {
		return false
	}
	t.calls++
	if p := t.byID(parent); p != nil {
		p.calls++
	}
	for i := len(t.steps) - 1; i >= 0; i-- {
		s := &t.steps[i]
		if s.parent != parent {
			continue
		}
		if s.label == label && !s.failed && !s.event && !s.sub {
			if s.active == 0 {
				s.since = now
			}
			s.count++
			s.active++
			return true
		}
		break
	}
	t.add(traceStep{parent: parent, label: label, count: 1, active: 1, since: now})
	return true
}

func (t *trace) subStart(title string, now time.Time) int {
	return t.add(traceStep{label: "Sub-agent: " + title, sub: true, count: 1, active: 1, since: now}).id
}

func (t *trace) subEnd(id int, failed bool) {
	if s := t.byID(id); s != nil {
		s.active, s.failed = 0, failed
	}
}

func (t *trace) event(label string, now time.Time) { t.eventMarked("", label, now) }

func (t *trace) eventMarked(mark, label string, now time.Time) {
	if n := len(t.steps); n > 0 && t.steps[n-1].label == label && t.steps[n-1].event {
		t.steps[n-1].count++
		return
	}
	t.add(traceStep{label: label, mark: mark, count: 1, event: true, since: now})
}

func (t *trace) compacted(before, after int, summarized bool, now time.Time) {
	what := "Context compacted"
	if !summarized {
		what = "Old outputs trimmed"
	}
	cut := 0
	if before > 0 {
		cut = (before - after) * 100 / before
	}
	t.eventMarked("🗜️ ", fmt.Sprintf("%s: %s ➔ %s (-%d%%)", what, agent.HumanTokens(before), agent.HumanTokens(after), cut), now)
	t.ctxUsed = after
}

func (t *trace) running(parent int, name string) *traceStep {
	label := toolLabel(name)
	for i := len(t.steps) - 1; i >= 0; i-- {
		if s := &t.steps[i]; s.parent == parent && s.label == label && s.active > 0 && !s.event && !s.sub {
			return s
		}
	}
	return nil
}

func (t *trace) progress(parent int, name, text string, output bool) {
	text = strings.ReplaceAll(text, "`", "'")
	if s := t.running(parent, name); s != nil {
		if output {
			s.output = text
		} else {
			s.detail = text
		}
	}
}

func (t *trace) end(parent int, name string, failed bool) {
	if s := t.running(parent, name); s != nil {
		s.active--
		s.failed = s.failed || failed
		if s.active == 0 {
			s.detail, s.output = "", ""
		}
	}
}

func (t *trace) busy() bool {
	for _, s := range t.steps {
		if s.active > 0 {
			return true
		}
	}
	return false
}

const traceBranch = 5

func (t *trace) card(now, started time.Time, animate bool) (title, body string) {
	frames := []rune(traceSpinner)
	total := now.Sub(started)
	spin := string(frames[int(total/(250*time.Millisecond))%len(frames)])
	var head strings.Builder
	if t.title != "" {
		head.WriteString(traceBgTitle + textutil.TruncateRunes(t.title, 40, "…"))
	} else {
		head.WriteString(traceTitle)
	}
	if animate && !t.busy() {
		head.WriteString(" " + spin)
	}
	if t.calls > 0 {
		fmt.Fprintf(&head, " · step %d", t.calls)
	}
	if total >= time.Second {
		fmt.Fprintf(&head, " · %d:%02d", int(total.Minutes()), int(total.Seconds())%60)
	}
	var sb strings.Builder
	sb.WriteString(traceRule)
	if t.ctxUsed > 0 && t.ctxWindow > 0 {
		fmt.Fprintf(&sb, "\nctx %s %s/%s", agent.ContextBar(t.ctxUsed, t.ctxWindow), agent.HumanTokens(t.ctxUsed), agent.HumanTokens(t.ctxWindow))
	}
	var lines []string
	for _, s := range t.steps {
		if s.parent != 0 {
			continue
		}
		lines = append(lines, t.line(s, "", "  ", now, spin, animate)...)
		if !s.sub {
			continue
		}
		var kids []traceStep
		for _, c := range t.steps {
			if c.parent == s.id {
				kids = append(kids, c)
			}
		}
		if len(kids) > traceBranch {
			lines = append(lines, fmt.Sprintf("  ├─ … +%d", len(kids)-(traceBranch-1)))
			kids = kids[len(kids)-(traceBranch-1):]
		}
		for i, c := range kids {
			first, rest := "  ├─ ", "  │    "
			if i == len(kids)-1 {
				first, rest = "  └─ ", "       "
			}
			lines = append(lines, t.line(c, first, rest, now, spin, animate)...)
		}
	}
	if len(lines) > traceMaxLines {
		sb.WriteString("\n…")
		lines = lines[len(lines)-(traceMaxLines-1):]
	}
	for _, l := range lines {
		sb.WriteString("\n" + l)
	}
	return head.String(), sb.String()
}

func (t *trace) markdown(now, started time.Time, animate bool) string {
	title, body := t.card(now, started, animate)
	if t.note != "" {
		title += "\n" + t.note
	}
	if p := planMarkdown(t.plan); p != "" {
		title += "\n\n" + p + "\n"
	}
	return title + "\n```bash\n" + body + "\n```"
}

func (t *trace) html(now, started time.Time, animate bool) string {
	title, body := t.card(now, started, animate)
	if t.note != "" {
		title += "\n" + t.note
	}
	head := telegram.EscapeHTML(title)
	if p := planHTML(t.plan); p != "" {
		head += "\n\n" + p
	}
	return head + "\n<pre><code class=\"language-bash\">" + telegram.EscapeHTML(body) + "</code></pre>"
}

func (t *trace) line(s traceStep, first, rest string, now time.Time, spin string, animate bool) []string {
	line := traceDone
	switch {
	case s.event && s.mark != "":
		line = s.mark
	case s.event:
		line = traceEvent
	case s.active > 0:
		line = traceRunning
	case s.failed:
		line = traceFailed
	}
	line += s.label
	if s.count > 1 {
		line += fmt.Sprintf(" ×%d", s.count)
	}
	if s.sub && s.active == 0 {
		line += fmt.Sprintf(" (%d %s)", s.calls, map[bool]string{true: "step", false: "steps"}[s.calls == 1])
	}
	if s.active > 0 {
		switch {
		case !animate:
			line += " …"
		case now.Sub(s.since) >= 3*time.Second:
			line += fmt.Sprintf(" %s %ds", spin, int(now.Sub(s.since).Seconds()))
		default:
			line += " " + spin
		}
		if s.detail != "" {
			line += " · " + s.detail
		}
	}
	out := []string{first + textutil.TruncateRunes(line, 60-utf8.RuneCountInString(first), "…")}
	if s.active > 0 && s.output != "" {
		out = append(out, rest+"↳ "+textutil.TruncateRunes(s.output, 52-utf8.RuneCountInString(rest), "…"))
	}
	return out
}

var exitFailRe = regexp.MustCompile(`(?m)^\[(?:exit -?[1-9]\d*|timed out)`)

func failedResult(r string) bool {
	l := strings.ToLower(firstLine(r))
	for _, p := range []string{"error", "tool error", "search error", "fetch error", "music search error", "download failed", "write error",
		"read error", "list error", "create ", "no results", "no direct music"} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	if strings.HasPrefix(r, tools.TranscriptPrefix) {
		return exitFailRe.MatchString(r)
	}
	return strings.Contains(l, " failed (exit ")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func planMarkdown(items []agent.PlanItem) string {
	return planTasks(items, "📋", "⏳", "`", func(s string) string { return "**" + planText(s) + "**" })
}

func planTasks(items []agent.PlanItem, icon, running, tick string, bold func(string) string) string {
	if len(items) == 0 {
		return ""
	}
	done := 0
	var lines []string
	for _, it := range items {
		switch it.Status {
		case agent.PlanDone:
			done++
			lines = append(lines, "- [x] "+planText(it.Text))
		case agent.PlanInProgress:
			lines = append(lines, "- [ ] "+running+" "+bold(it.Text))
		default:
			lines = append(lines, "- [ ] "+planText(it.Text))
		}
	}
	return fmt.Sprintf("%s %s%d/%d%s\n\n", icon, tick, done, len(items), tick) + strings.Join(lines, "\n")
}

var planLead = regexp.MustCompile(`^(\d{1,9})([.)])|^([-+])`)

func planText(s string) string {
	return planLead.ReplaceAllString(escapeMarkdown(strings.TrimSpace(s)), `$1\$2$3`)
}

func planHTML(items []agent.PlanItem) string {
	if len(items) == 0 {
		return ""
	}
	done := 0
	var lines []string
	for _, it := range items {
		text := telegram.EscapeHTML(it.Text)
		switch it.Status {
		case agent.PlanDone:
			done++
			lines = append(lines, "✅ "+text)
		case agent.PlanInProgress:
			lines = append(lines, "⏳ <b>"+text+"</b>")
		default:
			lines = append(lines, "▫️ "+text)
		}
	}
	return fmt.Sprintf("📋 %d/%d\n", done, len(items)) + strings.Join(lines, "\n")
}

var markdownSpecial = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`, "~", `\~`, "|", `\|`,
	"=", `\=`, "#", `\#`, "<", `\<`, ">", `\>`)

func escapeMarkdown(s string) string { return markdownSpecial.Replace(s) }

func (t *trace) planMD() string { return planMarkdown(t.plan) }
