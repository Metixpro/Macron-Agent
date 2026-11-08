package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/emoji"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// The console: the live terminal drawn as ordinary lines instead of a code
// block, so every icon in it can be a premium emoji (Telegram shows none
// inside code). Each line starts with its tool's icon, the state follows the
// name, and the numbers sit in small code chips:
//
//	💭 **MACRON** ⟫ live `step 6` `0:21`
//
//	📋 `2/4`
//	✅ گرفتن قیمت طلا
//	⏳ **کشیدن نمودار**
//	▫️ فرستادن نتیجه
//
//	🧠 `ctx` 🟩🟩🟩⬛⬛⬛⬛⬛ `41% · 105k/256k`
//	🔎 Search `×2` ✅
//	📊 **Chart** ⏳ `6s`
//	 ↳ `rendering 2 series`
//	🤖 **Sub-agent: Excel report** ⏳ `9s`
//	├ 📗 Spreadsheet ✅
//	└ 🧪 **Run Code** ⏳ `45% · 3/7`
//	🗜️ Context compacted `160k ➔ 80k (-50%)`
//
// Every icon is an ordinary emoji followed by look-alikes: the first one with
// a premium emoji (🎨 in the panel, or a whole pack imported from its
// t.me/addemoji link) is used, else the first. Once the job is answered, its
// steps fold into a «🧰 work log» above the answer.

// The trace styles (store.SettingTraceStyle): the console, or the classic code block.
const traceStyleCode = "code"

// workLogAfter: a quicker job needs no work log above its answer.
const workLogAfter = 10 * time.Second

var (
	iconThinking   = []string{"💭"}
	iconBackground = []string{"🔄"}
	iconRunning    = []string{"⏳", "⌛"}
	iconDone       = []string{"✅", "✔️", "☑️"}
	iconFailed     = []string{"❌", "✖️", "⛔"}
	iconPlan       = []string{"📋", "🗒️"}
	iconTodo       = []string{"▫️", "◻️", "⚪"}
	iconSubAgent   = []string{"🤖", "👾"}
	iconEvent      = []string{"💬", "✉️"}
	iconCompact    = []string{"🗜️", "📉"}
	iconContext    = []string{"🧠"}
	iconWorkLog    = []string{"🧰", "🛠️"}
	iconTool       = []string{"🔌", "🧩"} // a tool without an icon of its own (plugins, MCP)
	iconCellLow    = []string{"🟩"}
	iconCellMid    = []string{"🟨"}
	iconCellHigh   = []string{"🟥"}
	iconCellEmpty  = []string{"⬛", "⬜"}
)

// toolIcons are the tools' icons, by their label (toolLabels).
var toolIcons = map[string][]string{
	"Search":               {"🔎", "🔍"},
	"Read Page":            {"🌐", "📰"},
	"Deep Research":        {"🔬", "🧠"},
	"Music Search":         {"🎵", "🎶"},
	"Download":             {"📥", "⬇️"},
	"Split Video":          {"✂️", "🎞️"},
	"Browser":              {"🧭", "🖥️"},
	"Motion Graphic":       {"🎬", "🎞️"},
	"Music Synth":          {"🎹", "🎼"},
	"Song":                 {"🎤", "🎶"},
	"Generate Image":       {"🎨", "🖼️"},
	"Edit Image":           {"🖌️", "✏️"},
	"Image Search":         {"🖼️", "🏞️"},
	"Chart":                {"📊", "📈"},
	"QR Code":              {"🔳", "▪️"},
	"Text to Speech":       {"🗣️", "🔊"},
	"PDF":                  {"📄", "📃"},
	"Convert":              {"🔁", "🔄"},
	"Crypto Prices":        {"🪙", "💹"},
	"Gold & Currency":      {"💰", "💵"},
	"Group Admin":          {"🛡️", "👮"},
	"Whois":                {"🌍", "🌐"},
	"GitHub":               {"🐙", "🐱"},
	"Terminal":             {"💻", "⌨️"},
	"Run Code":             {"🧪", "⚙️"},
	"Install Package":      {"📦", "🧰"},
	"Document Reader":      {"📑", "📄"},
	"Data Query":           {"🗃️", "🗄️"},
	"Spreadsheet":          {"📗", "📊"},
	"Write File":           {"✍️", "📝"},
	"Edit File":            {"📝", "✏️"},
	"Apply Patch":          {"🩹", "📝"},
	"Background Jobs":      {"🛰️", "🔄"},
	"Servers":              {"🖥️", "🗄️"},
	"Server Command":       {"🖥️", "⌨️"},
	"Upload to Server":     {"📤", "⬆️"},
	"Download from Server": {"📥", "⬇️"},
	"Read on Server":       {"📖", "👀"},
	"Edit on Server":       {"✏️", "📝"},
	"Project Map":          {"🗺️", "🧭"},
	"Search Code":          {"🔍", "🔎"},
	"Find Files":           {"🗂️", "📂"},
	"Code Outline":         {"🧩", "📐"},
	"Extract":              {"📂", "🗃️"},
	"Import Repo":          {"🧲", "📥"},
	"Zip":                  {"🧳", "📦"},
	"Read File":            {"📖", "📄"},
	"Files":                {"📁", "🗂️"},
	"View Image":           {"👁️", "👀"},
	"Profile Photo":        {"👤", "🖼️"},
	"Attach File":          {"📎", "📤"},
	"Ask":                  {"❓", "💬"},
	"Schedule":             {"⏰", "🗓️"},
	"Schedules":            {"📅", "🗓️"},
	"Cancel Schedule":      {"🚫", "❌"},
	"Group History":        {"📜", "🗒️"},
	"Remember":             {"📌", "🧷"},
	"Forget":               {"🧹", "🗑️"},
	"Dice":                 {"🎲"},
	"Location":             {"📍", "🗺️"},
	"Poll":                 {"🗳️", "📊"},
	"Chat Info":            {"ℹ️", "🪪"},
	"Lookup":               {"🔦", "🔎"},
	"Plans":                {"💎", "💳"},
	"Skills":               {"📚"},
	"Skill":                {"📘", "📖"},
	"Learn Skill":          {"🎓", "💡"},
}

