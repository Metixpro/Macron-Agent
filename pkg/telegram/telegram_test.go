package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

func TestMarkdownToHTML(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"escapes angle brackets and ampersands", "x < 5 && y > 3, R&D <script>", "x &lt; 5 &amp;&amp; y &gt; 3, R&amp;D &lt;script&gt;"},
		{"bold italic strike", "**bold** and *it* and ~~old~~", "<b>bold</b> and <i>it</i> and <s>old</s>"},
		{"inline code is escaped and protected", "use `a<b && **c**` now", "use <code>a&lt;b &amp;&amp; **c**</code> now"},
		{"fenced code", "```python\nif a < b:\n    print(\"**x**\")\n```", "<pre><code class=\"language-python\">if a &lt; b:\n    print(\"**x**\")</code></pre>"},
		{"fence without language", "```\nraw <b>\n```", "<pre>raw &lt;b&gt;</pre>"},
		{"unclosed fence still renders", "```js\nlet a = 1", "<pre><code class=\"language-js\">let a = 1</code></pre>"},
		{"headers", "# Title\n### Sub", "<b>Title</b>\n<b>Sub</b>"},
		{"bullets are not italics", "* one\n- two\n  + three", "• one\n• two\n  • three"},
		{"math is not italic", "2*3*4 = 24", "2*3*4 = 24"},
		{"snake_case untouched", "see deep_research_123.md and __init__", "see deep_research_123.md and <b>init</b>"},
		{"links", "[Go](https://go.dev/doc?a=1&b=2)", `<a href="https://go.dev/doc?a=1&amp;b=2">Go</a>`},
		{"javascript links are not linked", "[x](javascript:alert(1))", "[x](javascript:alert(1))"},
		{"blockquote", "> quoted <b>\n> more\nnormal", "<blockquote>quoted &lt;b&gt;\nmore</blockquote>\nnormal"},
		{"rule", "a\n---\nb", "a\n──────────\nb"},
		{"persian", "**سلام** دنیا، قیمت < ۵۰ تومان", "<b>سلام</b> دنیا، قیمت &lt; ۵۰ تومان"},
		{"table becomes pre", "| a | b |\n|---|---|\n| 1 | <2> |", "<pre>| a | b |\n| 1 | &lt;2&gt; |</pre>"},
		{"collapse blank lines", "a\n\n\n\n\nb", "a\n\nb"},
	}
	for _, c := range cases {
		got := MarkdownToHTML(c.in)
		if got != c.want {
			t.Errorf("%s:\n in:   %q\n got:  %q\n want: %q", c.name, c.in, got, c.want)
		}
		if !ValidHTML(got) {
			t.Errorf("%s: output is not valid Telegram HTML: %q", c.name, got)
		}
	}
}

func TestMarkdownNeverProducesInvalidHTML(t *testing.T) {
	nasty := []string{
		"**a *b** c*", "*a **b* c**", "`a **b` c**", "<b>raw</b> **x**", "[**x](http://a.b)**", "```\n</pre>\n```",
		"~~**a~~ b**", "> **a\n> b**", "__a **b__ c**", "<<>>&&;;", strings.Repeat("**", 99),
	}
	for _, s := range nasty {
		got := MarkdownToHTML(s)
		if !ValidHTML(got) {

			if plain := textutil.StripHTML(got); strings.Contains(plain, "\x00") {
				t.Errorf("placeholder leaked for %q: %q", s, plain)
			}
		}
		if strings.Contains(got, "\x00") {
			t.Errorf("placeholder leaked for %q: %q", s, got)
		}
	}
}

