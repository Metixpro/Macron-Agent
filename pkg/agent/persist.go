package agent

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const noticePersist = "[system notice] Don't give up yet: a step failed, but there is budget left. Before answering, try another real " +
	"approach: a different tool, source, site, parameters or format, a web search for a workaround, or doing it with code. If you " +
	"already tried at least two different ways, or the request itself has to be refused, give your answer now and say briefly what you tried."

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

var giveUpPhrases = []string{
	"نمیتونم", "نمیشه", "نتونستم", "امکانپذیر نیست", "ممکن نیست", "موفق نشدم", "برنمیاد", "قادر نیستم",
	"i can't", "i cannot", "i couldn't", "i could not", "i'm unable to", "i am unable to", "not possible", "isn't possible",
	"wasn't able", "was not able",
}

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

func maxNudges(maxSteps int) int {
	if maxSteps >= 16 {
		return 2
	}
	return 1
}

const noticeYouCan = "[system notice] You do have the tools for what you just said you can't do (%s). Do the task with them now; " +
	"refuse only what the rules forbid."

const noticeEmpty = "[system notice] Your reply was empty. Answer the user's message now."

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
	{"pictures", []string{"search_image"}, []string{"نمیتونم عکس پیدا کنم", "نمیتونم تصویر پیدا کنم",
		"i can't find images", "i cannot find images"}},
	{"running code", []string{"run_code", "bash_execute"}, []string{"نمیتونم کد اجرا کنم", "امکان اجرای کد ندارم", "i can't run code", "i cannot run code"}},
}

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

const noticeNameless = "[system notice] Your tool call reached me without a tool name, so nothing ran. Call the tool again, " +
	"with its exact name and its parameters."

const noticeImitated = "[system notice] Your reply wrote tool calls out as text (%s): nothing ran, no file was made or changed, " +
	"and the user would read it as work done. Call the tools themselves now (not in the text), then answer with what really happened."

const noticeActNow = "[system notice] You said you would do it, but you called no tool, so nothing happened and the user is still " +
	"waiting. Do it now: call the tool, with its exact name and parameters. If no tool is needed, write the complete answer now " +
	"instead of announcing it."

var (
	actNowRe = regexp.MustCompile(`(?i)\b(?:let me|i'?ll|i will|i'?m going to|i am going to|allow me to|first,? i'?ll|now i'?ll)\s+(?:\w+\s+){0,2}?` +
		`(?:search|look|check|find|fetch|download|create|make|generate|write|run|execute|try|open|read|analy[sz]e|convert|send|prepare|build|` +
		`draw|calculate|translate|summari[sz]e|compose|get|pull|grab|browse|scan|test)\b` +
		`|^(?:one moment|just a (?:moment|sec(?:ond)?)|please wait|hold on|working on it|on it)\b` +
		`|(?:الان|همین الان|یه لحظه|یک لحظه|چند لحظه|صبر کن|صبر کنید|بذار|بزار|اول)[^.!؟?\n]{0,40}?` +
		`(?:سرچ|جستجو|بگرد|میگرد|بررسی|چک|دانلود|بساز|میساز|بنویس|مینویس|اجرا|بفرست|میفرست|تبدیل|ترجمه|حساب|پیدا|ببین|درست|آماده|بخون|میخون)` +
		`|(?:دارم|داریم)\s+(?:\S+\s+){0,2}?(?:سرچ|جستجو|بررسی|دانلود|میسازم|مینویسم|اجرا|درست|آماده|پیدا|میگردم|میخونم)`)

	promiseRe = regexp.MustCompile(`(?:^|[^\p{L}])(?:می\s?(?:گیرم|گیرمش|گردم|سازم|سازمش|فرستم|فرستمش|نویسم|خونم|کشم|کشمش|آرم|ارم|یارم|آورم)|` +
		`(?:دانلود|پیدا|چک|بررسی|درست|آماده|اجرا|تبدیل|ترجمه|جستجو|سرچ|حساب|نصب)\s?(?:ش\s?|شون\s?)?می\s?کنم(?:ش)?)(?:[^\p{L}]|$)`)

	actNowNot = regexp.MustCompile(`(?i)let me know|if you (?:want|like|wish|need)|would you like|do you want|want me to|shall i|should i|` +
		`اگه (?:بخوای|خواستی|میخوای|بخواید|خواستید)|اگر (?:بخواهید|مایل)|میخوای|میخواید|بگو تا|خبرم کن|بهم بگو`)
)

