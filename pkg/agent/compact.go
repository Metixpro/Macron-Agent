package agent

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	DefaultContextWindow = 256000

	compactAt = 0.75

	compactTo = 0.5

	tailShare     = 0.20
	minTailRounds = 1

	summaryReserve = 3000

	recompactGap = 0.15

	historyShare = 0.25

	summaryWords = 900

	imageTokens = 1200
)

const summaryPrefix = "[context summary]"

const compactRecordRunes = 2500

var contextWindows = map[string]int{
	"claude-fable": 1000000, "claude-opus-5": 1000000, "claude-sonnet-5": 1000000, "claude-opus-4-6": 1000000, "claude-sonnet-4-6": 1000000,
	"claude-opus-4.6": 1000000, "claude-sonnet-4.6": 1000000, "claude-haiku": 200000, "claude": 200000,
	"gpt-6": 1050000, "gpt-5.5": 1050000, "gpt-5.4": 1050000, "gpt-5.4-mini": 400000, "gpt-5.4-nano": 400000, "gpt-5": 400000,
	"gpt-4.1": 1047576, "gpt-4o": 128000, "gpt-4": 128000, "o3": 200000, "o4": 200000,
	"gemini": 1048576, "gemma-3": 131072, "gemma": 8192,
	"deepseek-v4": 1000000, "deepseek-chat": 1000000, "deepseek-reasoner": 1000000, "deepseek": 128000,
	"qwen3-coder-plus": 1000000, "qwen3-coder": 262144, "qwen3-max": 262144, "qwen": 131072,
	"llama-4": 1048576, "llama": 131072, "mistral-large": 131072, "mistral": 32768, "grok-4": 256000, "grok": 131072,
	"kimi": 262144, "glm-5": 204800, "glm": 128000, "minimax-m3": 1000000, "minimax": 204800,
}

func ContextWindow(model string) int {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	best, n := "", 0
	for k, v := range contextWindows {
		if strings.Contains(m, k) && len(k) > len(best) {
			best, n = k, v
		}
	}
	if n == 0 {
		return DefaultContextWindow
	}
	return n
}

func ContextWindowKnown(model string) bool {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	for k := range contextWindows {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}

func compactTokens(window int) int {
	return int(float64(window) * compactAt)
}

func estimateText(s string) int {
	ascii, other := 0, 0
	for _, r := range s {
		if r < 128 || unicode.IsSpace(r) {
			ascii++
		} else {
			other++
		}
	}
	return ascii/4 + (other+1)/2
}

func estimateMessage(m llm.Message) int {
	n := 4 + estimateText(m.Content)
	for _, p := range m.Parts {
		if p.Type == "image_url" {
			n += imageTokens
		} else {
			n += estimateText(p.Text)
		}
	}
	for _, tc := range m.ToolCalls {
		n += 8 + estimateText(tc.Function.Name) + estimateText(tc.Function.Arguments)
	}
	return n
}

func estimateTokens(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		n += estimateMessage(m)
	}
	return n
}

type contextMeter struct {
	window      int
	fixed       bool
	real, guess int
	floor       int
	shown       int

	ceiling, fits int

	carry          int
	carryCompacted bool
	input          int
	offset         int
	based          bool
}

func (m *contextMeter) overflowed(used int) {
	if m.ceiling == 0 || used < m.ceiling {
		m.ceiling = used
	}
	m.window, m.fixed = max(int(float64(m.ceiling)*0.9), 16000), true
}

func (m *contextMeter) report(l *EventListener, used int) {
	if !m.based {
		m.based = true
		switch {
		case m.carry <= 0:
		case m.carryCompacted:
			if l.OnCompact != nil {
				l.OnCompact(m.carry, used, m.limit(), true)
			}
		default:
			m.offset = max(0, m.carry+m.input-used)
		}
	}
	shown := used + m.offset
	if m.offset > 0 && shown >= compactTokens(m.window) {

		if l.OnCompact != nil {
			l.OnCompact(max(shown, m.shown), used, m.limit(), false)
		}
		m.offset, m.shown, shown = 0, used, used
	}
	m.shown = max(m.shown, shown)
	if l.OnContext != nil {
		l.OnContext(m.shown, m.limit())
	}
}

func (m *contextMeter) limit() int { return compactTokens(m.window) }

func (m *contextMeter) compacted(l *EventListener, before, after int, summarized bool) {
	if l.OnCompact != nil {
		l.OnCompact(max(before, m.shown), after, m.limit(), summarized)
	}
	m.shown, m.offset, m.based = after, 0, true
	m.report(l, after)
}

