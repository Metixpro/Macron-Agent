package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const ToolPlan = "update_plan"

const (
	PlanPending    = "pending"
	PlanInProgress = "in_progress"
	PlanDone       = "done"
)

type PlanItem struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

const (
	maxPlanItems = 12
	maxPlanRunes = 100
)

type planState struct {
	mu    sync.Mutex
	items []PlanItem
}

func (p *planState) set(items []PlanItem) {
	p.mu.Lock()
	p.items = items
	p.mu.Unlock()
}

func (p *planState) get() []PlanItem {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]PlanItem(nil), p.items...)
}

func (p *planState) String() string {
	items := p.get()
	if len(items) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Plan:")
	for _, it := range items {
		mark := map[string]string{PlanDone: "[x]", PlanInProgress: "[>]"}[it.Status]
		if mark == "" {
			mark = "[ ]"
		}
		fmt.Fprintf(&sb, "\n%s %s", mark, it.Text)
	}
	return sb.String()
}

func (p *planState) progress() (done, total int, current string) {
	for _, it := range p.get() {
		switch it.Status {
		case PlanDone:
			done++
		case PlanInProgress:
			current = it.Text
		}
		total++
	}
	return done, total, current
}

const (
	planNudgeAfter = 3
	planStartAfter = 4
)

type planPace struct {
	since, turns int
	askedForPlan bool
}

func (pp *planPace) after(calls []llm.ToolCall, plan *planState) string {
	pp.turns++
	for _, c := range calls {
		if c.Function.Name == ToolPlan {
			pp.since = 0
			return ""
		}
	}
	pp.since++
	done, total, current := plan.progress()
	switch {
	case total == 0:
		if !pp.askedForPlan && pp.turns >= planStartAfter {
			pp.askedForPlan = true
			return "[plan] This job is taking several steps. If more remain, call " + ToolPlan + " with your plan (short steps, " +
				"what is finished marked done, one in progress) in the same turn as your next calls, so the user can follow along."
		}
	case done < total && pp.since >= planNudgeAfter:
		pp.since = 0
		now := ""
		if current != "" {
			now = fmt.Sprintf(", in progress: «%s»", current)
		}
		return fmt.Sprintf("[plan] Your plan was not updated for %d turns (%d/%d done%s). If a step is finished, call %s now, in the "+
			"same turn as your next calls: mark it done and the next one in progress, so the user sees the work move.",
			planNudgeAfter, done, total, now, ToolPlan)
	}
	return ""
}

func planStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(strings.NewReplacer("-", "_", " ", "_").Replace(s))) {
	case "done", "completed", "complete", "finished", "x", "true", "ok", "success", "succeeded", "checked", "✅", "✓", "✔",
		"انجام_شد", "انجام‌شده", "تمام", "تموم", "تموم_شد", "تمام_شد":
		return PlanDone
	case "in_progress", "doing", "active", "current", "running", "started", "working", "now", "wip", "⏳",
		"در_حال_انجام", "در‌حال‌انجام":
		return PlanInProgress
	}
	return PlanPending
}

var (
	planLead  = regexp.MustCompile(`^(?:(?:\d{1,2}|[۰-۹]{1,2})\s*[.)\-:]\s+|\(\d{1,2}\)\s+|[-*•·]\s+)`)
	planBox   = regexp.MustCompile(`^\[( |x|X|✓|✔|\*|-|>|~|/)\]\s*`)
	planTail  = regexp.MustCompile(`(?i)\s*(?:[\[(]\s*(done|completed?|finished|ok|x|✓|✔|in[ _-]?progress|doing|current|now|wip|running|pending|todo|to ?do|next|انجام شد|انجام‌شده|تموم شد|در حال انجام|مونده)\s*[\])]|[-–—:]\s*(done|completed|in progress|pending|todo))\s*$`)
	planIcons = map[string]string{"✅": PlanDone, "✔️": PlanDone, "✔": PlanDone, "☑️": PlanDone, "☑": PlanDone, "✓": PlanDone,
		"🟢": PlanDone, "⏳": PlanInProgress, "⌛": PlanInProgress, "🔄": PlanInProgress, "▶️": PlanInProgress, "▶": PlanInProgress,
		"👉": PlanInProgress, "🔵": PlanInProgress, "▫️": PlanPending, "◻️": PlanPending, "⬜": PlanPending, "☐": PlanPending,
		"⚪": PlanPending, "🔲": PlanPending}
)

func planMarks(text string) (string, string) {
	status := ""
	for range 4 {
		text = strings.TrimSpace(text)
		before := text
		text = planLead.ReplaceAllString(text, "")
		if m := planBox.FindStringSubmatch(text); m != nil {
			switch m[1] {
			case "x", "X", "✓", "✔", "*":
				status = PlanDone
			case ">", "~", "/", "-":
				status = PlanInProgress
			default:
				if status == "" {
					status = PlanPending
				}
			}
			text = text[len(m[0]):]
		}
		for icon, st := range planIcons {
			if rest, ok := strings.CutPrefix(text, icon); ok {
				text, status = rest, st
				break
			}
		}
		if text == before {
			break
		}
	}
	for range 2 {
		m := planTail.FindStringSubmatchIndex(text)
		if m == nil {
			break
		}
		word := ""
		for g := 2; g+1 < len(m); g += 2 {
			if m[g] >= 0 {
				word = text[m[g]:m[g+1]]
			}
		}
		if st := planStatus(word); word != "" {
			status = st
			if w := strings.ToLower(word); strings.Contains(w, "todo") || strings.Contains(w, "to do") || w == "next" || w == "pending" || w == "مونده" {
				status = PlanPending
			}
		}
		text = strings.TrimSpace(text[:m[0]])
	}
	for icon, st := range planIcons {
		if rest, ok := strings.CutSuffix(text, icon); ok && st == PlanDone {
			text, status = strings.TrimSpace(rest), st
			break
		}
	}
	return strings.TrimSpace(text), status
}

