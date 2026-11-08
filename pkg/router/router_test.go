package router

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		in   Input
		kind Kind
		hard bool
	}{
		{Input{Text: "سلام خوبی؟"}, Chat, false},
		{Input{Text: "مرسی دمت گرم"}, Chat, false},
		{Input{Text: "احتمالاً فردا میام، منطقیه؟"}, Quick, false},
		{Input{Text: "قیمت دلار امروز چنده؟"}, Quick, false},
		{Input{Text: "یادم بنداز ساعت ۸ دارو بخورم"}, Quick, false},
		{Input{Text: "این آهنگ رو دانلود کن https://youtu.be/xyz"}, Media, false},
		{Input{Text: "یه عکس از یه گربه فضانورد بساز"}, Media, false},
		{Input{Text: "یه کپشن قشنگ برای پست اینستاگرامم بنویس"}, Writing, false},
		{Input{Text: "این متن رو به انگلیسی ترجمه کن: سلام دنیا"}, Writing, false},
		{Input{Text: "یه کتاب خوب برای تعطیلات معرفی کن"}, Chat, false},
		{Input{Text: "این کد پایتون ارور میده:\n```\nTraceback (most recent call last)\n```"}, Code, false},
		{Input{Text: "یه تابع بنویس که لیست رو مرتب کنه"}, Code, false},
		{Input{Text: "یه ربات تلگرام کامل با پنل مدیریت و دیتابیس برام بساز"}, Project, true},
		{Input{Text: "build me a full-stack app with login and payments"}, Project, false},
		{Input{Text: "nginx روی سرورم ارور 502 میده درستش کن"}, Project, false},
		{Input{Text: "مقایسه کامل react و vue برای یه پروژه بزرگ"}, Research, true},
		{Input{Text: "یه تحقیق درباره تاریخ هخامنشیان با منابع معتبر"}, Research, false},
		{Input{Text: "انتگرال x^2 از ۰ تا ۱ رو حل کن"}, Reasoning, false},
		{Input{Text: "خلاصه‌اش کن", Files: []string{"report.pdf"}}, Documents, false},
		{Input{Text: "این چیه؟", Images: 1}, Vision, false},
		{Input{Text: "اینو درست کن", Files: []string{"main.go"}}, Code, false},
		{Input{Text: "سلام", Deep: true}, Chat, true},
		{Input{Text: "آیفون یا سامسونگ، کدوم بهتره؟"}, Research, false}, // «کدوم» is not «کد»
		{Input{Text: "پسته‌ها رو کجا بذارم؟"}, Quick, false},            // nor «پسته» a «پست»
		{Input{Text: "هواپیمای تهران مشهد کی می‌رسه؟"}, Quick, false},
		{Input{Text: "یه ایمیل بنویس:\n- تشکر\n- قیمت\n- زمان تحویل\n- شرایط پرداخت"}, Writing, true},
		{Input{Text: strings.Repeat("یه داستان بلند ", 200)}, Writing, true},
	} {
		v := Classify(c.in)
		if v.Kind != c.kind || v.Hard != c.hard {
			t.Errorf("%q: %s hard=%v, want %s hard=%v (%v)", c.in.Text, v.Kind, v.Hard, c.kind, c.hard, v.Signals)
		}
		if len(v.Signals) == 0 {
			t.Errorf("%q: no reason given", c.in.Text)
		}
	}
}

func TestDecide(t *testing.T) {
	code, chatHard, writing := Verdict{Kind: Code}, Verdict{Kind: Chat, Hard: true}, Verdict{Kind: Writing}
	for _, c := range []struct {
		v    Verdict
		s    Sensitivity
		want bool
	}{
		{code, Cheap, true}, {code, Balanced, true},
		{chatHard, Cheap, false}, {chatHard, Balanced, true},
		{writing, Balanced, false}, {writing, Quality, true},
		{Verdict{Kind: Media}, Quality, false},
	} {
		if got := Decide(c.v, DefaultStrong, c.s); got != c.want {
			t.Errorf("%+v %s: %v", c.v, c.s, got)
		}
	}
	if Decide(code, map[Kind]bool{}, Balanced) {
		t.Error("the admins' table decides")
	}
}

// More words of a kind are more evidence: a Python function that reads a
// CSV file is code, not a document.
func TestMoreWordsWeighMore(t *testing.T) {
	if v := Classify(Input{Text: "یه تابع پایتون بنویس که یه فایل csv رو بخونه و جمع ستون دوم رو برگردونه"}); v.Kind != Code {
		t.Errorf("%s %v", v.Kind, v.Signals)
	}
}

// The replied message decides only when the message's own words say nothing.
func TestTheRepliedMessageFillsIn(t *testing.T) {
	answer := "```go\nfunc main() { fmt.Println(\"hi\") }\n```\nThis python script fails with a traceback"
	if v := Classify(Input{Text: "اینو درست کن", Hint: answer}); v.Kind != Code || !v.Signals[0].Hint {
		t.Errorf("fix this: %s %v", v.Kind, v.Signals)
	}
	if v := Classify(Input{Text: "ممنون، عالی بود", Hint: answer}); v.Kind != Chat {
		t.Errorf("thanks: %s %v", v.Kind, v.Signals)
	}
}
