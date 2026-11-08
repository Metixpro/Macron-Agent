package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRichMessagePayloads(t *testing.T) {
	bot, f := newFakeTG(t)
	ctx := context.Background()
	kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{{Text: "پنل", CallbackData: "x", Style: StyleSuccess, IconCustomEmojiID: "5368324170671202286"}}}}
	if _, err := bot.SendRichMessage(ctx, 5, RichMessage{Markdown: "# سلام\n**دنیا**", IsRTL: true}, SendOptions{ReplyTo: 9, Markup: kb, EffectID: "eff"}); err != nil {
		t.Fatal(err)
	}
	b := f.bodies[0]
	if f.calls[0] != "sendRichMessage" || b["chat_id"].(float64) != 5 || b["message_effect_id"] != "eff" {
		t.Fatalf("%v %v", f.calls, b)
	}
	rm := b["rich_message"].(map[string]any)
	if rm["markdown"] != "# سلام\n**دنیا**" || rm["is_rtl"] != true || rm["html"] != nil {
		t.Errorf("rich_message: %v", rm)
	}
	rp := b["reply_parameters"].(map[string]any)
	if rp["message_id"].(float64) != 9 || rp["allow_sending_without_reply"] != true {
		t.Errorf("reply_parameters: %v", rp)
	}
	btn := b["reply_markup"].(map[string]any)["inline_keyboard"].([]any)[0].([]any)[0].(map[string]any)
	if btn["style"] != "success" || btn["icon_custom_emoji_id"] != "5368324170671202286" {
		t.Errorf("button: %v", btn)
	}

	bot.SendRichDraft(ctx, 5, 77, RichMessage{HTML: "<tg-thinking>…</tg-thinking>"}, true)
	d := f.bodies[1]
	if f.calls[1] != "sendRichMessageDraft" || d["draft_id"].(float64) != 77 || d["can_stop"] != true {
		t.Errorf("draft: %v", d)
	}
	bot.EditRichMessage(ctx, 5, 3, RichMessage{Markdown: "x"}, nil)
	if f.calls[2] != "editMessageText" || f.bodies[2]["rich_message"] == nil || f.bodies[2]["text"] != nil {
		t.Errorf("edit rich: %v", f.bodies[2])
	}
	bot.SetReaction(ctx, 5, 3, "🔥", true)
	r := f.bodies[3]
	if f.calls[3] != "setMessageReaction" || r["is_big"] != true || r["reaction"].([]any)[0].(map[string]any)["emoji"] != "🔥" {
		t.Errorf("reaction: %v", r)
	}
	bot.SetReaction(ctx, 5, 3, "", false)
	if got := f.bodies[4]["reaction"].([]any); len(got) != 0 {
		t.Errorf("clearing must send an empty list: %v", got)
	}
	bot.CopyMessage(ctx, 1, 2, 3, SendOptions{Silent: true})
	if f.calls[5] != "copyMessage" || f.bodies[5]["from_chat_id"].(float64) != 2 || f.bodies[5]["disable_notification"] != true {
		t.Errorf("copy: %v", f.bodies[5])
	}
	bot.SetMyCommandsScoped(ctx, []BotCommand{{Command: "admin", Description: "x"}}, &BotCommandScope{Type: "chat", ChatID: 42})
	if sc := f.bodies[6]["scope"].(map[string]any); sc["type"] != "chat" || sc["chat_id"].(float64) != 42 {
		t.Errorf("scope: %v", sc)
	}
}

func TestUnsupportedDetection(t *testing.T) {
	bot, f := newFakeTG(t)
	f.handler = func(m string, body map[string]any, w http.ResponseWriter) bool {
		tgError(w, 400, "Bad Request: method is not supported for this chat type", 0)
		return true
	}
	err := bot.SendRichDraft(context.Background(), -100, 1, RichMessage{Markdown: "x"}, false)
	if !IsUnsupported(err) || !IsBadRequest(err) {
		t.Fatalf("got %v", err)
	}
	if IsUnsupported(fmt.Errorf("network down")) {
		t.Error("plain errors are not 'unsupported'")
	}
}