func planRaw(args map[string]any) []any {
	for _, key := range []string{"items", "plan", "steps", "todos", "tasks", "todo"} {
		switch v := args[key].(type) {
		case []any:
			if len(v) > 0 {
				return v
			}
		case string:
			if s := strings.TrimSpace(v); strings.HasPrefix(s, "[") {
				var list []any
				if json.Unmarshal([]byte(s), &list) == nil && len(list) > 0 {
					return list
				}
			} else if s != "" {
				var out []any
				for _, line := range strings.Split(s, "\n") {
					if line = strings.TrimSpace(line); line != "" {
						out = append(out, line)
					}
				}
				return out
			}
		}
	}
	return nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func parsePlanMarks(args map[string]any) (items []PlanItem, marked bool, _ error) {
	raw := planRaw(args)
	if len(raw) == 0 {
		return nil, false, fmt.Errorf("items is required: the whole plan, 2-%d short steps", maxPlanItems)
	}
	for _, r := range raw {
		var text, status string
		switch v := r.(type) {
		case map[string]any:
			text = firstString(v, "text", "step", "title", "content", "task", "description", "name", "label")
			status = firstString(v, "status", "state")
			for _, k := range []string{"done", "completed", "checked", "complete"} {
				if b, ok := v[k].(bool); ok && b && status == "" {
					status = PlanDone
				}
			}
		case string:
			text = v
		}
		text = textutil.OneLine(strings.TrimSpace(text))
		clean, mark := planMarks(text)
		st := planStatus(status)
		if mark != "" && (status == "" || st == PlanPending) && mark != PlanPending {
			st, marked = mark, true
		}
		text = textutil.TruncateRunes(clean, maxPlanRunes, "…")
		if text == "" {
			continue
		}
		items = append(items, PlanItem{Text: text, Status: st})
		if len(items) == maxPlanItems {
			break
		}
	}
	if len(items) == 0 {
		return nil, false, fmt.Errorf("items is required: the whole plan, 2-%d short steps", maxPlanItems)
	}

	current := -1
	for i, it := range items {
		if it.Status == PlanInProgress {
			if current >= 0 {
				items[i].Status = PlanPending
				continue
			}
			current = i
		}
	}
	for i := 0; i < current; i++ {
		if items[i].Status == PlanPending {
			items[i].Status = PlanDone
		}
	}
	return items, marked, nil
}

func (a *Agent) planTool(state *planState, l *EventListener) LocalTool {
	def := llm.Tool{Type: "function", Function: llm.ToolDef{
		Name: ToolPlan,
		Description: "Your checklist for a job of 3 or more steps; the user watches it live. Send the WHOLE plan every time: 2-" +
			fmt.Sprint(maxPlanItems) + " short steps in the user's language, exactly one in_progress. Call it with your first real calls " +
			"(same turn), then each time a step is finished, right away, in the same turn as your next calls: mark it done and the next " +
			"in_progress (never several at the end). Not for quick questions.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"items": map[string]any{"type": "array", "description": "The whole plan, in order",
				"items": map[string]any{"type": "object", "properties": map[string]any{
					"text":   map[string]any{"type": "string", "description": "A short step, e.g. «دانلود ویدیو»"},
					"status": map[string]any{"type": "string", "enum": []string{PlanPending, PlanInProgress, PlanDone}},
				}, "required": []string{"text", "status"}}},
		}, "required": []string{"items"}},
	}}
	return LocalTool{Def: def, Handler: func(_ context.Context, args map[string]any) (ToolOutput, error) {
		items, marked, err := parsePlanMarks(args)
		if err != nil {
			return ToolOutput{Text: "error: " + err.Error()}, nil
		}
		note := ""
		if marked {
			note = " (the states written in the step texts were read; next time put them in status: pending, in_progress or done)"
		}
		state.set(items)
		if l.OnPlan != nil {
			l.OnPlan(items)
		}
		done := 0
		next := ""
		for _, it := range items {
			switch it.Status {
			case PlanDone:
				done++
			case PlanInProgress:
				next = it.Text
			}
		}
		switch {
		case done == len(items):
			return ToolOutput{Text: fmt.Sprintf("Plan updated: all %d steps done%s. Check the result, then give the final answer.", len(items), note)}, nil
		case next != "":
			return ToolOutput{Text: fmt.Sprintf("Plan updated (%d/%d done%s). Now: %s", done, len(items), note, next)}, nil
		}
		return ToolOutput{Text: fmt.Sprintf("Plan updated (%d/%d done%s). Mark the step you work on in_progress.", done, len(items), note)}, nil
	}}
}
