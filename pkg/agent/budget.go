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
	"github.com/Metixpro/macron-agent/pkg/tools"
)

const (
	stepsWarning = 2

	lengthContinuations = 3

	repeatLimit = 2

	maxChecks = 2

	parallelTools = 4

	parallelAgents = 4

	maxCallsPerStep = 24

	midRunRetries = 2
)

var midRunBackoff = 3 * time.Second

var parallelSafe = map[string]bool{
	"web_search": true, "web_fetch": true, "search_music": true, "search_image": true, "crypto_currency_price": true, "currency_gold_price": true,
	"whois_lookup": true, "github_inspect": true, "read_file": true, "list_files": true, "document_reader": true,
	"deep_research": true, "view_image": true, tools.ToolProjectTree: true, tools.ToolSearchCode: true, tools.ToolFindFiles: true,
	tools.ToolCodeOutline: true, tools.ToolListSkills: true, tools.ToolReadSkill: true, ToolDelegate: true, ToolPlan: true,
}

func reserve(total time.Duration) time.Duration {
	return min(max(total/6, 5*time.Second), time.Minute)
}

func budgetNote(steps int, total time.Duration) string {
	s := fmt.Sprintf("Budget for this request: up to %d tool rounds", steps)
	if total > 0 {
		s += fmt.Sprintf(" and about %d minutes", max(1, int(total.Round(time.Minute).Minutes())))
	}
	return s + ". Spend what the request needs: a quick question takes no tools or one round; a big job fits too, so work " +
		"through it until it is done. Near the end of the budget you get a notice to wrap up."
}

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
	noticeCutOff    = "[system notice] Your reply above was cut off there because the user sent the message below. " +
		"Take it into account; your next reply replaces the cut one, so make it whole."
	noticeCut = "[system notice] Your reply was cut off by the output length limit. Continue exactly where it stopped, without " +
		"repeating anything and without any preface."
	noticeCutCalls = "[system notice] Your last message hit the output length limit, so a tool call may be incomplete. " +
		"Write big content in parts (write_file with append=true, a few hundred lines per call) instead of one huge call."
	noticeRepeat = "[notice] You already made this exact call %d times with the same arguments. Don't repeat it: use the earlier " +
		"result or change your approach.\n"
)

type workLog struct {
	mu    sync.Mutex
	steps []step
}

func (w *workLog) add(name, args, result string) {
	s := describeStep(name, args, result)
	w.mu.Lock()
	w.steps = append(w.steps, s)
	w.mu.Unlock()
}

func (w *workLog) snapshot() []step {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]step(nil), w.steps...)
}

func (w *workLog) String() string { return w.record(workLogRunes) }

func (w *workLog) record(budget int) string {
	return fitEntries(groupSteps(w.snapshot()), budget)
}

func (w *workLog) len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.steps)
}

const (
	workLogRunes = 6000
	cutKeepRunes = 4000
)

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

type stopReason int

const (
	stopFailed stopReason = iota
	stopTime
	stopSteps
	stopTokens
	stopCanceled
	stopRepeat
	stopIdle
	stopBlocked
)

const ExtendForever = -1

func extensionSize(initial int) int { return max(initial/2, 25) }

const noticeExtended = "[system notice] The work is making progress, so this request got %d more tool rounds (extension %d). " +
	"Carry on from where you are; do not redo finished steps. Progress so far:\n%s"

const noticeSaveSkill = "[system notice] If you just finished a hard part of this job (one that took several tries, or a procedure " +
	"worth repeating), save how you did it with save_skill, as a short general procedure without anything private. Otherwise " +
	"ignore this and carry on."

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
	case stopBlocked:
		return "blocked"
	}
	return "failed"
}

const InterjectionNote = "[The user sent this while you were working on the request above. Take it into account now: " +
	"add it to the job or change course; if it is a separate question, answer it too in your reply; if they want you to stop, " +
	"stop and say so briefly.]\n"

