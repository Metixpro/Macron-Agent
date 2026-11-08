package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/textutil"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

// Long jobs: the agent knows its budget (tool rounds and time), is told when
// it is about to run out, and always lands softly: it reports what it did and
// what is left instead of failing, and keeps a work log so "continue" picks
// the job up where it stopped.

const (
	// stepsWarning: rounds left when the model is told to wrap up.
	stepsWarning = 2
	// lengthContinuations: how many times an answer cut by the output limit is continued.
	lengthContinuations = 3
	// repeatLimit: identical calls allowed before the model is told it is repeating itself.
	repeatLimit = 2
	// maxChecks: answers Request.Check may send back in one request.
	maxChecks = 2
	// parallelTools: calls of one turn that may run at the same time.
	parallelTools = 4
	// parallelAgents: sub-agents of one turn that work at the same time.
	parallelAgents = 4
	// maxCallsPerStep: tool calls of one turn that are run at all.
	maxCallsPerStep = 24
	// midRunRetries: extra attempts when the model stops answering in the middle of a job.
	midRunRetries = 2
)

// midRunBackoff scales the waits between mid-job retries (1×, 4×).
var midRunBackoff = 3 * time.Second

// parallelSafe are tools that only read: several of them in one turn run at once.
var parallelSafe = map[string]bool{
	"web_search": true, "web_fetch": true, "search_music": true, "search_image": true, "crypto_currency_price": true, "currency_gold_price": true,
	"whois_lookup": true, "github_inspect": true, "read_file": true, "list_files": true, "document_reader": true,
	"deep_research": true, "view_image": true, tools.ToolProjectTree: true, tools.ToolSearchCode: true, tools.ToolFindFiles: true,
	tools.ToolCodeOutline: true, tools.ToolListSkills: true, tools.ToolReadSkill: true, ToolDelegate: true, ToolPlan: true,
}

// reserve is the time kept at the end of a request for the final summary.
func reserve(total time.Duration) time.Duration {
	return min(max(total/6, 5*time.Second), time.Minute)
}

// budgetNote tells the model how much room it has.
func budgetNote(steps int, total time.Duration) string {
	s := fmt.Sprintf("Budget for this request: up to %d tool rounds", steps)
	if total > 0 {
		s += fmt.Sprintf(" and about %d minutes", max(1, int(total.Round(time.Minute).Minutes())))
	}
	return s + ". Spend what the request needs: a quick question takes no tools or one round; a big job fits too, so work " +
		"through it until it is done. Near the end of the budget you get a notice to wrap up."
}

// ultracodeNote goes with a request whose model runs at its highest effort.
const ultracodeNote = "Ultracode: your model runs at its highest effort with a bigger budget, for work that has to be right. Plan the " +
	"whole job, build all of it (no placeholders or TODOs), run and test it for real, read your own work back and fix what you find, " +
	"then answer."

const (
	noticeStepsEnding = "[system notice] Only %d tool rounds are left for this request. Finish the most important part now; " +
		"then answer with what is done and what is still left."
	noticeTimeEnding = "[system notice] The time for this request is almost over. Do not call any more tools."
	noticeWrapUp     = "[system notice] Stop working now and do not call any tools. In the user's language, tell them briefly what you " +
		"got done (with the files you made), what is still left, and that they can say «ادامه بده» (continue) to carry on from here."
	noticeWrapUpAuto = "[system notice] Stop working now and do not call any tools. In the user's language, tell them briefly what " +
		"you got done so far (with the files you made) and what is still left. The job goes on automatically right after this, " +
		"so do not ask them to say continue."
	noticeWrapUpSub = "[system notice] Stop working now and do not call any tools. Report to the main agent what you got done (with " +
		"the files you made) and what is still left."
	noticeTokensEnding = "[system notice] The user's tokens for today ran out. Do not call any more tools."
	noticeCutCall      = "error: this call was cut off by the output length limit, so it was NOT run (its arguments were incomplete). " +
		"Send it again smaller: write big content in parts (write_file with append=true, a few hundred lines per call)."
	noticeLate = "[system notice] Your answer above was not delivered: the user's message below arrived first. Handle it, " +
		"then write one final answer that covers everything (they never saw the one above)."
	noticeAnswerNow = "[system notice] Answer the user now from what you have, without calling any tools."
	noticeCut       = "[system notice] Your reply was cut off by the output length limit. Continue exactly where it stopped, without " +
		"repeating anything and without any preface."
	noticeCutCalls = "[system notice] Your last message hit the output length limit, so a tool call may be incomplete. " +
		"Write big content in parts (write_file with append=true, a few hundred lines per call) instead of one huge call."
	noticeRepeat = "[notice] You already made this exact call %d times with the same arguments. Don't repeat it: use the earlier " +
		"result or change your approach.\n"
)

