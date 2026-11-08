package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Triage is what a request needs, decided on this machine before the model
// works on it: whether it needs tools at all, its kind, how much work it is,
// how much of the earlier conversation it leans on, and which groups of tools
// it will use. The bot turns it into its settings for the request (the model,
// how hard it thinks, the tools and history it gets).
//
// Two sources give the same answers: Local reads the words (free, instant,
// nothing leaves the machine) and, when the owner asks for it, one short call
// to the agent's own model reads the request (TriagePrompt, ParseTriage).

// Context answers: how much of the earlier conversation a message needs.
const (
	ContextStandalone = "standalone"
	ContextRecent     = "recent"
	ContextEarlier    = "earlier"
)

// Group is a group of tools a request may need (agent.ToolGroups).
type Group struct {
	Key   string
	About string
}

// Turn is one message of the earlier conversation.
type Turn struct {
	Role string // "user" or "assistant"
	Text string
}

// Triage is the decision about one request.
type Triage struct {
	NeedsTools  float64 // the probability it needs tools (an action, not just a reply)
	Kind        Kind
	KindConf    float64
	Work        float64 // 0 a one-line answer, 1 a quick step or two, 2 several steps, 3 a long project
	Context     string  // ContextStandalone, ContextRecent or ContextEarlier
	ContextConf float64
	Groups      map[string]float64 // each group's probability of being needed (nil: not judged)
	Source      string             // "local" or "model"
	Took        time.Duration
}

// TriageInput is what a triage reads.
type TriageInput struct {
	Input
	History []Turn // the last turns, oldest first
}

// KindAbout describes each kind, as a model is asked about it.
var KindAbout = map[Kind]string{
	Chat:      "Conversation: greetings, thanks, feelings, opinions, advice",
	Quick:     "A short factual question, a definition, a price or a quick lookup",
	Media:     "Download, convert or find music, videos, voice or pictures, or make a picture",
	Writing:   "Write, rewrite, translate or summarize text",
	Vision:    "Describe or read a picture the user sent",
	Code:      "Write, fix, explain, run or review code",
	Research:  "Research a topic in depth with sources, compare options, write a report",
	Reasoning: "Math, logic, puzzles, planning, careful step-by-step reasoning",
	Documents: "Read, analyze or make documents, books, spreadsheets or data",
	Project:   "A big multi-step job: build an app or site, manage or fix a server, automate a workflow",
}

// kindWork is how much work each kind usually is.
var kindWork = map[Kind]float64{
	Chat: 0, Quick: 0.5, Writing: 1, Media: 1, Vision: 1,
	Code: 2, Research: 2, Reasoning: 1.5, Documents: 2, Project: 3,
}

// kindTools is how likely each kind needs an action rather than a reply.
var kindTools = map[Kind]float64{
	Chat: 0.05, Writing: 0.3, Reasoning: 0.4, Quick: 0.6,
	Media: 0.95, Vision: 0.6, Code: 0.85, Research: 0.95, Documents: 0.9, Project: 0.95,
}

var (
	// A message that continues what was just said: «آره»، «ادامه بده»، «همونو بفرست».
	recentRe = regexp.MustCompile(`(?i)^\s*(?:آره|اره|بله|آری|نه|خیر|اوکی|باشه|حله|ادامه|بکن|بکنش|انجامش|بفرست|بفرستش|همین|همون|همونو|اونو|اینو|اون یکی|این یکی|دوباره|یه بار دیگه|بهترش|درستش|عوضش|` +
		`yes|yeah|yep|no|nope|ok|okay|sure|go on|go ahead|continue|do it|send it|that one|this one|the same|again|redo|fix it|better)(?:\s|[.!،,؟?]|$)`)
	// A message that points to something said long ago.
	earlierRe = regexp.MustCompile(`(?i)قبلا|قبلاً|دیروز|پریروز|هفته پیش|اون روز|اون دفعه|دفعه قبل|یادته|گفته بودم|گفته بودی|اون پروژه|` +
		`earlier|yesterday|last time|last week|before|remember when|you said|i told you|that project`)
	// Words that refer to something the message itself does not name.
	referRe = regexp.MustCompile(`(?i)\b(?:those|these|them|the file|the same|the one)\b|(?:^|\s)(?:اون|این|همون|همین|اونو|اینو)(?:\s|$)`)
)