func TestSplitHTML(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("<blockquote expandable><pre>trace line\n</pre></blockquote>\n\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&sb, "<b>بند %d</b>: این یک جمله فارسی است که کمی طولانی است &amp; شامل <code>کد %d</code> و متن بیشتر.\n", i, i)
	}
	sb.WriteString("<pre><code class=\"language-go\">" + strings.Repeat("fmt.Println(\"x &lt; y\")\n", 300) + "</code></pre>\n")
	sb.WriteString(strings.Repeat("کلمه ", 2000))
	in := sb.String()

	chunks := SplitHTML(in, 500)
	if len(chunks) < 10 {
		t.Fatalf("expected many chunks, got %d", len(chunks))
	}
	for i, ch := range chunks {
		if !ValidHTML(ch) {
			t.Fatalf("chunk %d is not valid HTML on its own: %q", i, ch)
		}
		if n := utf8.RuneCountInString(textutil.StripHTML(ch)); n > 520 {
			t.Fatalf("chunk %d has %d visible chars", i, n)
		}
		if !utf8.ValidString(ch) {
			t.Fatalf("chunk %d has invalid UTF-8", i)
		}
		if strings.Contains(ch, "&amp") && !strings.Contains(ch, "&amp;") || strings.HasSuffix(strings.TrimRight(ch, "\n"), "&") {
			t.Fatalf("chunk %d splits an entity", i)
		}
	}

	var joined strings.Builder
	for _, ch := range chunks {
		joined.WriteString(textutil.StripHTML(ch))
	}
	if got, want := strings.Join(strings.Fields(joined.String()), ""), strings.Join(strings.Fields(textutil.StripHTML(in)), ""); got != want {
		t.Fatalf("content changed by splitting (%d vs %d chars)", len(got), len(want))
	}

	if got := SplitHTML("short <b>msg</b>", 100); len(got) != 1 || got[0] != "short <b>msg</b>" {
		t.Errorf("short input must stay in one chunk: %q", got)
	}
	if got := SplitHTML("", 100); len(got) != 1 {
		t.Errorf("empty input: %q", got)
	}

	if got := SplitHTML(strings.Repeat("x", 1000), 100); len(got) != 10 {
		t.Errorf("hard split: %d chunks", len(got))
	}
}

type fakeTG struct {
	mu      sync.Mutex
	calls   []string
	bodies  []map[string]any
	handler func(method string, body map[string]any, w http.ResponseWriter) bool
}

func newFakeTG(t *testing.T) (*Bot, *fakeTG) {
	t.Helper()
	f := &fakeTG{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var body map[string]any
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			json.NewDecoder(r.Body).Decode(&body)
		}
		f.mu.Lock()
		f.calls = append(f.calls, method)
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		if f.handler != nil && f.handler(method, body, w) {
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":7,"chat":{"id":1}}}`)
	}))
	t.Cleanup(srv.Close)
	return NewBotWithBase("123:SECRET", srv.URL), f
}

func tgError(w http.ResponseWriter, code int, desc string, retry int) {
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":%q,"parameters":{"retry_after":%d}}`, code, desc, retry)
}

func TestSendMessageFallsBackToPlainText(t *testing.T) {
	bot, f := newFakeTG(t)
	f.handler = func(m string, body map[string]any, w http.ResponseWriter) bool {
		if body["parse_mode"] == "HTML" {
			tgError(w, 400, "Bad Request: can't parse entities: Unsupported start tag", 0)
			return true
		}
		return false
	}
	if _, err := bot.SendMessage(context.Background(), 1, "<b>bold</b> &amp; <i>x", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.bodies) != 2 || f.bodies[1]["text"] != "bold & x" || f.bodies[1]["parse_mode"] != nil {
		t.Fatalf("expected a plain-text retry, got %v", f.bodies)
	}
}

func TestEditMessageHandling(t *testing.T) {
	bot, f := newFakeTG(t)
	f.handler = func(m string, body map[string]any, w http.ResponseWriter) bool {
		tgError(w, 400, "Bad Request: message is not modified", 0)
		return true
	}
	if err := bot.EditMessageText(context.Background(), 1, 2, "same", nil); err != nil {
		t.Fatalf("'not modified' must be ignored, got %v", err)
	}
	f.handler = func(m string, body map[string]any, w http.ResponseWriter) bool {
		tgError(w, 403, "Forbidden: bot was blocked by the user", 0)
		return true
	}
	err := bot.EditMessageText(context.Background(), 1, 2, "x", nil)
	if !IsUnreachable(err) {
		t.Fatalf("blocked bot must be detectable: %v", err)
	}
}

func TestRateLimitIsRetriedOnce(t *testing.T) {
	bot, f := newFakeTG(t)
	var n int
	f.handler = func(m string, body map[string]any, w http.ResponseWriter) bool {
		if n++; n == 1 {
			tgError(w, 429, "Too Many Requests: retry after 1", 1)
			return true
		}
		return false
	}
	start := time.Now()
	if err := bot.SendChatAction(context.Background(), 1, "typing"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("calls=%v elapsed=%v", f.calls, time.Since(start))
	}
}