// workLog remembers the steps of a run, for the report when it stops early
// and for the next request ("continue").
type workLog struct {
	mu    sync.Mutex
	lines []string
}

func (w *workLog) add(name, args, result string) {
	line := fmt.Sprintf("%s(%s) → %s", name, textutil.ArgPreview(args, 80),
		textutil.TruncateRunes(textutil.OneLine(firstLine(result)), 100, "…"))
	w.mu.Lock()
	w.lines = append(w.lines, line)
	w.mu.Unlock()
}

// String is the log, newest steps kept when it is long.
func (w *workLog) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	lines := w.lines
	if len(lines) > 40 {
		lines = append([]string{fmt.Sprintf("… %d earlier steps", len(lines)-40)}, lines[len(lines)-40:]...)
	}
	var sb strings.Builder
	for i, l := range lines {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, l)
	}
	return strings.TrimSpace(sb.String())
}

// brief is the end of the log as remembered with an answer: the last steps,
// within a few hundred characters each.
func (w *workLog) brief(lines, chars int) string {
	w.mu.Lock()
	all := w.lines
	w.mu.Unlock()
	skipped := 0
	if len(all) > lines {
		skipped, all = len(all)-lines, all[len(all)-lines:]
	}
	var sb strings.Builder
	if skipped > 0 {
		fmt.Fprintf(&sb, "… %d earlier steps\n", skipped)
	}
	for _, l := range all {
		sb.WriteString(l + "\n")
	}
	return textutil.TruncateRunes(strings.TrimSpace(sb.String()), chars, "…")
}

func (w *workLog) len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.lines)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// stopReason says why a job ended before it was done.
type stopReason int

const (
	stopFailed   stopReason = iota // the model stopped answering
	stopTime                       // the time ran out
	stopSteps                      // the step budget ran out
	stopTokens                     // the user's tokens ran out
	stopCanceled                   // the user stopped it
	stopRepeat                     // the model kept making the same call: no progress
	stopIdle                       // nothing happened for the idle timeout
)

// ExtendForever is Request.Extensions with no limit: the budget grows for as
// long as the work makes progress.
const ExtendForever = -1

// extensionSize is how many rounds an extension adds: half the first budget,
// at least 25.
func extensionSize(initial int) int { return max(initial/2, 25) }

// noticeExtended tells the model its budget grew, with where the work stands,
// so nothing of the job is lost across the extension.
const noticeExtended = "[system notice] The work is making progress, so this request got %d more tool rounds (extension %d). " +
	"Carry on from where you are; do not redo finished steps. Progress so far:\n%s"

// noticeSaveSkill is the reminder of Request.SkillNudgeEvery.
const noticeSaveSkill = "[system notice] If you just finished a hard part of this job (one that took several tries, or a procedure " +
	"worth repeating), save how you did it with save_skill, as a short general procedure without anything private. Otherwise " +
	"ignore this and carry on."

// errIdle cancels a run that saw nothing happen for its idle timeout.
var errIdle = errors.New("agent: idle timeout")

func (r stopReason) String() string {
	switch r {
	case stopTime:
		return "time"
	case stopSteps:
		return "steps"
	case stopTokens:
		return "tokens"
	case stopCanceled:
		return "stopped"
	case stopRepeat:
		return "repeat"
	case stopIdle:
		return "idle"
	}
	return "failed"
}

// InterjectionNote introduces a message the user sent while the agent was working.
const InterjectionNote = "[The user sent this while you were working on the request above. Take it into account now: " +
	"add it to the job or change course; if it is a separate question, answer it too in your reply; if they want you to stop, " +
	"stop and say so briefly.]\n"