type layout struct {
	histFrom, histTo int
	input            int
}

func (m *contextMeter) observe(model string, promptTokens int, sent []llm.Message) {
	if !m.fixed && model != "" {
		m.window = ContextWindow(model)
	}
	if promptTokens > 0 {
		m.real, m.guess = promptTokens, estimateTokens(sent)
	}
	if m.ceiling > 0 && promptTokens > m.fits {
		m.fits = promptTokens
		m.window = max(m.window, min((m.fits+m.ceiling)/2, m.ceiling-1))
	}
}

func (m *contextMeter) ratio() float64 {
	if m.real > 0 && m.guess > 0 {
		return min(max(float64(m.real)/float64(m.guess), 0.5), 2.0)
	}
	return 1
}

func (m *contextMeter) used(msgs []llm.Message) int {
	return int(float64(estimateTokens(msgs)) * m.ratio())
}

func (m *contextMeter) due(used int) bool {
	return used > compactTokens(m.window) && (m.floor == 0 || used > m.floor+int(float64(m.window)*recompactGap))
}

func trimToolTokens(msgs []llm.Message, from, budget int) bool {
	total := estimateTokens(msgs)
	protected := len(msgs) - countTrailingTools(msgs)
	changed := false
	for i := from + 1; i < protected && total > budget; i++ {
		m := &msgs[i]
		if !isToolResult(*m) || len(m.Parts) > 0 || utf8.RuneCountInString(m.Content) <= trimmedToolRunes+200 {
			continue
		}
		before := estimateMessage(*m)
		m.Content = textutil.HeadTail(m.Content, trimmedToolRunes) + "\n[older tool output trimmed to save context]"
		total -= before - estimateMessage(*m)
		changed = true
	}
	return changed
}

func keepTail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

const DefaultHistoryTokens = 8000

const promptRoom = 40000

func FillHistoryTokens(window int) int {
	if window <= 0 {
		window = DefaultContextWindow
	}
	limit := compactTokens(window)
	return max(limit-promptRoom, int(float64(window)*historyShare))
}

func historyCap(window, kept int) int {
	if kept <= 0 {
		kept = DefaultHistoryTokens
	}
	return min(kept*3/2, FillHistoryTokens(window))
}

const (
	WorkLogMark = "[work log before stopping — the job is not finished]"
	notesKept   = 3
	notesBatch  = 3
)

var notesRe = regexp.MustCompile(`(?s)<work_notes>\n?(.*?)\n?</work_notes>`)

func noted(m llm.Message) (notes, logged bool) {
	if m.Role != "assistant" {
		return false, false
	}
	return strings.Contains(m.Content, "<work_notes>"), strings.Contains(m.Content, WorkLogMark)
}

func thinNotes(history []llm.Message, keep int) []llm.Message {
	total := 0
	for _, m := range history {
		if n, l := noted(m); n || l {
			total++
		}
	}
	thin := max(total-keep, 0) / notesBatch * notesBatch
	for i := range history {
		if thin == 0 {
			break
		}
		m := &history[i]
		notes, logged := noted(*m)
		if !notes && !logged {
			continue
		}
		thin--
		if notes {
			m.Content = notesRe.ReplaceAllStringFunc(m.Content, func(block string) string {
				return "<work_notes>\n" + olderNotes(notesRe.FindStringSubmatch(block)[1]) + "\n</work_notes>"
			})
		}
		if at := strings.Index(m.Content, WorkLogMark); logged && at >= 0 {
			m.Content = m.Content[:at] + WorkLogMark + "\n" + olderNotes(m.Content[at+len(WorkLogMark):])
		}
	}
	return history
}

func olderNotes(body string) string {
	var keep []string
	steps := 0
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "(") || line == "Plan:" || line == "Steps taken:" || strings.HasPrefix(line, "[x] "):
		case strings.HasPrefix(line, "[ ] ") || strings.HasPrefix(line, "[>] "):
			keep = append(keep, line)
		case strings.HasPrefix(line, "Files changed: "):
			keep = append([]string{line}, keep...)
		case strings.HasPrefix(line, "Plan: "):
			if strings.Contains(line, "[ ]") || strings.Contains(line, "[>]") {
				keep = append(keep, line)
			}
		case strings.HasPrefix(line, "… ") || strings.HasPrefix(line, "..."):
			if n := leadingNumber(strings.TrimLeft(line, "… .")); n > 0 {
				steps += n
			}
		default:
			steps++
		}
	}
	if steps > 0 {
		keep = append(keep, fmt.Sprintf("(%d steps; their details are kept only for the latest replies)", steps))
	}
	return strings.Join(keep, "\n")
}