func TestTokenIsScrubbedFromErrors(t *testing.T) {
	bot := NewBotWithBase("123:SECRET", "http://127.0.0.1:1")
	_, err := bot.GetMe(context.Background())
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("bot token leaked in error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bot.SendChatAction(ctx, 1, "typing"); !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("scrubbed errors must still unwrap: %v", err)
	}
}

func TestAPIErrorsAreTyped(t *testing.T) {
	bot, f := newFakeTG(t)
	f.handler = func(m string, body map[string]any, w http.ResponseWriter) bool {
		tgError(w, 401, "Unauthorized", 0)
		return true
	}
	_, err := bot.GetMe(context.Background())
	var api *APIError
	if !errors.As(err, &api) || api.Code != 401 {
		t.Fatalf("got %v", err)
	}
}

func TestUploadStreamsFileWithSanitizedName(t *testing.T) {
	var gotName, gotChat, gotCaption string
	var gotSize int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		f, hdr, err := r.FormFile("audio")
		if err != nil {
			t.Error(err)
			return
		}
		b, _ := io.ReadAll(f)
		gotName, gotSize = hdr.Filename, len(b)
		gotChat, gotCaption = r.FormValue("chat_id"), r.FormValue("caption")
		if r.FormValue("title") != "Song" {
			t.Errorf("title: %q", r.FormValue("title"))
		}
		fmt.Fprint(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()
	bot := NewBotWithBase("1:T", srv.URL)
	data := strings.NewReader(strings.Repeat("a", 300000))
	err := bot.SendAudio(context.Background(), 42, File{Name: "../../etc/we\"ird.mp3", Reader: data}, "Song", "Macron", "<b>cap</b>")
	if err != nil {
		t.Fatal(err)
	}
	if gotName != "we\"ird.mp3" || gotSize != 300000 || gotChat != "42" || gotCaption != "<b>cap</b>" {
		t.Fatalf("name=%q size=%d chat=%s caption=%q", gotName, gotSize, gotChat, gotCaption)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		tgError(w, 400, "Bad Request: file is too big", 0)
	}))
	defer srv2.Close()
	if err := NewBotWithBase("1:T", srv2.URL).SendDocument(context.Background(), 1, File{Name: "a", Reader: strings.NewReader("x")}, ""); err == nil {
		t.Fatal("expected API error")
	}
}

func TestSendHTMLChunksAttachesKeyboardToLast(t *testing.T) {
	bot, f := newFakeTG(t)
	long := strings.Repeat("<b>سلام</b> دنیا و متن بلند.\n", 600)
	kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{{Text: "x", CallbackData: "x"}}}}
	if err := bot.SendHTMLChunks(context.Background(), 1, long, kb); err != nil {
		t.Fatal(err)
	}
	if len(f.bodies) < 2 {
		t.Fatalf("expected several messages, got %d", len(f.bodies))
	}
	for i, b := range f.bodies {
		if utf8.RuneCountInString(textutil.StripHTML(b["text"].(string))) > MaxMessageChars {
			t.Errorf("message %d exceeds Telegram's limit", i)
		}
		if _, has := b["reply_markup"]; has != (i == len(f.bodies)-1) {
			t.Errorf("keyboard must be only on the last message (message %d)", i)
		}
	}
}

func TestFlexInt64(t *testing.T) {
	cases := map[string]FlexInt64{`123`: 123, `"456"`: 456, `" 789 "`: 789, `null`: 0, `"abc"`: 0, `-5`: -5}
	for in, want := range cases {
		var got FlexInt64
		if err := json.Unmarshal([]byte(in), &got); err != nil || got != want {
			t.Errorf("%s: got %d (%v), want %d", in, got, err, want)
		}
	}
}

