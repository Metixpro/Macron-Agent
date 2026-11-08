package snap

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Triage is what a request needs, in one call: whether it needs tools at
// all, its kind (the router's kinds), how much work it is, how much of the
// earlier conversation it leans on, and which groups of tools it will use.
// The bot turns it into its settings for the request (the model, how hard it
// thinks, the tools and history it gets); the language model does the work.

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
	Kind        string  // one of the kinds asked about
	KindConf    float64
	Work        float64 // 0 a one-line answer, 1 a quick step or two, 2 several steps, 3 a long project
	Context     string  // ContextStandalone, ContextRecent or ContextEarlier
	ContextConf float64
	Groups      map[string]float64 // each group's probability of being needed
	InputTokens int
	Took        time.Duration
}

// Question names.
const (
	qTools   = "needs_tools"
	qKind    = "kind"
	qWork    = "work"
	qContext = "context"
	qGroup   = "g_"
)

// TriageQuestions are the questions of a triage: kinds are the options of
// the kind question, groups the tool groups asked about one by one.
func TriageQuestions(kinds []Option, groups []Group) map[string]Question {
	qs := map[string]Question{
		qTools: Noul("To answer the newest message, is an action needed, not just a written reply?",
			"Running code; reading, making or sending files; fresh facts from the internet (prices, news, weather, anything that "+
				"changes); downloading; making or editing pictures; the user's servers, GitHub, reminders or Telegram groups",
			"Conversation, thanks, an opinion or advice, explaining something, writing or translating text, or anything a "+
				"well-read assistant knows by heart"),
		qWork: Score("How much work does the newest message ask for?",
			"A one-line answer", "One or two quick steps", "Several steps", "A long multi-step project"),
		qContext: Choice("To understand and do the newest message, how much of the earlier conversation is needed?",
			Option{ContextStandalone, "None: a new request that makes sense on its own"},
			Option{ContextRecent, "The last few messages: it continues or answers what was just said (yes, go on, that file, do it)"},
			Option{ContextEarlier, "Something from much earlier, or a long job that is still going on"}),
	}
	if len(kinds) >= 2 {
		qs[qKind] = Choice("What kind of job is the newest message?", kinds...)
	}
	for _, g := range groups {
		qs[qGroup+g.Key] = Noul("Will doing the newest message need this: "+g.About+"?", "Yes, this is part of the job", "No")
	}
	return qs
}

// ReadTriage reads a decision; what is missing reads as the safe side (tools
// needed, the whole conversation, real work).
func ReadTriage(r *Result) Triage {
	t := Triage{NeedsTools: 1, Work: 2, Context: ContextEarlier, Groups: map[string]float64{}, InputTokens: r.Usage.InputTokens, Took: r.Took}
	for name, a := range r.Answers {
		switch {
		case name == qTools && a.Type == TypeNoul:
			t.NeedsTools = clamp01(a.Noul)
		case name == qKind && a.Type == TypeChoice:
			t.Kind, t.KindConf = a.Choice, clamp01(a.Confidence)
		case name == qWork && a.Type == TypeScore:
			t.Work = a.Score
		case name == qContext && a.Type == TypeChoice && a.Choice != "":
			t.Context, t.ContextConf = a.Choice, clamp01(a.Confidence)
		case strings.HasPrefix(name, qGroup) && a.Type == TypeNoul:
			t.Groups[strings.TrimPrefix(name, qGroup)] = clamp01(a.Noul)
		}
	}
	return t
}

func clamp01(f float64) float64 { return min(max(f, 0), 1) }

// Picked are the groups at or above a probability.
func (t Triage) Picked(threshold float64) []string {
	var out []string
	for k, p := range t.Groups {
		if p >= threshold {
			out = append(out, k)
		}
	}
	return out
}

// Limits of the state a triage sends: enough to judge, small enough to be fast.
const (
	stateTurnRunes    = 300
	stateHistoryRunes = 2400
	stateReplyRunes   = 400
	stateMessageRunes = 4000
)

// TriageState lays out what the questions judge: the last turns of the
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

// String sums a triage up for logs and the admin's test.
func (t Triage) String() string {
	return fmt.Sprintf("tools %.0f%% · %s %.0f%% · work %.1f · %s · %s", t.NeedsTools*100, t.Kind, t.KindConf*100, t.Work, t.Context, t.Took.Round(time.Millisecond))
}
