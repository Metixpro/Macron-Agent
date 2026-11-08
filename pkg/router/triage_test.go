package router

import (
	"strings"
	"testing"
)

func TestLocalTriageReadsTheWords(t *testing.T) {
	hist := []Turn{{"user", "یه اسکریپت پایتون بنویس که فایل‌ها رو مرتب کنه"}, {"assistant", "ساختمش: sort.py"}}
	cases := []struct {
		name    string
		in      TriageInput
		kind    Kind
		context string
		tools   func(float64) bool
	}{
		{"a greeting", TriageInput{Input: Input{Text: "سلام، خوبی؟"}}, Chat, ContextStandalone, func(p float64) bool { return p < 0.5 }},
		{"a multi-tool job", TriageInput{Input: Input{Text: "این ویدیو رو از یوتیوب دانلود کن، صداش رو mp3 کن و برام بفرست https://youtu.be/x"}},
			Media, ContextStandalone, func(p float64) bool { return p >= 0.8 }},
		{"a continuation", TriageInput{Input: Input{Text: "آره بکن"}, History: hist}, Chat, ContextRecent, nil},
		{"something said long ago", TriageInput{Input: Input{Text: "اون پروژه‌ای که دیروز گفتم رو ادامه بده"}, History: hist}, "", ContextEarlier, nil},
		{"a new request in a conversation", TriageInput{Input: Input{Text: "Write a Python function that parses CSV files and returns rows"}, History: hist},
			Code, ContextStandalone, func(p float64) bool { return p >= 0.8 }},
	}
	for _, c := range cases {
		tr := Local(c.in)
		if c.kind != "" && tr.Kind != c.kind {
			t.Errorf("%s: kind %s, want %s (%s)", c.name, tr.Kind, c.kind, tr)
		}
		if tr.Context != c.context {
			t.Errorf("%s: context %s, want %s (%s)", c.name, tr.Context, c.context, tr)
		}
		if c.tools != nil && !c.tools(tr.NeedsTools) {
			t.Errorf("%s: needs tools %.2f (%s)", c.name, tr.NeedsTools, tr)
		}
		if tr.Source != "local" || tr.Groups != nil {
			t.Errorf("%s: a local triage leaves the groups to the agent's focus: %+v", c.name, tr)
		}
	}
}

// A local triage is never sure enough for the bot's no-tools chat (it needs 0.7).
func TestLocalTriageIsCautious(t *testing.T) {
	for _, s := range []string{"سلام", "ممنون", "hi", "thanks a lot"} {
		if tr := Local(TriageInput{Input: Input{Text: s}}); tr.KindConf >= 0.7 {
			t.Errorf("%q: kind confidence %.2f", s, tr.KindConf)
		}
	}
	if tr := Local(TriageInput{Input: Input{Text: "یه سایت کامل فروشگاهی با پنل مدیریت و پرداخت بساز و روی سرور دیپلوی کن"}}); tr.Work < 2.5 {
		t.Errorf("a project is a lot of work: %s", tr)
	}
}

func TestParseTriage(t *testing.T) {
	tr, err := ParseTriage("Sure:\n```json\n{\"needs_tools\":0.02,\"kind\":\"chat\",\"kind_confidence\":0.95,\"work\":0.2," +
		"\"context\":\"standalone\",\"context_confidence\":0.88,\"groups\":{\"media\":0.71,\"code\":1.4}}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if tr.NeedsTools != 0.02 || tr.Kind != Chat || tr.KindConf != 0.95 || tr.Context != ContextStandalone || tr.Work != 0.2 ||
		tr.Groups["media"] != 0.71 || tr.Groups["code"] != 1 || tr.Source != "model" {
		t.Fatalf("triage: %+v", tr)
	}
	if p := tr.Picked(0.3); len(p) != 2 || p[0] != "code" || p[1] != "media" {
		t.Fatalf("picked: %v", p)
	}
}

func TestParseTriageMissingIsTheSafeSide(t *testing.T) {
	tr, err := ParseTriage(`{"kind":"rocket","kind_confidence":0.99,"context":"sideways"}`)
	if err != nil {
		t.Fatal(err)
	}
	if tr.NeedsTools != 1 || tr.Context != ContextEarlier || tr.Work != 2 || tr.Kind != "" || tr.KindConf != 0 {
		t.Fatalf("defaults: %+v", tr)
	}
	if _, err := ParseTriage("I think it is a chat."); err == nil {
		t.Fatal("prose is not a decision")
	}
}

func TestTriagePromptAsksEverything(t *testing.T) {
	p := TriagePrompt(TriageState([]Turn{{"user", "hi"}}, "", nil, "download this song"), []Group{{"media", "download music"}})
	for _, want := range []string{"needs_tools", "kind_confidence", "context_confidence", "groups", "- media: download music", "- project:", "Newest message:\ndownload this song"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}
