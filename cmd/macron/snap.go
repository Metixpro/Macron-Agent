package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/memory"
	"github.com/Metixpro/macron-agent/pkg/router"
	"github.com/Metixpro/macron-agent/pkg/snap"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// Snap: a fast classifier decides, the language model works. Before a
// request reaches the model, SubQ's Snap (a decision model: typed answers in
// a fraction of a second, input-only pricing, about a hundredth of a
// language model's) reads it, with the last few turns, and says:
//
//   - its kind and how much work it is: a hard job the words did not show
//     goes to the strong model (modelroute.go). It only ever moves a request
//     up: what the words send to the strong model stays there;
//   - whether it is a conversation: a greeting, thanks or an opinion that
//     stands on its own goes with the light prompt, no tool definitions and
//     a low effort (the model can still ask for its tools, use_tools);
//   - how much of the conversation it leans on, and which groups of tools it
//     will use: when the admin turns trimming on, a self-contained request
//     goes with the last turns only (recall_history brings the rest back)
//     and the tools it needs (more_tools brings the rest).
//
// Snap never makes a request do less than it would without it: a job in
// progress (the last turns used tools or made files) gets none of the
// lighter settings, whatever Snap thinks of its words.
//
// The call starts with the request, in parallel with everything before the
// model is asked, so it costs no time; whatever fails or comes late (a
// timeout, a bad key, the service down) leaves the request exactly as it
// would have been without it, and after a few failures in a row Snap rests
// for a while. Off by default: on, it is a switch in the admin panel.

const (
	settingSnapOn      = "snap_on"
	settingSnapKey     = "snap_key" // sealed with WAZE_SECRET_KEY
	settingSnapModel   = "snap_model"
	settingSnapTimeout = "snap_timeout_ms"
	settingSnapRoute   = "snap_route"
	settingSnapEffort  = "snap_effort"
	settingSnapChat    = "snap_chat" // off unless an admin turns it on: a reply without tools cannot look anything up
	settingSnapTrim    = "snap_trim" // snapTrimAuto, snapTrimAlways or snapTrimOff

	snapTrimAuto   = "auto"   // only where the provider does not cache prompts
	snapTrimAlways = "always" // every request it fits
	snapTrimOff    = "off"

	snapTimeoutDefault = 1500 // ms: the most a request waits for the decision
	snapBreakAfter     = 3    // failures in a row before Snap rests
	snapBreakFor       = 2 * time.Minute
	snapGroupAt        = 0.3  // a tool group this likely is offered
	snapChatBelow      = 0.1  // needs no tools below this: a reply without them
	snapChatSure       = 0.7  // how sure Snap must be that it is a conversation, standing on its own
	snapChatRunes      = 280  // a longer message is more than a conversation
	snapKindAt         = 0.45 // a kind this sure routes the request
	snapCacheLow       = 0.3  // a tier caching less of its prompts than this is trimmed (auto)
	snapCacheSamples   = 5    // requests measured before the cache counts
	snapKeepStandalone = 3000 // history tokens a self-contained request keeps when trimmed
	snapKeepRecent     = 10000
	snapRecallTokens   = 6000 // what recall_history gives back at most
	snapPrice          = 0.04 // USD per million input tokens (subq-snap-1.1)
)

// snapState is the classifier's state in the bot.
type snapState struct {
	client     snap.Client
	clientOnce sync.Once
	fails      atomic.Int32
	pausedTill atomic.Int64 // unix nano: resting after failures
	cache      sync.Map     // provider ID → *cacheMeter: how much of its prompts each provider caches
	questions  map[string]snap.Question
	qOnce      sync.Once
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
	v, _ := b.snap.cache.LoadOrStore(provider, &cacheMeter{})
	v.(*cacheMeter).add(u.PromptTokens, u.CachedTokens)
}

// providerCaches reports how much of its prompts a provider caches, and
// whether that is known yet.
func (b *Bot) providerCaches(provider int64) (ratio float64, samples int, known bool) {
	v, ok := b.snap.cache.Load(provider)
	if !ok {
		return 0, 0, false
	}
	r, n := v.(*cacheMeter).get()
	return r, n, n >= snapCacheSamples
}

// snapTrims: a request to this provider goes with trimmed history and tools
// (auto: where it does not cache prompts, so what is not sent is saved).
func (b *Bot) snapTrims(provider int64) bool {
	switch b.snapTrimMode() {
	case snapTrimAlways:
		return true
	case snapTrimOff:
		return false
	}
	r, _, known := b.providerCaches(provider)
	return known && r < snapCacheLow
}

// snapKey is the panel's key, or the environment's.
func (b *Bot) snapKey() string {
	if k, err := b.st.SecretSetting(settingSnapKey); err == nil && strings.TrimSpace(k) != "" {
		return strings.TrimSpace(k)
	}
	return b.app.Config.SnapKey
}

