package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/memory"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/router"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

const (
	toolReact        = "react_to_message"
	toolProfilePhoto = "look_at_profile_photo"
	toolViewImage    = "view_image"
	toolSendFile     = "send_file"
	toolAskUser      = "ask_user"

	maxVisionImage = 8 << 20
)

var (
	errTooBig        = errors.New("file too big")
	errWorkspaceFull = errors.New("the workspace is full")
)

// promptOptions changes how one request is handled.
type promptOptions struct {
	free     bool             // does not count against the daily limit (an automatic continuation, a poll vote)
	vote     bool             // a poll vote: on a VIP demo it takes a demo request, never a free ride
	noReact  bool             // no reactions (m is the bot's own message)
	about    string           // extra context for the model
	resume   *agent.Suspended // a poll's answer: the job that asked, as it stopped
	noMemory bool             // no conversation history, nothing remembered (scheduled tasks)
	schedule *store.Schedule  // run as this scheduled task
	prefix   string           // put before the answer (the title of a scheduled task)
	noFlood  bool             // the flood limit was already checked (a message sent during a job)
	pending  []*interjection  // messages that join this request at its first step
	bridge   *activeRun       // the previous round's run, taken over by this one
	round    int              // auto-continue: 0 for the request itself, then 1, 2…
	rounds   int              // auto-continue: how many extra rounds the job may take
	tier     tierID           // the tier the first round ran on (a continuation keeps it)
	job      *jobTracker      // auto-continue: the job the first round started (see openjob.go)
	bg       *bgRun           // run as this background task (see bgtasks.go)
}

// errNotDone reports a request that ended without an answer (busy, quota,
// model down…); the user was already told why.
var errNotDone = errors.New("request not completed")

// responder delivers what one request produces: as messages in the chat
// (replying to the user's message when there is one), or, for a guest query,
// as its single answer.
type responder struct {
	b       *Bot
	m       *telegram.Message
	guestID string
}

// notice sends a short system text (busy, limit, error…).
func (r *responder) notice(ctx context.Context, html string) {
	if r.guestID != "" {
		r.b.answerGuestHTML(ctx, r.guestID, html, nil)
		return
	}
	r.b.sendHTML(ctx, r.m.Chat.ID, html, telegram.SendOptions{ReplyTo: r.m.MessageID})
}

// answer sends the assistant's Markdown answer.
func (r *responder) answer(ctx context.Context, md string) {
	if r.guestID != "" {
		r.b.answerGuestMarkdown(ctx, r.guestID, md, r.b.langOf(r.m.From))
		return
	}
	r.b.sendAnswer(ctx, r.m, md)
}

// promptOutcome is what a round of a request leaves for the next one.
type promptOutcome struct {
	goOn      bool            // the budget ran out and the job continues by itself
	leftovers []*interjection // messages that came after the answer
	bridge    *activeRun      // the job goes on: its run stays open until the next round takes it over
	tier      tierID          // the tier it ran on
	job       *jobTracker     // the job as written down (nil: not tracked)
}

// betweenRounds runs between two rounds of a job that goes on by itself (for the tests).
var betweenRounds func()

// answerSendBudget is the time to deliver an answer once the work is done (a
// variable for the tests).
var answerSendBudget = 5 * time.Minute

// handlePrompt answers one request. A long job whose budget runs out goes on
// by itself for a few more rounds (see autoRounds), and the messages the user
// sent after the answer was written are handled right after it.
func (b *Bot) handlePrompt(u *store.User, m *telegram.Message, text string, opt promptOptions) error {
	if opt.round == 0 {
		opt.rounds = b.autoRounds()
	}
	out, err := b.runPrompt(u, m, text, opt)
	job := out.job
	// Whatever ended the rounds, the job never stays "running" (a no-op once it ended).
	defer func() { job.interrupt(store.JobFailed) }()
	for err == nil && out.goOn {
		if betweenRounds != nil {
			betweenRounds()
		}
		if out.bridge != nil && out.bridge.stopped.Load() { // stopped between the rounds
			out.leftovers = append(out.leftovers, out.bridge.finish()...)
			b.runs.CompareAndDelete(u.ID, out.bridge)
			job.interrupt(store.JobStopped)
			break
		}
		l := b.langOf(m.From)
		next := promptOptions{free: true, noReact: true, noFlood: true, pending: out.leftovers, bridge: out.bridge, round: opt.round + 1, rounds: opt.rounds, tier: out.tier, job: job,
			about: fmt.Sprintf("This is an automatic continuation (round %d of up to %d) of the job in the conversation above: its previous "+
				"round ran out of budget. Carry on from the work log where it stopped and do not redo finished steps (look at the workspace "+
				"if unsure). When everything is done, give the final answer for the whole job.", opt.round+2, opt.rounds+1)}
		opt = next
		out, err = b.runPrompt(u, m, l.t("ادامه بده"), opt)
		if out.job != nil {
			job = out.job
		}
	}
	b.followUp(u, out.leftovers)
	return err
}

