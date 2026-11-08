package telegram

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeMessage(t *testing.T, raw string) *Message {
	t.Helper()
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return &m
}

func TestRichMessageText(t *testing.T) {
	m := decodeMessage(t, `{"message_id":5,"chat":{"id":1,"type":"private"},"rich_message":{"is_rtl":true,"blocks":[
		{"type":"heading","size":2,"text":"قیمت‌ها"},
		{"type":"paragraph","text":["دلار ",{"type":"bold","text":"۱۰۰ هزار"}," تومن ",{"type":"custom_emoji","custom_emoji_id":"1","alternative_text":"💵"}]},
		{"type":"list","items":[{"label":"1.","blocks":[{"type":"paragraph","text":"یورو"}]},{"has_checkbox":true,"is_checked":true,"blocks":[{"type":"paragraph","text":"طلا"}]}]},
		{"type":"pre","language":"go","text":"fmt.Println(1)"},
		{"type":"table","cells":[[{"text":"a"},{"text":"b"}],[{"text":"1"},{"text":{"type":"url","text":"site","url":"https://x.example"}}]]},
		{"type":"blockquote","blocks":[{"type":"paragraph","text":"نقل‌قول"}],"credit":"کسی"},
		{"type":"photo","photo":[{"file_id":"p1","width":10,"height":10}],"caption":{"text":"عکس برج"}},
		{"type":"details","summary":"بیشتر","blocks":[{"type":"paragraph","text":"جزئیات"}]}
	]}}`)
	got := m.Body()
	for _, want := range []string{"## قیمت‌ها", "دلار ۱۰۰ هزار تومن 💵", "1. یورو", "☑ طلا", "```go\nfmt.Println(1)\n```",
		"| a | b |", "| 1 | site (https://x.example) |", "> نقل‌قول", "> — کسی", "[photo]", "عکس برج", "بیشتر", "  جزئیات"} {
		if !strings.Contains(got, want) {
			t.Errorf("rich text misses %q:\n%s", want, got)
		}
	}
	if (&Message{Text: "x", RichMessage: m.RichMessage}).Body() != "x" {
		t.Error("text wins over the rich message")
	}
}

func TestRepliedReadsEveryKindOfReply(t *testing.T) {

	m := decodeMessage(t, `{"message_id":9,"chat":{"id":-1,"type":"supergroup"},"text":"این رو بزرگ کن","reply_to_message":{"message_id":5,
		"from":{"id":42,"is_bot":true,"first_name":"Macron"},"chat":{"id":-1,"type":"supergroup"},
		"rich_message":{"blocks":[{"type":"slideshow","blocks":[{"type":"photo","photo":[{"file_id":"big","width":800,"height":600}]}]},{"type":"paragraph","text":"بفرما"}]}}}`)
	r := m.Replied()
	if r == nil || r.Body() != "[photo]\nبفرما" || len(r.Photo) != 1 || r.Photo[0].FileID != "big" {
		t.Fatalf("replied: %+v", r)
	}
	if m.ReplyToMessage.Photo != nil {
		t.Error("the update itself is left as it was")
	}

	topic := decodeMessage(t, `{"message_id":20,"chat":{"id":-2,"type":"supergroup","is_forum":true},"is_topic_message":true,"message_thread_id":3,
		"text":"مکرون سلام","reply_to_message":{"message_id":3,"from":{"id":7,"first_name":"Ali"},"chat":{"id":-2,"type":"supergroup"},"forum_topic_created":{"name":"Talk"}}}`)
	if topic.Replied() != nil || topic.RepliedInChat() != nil {
		t.Error("a topic's first message is not a reply")
	}

	ext := decodeMessage(t, `{"message_id":30,"chat":{"id":-3,"type":"supergroup"},"text":"@bot ترجمه کن","quote":{"text":"Good morning","position":0},
		"external_reply":{"origin":{"type":"user","date":1,"sender_user":{"id":99,"first_name":"Sara"}},"photo":[{"file_id":"ph","width":5,"height":5}]}}`)
	r = ext.Replied()
	if r == nil || r.Text != "Good morning" || r.From == nil || r.From.ID != 99 || len(r.Photo) != 1 || r.MessageID != -30 {
		t.Fatalf("external reply: %+v", r)
	}
	hidden := decodeMessage(t, `{"message_id":31,"chat":{"id":-3,"type":"supergroup"},"text":"؟",
		"external_reply":{"origin":{"type":"hidden_user","date":1,"sender_user_name":"Ghost"}}}`)
	if r := hidden.Replied(); r == nil || r.From == nil || r.From.FirstName != "Ghost" || r.From.ID != 0 {
		t.Errorf("a hidden author keeps their name: %+v", r)
	}
	if (&Message{Text: "hi"}).Replied() != nil {
		t.Error("a message that replies to nothing")
	}
}