func (b *Bot) snapModel() string {
	if v, ok := b.st.Setting(settingSnapModel); ok && v != "" {
		return v
	}
	return snap.DefaultModel
}

func (b *Bot) snapTimeout() time.Duration {
	ms := b.st.SettingInt(settingSnapTimeout, snapTimeoutDefault)
	return time.Duration(min(max(ms, 300), 5000)) * time.Millisecond
}

// snapTrimMode is off until the admin chooses: a trimmed request may miss
// something it needed, which costs more than the tokens it saves.
func (b *Bot) snapTrimMode() string {
	switch v, _ := b.st.Setting(settingSnapTrim); v {
	case snapTrimAlways, snapTrimAuto:
		return v
	}
	return snapTrimOff
}

// snapOn: switched on, with a key.
func (b *Bot) snapOn() bool {
	return b.st.SettingBool(settingSnapOn, false) && b.snapKey() != ""
}

func (b *Bot) snapClient() *snap.Client {
	b.snap.clientOnce.Do(func() {
		b.snap.client = snap.Client{BaseURL: b.app.Config.SnapURL, HTTP: &http.Client{Timeout: 10 * time.Second}}
	})
	return &b.snap.client
}

// snapKinds are the router's kinds as the kind question offers them.
var snapKinds = []snap.Option{
	{Key: string(router.Chat), About: "Conversation: greetings, thanks, feelings, opinions, advice"},
	{Key: string(router.Quick), About: "A short factual question, a definition, a price or a quick lookup"},
	{Key: string(router.Media), About: "Download, convert or find music, videos, voice or pictures, or make a picture"},
	{Key: string(router.Writing), About: "Write, rewrite, translate or summarize text"},
	{Key: string(router.Vision), About: "Describe or read a picture the user sent"},
	{Key: string(router.Code), About: "Write, fix, explain, run or review code"},
	{Key: string(router.Research), About: "Research a topic in depth with sources, compare options, write a report"},
	{Key: string(router.Reasoning), About: "Math, logic, puzzles, planning, careful step-by-step reasoning"},
	{Key: string(router.Documents), About: "Read, analyze or make documents, books, spreadsheets or data"},
	{Key: string(router.Project), About: "A big multi-step job: build an app or site, manage or fix a server, automate a workflow"},
}

// snapQuestions are the triage's questions (the same every time).
func (b *Bot) snapQuestions() map[string]snap.Question {
	b.snap.qOnce.Do(func() {
		var groups []snap.Group
		for _, g := range agent.ToolGroups() {
			groups = append(groups, snap.Group{Key: g.Key, About: g.About})
		}
		b.snap.questions = snap.TriageQuestions(snapKinds, groups)
	})
	return b.snap.questions
}

// snapCall is a triage on its way.
type snapCall struct {
	done    chan struct{}
	tri     snap.Triage
	err     error
	job     bool // the conversation is in the middle of a job (jobInProgress)
	started time.Time
	timeout time.Duration
}

// failure says why there is no decision ("" when there is one).
func (c *snapCall) failure() string {
	select {
	case <-c.done:
		if c.err != nil {
			return snapErr(c.err)
		}
		return ""
	default:
		return "timeout"
	}
}

// result waits for the decision while its time lasts (false: none).
func (c *snapCall) result() (snap.Triage, bool) {
	if c == nil {
		return snap.Triage{}, false
	}
	left := c.timeout - time.Since(c.started)
	if left <= 0 {
		select {
		case <-c.done:
		default:
			return snap.Triage{}, false
		}
	} else {
		t := time.NewTimer(left)
		defer t.Stop()
		select {
		case <-c.done:
		case <-t.C:
			return snap.Triage{}, false
		}
	}
	return c.tri, c.err == nil
}