// runPrompt is one round of a request.
func (b *Bot) runPrompt(u *store.User, m *telegram.Message, text string, opt promptOptions) (out promptOutcome, _ error) {
	uid := u.ID
	defer func() {
		if opt.bridge != nil { // this round ended before it took the job over
			out.leftovers = append(out.leftovers, opt.bridge.finish()...)
			b.runs.CompareAndDelete(uid, opt.bridge)
		}
	}()
	admin := b.isAdmin(uid)
	key := strconv.FormatInt(uid, 10)
	scheduled := opt.schedule != nil
	bg := opt.bg != nil
	detached := scheduled || bg // runs on its own: nobody waits on it in the chat
	guest := m.GuestQueryID != ""
	resp := &responder{b: b, m: m, guestID: m.GuestQueryID}
	l := b.langOf(m.From)
	nctx, ncancel := b.ctx(30 * time.Second)
	defer ncancel()
	say := func(html string) (promptOutcome, error) {
		resp.notice(nctx, html)
		return promptOutcome{}, errNotDone
	}

	if !admin && !detached && !opt.noFlood {
		if ok, wait := b.flood.Allow(key); !ok {
			return say(l.f(textFloodWait, int(wait.Seconds())+1))
		}
	}
	if opt.round == 0 && !detached && !opt.vote {
		b.noteRequest(nctx, uid, m.Chat.ID, text) // a complaint about the last answer, or the same request again
	}
	st := standing{tier: tierOwner}
	// The triage starts now, beside the queue and the checks below: its
	// decision is ready by the time the request needs it (triage.go).
	tc := b.startTriage(uid, m, text, !detached && opt.round == 0 && !opt.vote && opt.bridge == nil)
	var release func()
	if detached {
		// A scheduled or background task never blocks its owner, and waits for a free slot.
		slot := fmt.Sprintf("bg-%d", opt.bg.taskID())
		if scheduled {
			slot = fmt.Sprintf("sched-%d", opt.schedule.ID)
		}
		var err error
		if release, err = b.acquireScheduled(slot); err != nil {
			opt.bg.end(store.BgFailed, "the server was too busy to start it")
			if bg {
				say(l.f(textBgNoSlot, esc(opt.bg.task.Title)))
			}
			return promptOutcome{}, err
		}
	} else {
		var why guard.Reason
		release, why = b.acquireQueued(key, resp, l)
		switch why {
		case guard.KeyBusy:
			// The job that holds the key may still be starting (not yet open to
			// messages): by now it is, and this message joins it.
			if opt.round == 0 && !opt.vote && opt.bridge == nil && b.interject(u, m, text) {
				return promptOutcome{}, errNotDone
			}
			return say(l.t(textBusy))
		case guard.AtCapacity:
			return say(l.t(textCapacity))
		}
	}
	defer release()

	if !b.prov.Configured() {
		return say(l.t(textProviderMissing))
	}

	out.tier, opt.tier = st.tier, st.tier
	conf := b.tier(st.tier)
	window := b.windowOf(st)
	acc := &providers.Account{UserID: uid, Tier: string(st.tier)} // every model call of the request is metered on it
	// The day's figures (the stats): a new request counts, and gives itself
	// back when the agent failed on its own side; the tokens it used are added.
	counted := !opt.free
	refund := false
	if counted {
		cctx, ccancel := b.ctx(10 * time.Second)
		if err := b.st.CountRequest(cctx, uid); err != nil {
			log.Printf("[user %d] counting a request: %v", uid, err)
		} else {
			refund = true
		}
		ccancel()
	}
	defer func() {
		ctx, cancel := b.ctx(10 * time.Second)
		defer cancel()
		if refund {
			if err := b.st.Refund(ctx, uid); err != nil {
				log.Printf("[user %d] giving back a request: %v", uid, err)
			}
			return
		}
		if used := acc.Billable(); used > 0 {
			_ = b.st.AddTokens(ctx, uid, used)
		}
	}()

	maxSteps, runTimeout := b.agentBudget()
	if bg {
		maxSteps, runTimeout = bgBudget(maxSteps, runTimeout) // a long job, on its own
	}
	// Fast mode: one round of light tools, then the answer (scheduled and
	// background tasks always get the full agent).
	mctx, mcancel := b.ctx(5 * time.Second)
	fast := !detached && b.fastMode(mctx, uid)
	mcancel()
	if fast {
		maxSteps = fastSteps
	}
	routed := b.route(b.jobCtx, st.tier) // the provider requests go to first
	// A hard request goes to the tier's strong model (modelroute.go).
	var rt *modelRoute
	var triaged func() (*router.Verdict, bool)
	if tc != nil {
		triaged = func() (*router.Verdict, bool) {
			tri, ok := tc.result()
			if !ok {
				return nil, false
			}
			return triageVerdict(tri), tri.Context != router.ContextStandalone
		}
	}
	rt = b.chooseModel(uid, st.tier, routeInput(m, text), opt.round > 0, triaged)
	routed = rt.prefer(routed)
	window = rt.window(b, conf, window)
	deep := false
	if p, ok := b.prov.ActiveFor(routed); ok && p.Effort == llm.EffortUltra && !fast && !bg {
		maxSteps, runTimeout, deep = maxSteps*2, runTimeout*3/2, true // the model's highest effort gets the room it needs
	}
	runCtx, cancelRun := context.WithTimeout(providers.WithAccount(routed, acc), runTimeout)
	defer cancelRun()
	allow := &tools.Allowance{} // no cap on the pictures and downloads of a request
	runCtx = tools.WithAllowance(runCtx, allow)
	run := newRun(uid, m, cancelRun, opt.pending, st.tier)
	switch {
	case bg:
		b.bgRuns.Store(opt.bg.task.ID, run) // stopped by its own button, never by the user's next request
	case !scheduled:
		b.runs.Store(uid, run)
	}
	if opt.bridge != nil {
		// The job goes on from the previous round: what was sent in between joins it.
		run.add(opt.bridge.finish())
		if opt.bridge.stopped.Load() {
			run.stop()
		}
		opt.bridge = nil
	}
	defer func() {
		if run.stopped.Load() {
			out.goOn = false // stopped while the answer was being sent: the job does not go on by itself
		}
		if out.goOn && !detached {
			// The run stays registered and open until the next round takes it
			// over: a message sent in between joins the job instead of
			// starting another one beside it.
			out.bridge = run
			return
		}
		// Messages that came after the answer are handled next (see handlePrompt).
		out.leftovers = run.finish()
		switch {
		case bg:
			b.bgRuns.CompareAndDelete(opt.bg.task.ID, run)
		case !scheduled:
			b.runs.CompareAndDelete(uid, run)
		}
	}()

	react := b.newReactor(m)
	if opt.noReact || guest || detached || m.MessageID == 0 {
		react.enabled = false
	}
	defer react.wait()
	react.set(b.seenReaction(), false)

	// The live card comes first: reading the message (a voice message is
	// written down) can take a while, and the card shows it.
	live := b.newLive(m, uid)
	switch {
	case bg:
		live.detach(opt.bg.task.Title)
		live.stopData = opt.bg.stopData()
	case !scheduled:
		live.stopData, live.runKey = run.stopData(), run.key // a scheduled task is not in b.runs: nothing to stop by button
	}
	live.start(runCtx)
	defer live.discard() // after the answer: removes the live message unless it became the answer

	in, err := b.buildInput(withListening(runCtx, live.listening), u, m, text, opt.round > 0)
	if errors.Is(err, errTooBig) || errors.Is(err, errWorkspaceFull) {
		react.clear()
		return say(b.fileRefusal(l, err))
	}
	if err != nil {
		log.Printf("[user %d] reading the message failed: %v", uid, err)
		react.clear()
		return say(l.t(textError))
	}
	if strings.TrimSpace(text) == "" && rt.upgrade(routeInput(m, in.msg.Content)) {
		runCtx = rt.prefer(runCtx) // a voice message's words ask for the strong model
	}
	if opt.about != "" {
		in.about = strings.TrimSpace(in.about + "\n" + opt.about)
	}
	if opt.round > 0 {
		in.memoryText = fmt.Sprintf("(automatic continuation, round %d)", opt.round+1)
	}
	if bg {
		in.memoryText = opt.bg.memoryText()
	}
	if fast {
		in.about = strings.TrimSpace(in.about + "\n" + fastNote)
	}
	// A greeting or a thank-you goes without the tools until the model asks for them.
	smallTalk := in.plain && !detached && opt.round == 0 && agent.IsSmallTalk(text)
	// The triage's decision (triage.go): ready by now.
	trec := b.triageDecision(tc, rt)
	triageOK := trec != nil && trec.ok
	// The lighter settings only outside a job: in the middle of one, the next
	// message is more of it, however small it looks.
	lighter := triageOK && !trec.job
	if lighter && !smallTalk && in.plain && !fast && b.triageChatOn() && triageChat(trec.tri, text) {
		smallTalk = true // a reply with the light prompt, no tools until it asks for them
		trec.did(triageDidChat)
	}
	var unCap bool // the effort cap is lifted if the conversation turns into a job
	if lighter && smallTalk {
		level := triageEffort(true)
		runCtx, unCap = llm.WithEffortCap(runCtx, level), true // it only lowers: a failover provider too
		if p, ok := b.prov.ActiveFor(runCtx); ok && effortLowered(p.Effort, level) {
			trec.did(triageDidEffort + level)
		}
	}

	mc := &memory.Context{}
	if !opt.noMemory {
		if mc, err = b.mem.Load(runCtx, uid); err != nil {
			log.Printf("[user %d] memory: %v", uid, err)
			react.clear()
			return say(l.t(textError))
		}
		modernizeHistory(mc.History) // old "(sent files: …)" notes, which the model used to copy
	}

	// The job is written down as it works (see openjob.go): whatever ends it
	// early (a stop, an error, a restart), «ادامه بده» knows which job to go
	// on with and how far it got, instead of picking up an older message.
	track := opt.job
	continuing := track != nil // a job going on needs all of its conversation
	if track == nil && !opt.noMemory && !guest && !detached && !smallTalk {
		request, base := in.memoryText, ""
		unfinished := b.unfinishedJob(uid)
		note, cont := jobOf(unfinished, m.Chat.ID, text, b.takeResume(uid))
		if note != "" {
			in.about = strings.TrimSpace(in.about + "\n" + note)
		}
		if cont {
			request, base = unfinished.Request, unfinished.Notes
			continuing = true
		}
		track = b.trackJob(uid, m.Chat.ID, m.MessageID, request, base, cont)
	}
	out.job = track
	defer func() {
		if !out.goOn {
			track.interrupt(store.JobFailed) // a panic: never left "running" (a no-op once it ended)
		}
	}()

	session := sessionFor(uid)
	box := &outbox{}
	files := newFileTracker(b.app.Machine, session)
	// toolEvents reports the calls of the agent (sub 0) or of one of its
	// sub-agents, whose files are tracked in its own workspace.
	toolEvents := func(sub int, files *fileTracker) *agent.EventListener {
		return &agent.EventListener{
			OnToolStart: func(name, args string) {
				files.start(name)
				live.toolStartWith(sub, name, args)
			},
			OnToolEnd: func(name, args, result string, took time.Duration) {
				files.end(name, result)
				go b.recordStat(store.StatTool, name, failedResult(result), 0, 0)
				live.toolEnd(sub, name, result)
			},
			OnToolProgress: func(name, text string, output bool) {
				live.toolProgress(sub, name, b.redact(text), output) // secrets and server paths never show
			},
		}
	}
	listener := toolEvents(0, files)
	if track != nil {
		shown := listener.OnToolEnd
		listener.OnToolEnd = func(name, args, result string, took time.Duration) {
			shown(name, args, result, took)
			track.step(name, args, result)
		}
	}
	listener.OnText = func(partial string) {
		if !live.streams(partial) {
			return // a preface or a short answer: the work shows until the answer is sent whole
		}
		live.setText(streamSafe(scrubFileNotes(partial)))
	}
	listener.OnContext = live.context
	listener.OnPlan = func(items []agent.PlanItem) {
		live.setPlan(items)
		track.setPlan(items)
	}
	listener.OnCompact = live.compacted
	listener.OnEscalate = live.escalated
	listener.SubAgent = func(title, workspace string) *agent.EventListener {
		id := live.subAgent(title)
		subFiles := newFileTracker(b.app.Machine, workspace)
		sub := toolEvents(id, subFiles)
		sub.OnDone = func(_ int, failed bool) { live.subDone(id, failed) }
		sub.OnFiles = func(moved map[string]string) { files.adopt(subFiles, moved) }
		return sub
	}
	// The job may go on by itself when its budget runs out.
	auto := opt.round < opt.rounds && !opt.noMemory && !guest && !detached && !fast
	var toolFilter func(string) bool
	if fast {
		toolFilter = fastTool
	}
	// Only the files the model chooses are sent (send_file, or shown in its
	// answer). An answer that says a file was sent while none goes with it is
	// sent back, and one that sends nothing while deliverables were made is
	// reminded of them once (a guest reply never carries files).
	var check func(string) string
	if !guest {
		check = fileClaimCheck(func(answer string) []string {
			return append(files.deliveries(box.list()), files.placed(answer)...)
		}, func(chosen []string) []string { return files.made(chosen...) })
	}
	if !b.app.Registry.CanTranscribe() { // shown only when a speech-to-text service is set
		keep := toolFilter
		toolFilter = func(name string) bool { return name != tools.ToolTranscribe && (keep == nil || keep(name)) }
	}

	// The triage's choice of history and tools: where the provider does not
	// cache prompts (or always, if the owner says so), a request that stands
	// on its own goes with the last turns (recall_history brings the rest) and
	// the tools it will likely use (more_tools brings the rest); a weak
	// model's focus picks by a model triage's reading rather than by the
	// words alone.
	history, localTools := mc.History, b.localTools(u, m, react, box, opt)
	focus, provID := b.focusTools(runCtx), int64(0)
	if p, ok := b.prov.ActiveFor(runCtx); ok {
		provID = p.ID
	}
	var focusPick []string
	trimmed := 0
	if triageOK && !smallTalk && !fast && !continuing {
		if lighter && b.triageTrims(provID) {
			if keep := triageKeep(trec.tri); keep > 0 {
				if kept, dropped := splitHistory(history, keep); len(dropped) > 0 {
					history, trimmed = kept, len(dropped)
					localTools = append(localTools, recallTool(dropped))
					trec.did(triageDidTrim)
				}
			}
		}
		if focus && trec.tri.Groups != nil { // a local triage leaves the groups to the focus's own reading
			focusPick = triagePick(trec.tri, trimmed > 0)
			trec.did(triageDidFocus)
		}
	}
	if trimmed > 0 { // how to get back what was left out
		in.about = strings.TrimSpace(in.about + "\n" + triageHint(trimmed))
	}
	escalate := rt.escalation()
	if esc := escalate; esc != nil && unCap {
		escalate = func(ctx context.Context) context.Context { return llm.WithEffortCap(esc(ctx), "") }
	}

	ownerChat := b.isOwner(uid) && m.Chat.Private() && !guest && !scheduled
	var carry int
	var carryCompacted bool
	if !opt.noMemory && !guest && !bg { // a background task's context is its own
		cctx, ccancel := b.ctx(5 * time.Second)
		carry, carryCompacted = b.contextCarry(cctx, uid, mc)
		ccancel()
	}
	res, err := b.app.Agent.Do(runCtx, agent.Request{
		SessionID: session,
		Input:     in.msg,
		Resume:    opt.resume,
		History:   history,
		Memory:    mc.Summary,
		Chat:      smallTalk,
		Earlier:   continuing || opt.round > 0, // the work of an earlier round has its receipts there
		// The memory keeps the conversation under this; the agent only
		// catches what grew past it since.
		HistoryTokens:    historyBudget(b.st, window),
		ContextWindow:    window,
		ContextCarry:     carry,
		ContextCompacted: carryCompacted,
		About:            in.about,
		Lang:             b.langOf(m.From).code(),
		MaxSteps:         maxSteps,
		Deep:             deep,
		Tools:            localTools,
		Listener:         listener,
		Inbox: func() []llm.Message {
			xs := run.take()
			msgs := make([]llm.Message, len(xs))
			for i, x := range xs {
				msgs[i] = x.msg
				live.note()
			}
			return msgs
		},
		AutoContinue:     auto,
		ToolFilter:       toolFilter,
		AnswerOnLastStep: fast,
		Check:            check,
		// The owner, in private only (a group would see what they see).
		Owner:     ownerChat,
		OwnerNote: b.ownerNote(ownerChat, conf),
		// A model that calls tools badly starts with the ones the request needs (toolskill.go).
		FocusTools: focus,
		FocusPick:  focusPick,
		FocusHint:  in.hint,
		// A light model that struggles hands the rest to the strong one.
		Escalate: escalate,
	})
	run.answered()
	logTriage(trec)
	if res != nil && !res.Escalated && !smallTalk && trimmed == 0 && focusPick == nil {
		b.noteCache(provID, res.Usage) // how much this provider caches: whether trimming pays (triage.go)
	}
	if res != nil {
		lctx, lcancel := b.ctx(5 * time.Second)
		b.learnToolSkill(lctx, res.CallQuality, false)
		lcancel()
	}
	rt.record(res != nil && res.Escalated, acc)
	live.stop()
	// Delivering gets its own time, counted from now: a long job that used
	// all of its time still reports back.
	sendCtx, cancelSend := b.ctx(answerSendBudget)
	defer cancelSend()
	sendCtx, answerMsgs := collectSent(sendCtx) // the messages of this answer

	if err != nil {
		react.clear()
		var api *llm.APIError
		var kb *telegram.InlineKeyboardMarkup
		html := ""
		worked := res != nil && res.WorkLog != ""
		// The job stays unfinished (with why), so «continue» or ▶️ goes on from
		// it; it is remembered once its files went (below).
		unfinished := ""
		goOn := func(reason string) {
			track.interrupt(reason)
			unfinished = reason
			if bg {
				kb = keyboard(row(b.button(l.t("🔁 دوباره امتحان کن"), opt.bg.retryData(), telegram.StylePrimary)))
			} else if !guest && !scheduled {
				label := l.t("🔁 دوباره امتحان کن")
				if worked {
					label = l.t("▶️ ادامه بده")
				}
				kb = keyboard(row(b.button(label, track.buttonData(uid, m.MessageID), telegram.StylePrimary)))
			}
		}
		switch {
		case b.jobCtx.Err() != nil:
			// The bot is going down: the job stays "running", and the next start
			// tells the user, with ▶️ (nothing can be sent now).
			track.shutdown()
			html = l.t(textError)
		case run.stopped.Load():
			// What was done stays: it is said, kept in the memory, and «continue» goes on from it.
			html = l.t(textCancelled)
			goOn(store.JobStopped)
			if worked {
				html = l.f(textStoppedWork, l.num(strings.Count(res.WorkLog, "\n")+1))
			} else {
				kb = nil // nothing done: a stop is a stop
			}
		case errors.Is(err, context.DeadlineExceeded):
			html = l.t(textTimeout)
			goOn(store.JobTimeout)
		case errors.Is(err, providers.ErrNoProvider):
			html = l.t(textProviderMissing)
			goOn(store.JobFailed)
			kb = nil
		case llm.IsPolicyViolation(err):
			log.Printf("[user %d] refused by every provider's safety filter: %v", uid, err)
			html = l.t(textPolicyViolation)
			track.drop() // nothing to go on with
		case errors.As(err, &api), llm.IsUnavailable(err):
			// Not only the provider's own errors: a refused connection, a stall or an
			// empty answer is the same to the user, the model is out of reach.
			log.Printf("[user %d] model error: %v", uid, err)
			html = l.t(textModelDown)
			goOn(store.JobFailed)
			b.alertModelDown(uid, err)
		default:
			log.Printf("[user %d] run failed: %v", uid, err)
			html = l.t(textError)
			goOn(store.JobFailed)
		}
		shutdown := b.jobCtx.Err() != nil
		if bg && !shutdown {
			opt.bg.failed(run.stopped.Load(), err)
			html = l.f("⚠️ <b>کار پس‌زمینه:</b> %s", esc(opt.bg.task.Title)) + "\n\n" + html
		}
		switch {
		case bg && shutdown:
			// The task stays "running": the next start tells its user, with 🔁.
		case bg || live.replaceHTML(sendCtx, html, l == fa, kb) != nil:
			// A background task's end is a new message: Telegram tells its user (an edit it would not).
			if kb != nil {
				b.sendHTML(sendCtx, m.Chat.ID, html, telegram.SendOptions{ReplyTo: m.MessageID, Markup: kb})
			} else {
				resp.notice(sendCtx, html)
			}
		}
		// The files the model already chose are finished work: a job that ran
		// out of time or failed still hands them over, as its live card said.
		// A stop is a stop (they stay in the workspace for «continue»); a
		// refused job, a guest's, or a background task (🔁 starts it over) sends none.
		var delivered []string
		if unfinished != "" && !run.stopped.Load() && !guest && !bg && !shutdown {
			if chosen, held := chosenFiles(files, box, allow); len(chosen) > 0 {
				delivered = b.deliverFiles(sendCtx, m, session, chosen, box.captionMap())
				if held > 0 {
					resp.notice(sendCtx, l.f(textPicturesHeld, l.num(held)))
				}
			}
		}
		if unfinished != "" && !opt.noMemory {
			b.rememberUnfinished(uid, in.memoryText, run.memo(), unfinished, res, delivered)
		}
		refund = refund && !run.stopped.Load() && len(delivered) == 0 // a job the user stopped, or that delivered, used its tokens
		return promptOutcome{}, err
	}
	refund = false
	answer := textutil.PersianEzafe(scrubFileNotes(b.redact(res.Answer))) // «همه فایل‌ها», even when the model writes «همه‌ی»
	if auto && res.Partial && (res.StopReason == "steps" || res.StopReason == "time") && !run.stopped.Load() {
		out.goOn = true
		answer = strings.TrimSpace(answer) + "\n\n" + l.f("⏩ خودم ادامه می‌دم… (دور %d از حداکثر %d)", opt.round+2, opt.rounds+1)
	}
	sent, held := chosenFiles(files, box, allow) // what the answer shows with ![…](file) goes too (planAnswer)
	if held > 0 {                                // pictures made with code and sent one by one
		answer = strings.TrimSpace(answer + "\n\n" + l.f(textPicturesHeld, l.num(held)))
	}
	captions := box.captionMap()
	if guest {
		// A guest reply is a single message: files wait in the user's workspace.
		if len(sent) > 0 || len(files.placed(answer)) > 0 || len(files.made()) > 0 {
			answer = strings.TrimSpace(withoutMediaRefs(answer) + "\n\n" + l.f(textGuestFiles, b.me.Username))
			sent = nil
		}
		answer = withoutMediaRefs(answer)
	}
	if strings.TrimSpace(answer) != "" && opt.prefix != "" {
		answer = opt.prefix + "\n\n" + answer
	}
	// The files travel inside the answer (a slideshow of pictures, songs and
	// documents under the text); what cannot follows as albums.
	var shown, rest []string
	switch {
	case guest:
		if strings.TrimSpace(answer) != "" {
			resp.answer(sendCtx, answer)
		}
	default:
		head := "" // the steps, folded above the answer
		if strings.TrimSpace(answer) != "" {
			head = live.workLog()
		}
		shown, rest = b.sendAnswerWithFiles(sendCtx, m, session, answer, head, sent, captions, box.extras.effectID(), live.inPlace())
	}
	asked := ""
	if q := box.question(); q != nil && res.EndedBy == toolAskUser {
		var pollID string
		asked, pollID = b.sendQuestion(sendCtx, u, m, q, strings.TrimSpace(answer) == "" && len(shown) == 0)
		b.suspendFor(pollID, res.Suspended) // the vote resumes this very job
	}
	var delivered []string
	if len(rest) > 0 {
		delivered = b.deliverFiles(sendCtx, m, session, rest, captions)
	}
	if !guest {
		box.extras.run(sendCtx) // map pins and polls the model prepared
	}
	react.finish(res.ToolsUsed, text)
	b.learnFrom(uid, res) // a step failed and another way worked: a lesson for the admins to check
	b.noteJob(m.Chat.ID, res)
	if !m.Chat.Private() && !guest { // a reply to this answer by someone else is understood
		b.linkAnswer(m.Chat.ID, answerMsgs.in(m.Chat.ID), answerRef{askerID: uid, askerName: u.DisplayName(), question: text})
	}
	opt.bg.ended(run.stopped.Load(), res)
	switch {
	case run.stopped.Load() && res.Partial:
		track.interrupt(store.JobStopped)
	case out.goOn:
		track.flush() // the next round takes it over
	case res.Partial:
		track.interrupt(partialReason(res.StopReason))
	default:
		track.done()
	}
	if opt.noMemory {
		return out, nil
	}

	userTurn := in.memoryText
	if added := run.memo(); added != "" {
		userTurn += "\n\n[messages the user sent while I was working on it]\n" + added
	}
	stored := withoutMediaRefs(answer)
	if note := fileNote(append(shown, delivered...)); note != "" { // only what arrived: never "sent" for a file that was lost
		stored += "\n\n" + note
	}
	if res.Partial && res.WorkLog != "" {
		// The job stopped early: what was done, so "continue" picks it up.
		stored += "\n\n[work log before stopping — the job is not finished]\n" + res.WorkLog
	} else if note := workNote(res.Notes); note != "" {
		stored += "\n\n" + note // what was done behind the answer: follow-ups understand it
	}
	if asked != "" {
		stored = strings.TrimSpace(stored + "\n\n" + asked)
	}
	// The memory has time of its own: a long delivery must not cost the conversation.
	memCtx, memCancel := b.memCtx()
	defer memCancel()
	if err := b.mem.Append(memCtx, uid, userTurn, stored); err != nil {
		log.Printf("[user %d] saving memory: %v", uid, err)
		return out, nil
	}
	if !guest && !bg {
		b.saveContextCarry(memCtx, uid, res.Context, mc) // the next request's meter goes on from here
	}
	// Compressing a long conversation is a model call: it never holds up the
	// user's next message (the next request reads the memory as it is).
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ctx, cancel := b.ctx(2 * time.Minute)
		defer cancel()
		compressed, err := b.mem.Tidy(providers.WithAccount(b.route(ctx, st.tier), acc), uid) // summarized by the tier's model (on Custom the user's own), on the user's costs
		if err != nil {
			log.Printf("[user %d] compressing memory: %v", uid, err)
		}
		if compressed && !guest {
			b.sendHTML(ctx, m.Chat.ID, l.t(textMemoryCompacted), telegram.SendOptions{ReplyTo: m.MessageID, Silent: true}) // on the message it was about
		}
	}()
	return out, nil
}