func TestGetUpdatesToleratesStringDraftIDAndBadUpdates(t *testing.T) {
	bot, f := newFakeTG(t)
	f.handler = func(method string, body map[string]any, w http.ResponseWriter) bool {
		fmt.Fprint(w, `{"ok":true,"result":[
			{"update_id":10,"stopped_message_generation":{"chat":{"id":5,"type":"private"},"draft_id":"98765"}},
			{"update_id":11,"message":{"message_id":"oops","chat":{"id":5}}},
			{"update_id":12,"message":{"message_id":3,"chat":{"id":5,"type":"private"},"text":"hi"}}
		]}`)
		return true
	}
	ups, err := bot.GetUpdates(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 3 {
		t.Fatalf("want 3 updates (one empty), got %d", len(ups))
	}
	if s := ups[0].StoppedMessageGeneration; s == nil || s.DraftID != 98765 {
		t.Errorf("string draft_id not decoded: %+v", s)
	}
	if ups[1].UpdateID != 11 || ups[1].Message != nil {
		t.Errorf("a malformed update keeps its id so the offset advances: %+v", ups[1])
	}
	if ups[2].Message == nil || ups[2].Message.Text != "hi" {
		t.Errorf("updates after a bad one are kept: %+v", ups[2])
	}
	if got := f.bodies[0]["allowed_updates"]; !strings.Contains(fmt.Sprint(got), "stopped_message_generation") {
		t.Errorf("allowed_updates = %v", got)
	}
}

func TestPollsAndAlbums(t *testing.T) {
	var gotForm map[string][]string
	var gotFiles []string
	bot, f := newFakeTG(t)
	f.handler = func(method string, body map[string]any, w http.ResponseWriter) bool {
		switch method {
		case "sendPoll":
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":9,"chat":{"id":5},"poll":{"id":"P1","question":"q","options":[{"text":"a"},{"text":"b"}]}}}`)
			return true
		}
		return false
	}
	m, err := bot.SendPoll(context.Background(), 5, "کدوم؟", []string{"a", "b"}, true, SendOptions{ReplyTo: 3})
	if err != nil || m.Poll == nil || m.Poll.ID != "P1" {
		t.Fatalf("poll: %+v %v", m, err)
	}
	b := f.bodies[0]
	if b["is_anonymous"] != false || b["allows_multiple_answers"] != true || fmt.Sprint(b["options"]) != "[map[text:a] map[text:b]]" || b["reply_parameters"] == nil {
		t.Errorf("sendPoll payload: %v", b)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		gotForm = r.MultipartForm.Value
		for k, fh := range r.MultipartForm.File {
			gotFiles = append(gotFiles, k+"="+fh[0].Filename)
		}
		fmt.Fprint(w, `{"ok":true,"result":[]}`)
	}))
	defer srv.Close()
	ab := NewBotWithBase("1:x", srv.URL)
	items := []MediaItem{{Type: "photo", File: File{Name: "a.png", Reader: strings.NewReader("1")}}, {Type: "video", File: File{Name: "b.mp4", Reader: strings.NewReader("2")}}}
	if err := ab.SendMediaGroup(context.Background(), 7, items, SendOptions{ReplyTo: 4}); err != nil {
		t.Fatal(err)
	}
	if gotForm["media"][0] != `[{"media":"attach://f0","type":"photo"},{"media":"attach://f1","type":"video"}]` || gotForm["chat_id"][0] != "7" {
		t.Errorf("album form: %v", gotForm)
	}
	if strings.Join(gotFiles, ",") != "f0=a.png,f1=b.mp4" && strings.Join(gotFiles, ",") != "f1=b.mp4,f0=a.png" {
		t.Errorf("album files: %v", gotFiles)
	}
	if err := ab.SendMediaGroup(context.Background(), 7, items[:1], SendOptions{}); err == nil {
		t.Error("an album needs at least two items")
	}

	var u Update
	if err := json.Unmarshal([]byte(`{"update_id":1,"poll_answer":{"poll_id":"P1","user":{"id":5,"first_name":"A"},"option_ids":[1]}}`), &u); err != nil ||
		u.PollAnswer == nil || u.PollAnswer.User.ID != 5 || u.PollAnswer.OptionIDs[0] != 1 {
		t.Errorf("poll answer: %+v %v", u.PollAnswer, err)
	}
}