func toolIcon(label string) []string {
	if c, ok := toolIcons[label]; ok {
		return c
	}
	return iconTool
}

// consoleIcon is one icon of the console, for the panel's list.
type consoleIcon struct {
	name  string
	cands []string
}

// consoleIconList is every icon the console uses.
func consoleIconList() []consoleIcon {
	list := []consoleIcon{{"MACRON", iconThinking}, {"Background", iconBackground}, {"Running", iconRunning}, {"Done", iconDone},
		{"Failed", iconFailed}, {"Plan", iconPlan}, {"To do", iconTodo}, {"Sub-agent", iconSubAgent}, {"New message", iconEvent},
		{"Compaction", iconCompact}, {"Context", iconContext}, {"Work log", iconWorkLog}, {"Other tools", iconTool},
		{"Context 0-60%", iconCellLow}, {"Context 60-85%", iconCellMid}, {"Context 85%+", iconCellHigh}, {"Context free", iconCellEmpty}}
	seen := map[string]bool{}
	var labels []string
	for _, label := range toolLabels {
		if label != "" && !seen[label] {
			seen[label] = true
			labels = append(labels, label)
		}
	}
	sort.Strings(labels) // the panel lists them in one order
	for _, label := range labels {
		list = append(list, consoleIcon{label, toolIcon(label)})
	}
	return list
}

// iconPicker chooses an icon among its look-alikes: the first one that has
// a premium emoji, else the first.
type iconPicker struct{ set *emoji.Set }

func (b *Bot) iconPicker() iconPicker { return iconPicker{b.premium()} }

func (p iconPicker) pick(cands []string) string {
	if p.set != nil {
		for _, c := range cands {
			if _, ok := p.set.ID(c); ok {
				return c
			}
		}
	}
	return cands[0]
}

// premium reports whether one of the look-alikes has a premium emoji.
func (p iconPicker) premium(cands []string) bool {
	if p.set != nil {
		for _, c := range cands {
			if _, ok := p.set.ID(c); ok {
				return true
			}
		}
	}
	return false
}

// consoleStyle reports whether the live terminal is drawn as the console.
func (b *Bot) consoleStyle() bool {
	v, _ := b.st.Setting(store.SettingTraceStyle)
	return v != traceStyleCode
}

// consoleFmt writes the console's lines in one of the two formats Telegram
// takes: rich Markdown (drafts, live messages) or HTML (classic messages).
type consoleFmt struct {
	bold, code, esc func(string) string
	md              bool // rich Markdown: the plan is a task list with real checkboxes
}

var (
	mdConsole = consoleFmt{
		bold: func(s string) string { return "**" + escapeMarkdown(s) + "**" },
		code: func(s string) string { return "`" + strings.ReplaceAll(s, "`", "'") + "`" },
		esc:  escapeMarkdown,
		md:   true,
	}
	htmlConsole = consoleFmt{
		bold: func(s string) string { return "<b>" + telegram.EscapeHTML(s) + "</b>" },
		code: func(s string) string { return "<code>" + telegram.EscapeHTML(s) + "</code>" },
		esc:  telegram.EscapeHTML,
	}
)