// rememberUnfinished keeps a request that ended early in the conversation:
// what was asked, the messages sent meanwhile, why it stopped and the steps
// already taken, so a later «continue» (or the ▶️ button) goes on from there
// instead of starting blind or from an older message.
func (b *Bot) rememberUnfinished(uid int64, asked, added, reason string, res *agent.Result, delivered []string) {
	userTurn := asked
	if added != "" {
		userTurn += "\n\n[messages the user sent while I was working on it]\n" + added
	}
	stored := "[" + unfinishedLine(reason) + "]"
	if note := fileNote(delivered); note != "" { // only what arrived
		stored += "\n\n" + note
	}
	if res != nil && res.WorkLog != "" {
		stored += "\n\n[work log before stopping — the job is not finished]\n" + b.redact(res.WorkLog)
	}
	ctx, cancel := b.memCtx()
	defer cancel()
	if err := b.mem.Append(ctx, uid, userTurn, stored); err != nil {
		log.Printf("[user %d] saving an unfinished request: %v", uid, err)
	}
}

// unfinishedLine is how the memory says why a request ended early.
func unfinishedLine(reason string) string {
	switch reason {
	case store.JobStopped:
		return "the user stopped this request before I finished it"
	case store.JobTimeout:
		return "this request ran out of time before I finished it"
	}
	return "an error stopped this request before I finished it (not the user's doing)"
}