func TestGuestMessageUpdate(t *testing.T) {
	var u Update
	raw := `{"update_id":9,"guest_message":{"message_id":3,"chat":{"id":-100,"type":"supergroup"},"from":{"id":5,"first_name":"A"},
		"text":"@WazeBot 2+2","guest_query_id":"GQ1","reply_to_message":{"message_id":2,"chat":{"id":-100,"type":"supergroup"},"text":"hi"}}}`
	if err := json.Unmarshal([]byte(raw), &u); err != nil || u.GuestMessage == nil || u.GuestMessage.GuestQueryID != "GQ1" ||
		u.GuestMessage.ReplyToMessage == nil {
		t.Fatalf("guest message: %+v %v", u.GuestMessage, err)
	}
	var me User
	_ = json.Unmarshal([]byte(`{"id":1,"is_bot":true,"first_name":"W","supports_guest_queries":true}`), &me)
	if !me.SupportsGuestQueries {
		t.Error("getMe reports guest mode")
	}
	found := false
	for _, a := range AllowedUpdates {
		found = found || a == "guest_message"
	}
	if !found {
		t.Error("guest messages must be requested from getUpdates")
	}
	bot, f := newFakeTG(t)
	f.handler = func(m string, body map[string]any, w http.ResponseWriter) bool {
		if m == "answerGuestQuery" {
			fmt.Fprint(w, `{"ok":true,"result":{"inline_message_id":"IM-1"}}`)
			return true
		}
		return false
	}
	id, err := bot.AnswerGuestQuery(context.Background(), "GQ1", GuestRich(RichMessage{Markdown: "**4**"}, nil))
	if err != nil || id != "IM-1" {
		t.Fatalf("the sent guest message can be edited later: %q %v", id, err)
	}
	b := f.bodies[0]
	if b["guest_query_id"] != "GQ1" || fmt.Sprint(b["result"]) != "map[id:answer input_message_content:map[rich_message:map[markdown:**4**]] title:Macron Ai type:article]" {
		t.Errorf("answerGuestQuery payload: %v", b)
	}

	if err := bot.EditInlineRich(context.Background(), "IM-1", RichMessage{Markdown: "**۴**", IsRTL: true}, nil); err != nil {
		t.Fatal(err)
	}
	if e := f.bodies[1]; f.calls[1] != "editMessageText" || e["inline_message_id"] != "IM-1" || e["chat_id"] != nil || e["rich_message"] == nil || e["text"] != nil {
		t.Errorf("a rich inline edit: %v", e)
	}
	if err := bot.EditInlineHTML(context.Background(), "IM-1", "<b>4</b>", nil); err != nil {
		t.Fatal(err)
	}
	if e := f.bodies[2]; e["inline_message_id"] != "IM-1" || e["text"] != "<b>4</b>" || e["parse_mode"] != "HTML" {
		t.Errorf("an HTML inline edit: %v", e)
	}

	f.handler = nil
	if id, err := bot.AnswerGuestQuery(context.Background(), "GQ2", GuestHTML("x", nil)); err != nil || id != "" {
		t.Errorf("true as the result: %q %v", id, err)
	}
}

func TestOnSentHearsMessagesAndAlbums(t *testing.T) {
	bot, f := newFakeTG(t)
	f.handler = func(m string, body map[string]any, w http.ResponseWriter) bool {
		if m == "sendMediaGroup" {
			fmt.Fprint(w, `{"ok":true,"result":[{"message_id":8,"chat":{"id":-5}},{"message_id":9,"chat":{"id":-5}}]}`)
			return true
		}
		return false
	}
	type key struct{}
	var mu sync.Mutex
	var heard []string
	bot.OnSent = func(ctx context.Context, chatID, messageID int64) {
		mu.Lock()
		heard = append(heard, fmt.Sprintf("%v:%d:%d", ctx.Value(key{}), chatID, messageID))
		mu.Unlock()
	}
	ctx := context.WithValue(context.Background(), key{}, "job")
	if _, err := bot.SendMessage(ctx, 1, "hi", nil); err != nil {
		t.Fatal(err)
	}
	items := []MediaItem{{Type: "photo", File: File{Name: "a.jpg", Reader: strings.NewReader("a")}}, {Type: "photo", File: File{Name: "b.jpg", Reader: strings.NewReader("b")}}}
	if err := bot.SendMediaGroup(ctx, -5, items, SendOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(heard, " "); got != "job:1:7 job:-5:8 job:-5:9" {
		t.Errorf("heard %q", got)
	}
}