// stoppedText is the report when the model cannot be asked for one. With
// auto the bot goes on by itself, so the user is not asked to continue.
func stoppedText(lang string, steps int, why stopReason, auto bool) string {
	if lang == "en" {
		reason := map[stopReason]string{stopFailed: "I lost the connection to my brain in the middle of the job",
			stopTime: "The time for this request ran out", stopSteps: "This job needed more steps than one request allows",
			stopTokens: "Your tokens ran out in the middle of the job", stopRepeat: "I stopped because I kept making the same call without getting anywhere",
			stopIdle: "I stopped because nothing happened for a long time (no answer from the model, no tool finishing)"}[why]
		s := fmt.Sprintf("⏸ %s. %d steps of the work are done and what I made is kept in the workspace.", reason, steps)
		if !auto {
			s += " Say «continue» and I'll pick it up from here."
		}
		return s
	}
	reason := map[stopReason]string{stopFailed: "ارتباطم با مغزم وسط کار قطع شد", stopTime: "وقت این درخواست تموم شد",
		stopSteps: "این کار از تعداد قدم‌های مجاز یه درخواست بیشتر بود", stopTokens: "توکن‌هات وسط کار تموم شد",
		stopRepeat: "متوقف شدم چون داشتم همون کار رو بی‌نتیجه تکرار می‌کردم", stopIdle: "متوقف شدم چون مدت زیادی هیچ اتفاقی نیفتاد (نه جوابی از مدل، نه ابزاری که تموم بشه)"}[why]
	s := fmt.Sprintf("⏸ %s. %s مرحله از کار انجام شد و هرچی ساختم توی فضای کاری مونده.", reason, persianDigits(steps))
	if !auto {
		s += " بگو «ادامه بده» تا از همین‌جا ادامه بدم."
	}
	return s
}

func persianDigits(n int) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return '۰' + (r - '0')
		}
		return r
	}, fmt.Sprint(n))
}

// completeRobust asks the model and copes with what long jobs run into: a
// conversation too long for the model (older tool results are shortened, then
// it asks again) and a provider that stops answering for a while or cuts its
// stream (it waits and asks again: twice once work has been done, once for
// the first answer).
func (a *Agent) completeRobust(ctx context.Context, messages []llm.Message, input int, tools []llm.Tool, onText func(string), noTools *textTools, working bool) (*llm.Response, error) {
	resp, err := a.complete(ctx, messages, tools, onText, noTools)
	if err != nil && llm.IsContextOverflow(err) && ctx.Err() == nil {
		log.Printf("agent: the conversation is too long for the model (%v); shortening older tool results", err)
		compactToolOutputs(messages, contextBudgetRunes/4)
		dropOldImages(messages, input)
		resp, err = a.complete(ctx, messages, tools, onText, noTools)
	}
	if err != nil && ctx.Err() == nil && !noTools.noImages && refusesInput(err) && hasImages(messages) {
		// A model that does not see pictures refuses the whole request with one
		// in it (a view_image of what it made): it goes on with a line instead.
		log.Printf("agent: the model refused a request with pictures (%v); going on without them", err)
		noTools.noImages = true
		resp, err = a.complete(ctx, messages, tools, onText, noTools)
	}
	retries := midRunRetries
	if !working {
		retries = 1 // the first answer too: a stream cut by a loaded server must not end the request
	}
	for attempt := 1; err != nil && attempt <= retries && retryableMidRun(err) && ctx.Err() == nil; attempt++ {
		wait := time.Duration(attempt*attempt) * midRunBackoff
		if d, ok := ctx.Deadline(); ok && time.Until(d) < wait+30*time.Second {
			break
		}
		log.Printf("agent: the model failed in the middle of a job (%v); trying again in %s", err, wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, err
		}
		resp, err = a.complete(ctx, messages, tools, onText, noTools)
	}
	if err == nil && onText != nil && brokenCalls(resp) && ctx.Err() == nil {
		// The provider's parser failed on the model's call while streaming
		// («"name":null,"arguments":"{}"»: the call is lost). Without
		// streaming the same reply arrives as text («<tool_call>{…}</tool_call>»),
		// which the agent reads itself.
		log.Printf("agent: a streamed tool call arrived broken (no name, or arguments that are not JSON); asking again without streaming, and the rest of this request goes without it")
		noTools.noStream = true
		if again, err2 := a.complete(ctx, messages, tools, nil, noTools); err2 == nil {
			resp = again
		}
	}
	return resp, err
}

// brokenCalls reports a streamed reply whose calls the provider's parser
// broke: one without a name, or one whose arguments are not JSON even once
// repaired (a call split in two: «download_file» with half its arguments and
// «filename» with «}»).
func brokenCalls(resp *llm.Response) bool {
	if resp == nil {
		return false
	}
	for _, c := range resp.Message.ToolCalls {
		if strings.TrimSpace(c.Function.Name) == "" {
			return true
		}
		if _, err := repairArgs(c.Function.Arguments); err != nil {
			return true
		}
	}
	return false
}

// retryableMidRun: errors worth waiting out once work is underway.
func retryableMidRun(err error) bool {
	if llm.IsPolicyViolation(err) || errors.Is(err, context.Canceled) {
		return false
	}
	var api *llm.APIError
	if errors.As(err, &api) {
		return api.Temporary() || api.Status == 0
	}
	return true // network errors, broken streams, empty answers
}

