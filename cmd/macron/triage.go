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
	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/memory"
	"github.com/Metixpro/macron-agent/pkg/router"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const (
	settingTriage     = "triage"
	settingTriageChat = "triage_chat"
	settingTriageTrim = "triage_trim"

	triageOff   = "off"
	triageLocal = "local"
	triageModel = "model"

	triageTrimAuto   = "auto"
	triageTrimAlways = "always"
	triageTrimOff    = "off"

	triageTimeout        = 4 * time.Second
	triageBreakAfter     = 3
	triageBreakFor       = 2 * time.Minute
	triageGroupAt        = 0.3
	triageChatBelow      = 0.1
	triageChatSure       = 0.7
	triageChatRunes      = 280
	triageKindAt         = 0.45
	triageCacheLow       = 0.3
	triageCacheSamples   = 5
	triageKeepStandalone = 3000
	triageKeepRecent     = 10000
	triageRecallTokens   = 6000
)

type triageState struct {
	fails      atomic.Int32
	pausedTill atomic.Int64
	cache      sync.Map
}

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

func (b *Bot) noteCache(provider int64, u llm.Usage) {
	if provider == 0 {
		return
	}
	v, _ := b.triage.cache.LoadOrStore(provider, &cacheMeter{})
	v.(*cacheMeter).add(u.PromptTokens, u.CachedTokens)
}

func (b *Bot) providerCaches(provider int64) (ratio float64, samples int, known bool) {
	v, ok := b.triage.cache.Load(provider)
	if !ok {
		return 0, 0, false
	}
	r, n := v.(*cacheMeter).get()
	return r, n, n >= triageCacheSamples
}

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

func (b *Bot) triageTrimMode() string {
	switch v, _ := b.st.Setting(settingTriageTrim); v {
	case triageTrimAlways, triageTrimAuto:
		return v
	}
	return triageTrimOff
}

func (b *Bot) triageMode() string {
	switch v, _ := b.st.Setting(settingTriage); v {
	case triageLocal, triageModel:
		return v
	}
	return triageOff
}

func (b *Bot) triageChatOn() bool { return b.st.SettingBool(settingTriageChat, false) }

func triageGroups() []router.Group {
	var out []router.Group
	for _, g := range agent.ToolGroups() {
		out = append(out, router.Group{Key: g.Key, About: g.About})
	}
	return out
}

type triageCall struct {
	done chan struct{}
	tri  router.Triage
	err  error
	job  bool
}

func (c *triageCall) result() (router.Triage, bool) {
	if c == nil {
		return router.Triage{}, false
	}
	<-c.done
	return c.tri, true
}

func (b *Bot) startTriage(uid int64, m *telegram.Message, text string, eligible bool) *triageCall {
	mode := b.triageMode()
	if !eligible || strings.TrimSpace(text) == "" || mode == triageOff {
		return nil
	}
	c := &triageCall{done: make(chan struct{})}
	go func() {
		defer guard.Recover("triage")
		defer close(c.done)
		ctx, cancel := context.WithTimeout(b.jobCtx, triageTimeout)
		defer cancel()
		var turns []router.Turn
		c.job = true
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
		r.did(triageRouteStrong)
	}
	return r
}

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

const (
	triageRouteStrong = "route_strong"
	triageDidChat     = "chat"
	triageDidTrim     = "trim"
	triageDidFocus    = "focus"
	triageDidEffort   = "effort_"
)

func lightKind(k router.Kind) bool {
	return k == router.Chat || k == router.Quick || k == router.Media || k == router.Writing || k == router.Vision
}

func triageVerdict(tri router.Triage) *router.Verdict {
	k := tri.Kind
	if tri.KindConf < triageKindAt || !router.ValidKind(k) {
		return nil
	}
	light := lightKind(k)
	hard := tri.Work >= 2.5 || (light && tri.Work >= 1.8)
	return &router.Verdict{Kind: k, Hard: hard, Signals: []router.Signal{{Why: "triage", Arg: fmt.Sprintf("%.0f%% · %.1f", tri.KindConf*100, tri.Work)}}}
}

func triageEffort(chat bool) string {
	if chat {
		return llm.EffortLow
	}
	return ""
}

func effortLowered(given, limit string) bool {
	g, c := slices.Index(llm.Efforts, given), slices.Index(llm.Efforts, limit)
	return g >= 0 && c >= 0 && c < g
}

func triageChat(tri router.Triage, text string) bool {
	return tri.Kind == router.Chat && tri.KindConf >= triageChatSure && tri.NeedsTools < triageChatBelow && tri.Work < 1 &&
		tri.Context == router.ContextStandalone && tri.ContextConf >= triageChatSure && utf8.RuneCountInString(strings.TrimSpace(text)) <= triageChatRunes &&
		router.Classify(router.Input{Text: text}).Kind == router.Chat
}

func triageKeep(tri router.Triage) int {
	switch {
	case tri.Context == router.ContextStandalone && tri.ContextConf >= 0.6:
		return triageKeepStandalone
	case tri.Context == router.ContextRecent && tri.ContextConf >= 0.5:
		return triageKeepRecent
	}
	return 0
}

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

func triageHint(trimmed int) string {
	return fmt.Sprintf("[history] %d older messages of this conversation are not shown here. If the user refers to something you "+
		"cannot see (a file, a name, an earlier answer), call %s first.", trimmed, toolRecallHistory)
}

const toolRecallHistory = "recall_history"

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
	for start < len(h) && h[start].Role != "user" {
		start++
	}
	return h[start:], h[:start]
}

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
			for i := len(dropped) - 1; i >= 0 && used < triageRecallTokens; i-- {
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

type triageRecord struct {
	tri     router.Triage
	ok      bool
	err     string
	applied []string
	job     bool
}

func (r *triageRecord) did(what string) { r.applied = append(r.applied, what) }

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