// partialReason is why a job that answered early is unfinished.
func partialReason(stop string) string {
	switch stop {
	case "time":
		return store.JobTimeout
	case "failed":
		return store.JobFailed
	}
	return store.JobBudget // steps or tokens
}

func (b *Bot) recordStat(kind, name string, failed bool, prompt, completion int) {
	ctx, cancel := b.ctx(5 * time.Second)
	defer cancel()
	if err := b.st.RecordStat(ctx, kind, name, failed, prompt, completion); err != nil {
		log.Printf("stats: %v", err)
	}
}

// ---------------------------------------------------------------- understanding the message

type promptInput struct {
	msg        llm.Message
	about      string
	hint       string // what came with the message (attachments, a reply, a forward): what its tools are chosen by
	memoryText string // how the user turn is remembered
	plain      bool   // only words: nothing attached, forwarded or shared
}

// buildInput reads the message. In a continuation round its files are the
// ones saved by the first round (not downloaded again over the changes made to them).
func (b *Bot) buildInput(ctx context.Context, u *store.User, m *telegram.Message, text string, continuation bool) (*promptInput, error) {
	l := b.langOf(m.From)
	vision := b.prov.VisionFor(ctx)
	session := sessionFor(u.ID)
	var (
		about  []string
		notes  []string // remembered alongside the text
		images []string
	)
	text = strings.TrimSpace(text)

	// The user and the chat, and what the model remembers about them.
	about = append(about, b.profileAbout(ctx, u, m)...)
	if facts := b.factsAbout(ctx, u.ID, !m.Chat.Private()); facts != "" {
		about = append(about, facts)
	}
	if !m.Chat.Private() && m.GuestQueryID == "" {
		if s := b.groupLogFor(ctx, m.Chat.ID); s.hours > 0 {
			about = append(about, fmt.Sprintf("This group keeps its messages for %d hours: for \"what did I miss?\", a summary of the chat "+
				"or questions about earlier messages, use %s.", s.hours, toolGroupHistory))
		}
	}

	// Attachments, and the rest of what came with the message.
	withMessage := len(about)
	lead := "The user sent"
	if continuation {
		lead = "With the original request the user sent"
	}
	att, err := b.attachAll(ctx, session, m, vision, lead, !continuation)
	if err != nil {
		return nil, err
	}
	plain := att == nil && m.Location == nil && m.Contact == nil && m.Origin() == nil
	if att != nil {
		about = append(about, att.about)
		notes = append(notes, att.note)
		images = append(images, att.images...)
		if text == "" {
			text = att.placeholder
		}
	}
	switch {
	case m.Location != nil:
		about = append(about, fmt.Sprintf("The user shared a location: latitude %.6f, longitude %.6f.", m.Location.Latitude, m.Location.Longitude))
		if text == "" {
			text = l.t("(یه موقعیت مکانی فرستاد)")
		}
	case m.Contact != nil:
		about = append(about, fmt.Sprintf("The user shared a contact card: %s %s.", m.Contact.FirstName, m.Contact.LastName))
		if text == "" {
			text = l.t("(یه مخاطب فرستاد)")
		}
	}
	if text == "" {
		text = l.t("(پیام خالی)")
	}

	if o := m.Origin(); o != nil {
		about = append(about, "This message was forwarded from "+o.Describe()+"; it is not the user's own words.")
		notes = append(notes, "[forwarded from "+o.Describe()+"]")
	}

	// The message being replied to (a rich answer of the bot's is read from
	// its blocks; one from another chat shows its media and the quoted part).
	if r := m.Replied(); r != nil {
		who := "another user"
		switch {
		case r.From != nil && r.From.ID != 0 && (r.From.ID == b.me.ID || (r.ViaBot != nil && r.ViaBot.ID == b.me.ID)):
			who = "you (your own earlier message)"
		case r.From != nil && r.From.ID != 0 && r.From.ID == u.ID:
			who = "the user themself"
		case r.SenderChat != nil:
			who = (&telegram.MessageOrigin{Type: "chat", SenderChat: r.SenderChat}).Describe()
		case r.From != nil:
			who = (&telegram.MessageOrigin{Type: "user", SenderUser: r.From}).Describe()
		}
		if o := r.Origin(); o != nil {
			who += ", who forwarded it from " + o.Describe()
		}
		if m.RepliedInChat() == nil && r.Chat.ID != 0 && r.Chat.ID != m.Chat.ID && r.Chat.Title != "" {
			who += " in «" + r.Chat.Title + "»"
		}
		body := strings.TrimSpace(telegram.UnmarkRTL(r.Body())) // without the direction marks of the bot's own lines
		quote := ""
		if m.Quote != nil {
			quote = strings.TrimSpace(m.Quote.Text)
		}
		line := "The user is replying to a message from " + who
		switch {
		case body != "":
			line += ":\n<replied_message>\n" + textutil.TruncateRunes(body, 3000, "…") + "\n</replied_message>"
			if quote != "" && quote != body {
				line += "\nThey quoted this part of it: «" + textutil.TruncateRunes(quote, 600, "…") + "»"
			}
		case !r.HasMedia() && r.Poll == nil:
			line += " (its text is not visible to you; if the request depends on it, ask them to quote or forward it)"
		}
		if r.Poll != nil {
			line += "\nIt is a poll: «" + r.Poll.Question + "»"
		}
		if r.From != nil && r.From.ID == b.me.ID {
			if s := b.replyToAnswer(m, u.ID); s != "" { // whose answer it was, in a group
				line += "\n" + s
			}
		}
		line += "\nWords like «this», «این», «اینو» or «اون پیام» in their request mean that message."
		quoted := body
		if quote != "" {
			quoted = quote
		}
		// Files in the replied message go into the workspace too, so "remove
		// the watermark from this" works on a photo sent earlier.
		if ratt, err := b.attach(ctx, session, r, vision, leadReplied, false); err == nil && ratt != nil {
			plain = false
			line += "\n" + ratt.about
			images = append(images, ratt.images...)
		} else if errors.Is(err, errTooBig) {
			line += "\n(It contains a file too big to open.)"
		} else if errors.Is(err, errWorkspaceFull) {
			line += "\n(It contains a file that does not fit in the user's full workspace; they can free space with /files.)"
		}
		about = append(about, line)
		notes = append(notes, "[replying to: «"+textutil.TruncateRunes(textutil.OneLine(quoted), 80, "…")+"»]")
	} else if s := m.ReplyToStory; s != nil {
		about = append(about, "The user is replying to a story of "+(&telegram.MessageOrigin{Type: "chat", SenderChat: &s.Chat}).Describe()+
			" (you cannot see stories; ask what it shows if the request depends on it).")
	}

	if files := b.workspaceAbout(session); files != "" { // files and project folders kept from earlier requests
		about = append(about, files)
	}

	msg := llm.Message{Role: "user", Content: text}
	if len(images) > 0 {
		msg.Parts = []llm.Part{llm.TextPart(text)}
		for _, img := range images {
			msg.Parts = append(msg.Parts, llm.ImagePart(img))
		}
	}
	memoryText := strings.TrimSpace(strings.Join(append(notes, text), " "))
	return &promptInput{msg: msg, about: strings.Join(about, "\n"), hint: strings.Join(about[withMessage:], "\n"), memoryText: memoryText, plain: plain}, nil
}

