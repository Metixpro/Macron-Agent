package agent

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A model that hits an error tends to apologise and stop. When an answer
// gives up after a failed step while budget is left, the agent sends it back
// once to try another way first (another tool, source, site, parameters, or
// code); if it already tried, it simply answers again.

const noticePersist = "[system notice] Don't give up yet: a step failed, but there is budget left. Before answering, try another real " +
	"approach: a different tool, source, site, parameters or format, a web search for a workaround, or doing it with code. If you " +
	"already tried at least two different ways, or the request itself has to be refused, give your answer now and say briefly what you tried."

// failedOutput reports a tool result that looks like a failure.
// toolErrorRe is a tool's own error line: "read error: …", "fetch error: …", "music search error: …".
var toolErrorRe = regexp.MustCompile(`^[a-z][a-z _-]{0,30} error: `)

func failedOutput(result string) bool {
	l := strings.ToLower(firstLine(result))
	if strings.HasPrefix(l, "error") || toolErrorRe.MatchString(l) {
		return true
	}
	for _, p := range []string{" failed", "failed:", "unreachable", "timed out", "refused", "not allowed", "no usable", "no results",
		"unavailable", "not available", "not found", "no such file", "http 403", "http 429", "http 5"} {
		if strings.Contains(l, p) {
			return true
		}
	}
	return false
}

// giveUpPhrases sound like "it can't be done" (Persian with its half-spaces
// removed, and English). Only whole words count ("نشده" is not "نشد"), and
// words that also open healthy answers ("unfortunately", "متاسفانه") are not
// on the list.
var giveUpPhrases = []string{
	"نمیتونم", "نمیشه", "نتونستم", "امکانپذیر نیست", "ممکن نیست", "موفق نشدم", "برنمیاد", "قادر نیستم",
	"i can't", "i cannot", "i couldn't", "i could not", "i'm unable to", "i am unable to", "not possible", "isn't possible",
	"wasn't able", "was not able",
}

// givesUp reports an answer that says the job can't be done.
func givesUp(answer string) bool {
	s := strings.ToLower(strings.ReplaceAll(answer, "\u200c", ""))
	for _, p := range giveUpPhrases {
		for from := 0; ; {
			i := strings.Index(s[from:], p)
			if i < 0 {
				break
			}
			i += from
			if wordEdge(s, i, true) && wordEdge(s, i+len(p), false) {
				return true
			}
			from = i + len(p)
		}
	}
	return false
}

// wordEdge reports that the text has no letter right before (or after) i.
func wordEdge(s string, i int, before bool) bool {
	var r rune
	if before {
		if i == 0 {
			return true
		}
		r, _ = utf8.DecodeLastRuneInString(s[:i])
	} else {
		if i >= len(s) {
			return true
		}
		r, _ = utf8.DecodeRuneInString(s[i:])
	}
	return !unicode.IsLetter(r)
}

// maxNudges is how often a job may be sent back to try another way: twice
// for a long job (it has the budget to), once otherwise.
func maxNudges(maxSteps int) int {
	if maxSteps >= 16 {
		return 2
	}
	return 1
}

// noticeYouCan answers a model that says it cannot reach the internet, send
// files, download, make pictures or run code, although the tools for it are
// offered in this request.
const noticeYouCan = "[system notice] You do have the tools for what you just said you can't do (%s). Do the task with them now; " +
	"refuse only what the rules forbid."

// noticeEmpty answers an empty reply.
const noticeEmpty = "[system notice] Your reply was empty. Answer the user's message now."

// denials are "I have no such ability" phrases (Persian without half-spaces,
// and English) with the tools that give the ability. Feelings and opinions
// ("as an AI I don't have feelings") are not on the list.
var denials = []struct {
	ability string
	tools   []string
	phrases []string
}{
	{"web search and pages", []string{"web_search", "web_fetch", "browser", "browser_agent"}, []string{"دسترسی به اینترنت ندارم", "به اینترنت دسترسی ندارم",
		"اینترنت ندارم", "نمیتونم توی اینترنت", "نمیتونم در اینترنت", "به لینک دسترسی ندارم", "نمیتونم لینک رو باز کنم", "نمیتونم سایت رو باز کنم",
		"i don't have access to the internet", "i do not have access to the internet", "i can't browse", "i cannot browse", "i'm unable to browse",
		"i am unable to browse", "i can't access the internet", "i cannot access the internet", "i can't access links", "i cannot access links",
		"i can't open links"}},
	{"sending files", []string{"send_file"}, []string{"نمیتونم فایل بفرستم", "نمیتونم فایل ارسال کنم", "امکان ارسال فایل ندارم",
		"قادر به ارسال فایل نیستم", "i can't send files", "i cannot send files"}},
	{"downloads", []string{"download_file", "download_media_ytdlp"}, []string{"نمیتونم دانلود کنم", "امکان دانلود ندارم", "قادر به دانلود نیستم",
		"i can't download", "i cannot download"}},
	{"pictures", []string{"generate_image", "search_image", "edit_image"}, []string{"نمیتونم عکس بسازم", "نمیتونم تصویر بسازم",
		"i can't generate images", "i cannot generate images"}},
	{"running code", []string{"run_code", "bash_execute"}, []string{"نمیتونم کد اجرا کنم", "امکان اجرای کد ندارم", "i can't run code", "i cannot run code"}},
}