// indent keeps its place at a line's start (Markdown drops leading spaces).
const indent = " "

// consoleView draws the card (see the top of this file). animate adds the
// running times, and spinner frames where no premium emoji moves by itself.
func (t *trace) consoleView(now, started time.Time, animate bool, f consoleFmt) string {
	ic := t.icons
	frames := []rune(traceSpinner)
	total := now.Sub(started)
	frame := string(frames[int(total/(250*time.Millisecond))%len(frames)])
	var head strings.Builder
	if t.title != "" {
		head.WriteString(ic.pick(iconBackground) + " " + f.bold("MACRON") + " ⟫ " + f.esc(textutil.TruncateRunes(t.title, 40, "…")))
	} else {
		head.WriteString(ic.pick(iconThinking) + " " + f.bold("MACRON") + " ⟫ live")
	}
	if t.calls > 0 {
		head.WriteString(" " + f.code(fmt.Sprintf("step %d", t.calls)))
	}
	clock := ""
	if total >= time.Second {
		clock = fmt.Sprintf("%d:%02d", int(total.Minutes()), int(total.Seconds())%60)
	}
	if animate && !t.busy() && !ic.premium(iconThinking) {
		clock = strings.TrimSpace(frame + " " + clock)
	}
	if clock != "" {
		head.WriteString(" " + f.code(clock))
	}
	if t.note != "" {
		head.WriteString("\n" + f.esc(t.note))
	}
	sections := []string{head.String()}
	if p := t.consolePlan(f); p != "" {
		sections = append(sections, p)
	}
	spin := ""
	if animate && !ic.premium(iconRunning) {
		spin = frame
	}
	var body []string
	if t.ctxUsed > 0 && t.ctxWindow > 0 {
		body = append(body, t.contextLine(f))
	}
	body = append(body, t.consoleSteps(now, spin, animate, f)...)
	if len(body) > 0 {
		sections = append(sections, strings.Join(body, "\n"))
	}
	return strings.Join(sections, "\n\n")
}

// consoleSteps draws the steps, a sub-agent's calls under it.
func (t *trace) consoleSteps(now time.Time, spin string, animate bool, f consoleFmt) []string {
	var lines []string
	for _, s := range t.steps {
		if s.parent != 0 {
			continue
		}
		lines = append(lines, t.consoleLine(s, "", indent, now, spin, animate, f)...)
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
			lines = append(lines, fmt.Sprintf("├ … +%d", len(kids)-(traceBranch-1)))
			kids = kids[len(kids)-(traceBranch-1):]
		}
		for i, c := range kids {
			first, rest := "├ ", "│"+indent
			if i == len(kids)-1 {
				first, rest = "└ ", indent+indent
			}
			lines = append(lines, t.consoleLine(c, first, rest, now, spin, animate, f)...)
		}
	}
	if len(lines) > traceMaxLines {
		lines = append([]string{"…"}, lines[len(lines)-(traceMaxLines-1):]...)
	}
	return lines
}

// consoleLine draws one step: its icon, name and state, then its numbers in
// a chip, and under it the last line its program printed.
func (t *trace) consoleLine(s traceStep, first, rest string, now time.Time, spin string, animate bool, f consoleFmt) []string {
	ic := t.icons
	name, chip := s.label, ""
	icon := toolIcon(s.label)
	switch {
	case s.event:
		icon = iconEvent
		if mark := strings.TrimSpace(s.mark); mark != "" {
			icon = []string{mark}
			if emoji.Key(mark) == emoji.Key(iconCompact[0]) {
				icon = iconCompact
			}
		}
		if i := strings.Index(name, ": "); i > 0 { // «Context compacted: 160k ➔ 80k (-50%)»
			name, chip = name[:i], name[i+2:]
		}
	case s.sub:
		icon = iconSubAgent
	}
	name = textutil.TruncateRunes(name, 44, "…")
	text := f.esc(name)
	if s.active > 0 {
		text = f.bold(name)
	}
	line := first + ic.pick(icon) + " " + text
	if s.count > 1 {
		line += " " + f.code(fmt.Sprintf("×%d", s.count))
	}
	switch {
	case s.event:
	case s.active > 0:
		line += " " + ic.pick(iconRunning)
	case s.failed:
		line += " " + ic.pick(iconFailed)
	default:
		line += " " + ic.pick(iconDone)
	}
	var meta []string
	if s.active > 0 && animate {
		took := ""
		if d := now.Sub(s.since); d >= 3*time.Second {
			took = fmt.Sprintf("%ds", int(d.Seconds()))
		}
		if took = strings.TrimSpace(spin + " " + took); took != "" {
			meta = append(meta, took)
		}
	}
	if s.active > 0 && s.detail != "" {
		meta = append(meta, s.detail)
	}
	if s.sub && s.active == 0 {
		meta = append(meta, fmt.Sprintf("%d %s", s.calls, map[bool]string{true: "step", false: "steps"}[s.calls == 1]))
	}
	if chip != "" {
		meta = append(meta, chip)
	}
	if len(meta) > 0 {
		line += " " + f.code(textutil.TruncateRunes(strings.Join(meta, " · "), 48, "…"))
	}
	out := []string{line}
	if s.active > 0 && s.output != "" {
		out = append(out, rest+"↳ "+f.code(textutil.TruncateRunes(s.output, 48, "…")))
	}
	return out
}