// recentFiles is how many of the workspace's newest files each request
// names, and recentFolders how many of its folders (projects).
const (
	recentFiles   = 12
	recentFolders = 10
)

// workspaceAbout names the newest files of the user's workspace (what the
// bot made for them, what they sent), so "that file", «همون عکس» or
// "convert it" finds its file at once, without a search or a question.
func (b *Bot) workspaceAbout(session string) string {
	var files, folders []string
	more := 0
	for _, f := range b.app.Machine.Recent(session, 200) {
		switch {
		case f.IsDir && len(folders) < recentFolders:
			folders = append(folders, f.Name+"/")
		case !f.IsDir && len(files) < recentFiles:
			files = append(files, fmt.Sprintf("%s (%s, %s ago)", f.Name, bytesWord(f.Size), ageWord(time.Since(f.ModTime))))
		case !f.IsDir:
			more++
		}
	}
	var lines []string
	if len(files) > 0 {
		line := "Files in the user's workspace now, newest first (what you made for them and what they sent; \"it\", «همون» or " +
			"\"that file\" usually means the newest one that fits): " + strings.Join(files, ", ")
		if more > 0 {
			line += fmt.Sprintf(" and %d older", more)
		}
		lines = append(lines, line)
	}
	if len(folders) > 0 {
		lines = append(lines, "Folders in the workspace (projects, extracted archives): "+strings.Join(folders, ", "))
	}
	return strings.Join(lines, "\n")
}

// bytesWord is a size for the model ("3.2 MB").
func bytesWord(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}