const noticeClaimed = "[system notice] Your reply says it is done, but no tool ran in this request: nothing was set, made, saved " +
	"or sent. If the user asked for an action, do it now with the tool. If it was done in an earlier message, or nothing needs " +
	"doing, say that plainly, without claiming new work."

var claimsDoneRe = regexp.MustCompile(`(?i)(?:تنظیم|ثبت|ذخیره|ارسال|دانلود|اجرا|حذف|نصب|آپلود|زمانبندی|اضافه|ساخته|کشیده)\s*(?:شد|شده|گردید|کردم|کردمش)(?:[^\p{L}]|$)|` +
	`(?:ساختم|ساختمش|فرستادم|فرستادمش|کشیدمش|تیک زدم|برات گذاشتم|برات تنظیم|برات ثبت|دانلودش کردم|اجراش کردم|پاکش کردم)|` +
	`فایل\S*\s+(?:\S+\s+){0,3}?آماده\s*(?:شد|است|ست|ه)(?:[^\p{L}]|$)|` +
	`\bi(?:'ve| have)?\s+(?:set|scheduled|created|made|saved|sent|uploaded|downloaded|ran|executed|deleted|added|generated|drawn|built)\b|` +
	`\b(?:has|have) been (?:set|scheduled|created|saved|sent|uploaded|downloaded|deleted|added|generated)\b|` +
	`\b(?:reminder|file|chart|image)\b[^.\n]{0,30}\b(?:is ready|is set|was created|was saved)\b`)

func claimsDone(answer string) bool {
	return claimsDoneRe.MatchString(strings.ReplaceAll(answer, "‌", ""))
}

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

var asksFirstRe = regexp.MustCompile(`(?i)(?:منظور(?:ت|تون|تان|ِ?شما)?\s+(?:از\s+)?\S.{0,60}?(?:چیه|چیست|چی\s*هست|کدومه|کدامه|کدومش|چی\s*بود)|` +
	`(?:^|[\s«"(])\S{2,40}\s+(?:دقیقاً\s+)?(?:چیه|چیست|چی\s*هست|چی\s*هستش)\s*[؟?]|` +
	`(?:کدوم|کدام)\s+(?:\S+\s+){0,4}?(?:رو|را)?\s*(?:می\s*خوای|میخوای|می\s*خواید|میخواید|می\s*خواهید|مد\s*نظرته|مد\s*نظرتونه|منظورته|منظورتونه)|` +
	`(?:لینک|آدرس|اسم\s+دقیق|نام\s+دقیق)\S*\s+(?:\S+\s+){0,3}?(?:بفرست|بده|بدید|داری|دارید|می\s*دی|میدی)|` +
	`(?:بیشتر|دقیق\s*تر)\s+توضیح\s+(?:بده|بدید|بدین)|توضیح\s+بیشتری\s+(?:بده|بدید|بدین)|` +
	`\bwhat\s+(?:do\s+you\s+mean|exactly\s+is|is|are)\b[^?\n]{0,60}\?|\bwhich\s+[^?\n]{0,40}\bdo\s+you\s+(?:mean|want)\b|` +
	`\b(?:send|share|give)\s+(?:me\s+)?(?:the|a)\s+link\b|\bcould\s+you\s+(?:clarify|specify|tell\s+me\s+more)\b)`)

func asksFirst(answer string) bool {
	s := strings.TrimSpace(strings.ReplaceAll(answer, "‌", " "))
	if s == "" || utf8.RuneCountInString(s) > 500 || strings.Contains(s, "```") || !strings.ContainsAny(s, "؟?") {
		return false
	}
	return asksFirstRe.MatchString(s)
}