// deniesTools reports an answer that says it lacks an ability this request's
// tools (offered) give, and which ability.
func deniesTools(answer string, offered map[string]bool) (string, bool) {
	s := strings.ToLower(strings.ReplaceAll(answer, "\u200c", ""))
	s = strings.NewReplacer("’", "'", "نمی ", "نمی", "ي", "ی", "ك", "ک").Replace(s)
	for _, d := range denials {
		has := false
		for _, t := range d.tools {
			has = has || offered[t]
		}
		if !has {
			continue
		}
		for _, p := range d.phrases {
			if strings.Contains(s, p) {
				return d.ability, true
			}
		}
	}
	return "", false
}

// A weaker model often says what it is about to do («الان برات سرچ می‌کنم…»,
// "Let me check that for you.") and ends its turn without calling anything:
// nothing happens and the user waits for a reply that never comes. Such a
// short announcement is sent back once, to do it now.

// noticeNameless answers a reply whose tool call reached the bot without a
// tool name (the provider lost it): nothing ran.
const noticeNameless = "[system notice] Your tool call reached me without a tool name, so nothing ran. Call the tool again, " +
	"with its exact name and its parameters."

// noticeImitated answers a reply that wrote its tool calls out as text.
const noticeImitated = "[system notice] Your reply wrote tool calls out as text (%s): nothing ran, no file was made or changed, " +
	"and the user would read it as work done. Call the tools themselves now (not in the text), then answer with what really happened."

const noticeActNow = "[system notice] You said you would do it, but you called no tool, so nothing happened and the user is still " +
	"waiting. Do it now: call the tool, with its exact name and parameters. If no tool is needed, write the complete answer now " +
	"instead of announcing it."

var (
	// actNowRe is an announcement of work: an action about to happen.
	actNowRe = regexp.MustCompile(`(?i)\b(?:let me|i'?ll|i will|i'?m going to|i am going to|allow me to|first,? i'?ll|now i'?ll)\s+(?:\w+\s+){0,2}?` +
		`(?:search|look|check|find|fetch|download|create|make|generate|write|run|execute|try|open|read|analy[sz]e|convert|send|prepare|build|` +
		`draw|calculate|translate|summari[sz]e|compose|get|pull|grab|browse|scan|test)\b` +
		`|^(?:one moment|just a (?:moment|sec(?:ond)?)|please wait|hold on|working on it|on it)\b` +
		`|(?:الان|همین الان|یه لحظه|یک لحظه|چند لحظه|صبر کن|صبر کنید|بذار|بزار|اول)[^.!؟?\n]{0,40}?` +
		`(?:سرچ|جستجو|بگرد|میگرد|بررسی|چک|دانلود|بساز|میساز|بنویس|مینویس|اجرا|بفرست|میفرست|تبدیل|ترجمه|حساب|پیدا|ببین|درست|آماده|بخون|میخون)` +
		`|(?:دارم|داریم)\s+(?:\S+\s+){0,2}?(?:سرچ|جستجو|بررسی|دانلود|میسازم|مینویسم|اجرا|درست|آماده|پیدا|میگردم|میخونم)`)
	// promiseRe is the first person of an action about to happen («برات
	// می‌گیرم»، «دانلودش می‌کنم»). It counts only before any tool ran: after
	// one, «نتیجه رو جدا برات می‌فرستم» is a true answer (a background task).
	promiseRe = regexp.MustCompile(`(?:^|[^\p{L}])(?:می\s?(?:گیرم|گیرمش|گردم|سازم|سازمش|فرستم|فرستمش|نویسم|خونم|کشم|کشمش|آرم|ارم|یارم|آورم)|` +
		`(?:دانلود|پیدا|چک|بررسی|درست|آماده|اجرا|تبدیل|ترجمه|جستجو|سرچ|حساب|نصب)\s?(?:ش\s?|شون\s?)?می\s?کنم(?:ش)?)(?:[^\p{L}]|$)`)
	// actNowNot are answers that only sound like one: an offer, or a request to hear back.
	actNowNot = regexp.MustCompile(`(?i)let me know|if you (?:want|like|wish|need)|would you like|do you want|want me to|shall i|should i|` +
		`اگه (?:بخوای|خواستی|میخوای|بخواید|خواستید)|اگر (?:بخواهید|مایل)|میخوای|میخواید|بگو تا|خبرم کن|بهم بگو`)
)