func leadingNumber(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func fitHistory(history []llm.Message, budget int) []llm.Message {
	total := estimateTokens(history)
	dropped := 0
	for total > budget && len(history) > 2 {
		total -= estimateMessage(history[0])
		history = history[1:]
		dropped++
		for len(history) > 0 && history[0].Role != "user" {
			total -= estimateMessage(history[0])
			history = history[1:]
			dropped++
		}
	}
	if dropped > 0 {
		log.Printf("agent: %d older messages left out of this request to fit the context window", dropped)
	}
	return history
}

func tailStart(msgs []llm.Message, from, budget, minRounds int) int {
	start, rounds, total := len(msgs), 0, 0
	for i := len(msgs) - 1; i > from; i-- {
		total += estimateMessage(msgs[i])
		boundary := msgs[i].Role == "assistant" || (msgs[i].Role == "user" && !isToolResult(msgs[i]))
		if !boundary {
			continue
		}
		if msgs[i].Role == "assistant" {
			rounds++
		}
		start = i
		if rounds >= minRounds && total >= budget {
			break
		}
	}
	return start
}

const compactSystem = `You compress the working memory of an AI agent in the middle of a job, so it can go on without the full transcript.
Write a concise, structured summary of what is below, with these sections:

## Earlier Conversation
Only what still matters from before the current request (facts about the user, decisions, open threads); leave the section out if there was none.

## Goal
The user's request in one or two lines, and what "done" looks like.

## User Preferences
Constraints, corrections and choices the user gave (quote short corrections).

## Completed Actions
Only the milestones, one line each (the exact step-by-step record is attached to your summary separately).

## Active State
Files created or changed in the workspace (one line each), the current approach, anything still running.

## Errors & Fixes
Errors met (exact text, shortened) and how each was solved or why it is still open.

## Key Facts
Exact values, names, numbers, links and findings the rest of the job depends on. Never write keys, tokens or passwords.

## Remaining Work
The next steps, in order.

Be concrete: file names, commands, numbers, results. At most about %d words. Write only the summary, in English, with no preface.`

func (a *Agent) compact(ctx context.Context, messages []llm.Message, lay layout, meter *contextMeter, work *workLog) ([]llm.Message, layout, bool) {
	ratio := meter.ratio()
	target := int(float64(compactTokens(meter.window)) * compactTo / ratio)
	head := estimateTokens(messages[:lay.histFrom]) + estimateTokens(messages[lay.histTo:lay.input+1])
	budget := min(max(target-head-summaryReserve, meter.window/25), int(float64(meter.window)*tailShare))
	start := tailStart(messages, lay.input, budget, minTailRounds)
	history, middle := messages[lay.histFrom:lay.histTo], messages[lay.input+1:start]
	if len(history)+len(middle) < 2 {
		return messages, lay, false
	}
	limit := min(240000, int(float64(meter.window)*0.8))
	var tr strings.Builder
	if len(history) > 0 {
		var hb strings.Builder
		for _, m := range history {
			renderTurn(&hb, m)
		}
		tr.WriteString("EARLIER CONVERSATION (before the current request):\n" + keepTail(hb.String(), limit/3) + "\n")
	}
	if len(middle) > 0 {
		tr.WriteString("STEPS OF THIS JOB:\n")
		for _, m := range middle {
			renderTurn(&tr, m)
		}
	}
	transcript := textutil.HeadTail(tr.String(), limit)
	input := lay.input
	summary := ""
	sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	resp, err := a.LLM.Complete(sctx, llm.Request{Messages: []llm.Message{
		{Role: "system", Content: fmt.Sprintf(compactSystem, summaryWords)},
		{Role: "user", Content: "The user's request:\n" + textutil.TruncateRunes(messages[input].Text(), 4000, "…") +
			"\n\nThe steps to summarize:\n" + transcript},
	}})
	cancel()
	if err == nil {
		summary = strings.TrimSpace(resp.Message.Content)
	}
	switch {
	case summary != "" && work.len() > 0:
		summary += "\n\n## Exact Step Record\n" + work.record(compactRecordRunes)
	case summary == "" && ctx.Err() != nil:
		return messages, lay, false
	case summary == "":
		log.Printf("agent: the compaction summary failed (%v); keeping the work log instead", err)
		summary = "## Completed Actions\n" + work.String() + "\n\n(The detailed summary could not be written; check the workspace files for details.)"
	}
	note := llm.Message{Role: "user", Content: summaryPrefix + " The earlier conversation and steps of this job were folded into this summary to free up room. " +
		"Go on from it and from the recent steps after it, without redoing what it records; read a file only when the next step needs details that are not here.\n\n" + summary}
	out := make([]llm.Message, 0, lay.histFrom+input-lay.histTo+2+len(messages)-start)
	out = append(out, messages[:lay.histFrom]...)
	out = append(out, messages[lay.histTo:input+1]...)
	kept := layout{histFrom: lay.histFrom, histTo: lay.histFrom, input: len(out) - 1}
	out = append(out, note)
	out = append(out, messages[start:]...)
	dropOldImages(out, kept.input)
	trimToolTokens(out, kept.input, target)
	return out, kept, true
}

func shrinkHard(messages []llm.Message, lay *layout) []llm.Message {
	out := append(append([]llm.Message(nil), messages[:lay.histFrom]...), messages[lay.histTo:]...)
	lay.input -= lay.histTo - lay.histFrom
	lay.histTo = lay.histFrom
	dropOldImages(out, lay.input)
	if m := &out[lay.input]; utf8.RuneCountInString(m.Content) > hugeRequestRunes {
		m.Content = textutil.HeadTail(m.Content, hugeRequestRunes) + "\n[the request was shortened to fit the model]"
	}
	trimToolTokens(out, lay.input, 0)
	return out
}

const hugeRequestRunes = 60000

func renderTurn(sb *strings.Builder, m llm.Message) {
	text := strings.TrimSpace(m.Text())
	switch {
	case m.Role == "assistant":
		if text != "" {
			fmt.Fprintf(sb, "ASSISTANT: %s\n", textutil.TruncateRunes(text, 2000, "…"))
		}
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(sb, "CALL %s(%s)\n", tc.Function.Name, textutil.ArgPreview(tc.Function.Arguments, 300))
		}
	case m.Role == "tool":
		fmt.Fprintf(sb, "RESULT %s: %s\n", m.Name, textutil.HeadTail(text, 1500))
	case isToolResult(m):
		fmt.Fprintf(sb, "RESULTS: %s\n", textutil.HeadTail(text, 3000))
	case strings.HasPrefix(text, summaryPrefix):
		fmt.Fprintf(sb, "EARLIER SUMMARY:\n%s\n", text)
	default:
		fmt.Fprintf(sb, "USER: %s\n", textutil.TruncateRunes(text, 1500, "…"))
	}
	sb.WriteByte('\n')
}

