package agent

import (
	"strings"
	"unicode"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

const toolUseTools = "use_tools"

var useToolsDef = llm.Tool{Type: "function", Function: llm.ToolDef{Name: toolUseTools,
	Description: "Get your tools (search, files, pictures, downloads, reminders, code…). Call it first when the message needs any of them.",
	Parameters:  map[string]any{"type": "object", "properties": map[string]any{}}}}

const (
	toolsReady        = "Your tools are available now. Carry on with the user's message."
	chatHistoryTokens = 6000
	smallTalkRunes    = 80
	smallTalkWords    = 12
)

var smallTalkVocab, drawnOut = map[string]bool{}, map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
		سلام سلامم درود هلو های عصر صبح شب ظهر روز وقت بخیر خیر مبارک عیدت عید
		خوبی خوبید خوبین خوبه خوبم خوب چطوری چطورین چطورید چطوره چخبر خبر خبرا حالت حالتون حال احوال احوالت
		عالی عالیه عالیم نیستم هستم هستی هست بود بودی شد منم توام تو شما ما خودت خودتون
		مرسی ممنون ممنونم متشکرم متشکر تشکر سپاس سپاسگزارم مچکرم مچکر دمت دمتگرم گرم دستت درد نکنه قربونت قربون برم فدات شم فدای لطف لطفت داری کردی
		ایول آفرین افرین قشنگه جالبه جالب باحاله باحال واو وای اوه آها اها عجب چقدر
		خخ هه
		خداحافظ خدافظ بای فعلا فعلاً بعدا بعداً میبینمت خوش بخوابی
		جان جون عزیز عزیزم داداش داش رفیق رفیقم دوست دوستم مکرون ربات بات
		خیلی واقعا واقعاً همینطور هم که و دیگه یه یک
		hi hello hey hiya yo salam slm sup good morning evening afternoon night gm gn day
		how are you u doing fine great im i am too thanks thank thx ty tnx merci mersi mrc mamnoon
		nice wow cool awesome amazing perfect lol haha hahaha lmao
		bye goodbye cya later see
		dear bro buddy mate man sir macron bot very so much a lot and the to`) {
		smallTalkVocab[w], drawnOut[dedupe(w)] = true, true
	}
}

func IsSmallTalk(text string) bool {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) == 0 || len(runes) > smallTalkRunes || strings.Contains(text, "://") || strings.Contains(text, "www.") {
		return false
	}
	text = strings.NewReplacer("چه خبر", "چخبر", "چه‌خبر", "چخبر").Replace(text)
	var words []string
	var word []rune
	flush := func() {
		if len(word) > 0 {
			words = append(words, string(word))
			word = word[:0]
		}
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsDigit(r):
			return false
		case r == 'ي':
			word = append(word, 'ی')
		case r == 'ك':
			word = append(word, 'ک')
		case unicode.IsLetter(r):
			word = append(word, r)
		case unicode.Is(unicode.Mn, r):
		default:
			flush()
		}
	}
	flush()
	if len(words) > smallTalkWords {
		return false
	}
	for _, w := range words {
		if !smallTalkVocab[w] && !drawnOut[dedupe(w)] {
			return false
		}
	}
	return true
}

func dedupe(w string) string {
	var out []rune
	for _, r := range w {
		if n := len(out); n > 0 && out[n-1] == r {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
