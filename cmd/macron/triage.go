package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/memory"
	"github.com/Metixpro/macron-agent/pkg/router"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// Triage: before a request reaches the model, the bot decides what it needs,
// on this machine. Nothing is sent to an outside service: the words are read
// here (router.Local), or, when the owner chooses, the agent's own model is
// asked the same questions in one short call (router.TriagePrompt). It says:
//
//   - its kind and how much work it is: a hard job the words did not show
//     goes to the strong model (modelroute.go). It only ever moves a request
//     up: what the words send to the strong model stays there;
//   - whether it is a conversation: a greeting, thanks or an opinion that
//     stands on its own goes with the light prompt, no tool definitions and
//     a low effort (the model can still ask for its tools, use_tools);
//   - how much of the conversation it leans on, and which groups of tools it
//     will use: when the owner turns trimming on, a self-contained request
//     goes with the last turns only (recall_history brings the rest back)
//     and the tools it needs (more_tools brings the rest).
//
// The triage never makes a request do less than it would without it: a job
// in progress (the last turns used tools or made files) gets none of the
// lighter settings, whatever its words look like.
//
// It starts with the request, in parallel with everything before the model
// is asked. A model triage that fails or comes late falls back to the local
// one; after a few failures in a row it rests for a while. Off by default.

const (
	settingTriage     = "triage"      // triageOff, triageLocal or triageModel
	settingTriageChat = "triage_chat" // off unless the owner turns it on: a reply without tools cannot look anything up
	settingTriageTrim = "triage_trim" // triageTrimAuto, triageTrimAlways or triageTrimOff

	triageOff   = "off"
	triageLocal = "local" // the words, read here: free and instant
	triageModel = "model" // one short call to the agent's own model

	triageTrimAuto   = "auto"   // only where the provider does not cache prompts
	triageTrimAlways = "always" // every request it fits
	triageTrimOff    = "off"

	triageTimeout        = 4 * time.Second // the most a request waits for a model's decision
	triageBreakAfter     = 3               // model failures in a row before it rests
	triageBreakFor       = 2 * time.Minute
	triageGroupAt        = 0.3  // a tool group this likely is offered
	triageChatBelow      = 0.1  // needs no tools below this: a reply without them
	triageChatSure       = 0.7  // how sure it must be that it is a conversation, standing on its own
	triageChatRunes      = 280  // a longer message is more than a conversation
	triageKindAt         = 0.45 // a kind this sure routes the request
	triageCacheLow       = 0.3  // a tier caching less of its prompts than this is trimmed (auto)
	triageCacheSamples   = 5    // requests measured before the cache counts
	triageKeepStandalone = 3000 // history tokens a self-contained request keeps when trimmed
	triageKeepRecent     = 10000
	triageRecallTokens   = 6000 // what recall_history gives back at most
)

// triageState is the triage's state in the bot.
type triageState struct {
	fails      atomic.Int32
	pausedTill atomic.Int64 // unix nano: the model triage rests after failures
	cache      sync.Map     // provider ID → *cacheMeter: how much of its prompts each provider caches
}

// cacheMeter follows how much of a provider's prompt tokens it reads from
// its cache (an average of the latest requests).
type cacheMeter struct {
	mu      sync.Mutex
	ratio   float64
	samples int
}