func ContextBar(used, window int) string {
	if window <= 0 {
		return ""
	}
	pct := min(used*100/window, 100)
	full := (pct*8 + 50) / 100
	return strings.Repeat("▰", full) + strings.Repeat("▱", 8-full) + fmt.Sprintf(" %d%%", pct)
}

func HumanTokens(n int) string {
	switch {
	case n >= 1000000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e6), ".0") + "M"
	case n >= 1000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	}
	return fmt.Sprint(n)
}

func (a *Agent) compactNow(ctx context.Context, messages []llm.Message, lay *layout, meter *contextMeter, work *workLog, res *Result,
	req Request, l *EventListener) []llm.Message {
	before, target := meter.used(messages), int(float64(compactTokens(meter.window))*compactTo)
	if trimToolTokens(messages, lay.input, int(float64(target)/meter.ratio())) {
		if now := meter.used(messages); now <= target {
			log.Printf("agent: old tool outputs shortened for session %s: ~%s → ~%s tokens", req.SessionID, HumanTokens(before), HumanTokens(now))
			meter.floor = now
			meter.compacted(l, before, now, false)
			return messages
		}
	}
	out, kept, ok := a.compact(ctx, messages, *lay, meter, work)
	if !ok {
		meter.floor = meter.used(messages)
		return messages
	}
	*lay = kept
	if a.Registry != nil && a.Registry.Machine != nil {
		if ws, err := a.Registry.Machine.Workspace(req.SessionID); err == nil && ws != "" {
			out = PreserveProjectAcrossCompaction(ws, out)
		}
	}
	res.Compactions++
	after := meter.used(out)
	meter.floor = after
	log.Printf("agent: context compacted for session %s: ~%s → ~%s tokens (window %s)", req.SessionID,
		HumanTokens(before), HumanTokens(after), HumanTokens(meter.window))
	meter.compacted(l, before, after, true)
	return out
}