// ageWord is how long ago, for the model ("5 min", "3 h").
func ageWord(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// profileAbout describes the user; the bio is refreshed at most once a day.
func (b *Bot) profileAbout(ctx context.Context, u *store.User, m *telegram.Message) []string {
	if m.Chat.Private() && (u.ProfileCheckedAt == nil || time.Since(*u.ProfileCheckedAt) > 24*time.Hour) {
		if info, err := b.api.GetChat(ctx, u.ID); err == nil {
			birth := ""
			if bd := info.Birthdate; bd != nil {
				birth = fmt.Sprintf("%02d-%02d", bd.Month, bd.Day)
				if bd.Year > 0 {
					birth = fmt.Sprintf("%04d-", bd.Year) + birth
				}
			}
			if err := b.st.SetProfileDetails(ctx, u.ID, info.Bio, birth); err == nil {
				u.Bio, u.Birthdate = info.Bio, birth
			}
		}
	}
	lines := []string{"About the user (from their Telegram profile):"}
	lines = append(lines, "- name: "+u.DisplayName())
	if u.Username != "" {
		lines = append(lines, "- username: @"+u.Username)
	}
	if u.LanguageCode != "" {
		lines = append(lines, "- app language: "+u.LanguageCode)
	}
	if b.lang(u.ID) == en {
		lines = append(lines, "- uses the bot in English: answer in English unless they write in another language")
	}
	if u.IsPremium {
		lines = append(lines, "- has Telegram Premium")
	}
	if u.Bio != "" {
		lines = append(lines, "- bio: "+textutil.OneLine(u.Bio))
	}
	if u.Birthdate != "" {
		lines = append(lines, "- birthday: "+u.Birthdate)
	}
	lines = append(lines, fmt.Sprintf("- talking with you since %s; %d messages so far", u.CreatedAt.In(b.loc).Format("2006-01-02"), u.MessagesTotal))
	if b.isOwner(u.ID) {
		lines = append(lines, "- this user is the bot's owner")
	}
	if !m.Chat.Private() {
		lines = append(lines, fmt.Sprintf("This conversation happens in the group «%s»; keep answers compact.", m.Chat.Title))
	}
	return lines
}

// attachment is what a message carries, saved in the workspace.
type attachment struct {
	about       string   // for the model
	note        string   // remembered in the conversation
	images      []string // data URLs the model sees
	placeholder string   // the user's text when they sent no caption
}

// attach saves the photo, file or media of m into the workspace and describes
// it; it returns nil when m carries none. lead starts the sentence ("The user
// sent"). With fresh the file is always downloaded (a new upload replaces an
// older file of the same name); otherwise a file already saved is reused, so
// replying to an old message does not undo the changes made to it since.
// leadReplied introduces what a replied message holds (attach tells it apart by it).
const leadReplied = "That replied message contains"

func (b *Bot) attach(ctx context.Context, session string, m *telegram.Message, vision bool, lead string, fresh bool) (*attachment, error) {
	l := b.langOf(m.From)
	have := func(name string) (int64, bool) {
		fi, err := b.app.Machine.Stat(session, name)
		if err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
			return fi.Size(), !fresh
		}
		return 0, false
	}
	look := func(name string) []string {
		if !vision {
			return nil
		}
		data, truncated, err := b.app.Machine.ReadFile(session, name, maxVisionImage)
		if err != nil || truncated {
			return nil
		}
		if url, _, err := visionImage(data); err == nil {
			return []string{url}
		}
		return nil
	}
	switch {
	case len(m.Photo) > 0:
		name := "photo_" + msgTag(m) + ".jpg"
		if _, ok := have(name); !ok {
			if _, err := b.saveIncoming(ctx, session, m.LargestPhoto(2560).FileID, name, 0); err != nil {
				return nil, err
			}
		}
		a := &attachment{about: fmt.Sprintf("%s a photo, saved in the workspace as %s.", lead, name),
			note: "[the user sent a photo: " + name + "]", placeholder: l.t("(یه عکس فرستاد)"), images: look(name)}
		if len(a.images) > 0 {
			a.about += " It is attached below."
		}
		return a, nil
	case m.Document != nil && m.Animation == nil:
		d := m.Document
		name := safeName(d.FileName, "file_"+msgTag(m))
		size, ok := have(name)
		if !ok {
			n, err := b.saveIncoming(ctx, session, d.FileID, name, d.FileSize)
			if err != nil {
				return nil, err
			}
			size = n
		}
		a := &attachment{about: fmt.Sprintf("%s a file: %s (%s, %s), saved in the workspace as %s.", lead, d.FileName, d.MimeType, sizeText(size), name),
			note: "[the user sent a file: " + name + "]", placeholder: l.f("(یه فایل فرستاد: %s)", name)}
		if tools.IsArchive(name) {
			a.about += " It is an archive (often a whole project): " + tools.ToolExtract + " unpacks it, then " + tools.ToolProjectTree + " maps it."
		}
		if strings.HasPrefix(d.MimeType, "image/") {
			a.images = look(name)
		}
		return a, nil
	case m.Sticker != nil:
		s := m.Sticker
		ext := ".webp"
		switch {
		case s.IsVideo:
			ext = ".webm"
		case s.IsAnimated:
			ext = ".tgs"
		}
		name := "sticker_" + msgTag(m) + ext
		if _, ok := have(name); !ok {
			if _, err := b.saveIncoming(ctx, session, s.FileID, name, s.FileSize); err != nil {
				return nil, err
			}
		}
		a := &attachment{about: fmt.Sprintf("%s a sticker %s (saved in the workspace as %s). React to the feeling it shows.", lead, s.Emoji, name),
			note: "[the user sent a sticker " + s.Emoji + "]", placeholder: s.Emoji}
		if vision {
			var url, desc string
			if s.IsAnimated { // Lottie: only Telegram's thumbnail can be shown
				url, desc = b.framePreview(ctx, session, "", 0, s.Thumbnail, true)
			} else {
				url, desc = b.framePreview(ctx, session, name, 3, s.Thumbnail, !s.IsVideo)
			}
			if url != "" {
				a.images, a.about = []string{url}, a.about+" "+desc
			}
		}
		return a, nil
	case m.Voice != nil, m.Audio != nil, m.Video != nil, m.VideoNote != nil, m.Animation != nil:
		fileID, kind, name := mediaInfo(m)
		size, ok := have(name)
		if !ok {
			n, err := b.saveIncoming(ctx, session, fileID, name, 0)
			if err != nil {
				return nil, err
			}
			size = n
		}
		a := &attachment{note: "[the user sent " + aOrAn(kind) + ": " + name + "]", placeholder: l.f("(یه %s فرستاد)", kind)}
		var moving *telegram.Video
		switch {
		case m.Animation != nil:
			moving, a.placeholder = m.Animation, l.t("(یه گیف فرستاد)")
		case m.Video != nil:
			moving = m.Video
		case m.VideoNote != nil:
			moving = m.VideoNote
		}
		preview, desc := "", ""
		if moving != nil && vision {
			preview, desc = b.framePreview(ctx, session, name, moving.Duration, moving.Thumbnail, false)
		}
		// A replied message: its words are what it holds, not the request. One
		// the bot sent itself (its own voice or round video) is not listened
		// to again: it knows what it said, and the card shows no «listening».
		replied := lead == leadReplied
		own := replied && m.From != nil && (m.From.ID == b.me.ID || (m.ViaBot != nil && m.ViaBot.ID == b.me.ID))
		heard, failed := "", ""
		if (m.Voice != nil || m.VideoNote != nil) && !own {
			heard, failed = b.hear(ctx, session, name)
		}
		switch {
		case own:
			if preview != "" {
				a.images = []string{preview}
			}
			a.about = fmt.Sprintf("%s a %s you sent yourself earlier in this chat (yours, e.g. made with text_to_speech or downloaded), saved in "+
				"the workspace as %s (%s). %s You can work with the file (convert, cut, send again…).%s", lead, kind, name, sizeText(size), desc, songNote(m, name))
		case heard != "" && replied:
			if preview != "" {
				a.images = []string{preview}
			}
			a.about = fmt.Sprintf("%s a %s, saved in the workspace as %s (%s). %s What it says (speech to text; small mistakes are possible): "+
				"«%s». That is the content of the replied message; the user's request is their own message.", lead, kind, name, sizeText(size), desc,
				textutil.TruncateRunes(heard, 3000, "…"))
		case heard != "":
			if preview != "" {
				a.images = []string{preview}
			}
			a.about = fmt.Sprintf("%s a %s, saved in the workspace as %s (%s). %s What it says (speech to text; small mistakes are possible) "+
				"is the user's message: answer it as if they had typed it, in its language.", lead, kind, name, sizeText(size), desc)
			a.placeholder = "🎙 " + heard
			a.note = "[the user sent " + aOrAn(kind) + ": " + name + "] " + textutil.TruncateRunes(heard, 2000, "…")
		case failed != "":
			a.about = fmt.Sprintf("%s a %s, saved in the workspace as %s (%s). Writing down its speech failed (%s); "+
				"do not try %s on it now: tell the user kindly, and ask them to type it or try again later.", lead, kind, name, sizeText(size), failed, tools.ToolTranscribe)
		case preview != "":
			a.images = []string{preview}
			a.about = fmt.Sprintf("%s a %s, saved in the workspace as %s (%s). %s You cannot hear its sound.", lead, kind, name, sizeText(size), desc)
		case moving != nil:
			a.about = fmt.Sprintf("%s a %s, saved in the workspace as %s (%s). You cannot watch it, but you can work with the file "+
				"(convert, cut, extract audio…). Tell the user kindly if they expect you to understand its content.", lead, kind, name, sizeText(size))
		case b.app.Registry.CanTranscribe():
			a.about = fmt.Sprintf("%s a %s, saved in the workspace as %s (%s). %s writes down its speech when that is what the user wants; "+
				"you can also work with the file (convert, cut, trim…).", lead, kind, name, sizeText(size), tools.ToolTranscribe)
		default:
			a.about = fmt.Sprintf("%s a %s, saved in the workspace as %s (%s). You cannot listen to it (speech to text is not set up; "+
				"the bot's admin can turn it on in the admin panel), but you can work with the file (convert, cut, trim…). "+
				"Tell the user kindly if they expect you to understand its content.", lead, kind, name, sizeText(size))
		}
		return a, nil
	}
	return nil, nil
}

// hear writes down a voice message (once: the transcript is kept next to
// it, so replying to it later does not ask the service again). failed tells
// why it could not; both are empty without a speech-to-text service.
func (b *Bot) hear(ctx context.Context, session, name string) (text, failed string) {
	cache := "." + name + ".transcript"
	if data, _, err := b.app.Machine.ReadFile(session, cache, 1<<20); err == nil {
		return strings.TrimSpace(string(data)), ""
	}
	if !b.app.Registry.CanTranscribe() {
		return "", ""
	}
	show := listeningFrom(ctx)
	show(listenStart)
	// A local whisper writes down one recording at a time: the card says
	// when this one waits for its turn.
	ctx = tools.WithProgress(ctx, tools.Progress{Status: func(s string) {
		switch s {
		case tools.StatusQueued:
			show(listenQueued)
		case tools.StatusListening:
			show(listenStart)
		}
	}})
	text, _, err := b.app.Registry.Transcribe(ctx, session, name, "")
	switch {
	case errors.Is(err, tools.ErrNoTranscriber):
		show(listenDone)
		return "", ""
	case err != nil:
		show(listenFailed)
		log.Printf("[%s] speech to text: %v", session, err)
		return "", textutil.TruncateRunes(err.Error(), 200, "…")
	case strings.TrimSpace(text) == "":
		show(listenDone)
		return "", "no speech in it"
	}
	show(listenDone)
	_ = b.app.Machine.WriteFile(session, cache, []byte(text))
	return text, ""
}