func stoppedText(lang string, steps int, why stopReason, auto bool) string {
	if lang == "en" {
		reason := map[stopReason]string{stopFailed: "I lost the connection to my brain in the middle of the job",
			stopTime: "The time for this request ran out", stopSteps: "This job needed more steps than one request allows",
			stopTokens: "Your tokens ran out in the middle of the job", stopRepeat: "I stopped because I kept making the same call without getting anywhere",
			stopIdle:    "I stopped because nothing happened for a long time (no answer from the model, no tool finishing)",
			stopBlocked: "I stopped because several commands in a row were refused by the safety gate; they need you to run them by hand"}[why]
		s := fmt.Sprintf("⏸ %s. %d steps of the work are done and what I made is kept in the workspace.", reason, steps)
		if !auto {
			s += " Say «continue» and I'll pick it up from here."
		}
		return s
	}
	reason := map[stopReason]string{stopFailed: "ارتباطم با مغزم وسط کار قطع شد", stopTime: "وقت این درخواست تموم شد",
		stopSteps: "این کار از تعداد قدم‌های مجاز یه درخواست بیشتر بود", stopTokens: "توکن‌هات وسط کار تموم شد",
		stopRepeat: "متوقف شدم چون داشتم همون کار رو بی‌نتیجه تکرار می‌کردم", stopIdle: "متوقف شدم چون مدت زیادی هیچ اتفاقی نیفتاد (نه جوابی از مدل، نه ابزاری که تموم بشه)",
		stopBlocked: "متوقف شدم چون گیت امنیتی چند دستور پشت‌سرهم رو رد کرد؛ اون دستورها رو باید خودت دستی اجرا کنی"}[why]
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

func (a *Agent) completeRobust(ctx context.Context, messages []llm.Message, input int, tools []llm.Tool, onText func(string), noTools *textTools, working bool) (*llm.Response, error) {
	resp, err := a.complete(ctx, messages, tools, onText, noTools)
	if err != nil && llm.IsContextOverflow(err) && ctx.Err() == nil {
		log.Printf("agent: the conversation is too long for the model (%v); shortening older tool results", err)
		compactToolOutputs(messages, contextBudgetRunes/4)
		dropOldImages(messages, input)
		resp, err = a.complete(ctx, messages, tools, onText, noTools)
	}
	if err != nil && ctx.Err() == nil && !noTools.noImages && refusesInput(err) && hasImages(messages) {

		log.Printf("agent: the model refused a request with pictures (%v); going on without them", err)
		noTools.noImages = true
		resp, err = a.complete(ctx, messages, tools, onText, noTools)
	}
	retries := midRunRetries
	if !working {
		retries = 1
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

		log.Printf("agent: a streamed tool call arrived broken (no name, or arguments that are not JSON); asking again without streaming, and the rest of this request goes without it")
		noTools.noStream = true
		if again, err2 := a.complete(ctx, messages, tools, nil, noTools); err2 == nil {
			resp = again
		}
	}
	return resp, err
}

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

func retryableMidRun(err error) bool {
	if llm.IsPolicyViolation(err) || errors.Is(err, context.Canceled) {
		return false
	}
	var api *llm.APIError
	if errors.As(err, &api) {
		return api.Temporary() || api.Status == 0
	}
	return true
}

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

func callKey(name, args string) string {
	return name + "\x00" + strings.Join(strings.Fields(args), " ")
}

type cachedOut struct {
	text string
	gen  int
}

func cacheable(name string) bool {
	return parallelSafe[name] && name != ToolDelegate && name != ToolPlan && name != "view_image" && name != "deep_research"
}

const noPicture = "[a picture was here; this model cannot see pictures: go on from the text, and say so if what it showed matters]"

func refusesInput(err error) bool {
	var api *llm.APIError
	return errors.As(err, &api) && (api.Status == 400 || api.Status == 413 || api.Status == 415 || api.Status == 422)
}

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