// contextLine is the context meter: eight cells, green, then yellow, then red.
func (t *trace) contextLine(f consoleFmt) string {
	ic := t.icons
	pct := min(t.ctxUsed*100/t.ctxWindow, 100)
	full := (pct*8 + 50) / 100
	if full == 0 && t.ctxUsed > 0 {
		full = 1
	}
	var bar strings.Builder
	for i := range 8 {
		switch {
		case i >= full:
			bar.WriteString(ic.pick(iconCellEmpty))
		case i < 5:
			bar.WriteString(ic.pick(iconCellLow))
		case i < 7:
			bar.WriteString(ic.pick(iconCellMid))
		default:
			bar.WriteString(ic.pick(iconCellHigh))
		}
	}
	return ic.pick(iconContext) + " " + f.code("ctx") + " " + bar.String() + " " +
		f.code(fmt.Sprintf("%d%% · %s/%s", pct, agent.HumanTokens(t.ctxUsed), agent.HumanTokens(t.ctxWindow)))
}

// consolePlan is the job's plan as console lines ("" without a plan).
func (t *trace) consolePlan(f consoleFmt) string {
	if len(t.plan) == 0 {
		return ""
	}
	ic := t.icons
	if f.md {
		return planTasks(t.plan, ic.pick(iconPlan), ic.pick(iconRunning), "`", f.bold)
	}
	done := 0
	var lines []string
	for _, it := range t.plan {
		switch it.Status {
		case agent.PlanDone:
			done++
			lines = append(lines, ic.pick(iconDone)+" "+f.esc(it.Text))
		case agent.PlanInProgress:
			lines = append(lines, ic.pick(iconRunning)+" "+f.bold(it.Text))
		default:
			lines = append(lines, ic.pick(iconTodo)+" "+f.esc(it.Text))
		}
	}
	return ic.pick(iconPlan) + " " + f.code(fmt.Sprintf("%d/%d", done, len(t.plan))) + "\n" + strings.Join(lines, "\n")
}

// planMD is the plan in the card's own style, as Markdown.
func (t *trace) planMD() string {
	if t.console {
		return t.consolePlan(mdConsole)
	}
	return planMarkdown(t.plan)
}

// workLog is the job's steps folded above its answer, «🧰 Work log · 9 tools
// · 0:42» ("" for a quick job or one of fewer than two tool calls).
func (t *trace) workLog(l lang, took time.Duration) string {
	if t.calls < 2 || took < workLogAfter {
		return ""
	}
	lines := t.consoleSteps(time.Time{}, "", false, mdConsole)
	if len(lines) == 0 {
		return ""
	}
	summary := t.icons.pick(iconWorkLog) + " " + l.f("گزارش کار · %s ابزار · %s", l.num(t.calls), clockOf(l, took))
	return "<details><summary>**" + escapeMarkdown(summary) + "**</summary>\n\n" + strings.Join(lines, "\n") + "\n\n</details>"
}

// clockOf writes a duration as m:ss, in the language's digits.
func clockOf(l lang, d time.Duration) string {
	s := int(d.Seconds()) % 60
	sec := l.num(s)
	if s < 10 {
		sec = l.num(0) + sec
	}
	return l.num(int(d.Minutes())) + ":" + sec
}

// workLog is the job's work log for the head of its answer ("" when off or short).
func (l *liveView) workLog() string {
	if !l.b.st.SettingBool(store.SettingWorkLog, true) {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tr.workLog(l.lng, time.Since(l.started))
}