func (c *cacheMeter) add(prompt, cached int) {
	if prompt <= 0 {
		return
	}
	r := min(float64(cached)/float64(prompt), 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.samples == 0 {
		c.ratio = r
	} else {
		c.ratio = c.ratio*0.8 + r*0.2
	}
	c.samples++
}

func (c *cacheMeter) get() (float64, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ratio, c.samples
}

// noteCache measures the cached share of a request a provider answered
// (whole requests only: a trimmed or focused one caches less by its nature).
func (b *Bot) noteCache(provider int64, u llm.Usage) {
	if provider == 0 {
		return
	}
	v, _ := b.triage.cache.LoadOrStore(provider, &cacheMeter{})
	v.(*cacheMeter).add(u.PromptTokens, u.CachedTokens)
}

// providerCaches reports how much of its prompts a provider caches, and
// whether that is known yet.
func (b *Bot) providerCaches(provider int64) (ratio float64, samples int, known bool) {
	v, ok := b.triage.cache.Load(provider)
	if !ok {
		return 0, 0, false
	}
	r, n := v.(*cacheMeter).get()
	return r, n, n >= triageCacheSamples
}

// triageTrims: a request to this provider goes with trimmed history and tools
// (auto: where it does not cache prompts, so what is not sent is saved).
func (b *Bot) triageTrims(provider int64) bool {
	switch b.triageTrimMode() {
	case triageTrimAlways:
		return true
	case triageTrimOff:
		return false
	}
	r, _, known := b.providerCaches(provider)
	return known && r < triageCacheLow
}

// triageTrimMode is off until the owner chooses: a trimmed request may miss
// something it needed, which costs more than the tokens it saves.
func (b *Bot) triageTrimMode() string {
	switch v, _ := b.st.Setting(settingTriageTrim); v {
	case triageTrimAlways, triageTrimAuto:
		return v
	}
	return triageTrimOff
}

// triageMode is how requests are triaged (off by default).
func (b *Bot) triageMode() string {
	switch v, _ := b.st.Setting(settingTriage); v {
	case triageLocal, triageModel:
		return v
	}
	return triageOff
}

// triageChatOn: a conversation may go without its tools (off by default).
func (b *Bot) triageChatOn() bool { return b.st.SettingBool(settingTriageChat, false) }

// triageGroups are the tool groups a model triage is asked about.
func triageGroups() []router.Group {
	var out []router.Group
	for _, g := range agent.ToolGroups() {
		out = append(out, router.Group{Key: g.Key, About: g.About})
	}
	return out
}

// triageCall is a triage on its way.
type triageCall struct {
	done chan struct{}
	tri  router.Triage
	err  error // the model triage failed (tri is then the local one)
	job  bool  // the conversation is in the middle of a job (jobInProgress)
}

// result waits for the decision, which always comes (a model triage is
// bounded by triageTimeout and falls back to the local one).
func (c *triageCall) result() (router.Triage, bool) {
	if c == nil {
		return router.Triage{}, false
	}
	<-c.done
	return c.tri, true
}

// startTriage starts the triage of a request (nil: the triage is off, or the
// request is not one it decides: a continuation, a background job, a voice
// message before its words are known, a user's own providers).
func (b *Bot) startTriage(uid int64, m *telegram.Message, text string, eligible bool) *triageCall {
	mode := b.triageMode()
	if !eligible || strings.TrimSpace(text) == "" || mode == triageOff {
		return nil
	}
	c := &triageCall{done: make(chan struct{})}
	go func() {
		defer close(c.done)
		ctx, cancel := context.WithTimeout(b.jobCtx, triageTimeout)
		defer cancel()
		var turns []router.Turn
		c.job = true // unknown: the safe side
		if mc, err := b.mem.Load(ctx, uid); err == nil {
			c.job = jobInProgress(mc.History)
			from := max(len(mc.History)-6, 0)
			for _, h := range mc.History[from:] {
				if (h.Role == "user" || h.Role == "assistant") && strings.TrimSpace(h.Content) != "" {
					turns = append(turns, router.Turn{Role: h.Role, Text: h.Content})
				}
			}
		}
		in := routeInput(m, text)
		c.tri = router.Local(router.TriageInput{Input: in, History: turns})
		if mode != triageModel {
			return
		}
		if till := b.triage.pausedTill.Load(); till > 0 && time.Now().UnixNano() < till {
			return
		}
		var attached []string
		if in.Images > 0 {
			attached = append(attached, fmt.Sprintf("%d picture(s)", in.Images))
		}
		attached = append(attached, in.Files...)
		tri, err := b.modelTriage(ctx, router.TriageState(turns, in.Hint, attached, text))
		if err != nil {
			c.err = err
			b.triageFailed(err)
			return
		}
		b.triage.fails.Store(0)
		c.tri = tri
	}()
	return c
}

// modelTriage asks the agent's own model the triage's questions.
func (b *Bot) modelTriage(ctx context.Context, state string) (router.Triage, error) {
	start := time.Now()
	resp, err := b.prov.Complete(ctx, llm.Request{
		Messages:  []llm.Message{{Role: "user", Content: router.TriagePrompt(state, triageGroups())}},
		MaxTokens: 300,
	})
	if err != nil {
		return router.Triage{}, err
	}
	tri, err := router.ParseTriage(resp.Message.Content)
	tri.Took = time.Since(start)
	return tri, err
}

// triageFailed counts a model failure; after a few in a row it rests a while
// (the local triage decides meanwhile).
func (b *Bot) triageFailed(err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	if n := b.triage.fails.Add(1); n >= triageBreakAfter {
		b.triage.fails.Store(0)
		b.triage.pausedTill.Store(time.Now().Add(triageBreakFor).UnixNano())
		log.Printf("🧭 triage: %d model failures in a row (%v); the local triage decides for %s", n, err, triageBreakFor)
	}
}

// triageDecision reads the decision of a request (nil: it was not triaged);
// rt is how the request was routed.
func (b *Bot) triageDecision(tc *triageCall, rt *modelRoute) *triageRecord {
	if tc == nil {
		return nil
	}
	tri, ok := tc.result()
	r := &triageRecord{tri: tri, ok: ok, job: tc.job}
	if tc.err != nil {
		r.err = shortText(tc.err.Error(), 80)
	}
	if rt != nil && rt.strong && len(rt.verdict.Signals) > 0 && rt.verdict.Signals[0].Why == "triage" {
		r.did(triageRouteStrong) // a hard job the words did not show
	}
	return r
}

// jobInProgress reports a conversation in the middle of a job: one of the
// last turns used tools, made or sent files, or stopped unfinished. Its next
// message («بهترش کن», a list of changes) is more of that job, whatever
// it looks like on its own.
func jobInProgress(history []llm.Message) bool {
	from := max(len(history)-6, 0)
	for _, m := range history[from:] {
		if len(m.ToolCalls) > 0 || m.Role == "tool" {
			return true
		}
		if m.Role == "assistant" && (strings.Contains(m.Content, "<work_notes>") || strings.Contains(m.Content, "<files_sent>") ||
			strings.Contains(m.Content, "[work log before stopping")) {
			return true
		}
	}
	return false
}

// What the triage changed in a request, as the log names it.
const (
	triageRouteStrong = "route_strong"
	triageDidChat     = "chat"
	triageDidTrim     = "trim"
	triageDidFocus    = "focus"
	triageDidEffort   = "effort_" // + the level
)

// lightKind: a kind the light model handles.
func lightKind(k router.Kind) bool {
	return k == router.Chat || k == router.Quick || k == router.Media || k == router.Writing || k == router.Vision
}

// triageVerdict is a triage as the router reads it (nil: not sure enough).
func triageVerdict(tri router.Triage) *router.Verdict {
	k := tri.Kind
	if tri.KindConf < triageKindAt || !router.ValidKind(k) {
		return nil
	}
	light := lightKind(k)
	hard := tri.Work >= 2.5 || (light && tri.Work >= 1.8)
	return &router.Verdict{Kind: k, Hard: hard, Signals: []router.Signal{{Why: "triage", Arg: fmt.Sprintf("%.0f%% · %.1f", tri.KindConf*100, tri.Work)}}}
}

// triageEffort is the effort cap of a request ("" none): only a conversation
// thinks lightly. A job keeps its provider's effort: one that thinks less
// answers sooner and does less, which no saving is worth.
func triageEffort(chat bool) string {
	if chat {
		return llm.EffortLow
	}
	return ""
}

// effortLowered reports a cap below the effort a provider was given (a
// provider left to its model's default is not capped).
func effortLowered(given, limit string) bool {
	g, c := slices.Index(llm.Efforts, given), slices.Index(llm.Efforts, limit)
	return g >= 0 && c >= 0 && c < g
}

// triageChat reports a conversation that needs no tools: a greeting, thanks,
// an opinion, standing on its own, short, and sure, whose words point to no
// job either (code, a download, a server…). Anything else keeps its tools: a
// model without them writes what it would have done instead of doing it.
func triageChat(tri router.Triage, text string) bool {
	return tri.Kind == router.Chat && tri.KindConf >= triageChatSure && tri.NeedsTools < triageChatBelow && tri.Work < 1 &&
		tri.Context == router.ContextStandalone && tri.ContextConf >= triageChatSure && utf8.RuneCountInString(strings.TrimSpace(text)) <= triageChatRunes &&
		router.Classify(router.Input{Text: text}).Kind == router.Chat
}

// triageKeep is how much history a trimmed request keeps (0: all of it).
func triageKeep(tri router.Triage) int {
	switch {
	case tri.Context == router.ContextStandalone && tri.ContextConf >= 0.6:
		return triageKeepStandalone
	case tri.Context == router.ContextRecent && tri.ContextConf >= 0.5:
		return triageKeepRecent
	}
	return 0
}

// triagePick are the tools of the groups a request will likely use, with
// recall_history when its history was trimmed.
func triagePick(tri router.Triage, trimmed bool) []string {
	pick := []string{}
	for _, g := range agent.ToolGroups() {
		if tri.Groups[g.Key] >= triageGroupAt {
			pick = append(pick, g.Tools...)
		}
	}
	if trimmed {
		pick = append(pick, toolRecallHistory)
	}
	return pick
}

// triageHint tells the model that older turns were left out, and how to get
// them back. Nothing else of the decision reaches it: a note that a job
// looks small makes a model do less of it.
func triageHint(trimmed int) string {
	return fmt.Sprintf("[history] %d older messages of this conversation are not shown here. If the user refers to something you "+
		"cannot see (a file, a name, an earlier answer), call %s first.", trimmed, toolRecallHistory)
}

// ---------------------------------------------------------------- trimmed history

const toolRecallHistory = "recall_history"

// splitHistory keeps the latest turns within budget tokens (whole turns,
// from a user message on) and returns the older ones apart.
func splitHistory(h []llm.Message, budget int) (kept, dropped []llm.Message) {
	total := 0
	start := len(h)
	for i := len(h) - 1; i >= 0; i-- {
		total += memory.EstimateTokens(h[i].Content) + 4
		if total > budget {
			break
		}
		start = i
	}
	for start < len(h) && h[start].Role != "user" { // a turn starts with the user
		start++
	}
	return h[start:], h[:start]
}

// recallTool gives back the turns a trimmed request left out.
func recallTool(dropped []llm.Message) agent.LocalTool {
	return agent.LocalTool{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name: toolRecallHistory,
			Description: "The older messages of this conversation, left out of this request. Call it when the user refers to " +
				"something you cannot see (a file, a name, an earlier answer).",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Words to look for (optional): only the messages with them"},
			}},
		}},
		Handler: func(_ context.Context, args map[string]any) (agent.ToolOutput, error) {
			q, _ := args["query"].(string)
			words := strings.Fields(strings.ToLower(q))
			var lines []string
			used := 0
			for i := len(dropped) - 1; i >= 0 && used < triageRecallTokens; i-- { // newest first
				m := dropped[i]
				if strings.TrimSpace(m.Content) == "" || (m.Role != "user" && m.Role != "assistant") {
					continue
				}
				if len(words) > 0 {
					low := strings.ToLower(m.Content)
					found := false
					for _, w := range words {
						found = found || strings.Contains(low, w)
					}
					if !found {
						continue
					}
				}
				line := m.Role + ": " + m.Content
				used += memory.EstimateTokens(line)
				lines = append([]string{line}, lines...)
			}
			if len(lines) == 0 {
				return agent.ToolOutput{Text: "Nothing in the older messages matches; the summary in [memory] covers what came before them."}, nil
			}
			return agent.ToolOutput{Text: "Older messages (oldest first):\n\n" + strings.Join(lines, "\n\n")}, nil
		},
	}
}

// ---------------------------------------------------------------- the record

// triageRecord is what the triage changed in one request, for the log.
type triageRecord struct {
	tri     router.Triage
	ok      bool
	err     string
	applied []string
	job     bool // in the middle of a job: none of the lighter settings
}

func (r *triageRecord) did(what string) { r.applied = append(r.applied, what) }

// logTriage writes what a decision changed, once the request is done.
func logTriage(r *triageRecord) {
	if r == nil || (len(r.applied) == 0 && r.err == "") {
		return
	}
	line := "🧭 triage: " + r.tri.String()
	if len(r.applied) > 0 {
		line += " → " + strings.Join(r.applied, ", ")
	}
	if r.err != "" {
		line += " (model failed: " + r.err + ")"
	}
	log.Print(line)
}