// startSnap starts the triage of a request (nil: Snap is off, resting, or
// the request is not one it decides: a continuation, a background job, a
// voice message before its words are known, a user's own providers).
func (b *Bot) startSnap(uid int64, m *telegram.Message, text string, t tierID, eligible bool) *snapCall {
	if !eligible || strings.TrimSpace(text) == "" || !b.snapOn() {
		return nil
	}
	if till := b.snap.pausedTill.Load(); till > 0 && time.Now().UnixNano() < till {
		return nil
	}
	c := &snapCall{done: make(chan struct{}), started: time.Now(), timeout: b.snapTimeout()}
	key, model := b.snapKey(), b.snapModel()
	go func() {
		defer close(c.done)
		ctx, cancel := context.WithTimeout(b.jobCtx, c.timeout)
		defer cancel()
		var turns []snap.Turn
		c.job = true // unknown: the safe side
		if mc, err := b.mem.Load(ctx, uid); err == nil {
			c.job = jobInProgress(mc.History)
			from := max(len(mc.History)-6, 0)
			for _, h := range mc.History[from:] {
				if (h.Role == "user" || h.Role == "assistant") && strings.TrimSpace(h.Content) != "" {
					turns = append(turns, snap.Turn{Role: h.Role, Text: h.Content})
				}
			}
		}
		in := routeInput(m, text)
		var attached []string
		if in.Images > 0 {
			attached = append(attached, fmt.Sprintf("%d picture(s)", in.Images))
		}
		attached = append(attached, in.Files...)
		state := b.snapScrub(snap.TriageState(turns, in.Hint, attached, text))
		res, err := b.snapClient().Decide(ctx, key, model, state, b.snapQuestions())
		if err != nil {
			c.err = err
			b.snapFailed(err)
			return
		}
		b.snap.fails.Store(0)
		c.tri = snap.ReadTriage(res)
	}()
	return c
}

// Secrets in what Snap reads: it judges a request by its words, never needs
// a password or a key, and SubQ keeps what it is sent for a while.
var (
	snapTokenRe = regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{16,}|gh[pousr]_[a-z0-9]{20,}|github_pat_[a-z0-9_]{20,}|xox[abprs]-[a-z0-9-]{10,}|` +
		`AKIA[0-9A-Z]{16}|AIza[0-9a-z_-]{30,}|glpat-[a-z0-9_-]{20,}|\d{6,12}:[a-z0-9_-]{30,}|eyJ[a-z0-9_-]{10,}\.[a-z0-9_-]{10,}\.[a-z0-9_-]+)`)
	snapLongRe     = regexp.MustCompile(`[A-Za-z0-9+/_=-]{40,}`) // a long random string: a key, a hash, a token
	snapURLAuthRe  = regexp.MustCompile(`(://)[^/\s:@]+:[^/\s@]+@`)
	snapPasswordRe = regexp.MustCompile(`(?i)((?:password|passwd|pwd|pass|passphrase|token|secret|api[_ -]?key|پسورد|رمز(?:\s*عبور)?|توکن|کلید)\s*[:=]\s*)\S+`)
)

// pemBlock is a private key, whole.
var pemBlock = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)

// snapScrub hides secrets in what goes to Snap: the bot's own (keys, its
// token, its paths and addresses), private keys, and anything shaped like a
// key or a password.
func (b *Bot) snapScrub(s string) string {
	s = pemBlock.ReplaceAllString(b.redact(s), "[a private key]")
	s = snapURLAuthRe.ReplaceAllString(s, "${1}•••@")
	s = snapPasswordRe.ReplaceAllString(s, "${1}•••")
	s = snapTokenRe.ReplaceAllString(s, "•••")
	return snapLongRe.ReplaceAllString(s, "•••")
}

// snapFailed counts a failure; after a few in a row Snap rests a while.
func (b *Bot) snapFailed(err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	if n := b.snap.fails.Add(1); n >= snapBreakAfter {
		b.snap.fails.Store(0)
		b.snap.pausedTill.Store(time.Now().Add(snapBreakFor).UnixNano())
		log.Printf("⚡ snap: %d failures in a row (%v); requests go on without it for %s", n, err, snapBreakFor)
	}
}