// Local is a triage from the words alone. It is cautious where a mistake
// costs (it is never sure enough to send a request without its tools) and
// leaves the groups of tools unjudged: the agent's own focus reads the words
// for those already.
func Local(in TriageInput) Triage {
	start := time.Now()
	v := Classify(in.Input)
	t := Triage{Kind: v.Kind, Source: "local"}

	named := len(v.Signals) > 0 && v.Signals[0].Why != "none"
	if named {
		score := v.Scores[v.Kind]
		t.KindConf = clamp01(0.4 + 0.1*float64(score))
	} else {
		t.KindConf = 0.35
	}

	t.Work = kindWork[v.Kind]
	if v.Hard {
		t.Work = min(t.Work+1, 3)
	}
	runes := utf8.RuneCountInString(strings.TrimSpace(in.Text))
	if runes > 600 {
		t.Work = max(t.Work, 2)
	}

	t.NeedsTools = kindTools[v.Kind]
	if in.Images > 0 || len(in.Files) > 0 {
		t.NeedsTools = max(t.NeedsTools, 0.8)
	}

	text := strings.TrimSpace(in.Text)
	switch {
	case len(in.History) == 0:
		t.Context, t.ContextConf = ContextStandalone, 0.9
	case earlierRe.MatchString(text):
		t.Context, t.ContextConf = ContextEarlier, 0.7
	case recentRe.MatchString(text) || strings.TrimSpace(in.Hint) != "":
		t.Context, t.ContextConf = ContextRecent, 0.7
	case !named && runes < 40:
		t.Context, t.ContextConf = ContextRecent, 0.55
	case named && (runes >= 80 || (runes >= 25 && !referRe.MatchString(text))): // a long request describes itself
		t.Context, t.ContextConf = ContextStandalone, 0.65
	default:
		t.Context, t.ContextConf = ContextRecent, 0.5
	}
	t.Took = time.Since(start)
	return t
}

func clamp01(f float64) float64 { return min(max(f, 0), 1) }