func TestChatMemberAndInfo(t *testing.T) {
	cases := map[string]bool{"creator": true, "administrator": true, "member": true, "left": false, "kicked": false}
	for status, want := range cases {
		if (&ChatMember{Status: status}).Joined() != want {
			t.Errorf("%s", status)
		}
	}
	if !(&ChatMember{Status: "restricted", IsMember: true}).Joined() || (&ChatMember{Status: "restricted"}).Joined() {
		t.Error("restricted members")
	}
	bot, f := newFakeTG(t)
	f.handler = func(m string, body map[string]any, w http.ResponseWriter) bool {
		switch m {
		case "getChatMember":
			fmt.Fprint(w, `{"ok":true,"result":{"status":"member","user":{"id":5,"first_name":"A"}}}`)
		case "getChat":
			fmt.Fprint(w, `{"ok":true,"result":{"id":5,"type":"private","first_name":"A","bio":"گوفر 🐹","birthdate":{"day":1,"month":5},"personal_chat":{"id":-100,"type":"channel","title":"blog"}}}`)
		default:
			return false
		}
		return true
	}
	m, err := bot.GetChatMember(context.Background(), -100, 5)
	if err != nil || !m.Joined() {
		t.Fatalf("%+v %v", m, err)
	}
	info, err := bot.GetChat(context.Background(), int64(5))
	if err != nil || info.Bio != "گوفر 🐹" || info.Birthdate.Month != 5 || info.PersonalChat.Title != "blog" {
		t.Fatalf("%+v %v", info, err)
	}
}

func TestDownload(t *testing.T) {
	content := bytes.Repeat([]byte("x"), 5000)
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			size := 5000
			if body["file_id"] == "huge" {
				size = 50 << 20
			}
			fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"f","file_path":"photos/a.jpg","file_size":%d}}`, size)
		case r.URL.Path == "/file/bot1:T/photos/a.jpg":
			w.Write(content)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	bot := NewBotWithBase("1:T", srvURL)
	var buf bytes.Buffer
	n, err := bot.Download(context.Background(), "f", &buf, 1<<20)
	if err != nil || n != 5000 || buf.Len() != 5000 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := bot.Download(context.Background(), "huge", io.Discard, 20<<20); err != ErrFileTooLarge {
		t.Errorf("size from getFile must be checked first: %v", err)
	}
	if _, err := bot.Download(context.Background(), "f", io.Discard, 100); err != ErrFileTooLarge {
		t.Errorf("streamed size must be capped: %v", err)
	}
}

func TestLargestPhoto(t *testing.T) {
	m := &Message{Photo: []PhotoSize{{FileID: "s", Width: 90, Height: 60}, {FileID: "m", Width: 800, Height: 600}, {FileID: "l", Width: 2560, Height: 1920}}}
	if p := m.LargestPhoto(1600); p.FileID != "m" {
		t.Errorf("got %s", p.FileID)
	}
	if p := m.LargestPhoto(50); p.FileID != "s" {
		t.Errorf("all too big → smallest available, got %s", p.FileID)
	}
	if (&Message{}).LargestPhoto(100) != nil {
		t.Error("no photo")
	}
	if (&Message{Caption: "c"}).Body() != "c" || (&Message{Text: "t", Caption: "c"}).Body() != "t" {
		t.Error("Body")
	}
}

func TestSplitMarkdown(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&sb, "## بخش %d\nاین یک پاراگراف فارسی نسبتاً طولانی است که توضیح می‌دهد چه اتفاقی افتاده است.\n\n", i)
		if i == 10 {
			sb.WriteString("```go\n" + strings.Repeat("fmt.Println(\"hello\")\n", 15) + "```\n\n")
		}
		if i == 20 {
			sb.WriteString("| a | b |\n|---|---|\n| 1 | 2 |\n| 3 | 4 |\n\n")
		}
	}
	sb.WriteString("```python\n" + strings.Repeat("print('a very long line of python code')\n", 200) + "```\n")
	md := sb.String()

	parts := SplitMarkdown(md, 1500)
	if len(parts) < 5 {
		t.Fatalf("expected several parts, got %d", len(parts))
	}
	for i, p := range parts {
		if n := utf8.RuneCountInString(p); n > 1500 {
			t.Errorf("part %d has %d runes", i, n)
		}
		if strings.Count(p, "```")%2 != 0 {
			t.Errorf("part %d has an unbalanced code fence:\n%s", i, p)
		}
		if strings.Contains(p, "| 1 | 2 |") && !strings.Contains(p, "| a | b |") {
			t.Errorf("a table was split from its header")
		}
	}
	joined := strings.Join(parts, "\n")
	for _, want := range []string{"بخش 0", "بخش 39", "| 3 | 4 |", "fmt.Println"} {
		if !strings.Contains(joined, want) {
			t.Errorf("lost %q", want)
		}
	}
	if got := SplitMarkdown("short", 100); len(got) != 1 || got[0] != "short" {
		t.Errorf("short: %q", got)
	}
	if got := SplitMarkdown(strings.Repeat("x", 250), 100); len(got) != 3 {
		t.Errorf("one huge line must be hard-split: %d parts", len(got))
	}
}

