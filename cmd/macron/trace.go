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

// The live view: while the agent works, the user sees which tools it uses,
// one line per tool, named after the tool's function, and nothing else (no
// arguments, outputs, commands or the model's notes). By default it is drawn
// as the console (console.go), whose icons can be premium emoji; this file
// draws the classic style, a code block:
//
//	💭 MACRON ⟫ live · step 3 · 0:07
//	━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
//	ctx ▰▰▰▱▱▱▱▱ 41% 105k/256k
//	✓ Search ×2
//	✓ Read Page
//	◆ Download ⠹ 6s · 45% · 12.3/27.0 MB
//
// A running program shows the last line it printed under its step:
//
//	◆ Run Code ⠹ 12s
//	  ↳ epoch 3/10 · loss 0.412
//
// A sub-agent's calls hang under it; it may work while the agent goes on:
//
//	✓ Generate Image
//	◆ Sub-agent: Music search & download ⠹ 9s
//	  ├─ ✓ Music Search
//	  └─ ◆ Download ⠹ 4s
//	🗜️ Context compacted: 160k ➔ 80k (-50%)

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

// toolLabels name each tool after its function. Tools missing here show their
// function name in title case ("split_video" → "Split Video"); "" hides one.
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
	toolSendFile:                "Attach File", // it goes with the answer, when the job ends
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
	agent.ToolDelegate:          "", // a sub-agent shows as its own branch (liveView.subAgent)
	agent.ToolPlan:              "", // the plan shows above the terminal (liveView.setPlan)
	agent.ToolMoreTools:         "", // a weaker model asking for the rest of the tools: not a step of the work
}

// toolLabel is the name a tool shows in the live view ("" = not shown).
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

// traceStep is one line: a tool, how many times in a row it ran, and whether
// a call is still running or any failed. A sub-agent is a line of its own;
// the steps with its id as parent are its calls.
type traceStep struct {
	id     int
	parent int // the sub-agent the call belongs to (0: the agent itself)
	label  string
	count  int
	active int
	failed bool
	event  bool      // not a tool: something that happened (a message from the user)
	mark   string    // an event's sign ("" = traceEvent)
	sub    bool      // a sub-agent
	calls  int       // a sub-agent's tool calls
	since  time.Time // when it last started running
	detail string    // live progress of the running call ("45% · 12/27 MB")
	output string    // the last line the running program printed
}

// trace is the list of steps, and how full the model's context is.
type trace struct {
	steps     []traceStep
	lastID    int
	calls     int
	ctxUsed   int
	ctxWindow int
	note      string           // a line under the title, outside the code block (e.g. «🎧 listening»)
	title     string           // a background task's name: shown in the title line
	plan      []agent.PlanItem // the job's checklist, above the terminal
	console   bool             // drawn as the console (console.go), not a code block
	icons     iconPicker       // the console's icons, premium where they can be
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

// prune forgets the oldest finished steps (only the latest lines show).
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

// start records a tool call of the agent (parent 0) or of a sub-agent; it
// reports false for tools that are not shown. A tool that runs again right
// after itself adds to the same line.
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

// subStart adds a sub-agent; its calls hang under it. It returns its id.
func (t *trace) subStart(title string, now time.Time) int {
	return t.add(traceStep{label: "Sub-agent: " + title, sub: true, count: 1, active: 1, since: now}).id
}

// subEnd marks a sub-agent done.
func (t *trace) subEnd(id int, failed bool) {
	if s := t.byID(id); s != nil {
		s.active, s.failed = 0, failed
	}
}

// event adds a line for something that happened during the job.
func (t *trace) event(label string, now time.Time) { t.eventMarked("", label, now) }

func (t *trace) eventMarked(mark, label string, now time.Time) {
	if n := len(t.steps); n > 0 && t.steps[n-1].label == label && t.steps[n-1].event {
		t.steps[n-1].count++
		return
	}
	t.add(traceStep{label: label, mark: mark, count: 1, event: true, since: now})
}

// compacted shows that the context was compacted, and by how much:
// «🗜️ Context compacted: 160k ➔ 80k (-50%)».
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

// running finds the latest running call of a tool in a branch.
func (t *trace) running(parent int, name string) *traceStep {
	label := toolLabel(name)
	for i := len(t.steps) - 1; i >= 0; i-- {
		if s := &t.steps[i]; s.parent == parent && s.label == label && s.active > 0 && !s.event && !s.sub {
			return s
		}
	}
	return nil
}

// progress records how far a running call is: a short state, or the last
// line its program printed.
func (t *trace) progress(parent int, name, text string, output bool) {
	text = strings.ReplaceAll(text, "`", "'") // never closes the terminal's code block
	if s := t.running(parent, name); s != nil {
		if output {
			s.output = text
		} else {
			s.detail = text
		}
	}
}

// end records the end of a call.
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

// traceBranch is how many calls of a sub-agent show under it.
const traceBranch = 5

// card draws the view: the title line, shown outside the code block so its
// 💭 can be a premium emoji, and the monospace body. animate adds the spinner
// and the running time (drafts); a group message, edited rarely, shows "…" instead.
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
	if t.console {
		return t.consoleView(now, started, animate, mdConsole)
	}
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
	if t.console {
		return t.consoleView(now, started, animate, htmlConsole)
	}
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

// line draws one step: first starts its line, rest indents what follows it
// (the last line its program printed).
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

// failedResult reports a tool result that is an error.
func failedResult(r string) bool {
	l := strings.ToLower(firstLine(r))
	for _, p := range []string{"error", "tool error", "search error", "fetch error", "music search error", "download failed", "write error",
		"read error", "list error", "create ", "no results", "no direct music"} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	if strings.HasPrefix(r, tools.TranscriptPrefix) { // run_code / install_package: a failed exit
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

// planMarkdown is the job's plan above the terminal: a task list, which a
// rich message draws with real checkboxes (ticked when done), the step being
// worked on in bold ("" without a plan).
func planMarkdown(items []agent.PlanItem) string {
	return planTasks(items, "📋", "⏳", "`", func(s string) string { return "**" + planText(s) + "**" })
}

// planTasks lays a plan out as a rich Markdown task list under its count.
// Each step is a list item of its own: lines that only follow each other
// would run together into one paragraph.
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

// planLead is what would make a step's text a list or a heading of its own
// («1. …», «- …», «+ …»).
var planLead = regexp.MustCompile(`^(\d{1,9})([.)])|^([-+])`)

// planText is a step's text as Markdown: its own formatting characters, and
// a number or a dash at its start, stay text.
func planText(s string) string {
	return planLead.ReplaceAllString(escapeMarkdown(strings.TrimSpace(s)), `$1\$2$3`)
}

// planHTML is the plan for a classic (HTML) trace message.
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

// markdownSpecial are the characters that would turn a plan step into formatting.
var markdownSpecial = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`, "~", `\~`, "|", `\|`,
	"=", `\=`, "#", `\#`, "<", `\<`, ">", `\>`)

func escapeMarkdown(s string) string { return markdownSpecial.Replace(s) }