// dropOldImages removes pictures from all but the last user turn with
// pictures and the user's request (input): the photo the user sent is what
// the edits after it refer to, whatever tools showed since.
func dropOldImages(messages []llm.Message, input int) {
	last := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" && len(messages[i].Parts) > 0 {
			last = i
			break
		}
	}
	for i := range messages {
		m := &messages[i]
		if i == last || i == input || len(m.Parts) == 0 {
			continue
		}
		var keep []llm.Part
		dropped := 0
		for _, p := range m.Parts {
			if p.Type == "image_url" {
				dropped++
				continue
			}
			keep = append(keep, p)
		}
		if dropped > 0 {
			keep = append(keep, llm.TextPart(fmt.Sprintf("[%d older image(s) removed to save context]", dropped)))
			m.Parts = keep
		}
	}
}

// continueCut continues an answer that the output limit cut off.
func (a *Agent) continueCut(ctx context.Context, messages []llm.Message, text string, onText func(string)) string {
	for i := 0; i < lengthContinuations && ctx.Err() == nil; i++ {
		msgs := append(messages[:len(messages):len(messages)],
			llm.Message{Role: "assistant", Content: text}, llm.Message{Role: "user", Content: noticeCut})
		var stream func(string)
		if onText != nil {
			prefix := text
			stream = func(s string) { onText(prefix + s) }
		}
		resp, err := a.LLM.Complete(ctx, llm.Request{Messages: msgs, OnDelta: stream})
		if err != nil || strings.TrimSpace(resp.Message.Content) == "" {
			break
		}
		text += resp.Message.Content
		if resp.FinishReason != "length" {
			break
		}
	}
	return text
}

// callKey identifies a call by its tool and (normalized) arguments.
func callKey(name, args string) string {
	return name + "\x00" + strings.Join(strings.Fields(args), " ")
}

// cachedOut is what a read-only call answered, and the workspace generation it saw.
type cachedOut struct {
	text string
	gen  int
}

// cacheable: a read-only call whose answer can be given again (not a
// sub-agent's work or a picture to look at).
func cacheable(name string) bool {
	return parallelSafe[name] && name != ToolDelegate && name != ToolPlan && name != "view_image" && name != "deep_research"
}

// noPicture stands where a picture was, for a model that does not see them.
const noPicture = "[a picture was here; this model cannot see pictures: go on from the text, and say so if what it showed matters]"

// refusesInput reports a provider that refused the request itself (a model
// without vision answers so to a picture).
func refusesInput(err error) bool {
	var api *llm.APIError
	return errors.As(err, &api) && (api.Status == 400 || api.Status == 413 || api.Status == 415 || api.Status == 422)
}

// hasImages reports whether any message carries a picture.
func hasImages(messages []llm.Message) bool {
	for _, m := range messages {
		for _, p := range m.Parts {
			if p.Type == "image_url" {
				return true
			}
		}
	}
	return false
}

// withoutImages is messages with a line in place of each picture (a copy:
// the job keeps its pictures, for a later model that sees them).
func withoutImages(messages []llm.Message) []llm.Message {
	if !hasImages(messages) {
		return messages
	}
	out := slices.Clone(messages)
	for i, m := range out {
		if len(m.Parts) == 0 {
			continue
		}
		var texts []string
		for _, p := range m.Parts {
			switch p.Type {
			case "image_url":
				texts = append(texts, noPicture)
			default:
				texts = append(texts, p.Text)
			}
		}
		out[i].Parts, out[i].Content = nil, strings.Join(texts, "\n")
	}
	return out
}

// touching is the listener with each event also counted as activity (the
// idle watchdog); the original listener is unchanged.
func (l *EventListener) touching(touch func()) *EventListener {
	c := *l
	c.OnToolStart = func(name, args string) {
		touch()
		if l.OnToolStart != nil {
			l.OnToolStart(name, args)
		}
	}
	c.OnToolEnd = func(name, args, result string, took time.Duration) {
		touch()
		if l.OnToolEnd != nil {
			l.OnToolEnd(name, args, result, took)
		}
	}
	c.OnToolProgress = func(name, text string, output bool) {
		touch()
		if l.OnToolProgress != nil {
			l.OnToolProgress(name, text, output)
		}
	}
	if l.OnText != nil {
		c.OnText = func(s string) { touch(); l.OnText(s) }
	}
	if l.OnThought != nil {
		c.OnThought = func(s string) { touch(); l.OnThought(s) }
	}
	return &c
}