// Picked are the groups at or above a probability, sorted.
func (t Triage) Picked(threshold float64) []string {
	var out []string
	for k, p := range t.Groups {
		if p >= threshold {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// String sums a triage up for logs and the owner's test.
func (t Triage) String() string {
	return fmt.Sprintf("%s · tools %.0f%% · %s %.0f%% · work %.1f · %s %.0f%% · %s", t.Source, t.NeedsTools*100, t.Kind, t.KindConf*100,
		t.Work, t.Context, t.ContextConf*100, t.Took.Round(time.Millisecond))
}

// Limits of the state a triage reads: enough to judge, small enough to be fast.
const (
	stateTurnRunes    = 300
	stateHistoryRunes = 2400
	stateReplyRunes   = 400
	stateMessageRunes = 4000
)

// TriageState lays out what a model triage judges: the last turns of the
// conversation (oldest first), what the message replies to, what came with
// it, and the newest message itself.
func TriageState(history []Turn, reply string, attached []string, message string) string {
	var turns []string
	used := 0
	for i := len(history) - 1; i >= 0 && used < stateHistoryRunes; i-- {
		text := cut(strings.Join(strings.Fields(history[i].Text), " "), stateTurnRunes)
		if text == "" {
			continue
		}
		line := history[i].Role + ": " + text
		used += utf8.RuneCountInString(line)
		turns = append([]string{line}, turns...)
	}
	var sb strings.Builder
	if len(turns) > 0 {
		sb.WriteString("Earlier conversation (oldest first):\n" + strings.Join(turns, "\n") + "\n\n")
	} else {
		sb.WriteString("There is no earlier conversation.\n\n")
	}
	if r := strings.TrimSpace(reply); r != "" {
		sb.WriteString("The newest message replies to: «" + cut(strings.Join(strings.Fields(r), " "), stateReplyRunes) + "»\n")
	}
	if len(attached) > 0 {
		sb.WriteString("Sent with it: " + strings.Join(attached, ", ") + "\n")
	}
	if strings.TrimSpace(message) == "" {
		message = "(no text)"
	}
	sb.WriteString("\nNewest message:\n" + cut(strings.TrimSpace(message), stateMessageRunes))
	return sb.String()
}

func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// TriagePrompt asks a model the triage's questions about a state, to be
// answered with one JSON object.
func TriagePrompt(state string, groups []Group) string {
	var sb strings.Builder
	sb.WriteString("You sort requests for an assistant before it works on them. Do not answer the request: judge it.\n\n")
	sb.WriteString(state)
	sb.WriteString("\n\nAnswer with one JSON object and nothing else:\n{\n")
	sb.WriteString(`  "needs_tools": 0..1, // is an action needed (running code; files; fresh facts from the internet; downloads; pictures; servers, GitHub, reminders, groups), not just a written reply?` + "\n")
	sb.WriteString(`  "kind": one of the kinds below, "kind_confidence": 0..1,` + "\n")
	sb.WriteString(`  "work": 0..3, // 0 a one-line answer, 1 one or two quick steps, 2 several steps, 3 a long multi-step project` + "\n")
	sb.WriteString(`  "context": "standalone" | "recent" | "earlier", "context_confidence": 0..1, // how much of the earlier conversation the newest message needs` + "\n")
	sb.WriteString(`  "groups": {"<group>": 0..1, ...} // for each group below, the probability the job needs it` + "\n}\n\nKinds:\n")
	for _, k := range Kinds {
		fmt.Fprintf(&sb, "- %s: %s\n", k, KindAbout[k])
	}
	if len(groups) > 0 {
		sb.WriteString("\nGroups of tools:\n")
		for _, g := range groups {
			fmt.Fprintf(&sb, "- %s: %s\n", g.Key, g.About)
		}
	}
	return sb.String()
}

// ErrNoTriage is a model reply that holds no triage.
var ErrNoTriage = errors.New("triage: the reply holds no decision")

// ParseTriage reads a model's triage. What is missing reads as the safe side
// (tools needed, the whole conversation, real work); an unknown kind is not
// trusted at all.
func ParseTriage(reply string) (Triage, error) {
	raw := strings.TrimSpace(reply)
	i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if i < 0 || j <= i {
		return Triage{}, ErrNoTriage
	}
	var v struct {
		NeedsTools  *float64           `json:"needs_tools"`
		Kind        string             `json:"kind"`
		KindConf    float64            `json:"kind_confidence"`
		Work        *float64           `json:"work"`
		Context     string             `json:"context"`
		ContextConf float64            `json:"context_confidence"`
		Groups      map[string]float64 `json:"groups"`
	}
	if err := json.Unmarshal([]byte(raw[i:j+1]), &v); err != nil {
		return Triage{}, fmt.Errorf("%w: %v", ErrNoTriage, err)
	}
	t := Triage{NeedsTools: 1, Work: 2, Context: ContextEarlier, Groups: map[string]float64{}, Source: "model"}
	if v.NeedsTools != nil {
		t.NeedsTools = clamp01(*v.NeedsTools)
	}
	if k := Kind(strings.ToLower(strings.TrimSpace(v.Kind))); ValidKind(k) {
		t.Kind, t.KindConf = k, clamp01(v.KindConf)
	}
	if v.Work != nil {
		t.Work = min(max(*v.Work, 0), 3)
	}
	switch c := strings.ToLower(strings.TrimSpace(v.Context)); c {
	case ContextStandalone, ContextRecent, ContextEarlier:
		t.Context, t.ContextConf = c, clamp01(v.ContextConf)
	}
	for k, p := range v.Groups {
		t.Groups[k] = clamp01(p)
	}
	return t, nil
}

// ValidKind reports one of Kinds.
func ValidKind(k Kind) bool {
	for _, x := range Kinds {
		if x == k {
			return true
		}
	}
	return false
}