// listenState is where writing down a voice message is.
type listenState int

const (
	listenStart listenState = iota
	listenQueued
	listenDone
	listenFailed
)

type listenKey struct{}

// withListening lets reading a message tell the live card that it listens.
func withListening(ctx context.Context, f func(listenState)) context.Context {
	return context.WithValue(ctx, listenKey{}, f)
}

func listeningFrom(ctx context.Context) func(listenState) {
	if f, ok := ctx.Value(listenKey{}).(func(listenState)); ok && f != nil {
		return f
	}
	return func(listenState) {}
}

// memCtx is the time to write a turn to the memory: its own, so a shutdown
// during the files' upload before it does not lose the turn.
func (b *Bot) memCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(b.jobCtx), 30*time.Second)
}

// aOrAn puts the article before a word: "an audio file", "a video".
func aOrAn(w string) string {
	if w != "" && strings.ContainsRune("aeiouAEIOU", rune(w[0])) {
		return "an " + w
	}
	return "a " + w
}

func mediaInfo(m *telegram.Message) (fileID, kind, name string) {
	tag := msgTag(m)
	switch {
	case m.Voice != nil:
		return m.Voice.FileID, "voice message", "voice_" + tag + ".ogg"
	case m.Audio != nil:
		return m.Audio.FileID, "audio file", safeName(m.Audio.FileName, "audio_"+tag+".mp3")
	case m.Video != nil:
		return m.Video.FileID, "video", safeName(m.Video.FileName, "video_"+tag+".mp4")
	case m.VideoNote != nil:
		return m.VideoNote.FileID, "round video message", "videonote_" + tag + ".mp4"
	default:
		return m.Animation.FileID, "GIF animation", safeName(m.Animation.FileName, "animation_"+tag+".mp4")
	}
}

// songNote tells the model where the words of a song it made are, when a
// replied audio message is one (its name starts with Macron_AI_-_): never
// transcribed with a speech tool, they were kept.
func songNote(m *telegram.Message, name string) string {
	if m.Audio == nil || !strings.HasPrefix(name, "Macron_AI_-_") {
		return ""
	}
	return " It is a song you made: its words are in your earlier answer or in " + strings.TrimSuffix(name, path.Ext(name)) +
		".txt in the workspace (read_file it; also in the mp3's tags), so never transcribe it with a speech tool."
}

// msgTag names files after their message. A user's workspace is shared by
// their private chat and their groups, where message ids repeat, so group
// files also carry a piece of the chat id.
func msgTag(m *telegram.Message) string {
	if m.Chat.Private() || m.Chat.ID == 0 {
		return strconv.FormatInt(m.MessageID, 10)
	}
	id := m.Chat.ID
	if id < 0 {
		id = -id
	}
	return fmt.Sprintf("%d_g%04d", m.MessageID, id%10000)
}

// fetch downloads a Telegram file into memory.
func (b *Bot) fetch(ctx context.Context, fileID string, max int64) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := b.api.Download(ctx, fileID, &buf, max); err != nil {
		if errors.Is(err, telegram.ErrFileTooLarge) {
			return nil, errTooBig
		}
		return nil, err
	}
	return buf.Bytes(), nil
}

// saveIncoming streams a Telegram file into the user's workspace.
func (b *Bot) saveIncoming(ctx context.Context, session, fileID, name string, size int64) (int64, error) {
	limit, room := b.api.DownloadLimit(), b.app.Machine.FreeSpace()
	switch {
	case size > limit:
		return 0, errTooBig
	case size > room:
		return 0, errWorkspaceFull
	}
	full := room < limit // a file the size doesn't tell can still not fit
	limit = min(limit, room)
	f, err := b.app.Machine.Create(session, name)
	if err != nil {
		return 0, err
	}
	n, err := b.api.Download(ctx, fileID, f, limit)
	f.Close()
	if err != nil {
		_ = b.app.Machine.Remove(session, name)
		if errors.Is(err, telegram.ErrFileTooLarge) {
			if full {
				return 0, errWorkspaceFull
			}
			return 0, errTooBig
		}
		return 0, err
	}
	return n, nil
}

// fileRefusal says why a file the user sent was not taken.
func (b *Bot) fileRefusal(l lang, err error) string {
	if errors.Is(err, errWorkspaceFull) {
		return l.t(textWorkspaceFull)
	}
	return l.f(textFileTooBig, sizeWord(l, b.api.DownloadLimit()))
}

// sizeWord writes a limit the way people say it: «۲۰ مگابایت», «۲ گیگابایت».
func sizeWord(l lang, n int64) string {
	if mb := n >> 20; mb >= 1000 && mb%1000 == 0 {
		return l.f("%s گیگابایت", l.num(int(mb/1000)))
	}
	return l.f("%s مگابایت", l.num(int(n>>20)))
}

func dataURL(mime string, data []byte) string {
	if mime == "" || mime == "application/octet-stream" {
		mime = http.DetectContentType(data)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func safeName(name, fallback string) string {
	clean := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 32 {
			return '_'
		}
		return r
	}, path.Base(strings.TrimSpace(name)))
	clean = strings.TrimLeft(clean, ".-")
	if clean == "" || clean == "." {
		return fallback
	}
	return textutil.TruncateRunes(clean, 100, "")
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<30:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/(1<<30)), ".0") + " GB"
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// ---------------------------------------------------------------- request-scoped tools

// outbox collects what the model hands to the user during a turn: files
// (send_file) and a question (ask_user).
type outbox struct {
	mu       sync.Mutex
	names    []string
	captions map[string]string // file → the caption the model wrote for it
	ask      *pollQuestion
	extras   extras // what follows the answer (map pins, polls) and its effect
}

func (o *outbox) question() *pollQuestion {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ask
}

func (o *outbox) add(name, caption string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !slices.Contains(o.names, name) {
		o.names = append(o.names, name)
	}
	if caption = strings.TrimSpace(caption); caption != "" {
		if o.captions == nil {
			o.captions = map[string]string{}
		}
		o.captions[name] = caption
	}
}

// captionMap returns the captions by file name.
func (o *outbox) captionMap() map[string]string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make(map[string]string, len(o.captions))
	for k, v := range o.captions {
		out[k] = v
	}
	return out
}

// list returns the chosen files in the order they were chosen.
func (o *outbox) list() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.names...)
}

// workspaceName accepts "x", "./x" and "/workspace/x".
func workspaceName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, machine.MountPoint+"/")
	return strings.TrimPrefix(s, "./")
}

// localTools are the tools that exist only for this request. A guest reply
// is one message (no files, polls or scheduling into a chat the bot is not
// in); a scheduled task runs unattended (no polls, no new schedules).
func (b *Bot) localTools(u *store.User, m *telegram.Message, react *reactor, box *outbox, opt promptOptions) []agent.LocalTool {
	guest, scheduled, bg := m.GuestQueryID != "", opt.schedule != nil, opt.bg != nil
	out := b.requestTools(u, m, react, box, b.prov.VisionFor(b.route(context.Background(), opt.tier)))
	if m.Chat.Private() && !guest { // plugins, MCP servers: the owner's private chat only
		out = append(out, b.pluginsTool(u, m)...)
	}
	if !guest && !scheduled && !bg {
		out = append(out, b.backgroundTool(u, m)) // a long job can go on by itself
	}
	if guest || scheduled || bg {
		drop := map[string]bool{toolAskUser: true, toolSchedule: true, toolListSchedules: true, toolCancelSchedule: true}
		if guest {
			// The bot is not in the chat: one text reply, nothing else.
			for _, name := range []string{toolSendFile, toolDice, toolLocation, toolCreatePoll, toolEffect, toolChatInfo} {
				drop[name] = true
			}
		}
		kept := out[:0]
		for _, t := range out {
			if !drop[t.Def.Function.Name] {
				kept = append(kept, t)
			}
		}
		out = kept
	}
	return out
}