// snapDecision reads the decision of a request (nil: Snap did not judge it);
// rt is how the request was routed.
func (b *Bot) snapDecision(sc *snapCall, t tierID, rt *modelRoute) *snapRecord {
	if sc == nil {
		return nil
	}
	tri, ok := sc.result()
	r := &snapRecord{tri: tri, ok: ok, tier: t, job: true}
	if ok {
		r.job = sc.job // written before done was closed
	}
	if !ok {
		r.err = sc.failure()
		return r
	}
	if rt != nil && rt.strong && len(rt.verdict.Signals) > 0 && rt.verdict.Signals[0].Why == "snap" {
		r.did(snapRouteStrong) // Snap saw a hard job the words did not
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

// What Snap changed in a request, as the log names it.
const (
	snapRouteStrong = "route_strong"
	snapDidChat     = "chat"
	snapDidTrim     = "trim"
	snapDidFocus    = "focus"
	snapDidEffort   = "effort_" // + the level
)

// lightKind: a kind the light model handles.
func lightKind(k router.Kind) bool {
	return k == router.Chat || k == router.Quick || k == router.Media || k == router.Writing || k == router.Vision
}

// snapVerdict is a triage as the router reads it (nil: not sure enough).
func snapVerdict(tri snap.Triage) *router.Verdict {
	k := router.Kind(tri.Kind)
	if tri.KindConf < snapKindAt || !validKind(k) {
		return nil
	}
	light := lightKind(k)
	hard := tri.Work >= 2.5 || (light && tri.Work >= 1.8)
	return &router.Verdict{Kind: k, Hard: hard, Signals: []router.Signal{{Why: "snap", Arg: fmt.Sprintf("%.0f%% · %.1f", tri.KindConf*100, tri.Work)}}}
}

func validKind(k router.Kind) bool {
	for _, x := range router.Kinds {
		if x == k {
			return true
		}
	}
	return false
}

// snapEffort is the effort cap of a request ("" none): only a conversation
// thinks lightly. A job keeps its provider's effort: one that thinks less
// answers sooner and does less, which no saving is worth.
func snapEffort(chat bool) string {
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

// snapChat reports a conversation that needs no tools: a greeting, thanks,
// an opinion, standing on its own, short, and sure, whose words point to no
// job either (code, a download, a server…). Anything else keeps its tools: a
// model without them writes what it would have done instead of doing it.
func snapChat(tri snap.Triage, text string) bool {
	return router.Kind(tri.Kind) == router.Chat && tri.KindConf >= snapChatSure && tri.NeedsTools < snapChatBelow && tri.Work < 1 &&
		tri.Context == snap.ContextStandalone && tri.ContextConf >= snapChatSure && utf8.RuneCountInString(strings.TrimSpace(text)) <= snapChatRunes &&
		router.Classify(router.Input{Text: text}).Kind == router.Chat
}

// snapKeep is how much history a trimmed request keeps (0: all of it).
func snapKeep(tri snap.Triage) int {
	switch {
	case tri.Context == snap.ContextStandalone && tri.ContextConf >= 0.6:
		return snapKeepStandalone
	case tri.Context == snap.ContextRecent && tri.ContextConf >= 0.5:
		return snapKeepRecent
	}
	return 0
}

// snapPick are the tools of the groups a request will likely use, with
// recall_history when its history was trimmed.
func snapPick(tri snap.Triage, trimmed bool) []string {
	pick := []string{}
	for _, g := range agent.ToolGroups() {
		if tri.Groups[g.Key] >= snapGroupAt {
			pick = append(pick, g.Tools...)
		}
	}
	if trimmed {
		pick = append(pick, toolRecallHistory)
	}
	return pick
}

// snapHint tells the model that older turns were left out, and how to get
// them back. Nothing else of the decision reaches it: a note that a job
// looks small makes a model do less of it.
func snapHint(trimmed int) string {
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

// historyTokens estimates what turns cost a call.
func historyTokens(h []llm.Message) int {
	n := 0
	for _, m := range h {
		n += memory.EstimateTokens(m.Content) + 4
	}
	return n
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
			for i := len(dropped) - 1; i >= 0 && used < snapRecallTokens; i-- { // newest first
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

// snapRecord is what Snap changed in one request, for the log.
type snapRecord struct {
	tri     snap.Triage
	ok      bool
	err     string
	applied []string
	history int  // history tokens not sent, per model call
	tools   bool // the tools not offered are Snap's doing (chat, or its focus)
	job     bool // in the middle of a job: none of the lighter settings
	tier    tierID
}

func (r *snapRecord) did(what string) { r.applied = append(r.applied, what) }

// saveSnap logs a decision once the request is done (res: its outcome, nil
// when it failed early).
func (b *Bot) saveSnap(r *snapRecord, res *agent.Result) {
	if r == nil {
		return
	}
	l := store.SnapLog{Tier: string(r.tier), Applied: r.applied, Error: r.err}
	if r.ok {
		l.Kind, l.KindConf, l.NeedsTools, l.Work, l.Context = r.tri.Kind, r.tri.KindConf, r.tri.NeedsTools, r.tri.Work, r.tri.Context
		l.LatencyMS, l.SnapTokens = int(r.tri.Took.Milliseconds()), r.tri.InputTokens
		if res != nil {
			l.SavedTokens = r.history * max(res.Steps, 1)
			if r.tools {
				l.SavedTokens += res.ToolsUnsent
			}
		}
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ctx, cancel := b.ctx(10 * time.Second)
		defer cancel()
		if err := b.st.AddSnapLog(ctx, l); err != nil {
			log.Printf("snap log: %v", err)
		}
	}()
}

// snapErr is a failure as the log keeps it (no key, no address).
func snapErr(err error) string {
	var e *snap.Error
	switch {
	case errors.As(err, &e):
		return strconv.Itoa(e.Status) + " " + e.Type
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "unreachable"
}

// snapDecides says whether one of Snap's decisions is on. Every one is, but
// the reply without tools: an admin turns that on for its saving, knowing a
// message read as chat then cannot search, download or look anything up.
func (b *Bot) snapDecides(setting string) bool {
	return b.st.SettingBool(setting, setting != settingSnapChat)
}