func TestCloseOpenMarkdownAndRTL(t *testing.T) {
	if CloseOpenMarkdown("text\n```go\nfmt.") != "text\n```go\nfmt.\n```" || CloseOpenMarkdown("a ```b``` c") != "a ```b``` c" {
		t.Error("CloseOpenMarkdown")
	}
	if !IsMostlyRTL("سلام دنیا hello") || IsMostlyRTL("hello world سلام") || IsMostlyRTL("123 !!") {
		t.Error("IsMostlyRTL")
	}
	if NormalizeReaction("❤️") != "❤" || NormalizeReaction("🔥") != "🔥" || NormalizeReaction("🦖") != "" {
		t.Error("NormalizeReaction")
	}
}

func TestHardBreaksKeepTheLines(t *testing.T) {
	in := "🎵 آهنگ: X\n👤 خواننده: Y\n\n" +
		"```bash\nls -la\ncd /tmp\n```\n" +
		"| a | b |\n|---|---|\n| 1 | 2 |\n" +
		"# Title\ntext under it\n" +
		"Heading\n---\n" +
		"- item one\n- item two\n" +
		"~~~\nraw\nraw2\n~~~\n" +
		">quote one\n>quote two\n" +
		"already  \nbroken\\\nlast"
	want := "🎵 آهنگ: X  \n👤 خواننده: Y\n\n" +
		"```bash\nls -la\ncd /tmp\n```\n" +
		"| a | b |\n|---|---|\n| 1 | 2 |\n" +
		"# Title\ntext under it  \n" +
		"Heading\n---  \n" +
		"- item one  \n- item two\n" +
		"~~~\nraw\nraw2\n~~~\n" +
		">quote one  \n>quote two  \n" +
		"already  \nbroken\\\nlast"
	if got := HardBreaks(in); got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
	if HardBreaks("one line") != "one line" || HardBreaks("") != "" {
		t.Fatal("nothing to break")
	}

	gallery := "<tg-slideshow>\n![](tg://photo?id=m0)\n![](tg://photo?id=m1)\n</tg-slideshow>\n\nمتن\n![](tg://document?id=m3)\nپایان\nخط ✅ ![✅](tg://emoji?id=5)\nبعدی"
	want = strings.NewReplacer("پایان\n", "پایان  \n", "خط ✅ ![✅](tg://emoji?id=5)\n", "خط ✅ ![✅](tg://emoji?id=5)  \n").Replace(gallery)
	if got := HardBreaks(gallery); got != want {
		t.Fatalf("media: %q", got)
	}

	if got := HardBreaks("a\nb\n```\nc\nd"); got != "a  \nb\n```\nc\nd" {
		t.Fatalf("open fence: %q", got)
	}
}

func TestRichMessagesGoWithHardBreaks(t *testing.T) {
	rm := RichMessage{Markdown: "a\nb"}
	if w := rm.wire(); w.Markdown != "a  \nb" || rm.Markdown != "a\nb" {
		t.Fatalf("wire: %q (the original stays %q)", w.Markdown, rm.Markdown)
	}
	if h := (RichMessage{HTML: "a\nb"}).wire(); h.HTML != "a\nb" {
		t.Fatalf("HTML is left alone: %q", h.HTML)
	}
	g := GuestRich(rm, nil)
	if md := g["input_message_content"].(map[string]any)["rich_message"].(RichMessage).Markdown; md != "a  \nb" {
		t.Fatalf("guest: %q", md)
	}
}