// noticeClaimed answers a reply that says work is done when no tool ran.
const noticeClaimed = "[system notice] Your reply says it is done, but no tool ran in this request: nothing was set, made, saved " +
	"or sent. If the user asked for an action, do it now with the tool. If it was done in an earlier message, or nothing needs " +
	"doing, say that plainly, without claiming new work."

// claimsDoneRe is a reply saying an action was done: something set, made,
// saved, sent, run or deleted (writing a text needs no tool and is not one).
var claimsDoneRe = regexp.MustCompile(`(?i)(?:تنظیم|ثبت|ذخیره|ارسال|دانلود|اجرا|حذف|نصب|آپلود|زمانبندی|اضافه|ساخته|کشیده)\s*(?:شد|شده|گردید|کردم|کردمش)(?:[^\p{L}]|$)|` +
	`(?:ساختم|ساختمش|فرستادم|فرستادمش|کشیدمش|تیک زدم|برات گذاشتم|برات تنظیم|برات ثبت|دانلودش کردم|اجراش کردم|پاکش کردم)|` +
	`فایل\S*\s+(?:\S+\s+){0,3}?آماده\s*(?:شد|است|ست|ه)(?:[^\p{L}]|$)|` +
	`\bi(?:'ve| have)?\s+(?:set|scheduled|created|made|saved|sent|uploaded|downloaded|ran|executed|deleted|added|generated|drawn|built)\b|` +
	`\b(?:has|have) been (?:set|scheduled|created|saved|sent|uploaded|downloaded|deleted|added|generated)\b|` +
	`\b(?:reminder|file|chart|image)\b[^.\n]{0,30}\b(?:is ready|is set|was created|was saved)\b`)

// claimsDone reports a reply that says an action was done.
func claimsDone(answer string) bool {
	return claimsDoneRe.MatchString(strings.ReplaceAll(answer, "‌", ""))
}

// announcesWork reports a short reply that only says work is about to start.
// early: no tool ran in this request yet (a bare promise counts then).
func announcesWork(answer string, early bool) bool {
	s := strings.TrimSpace(strings.ReplaceAll(answer, "‌", ""))
	if s == "" || utf8.RuneCountInString(s) > 320 || strings.Contains(s, "```") || strings.Count(s, "\n") > 4 {
		return false
	}
	return (actNowRe.MatchString(s) || early && promiseRe.MatchString(s)) && !actNowNot.MatchString(s)
}

const noticeLookItUp = "[system notice] Your reply asks the user something you can find out yourself (what it is, which one, where " +
	"to get it): nothing was looked up. Search for it now and go on with the most likely meaning, saying in half a line which one you " +
	"took. Ask only what really is the user's to decide (their taste, their account, their own files), with the facts you found."

// asksFirstRe is a short reply that asks the user what something is, which
// one they mean, or for its link, where a search would tell.
var asksFirstRe = regexp.MustCompile(`(?i)(?:منظور(?:ت|تون|تان|ِ?شما)?\s+(?:از\s+)?\S.{0,60}?(?:چیه|چیست|چی\s*هست|کدومه|کدامه|کدومش|چی\s*بود)|` +
	`(?:^|[\s«"(])\S{2,40}\s+(?:دقیقاً\s+)?(?:چیه|چیست|چی\s*هست|چی\s*هستش)\s*[؟?]|` +
	`(?:کدوم|کدام)\s+(?:\S+\s+){0,4}?(?:رو|را)?\s*(?:می\s*خوای|میخوای|می\s*خواید|میخواید|می\s*خواهید|مد\s*نظرته|مد\s*نظرتونه|منظورته|منظورتونه)|` +
	`(?:لینک|آدرس|اسم\s+دقیق|نام\s+دقیق)\S*\s+(?:\S+\s+){0,3}?(?:بفرست|بده|بدید|داری|دارید|می\s*دی|میدی)|` +
	`(?:بیشتر|دقیق\s*تر)\s+توضیح\s+(?:بده|بدید|بدین)|توضیح\s+بیشتری\s+(?:بده|بدید|بدین)|` +
	`\bwhat\s+(?:do\s+you\s+mean|exactly\s+is|is|are)\b[^?\n]{0,60}\?|\bwhich\s+[^?\n]{0,40}\bdo\s+you\s+(?:mean|want)\b|` +
	`\b(?:send|share|give)\s+(?:me\s+)?(?:the|a)\s+link\b|\bcould\s+you\s+(?:clarify|specify|tell\s+me\s+more)\b)`)

// asksFirst reports a short reply that is mostly a question the tools could
// answer: what a thing is, which one, where to find it.
func asksFirst(answer string) bool {
	s := strings.TrimSpace(strings.ReplaceAll(answer, "‌", " "))
	if s == "" || utf8.RuneCountInString(s) > 500 || strings.Contains(s, "```") || !strings.ContainsAny(s, "؟?") {
		return false
	}
	return asksFirstRe.MatchString(s)
}