func (b *Bot) requestTools(u *store.User, m *telegram.Message, react *reactor, box *outbox, vision bool) []agent.LocalTool {
	session := sessionFor(u.ID)
	out := []agent.LocalTool{{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name: toolSendFile,
			Description: "Send a final, checked workspace file with your answer: nothing is sent unless you choose it (a ![caption](file) " +
				"line in your answer does the same without a step). Never drafts, frames, or files you only needed for the work.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"filename": map[string]any{"type": "string"},
				"caption":  map[string]any{"type": "string", "description": "A line under it in the user's language (≤300; **bold** works)"},
			}, "required": []string{"filename"}},
		}},
		Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
			name, _ := args["filename"].(string)
			name = workspaceName(name)
			if name == "" {
				return agent.ToolOutput{Text: "error: give the name of a file in the workspace"}, nil
			}
			f, info, err := b.app.Machine.Open(session, name)
			if err != nil {
				return agent.ToolOutput{Text: "error: " + name + " cannot be sent (missing, not a regular file or outside the workspace)"}, nil
			}
			f.Close()
			switch {
			case info.Size() == 0:
				return agent.ToolOutput{Text: "error: the file is empty"}, nil
			case info.Size() > b.api.UploadLimit():
				return agent.ToolOutput{Text: fmt.Sprintf("error: the file is larger than Telegram's %d MB limit; compress or split it", b.api.UploadLimit()>>20)}, nil
			}
			caption, _ := args["caption"].(string)
			box.add(name, textutil.TruncateRunes(strings.TrimSpace(caption), 300, "…"))
			note := ""
			if caption != "" {
				note = " with your caption"
			}
			return agent.ToolOutput{Text: fmt.Sprintf("%s (%s) will be sent%s with your answer.", name, sizeText(info.Size()), note)}, nil
		},
	}}
	if react.on() {
		out = append(out, agent.LocalTool{
			Def: llm.Tool{Type: "function", Function: llm.ToolDef{
				Name: toolReact,
				Description: "React to the user's message with one emoji, when it adds warmth: ❤ thanks, 🔥 exciting, 🤣 a joke, " +
					"🤓 study, 👨‍💻 code, 🎉 good news, 🤗 hello, 🤔 tricky, 😢 sad, 🫡 on it.",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{
					"emoji": map[string]any{"type": "string"},
				}, "required": []string{"emoji"}},
			}},
			Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
				e, _ := args["emoji"].(string)
				if telegram.NormalizeReaction(e) == "" {
					return agent.ToolOutput{Text: "not an allowed reaction; Telegram takes: " + strings.Join(telegram.ReactionEmoji, " ")}, nil
				}
				react.choose(e)
				return agent.ToolOutput{Text: "reaction set"}, nil
			},
		})
	}
	out = append(out, b.askUserTool(box), b.lookupTool(u, m))
	out = append(out, b.toolkit(m, &box.extras)...)
	fctx, fcancel := b.ctx(5 * time.Second)
	if b.factsOn(fctx, u.ID) {
		out = append(out, b.factTools(u)...)
	}
	if m.GuestQueryID == "" {
		out = append(out, b.recallTool(u))
	}
	if !m.Chat.Private() && m.GuestQueryID == "" {
		if s := b.groupLogFor(fctx, m.Chat.ID); s.hours > 0 {
			out = append(out, b.groupHistoryTool(u, m, s.hours))
		}
		if b.moderator(fctx, m) { // the group's admins and the bot's: moderation in plain words
			out = append(out, b.groupAdminTool(m))
		}
	}
	fcancel()
	out = append(out, b.scheduleTools(u, m)...)
	if vision {
		out = append(out, agent.LocalTool{
			Def: llm.Tool{Type: "function", Function: llm.ToolDef{
				Name:        toolViewImage,
				Description: "Look at workspace images: check what you made before sending it, compare, or see again what the user sent.",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{
					"filenames": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Up to 4"},
				}, "required": []string{"filenames"}},
			}},
			Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
				return b.viewImages(session, args), nil
			},
		}, agent.LocalTool{
			Def: llm.Tool{Type: "function", Function: llm.ToolDef{
				Name:        toolProfilePhoto,
				Description: "The user's current Telegram profile picture.",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			}},
			Handler: func(ctx context.Context, _ map[string]any) (agent.ToolOutput, error) {
				photos, err := b.api.GetUserProfilePhotos(ctx, u.ID, 1)
				if err != nil {
					return agent.ToolOutput{}, err
				}
				if photos.TotalCount == 0 || len(photos.Photos) == 0 {
					return agent.ToolOutput{Text: "The user has no profile photo, or it is hidden by their privacy settings."}, nil
				}
				pm := &telegram.Message{Photo: photos.Photos[0]}
				data, err := b.fetch(ctx, pm.LargestPhoto(1280).FileID, 5<<20)
				if err != nil {
					return agent.ToolOutput{}, err
				}
				return agent.ToolOutput{Text: "Here is the user's current profile photo.", Images: []string{dataURL("image/jpeg", data)}}, nil
			},
		})
	}
	return out
}

// ---------------------------------------------------------------- reactions

type reactor struct {
	b       *Bot
	chatID  int64
	msgID   int64
	enabled bool

	mu      sync.Mutex
	current string
	chosen  bool
	queue   []reaction    // asked for and not sent yet, in order
	sending chan struct{} // closed when the queue is sent (nil: idle)
}

type reaction struct {
	emoji string
	big   bool
}

func (b *Bot) newReactor(m *telegram.Message) *reactor {
	return &reactor{b: b, chatID: m.Chat.ID, msgID: m.MessageID, enabled: b.st.SettingBool(store.SettingReactions, true)}
}

// set asks for a reaction. It never waits for Telegram (the 👀 used to delay
// every request by a round trip): one goroutine sends a message's reactions
// in the order they were asked for.
func (r *reactor) set(emoji string, big bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.enabled || r.current == emoji {
		return
	}
	r.current = emoji
	r.queue = append(r.queue, reaction{emoji, big})
	if r.sending == nil {
		r.sending = make(chan struct{})
		go r.send(r.sending)
	}
}

func (r *reactor) send(done chan struct{}) {
	defer close(done)
	for {
		r.mu.Lock()
		if len(r.queue) == 0 || !r.enabled {
			r.queue, r.sending = nil, nil
			r.mu.Unlock()
			return
		}
		next := r.queue[0]
		r.queue = r.queue[1:]
		r.mu.Unlock()
		ctx, cancel := r.b.ctx(10 * time.Second)
		err := r.b.api.SetReaction(ctx, r.chatID, r.msgID, next.emoji, next.big)
		cancel()
		if err != nil && !telegram.IsUnreachable(err) {
			r.mu.Lock()
			r.enabled = false // some groups restrict reactions; never let that disturb the conversation
			r.mu.Unlock()
		}
	}
}

// wait lets the reactions asked for reach Telegram before the message's
// handler returns (bounded: a slow Telegram never holds the next request).
func (r *reactor) wait() {
	r.mu.Lock()
	done := r.sending
	r.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}
}

// on reports whether reactions can be set.
func (r *reactor) on() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enabled
}

func (r *reactor) clear() { r.set("", false) }

// choose is called when the model picks a reaction itself.
func (r *reactor) choose(emoji string) {
	r.mu.Lock()
	r.chosen = true
	r.mu.Unlock()
	e := telegram.NormalizeReaction(emoji)
	r.set(e, e == "🔥" || e == "🎉" || e == "❤")
}

// finish replaces the "seen" reaction when the model did not choose one.
func (r *reactor) finish(tools []string, text string) {
	r.mu.Lock()
	chosen := r.chosen
	r.mu.Unlock()
	if chosen {
		return
	}
	r.set(guessReaction(tools, text), false)
}

func guessReaction(tools []string, text string) string {
	t := strings.ToLower(text)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(t, w) {
				return true
			}
		}
		return false
	}
	used := strings.Join(tools, ",")
	switch {
	case has("مرسی", "ممنون", "دمت گرم", "thank", "thx", "قربونت"):
		return "❤"
	case strings.Contains(used, "search_music") || strings.Contains(used, "ytdlp"):
		return "🔥"
	case strings.Contains(used, "generate_image") || strings.Contains(used, "search_image"):
		return "😍"
	case strings.Contains(used, "deep_research"):
		return "🤓"
	case strings.Contains(used, "bash_execute") || strings.Contains(used, "run_code") || strings.Contains(used, "install_package") || strings.Contains(used, "github"):
		return "👨‍💻"
	case strings.Contains(used, "crypto"):
		return "⚡"
	case has("سلام", "درود", "hello", "hi ", "صبح بخیر", "شب بخیر"):
		return "🤗"
	case len(tools) > 0:
		return "🫡"
	}
	return ""
}

// minAgentSteps is the smallest step budget the admin can set.
const minAgentSteps = 5

// agentBudget is how many tool rounds and how much time one request gets: the
// admin panel's settings, else the server's -max-steps and -run-timeout.
func (b *Bot) agentBudget() (steps int, timeout time.Duration) {
	steps, timeout = b.app.Agent.MaxSteps, b.runTimeout
	if n := b.st.SettingInt(store.SettingAgentSteps, 0); n > 0 {
		steps = n
	}
	if n := b.st.SettingInt(store.SettingAgentMinutes, 0); n > 0 {
		timeout = time.Duration(n) * time.Minute
	}
	return steps, timeout
}
