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
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestScrubEmptyTokenNoOp(t *testing.T) {
	b := &Bot{Token: ""}
	orig := errors.New("connection reset by peer")
	if got := b.scrub(orig); got != orig {
		t.Fatalf("scrub with empty token altered error: %v", got)
	}

	b.Token = "12345:SECRET_TOKEN"
	scrubbed := b.scrub(errors.New("POST https://api.telegram.org/bot12345:SECRET_TOKEN/getMe: timeout"))
	if strings.Contains(scrubbed.Error(), "12345:SECRET_TOKEN") {
		t.Fatalf("scrub leaked bot token: %s", scrubbed)
	}
	if !strings.Contains(scrubbed.Error(), "<token>") {
		t.Fatalf("scrub missing placeholder: %s", scrubbed)
	}
}

func TestMarkdownToHTMLUnderscoreURLSpoilerAndSafeSchemes(t *testing.T) {
	in := strings.Join([]string{
		"See [Go](https://en.wikipedia.org/wiki/Go_(programming_language)) and [**bold_link**](https://example.com/a_b_c?x=1&y=2).",
		"Ignore [bad](javascript:alert(1)) and ||hidden secret|| but keep `||literal||`.",
	}, "\n")
	out := MarkdownToHTML(in)
	if !strings.Contains(out, `<a href="https://en.wikipedia.org/wiki/Go_(programming_language)">Go</a>`) {
		t.Fatalf("underscore/paren URL mangled: %s", out)
	}
	if !strings.Contains(out, `<a href="https://example.com/a_b_c?x=1&amp;y=2"><b>bold_link</b></a>`) {
		t.Fatalf("link label or query mangled: %s", out)
	}
	if strings.Contains(out, "<i>") {
		t.Fatalf("italic tags injected from URL underscores: %s", out)
	}
	if strings.Contains(out, `href="javascript:`) {
		t.Fatalf("javascript URL rendered as href: %s", out)
	}
	if !strings.Contains(out, `<tg-spoiler>hidden secret</tg-spoiler>`) {
		t.Fatalf("missing tg-spoiler tag: %s", out)
	}
	if !strings.Contains(out, `<code>||literal||</code>`) {
		t.Fatalf("inline code spoiler altered: %s", out)
	}
	if !ValidHTML(out) {
		t.Fatalf("output invalid HTML: %s", out)
	}
}

func TestSplitHTMLUTF16AndEntityBoundary(t *testing.T) {
	// Each 🚀 is 1 rune but 2 UTF-16 code units. Limit 6 -> at most 3 🚀 per chunk.
	emojis := "<b>" + strings.Repeat("🚀", 10) + "</b>"
	chunks := SplitHTML(emojis, 6)
	if len(chunks) < 4 {
		t.Fatalf("expected >= 4 chunks for 20 UTF-16 units with limit 6, got %d (%v)", len(chunks), chunks)
	}
	for i, c := range chunks {
		if !ValidHTML(c) {
			t.Fatalf("chunk %d invalid HTML: %q", i, c)
		}
		if n := visibleUTF16Len(c); n > 6 {
			t.Fatalf("chunk %d exceeds UTF-16 limit 6: got %d (%q)", i, n, c)
		}
	}

	// &amp; starting right on cut boundary must stay intact.
	ent := SplitHTML("aaaaa&amp;bbbbb", 6)
	if ent[0] != "aaaaa&amp;" {
		t.Fatalf("entity should occupy one visible character: %q", ent)
	}
	for i, c := range ent {
		if !ValidHTML(c) {
			t.Fatalf("entity chunk %d invalid: %q", i, c)
		}
		stripped := strings.ReplaceAll(c, "&amp;", "")
		if strings.Contains(stripped, "&") {
			t.Fatalf("entity chunk %d split inside entity: %q", i, c)
		}
	}
	if got := SafeHTMLCaption("<b>"+strings.Repeat("&amp;", 1024)+"</b>", 1024); visibleUTF16Len(got) != 1024 || strings.HasSuffix(got, "…") {
		t.Fatal("a caption fitting the limit after entity parsing was truncated")
	}
	if got := SafeHTMLCaption("<b>&#x1f680;</b>", 1); got != "…" {
		t.Fatalf("caption with insufficient room for an astral character = %q", got)
	}
}

func TestSafeHTMLCaptionUTF16AndBalancing(t *testing.T) {
	short := "<b>OK 🚀</b>"
	if got := SafeHTMLCaption(short, 20); got != short {
		t.Fatalf("short caption modified: %q", got)
	}

	long := "<b>Status &amp; progress: " + strings.Repeat("🚀", 30) + "</b>"
	got := SafeHTMLCaption(long, 20)
	if !ValidHTML(got) {
		t.Fatalf("truncated caption invalid HTML: %q", got)
	}
	if n := visibleUTF16Len(got); n > 20 {
		t.Fatalf("truncated caption visible UTF-16 %d > 20: %q", n, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated caption missing ellipsis: %q", got)
	}
}

func TestSplitBigBlockLongFenceClamp(t *testing.T) {
	lang := strings.Repeat("x", 120)
	md := "```" + lang + "\n" + strings.Repeat("line\n", 15) + "```"
	parts := SplitMarkdown(md, 40)
	if len(parts) == 0 {
		t.Fatal("expected split parts")
	}
	for _, part := range parts {
		if utf8.RuneCountInString(part) > 40 || CloseOpenMarkdown(part) != part {
			t.Fatalf("unbalanced or overlong chunk: %q", part)
		}
	}
}

func TestMixedFencesKeepCodeLiteral(t *testing.T) {
	inner := "```\n**plain**\n```"
	partial := "~~~~md\n" + inner
	if got := CloseOpenMarkdown(partial); got != partial+"\n~~~~" {
		t.Fatalf("wrong closing marker: %q", got)
	}
	md := partial + "\n~~~~\nAfter"
	out := MarkdownToHTML(md)
	if !strings.Contains(out, "<pre><code class=\"language-md\">"+inner+"</code></pre>\nAfter") {
		t.Fatalf("nested code marker or literal Markdown was altered: %q", out)
	}
}

func TestFoldMalformedTGTagNoPanic(t *testing.T) {
	blocks := foldBlocks("<tg-\nnext line")
	if len(blocks) == 0 {
		t.Fatal("expected parsed fold blocks")
	}
	_ = FoldLong(strings.Repeat("word ", 600)+"<tg-\n</tg-x>", FoldOptions{MinRunes: 10, More: "more"})
}

func TestPollBoundsAndRetry429(t *testing.T) {
	var hits atomic.Int32
	payloads := make(chan map[string]any, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode poll request: %v", err)
		}
		payloads <- payload
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":          false,
				"error_code":  429,
				"description": "Too Many Requests",
				"parameters":  map[string]any{"retry_after": 1},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"message_id": 42,
				"chat":       map[string]any{"id": 1},
			},
		})
	}))
	defer srv.Close()

	b := NewBot("123:ABC")
	b.APIURL = srv.URL

	if _, err := b.SendPoll(context.Background(), 1, "Q?", []string{"only one"}, false, SendOptions{}); err == nil {
		t.Fatal("expected SendPoll to fail with <2 options")
	}

	opts := make([]string, 12)
	for i := range opts {
		opts[i] = strings.Repeat("o", 120)
	}
	q := strings.Repeat("?", 350)
	msg, err := b.SendPoll(context.Background(), 1, q, opts, false, SendOptions{})
	if err != nil {
		t.Fatalf("SendPoll failed after 429 retry: %v", err)
	}
	if msg.MessageID != 42 || hits.Load() != 2 {
		t.Fatalf("unexpected retry state: hits=%d msg=%+v", hits.Load(), msg)
	}
	<-payloads
	gotPayload := <-payloads
	if gotOpts, ok := gotPayload["options"].([]any); !ok || len(gotOpts) != 10 {
		t.Fatalf("expected 10 capped options, got %v", gotPayload["options"])
	} else if n := utf8.RuneCountInString(gotOpts[0].(map[string]any)["text"].(string)); n != 100 {
		t.Fatalf("truncated option length = %d, want 100", n)
	}
	if n := utf8.RuneCountInString(gotPayload["question"].(string)); n != 300 {
		t.Fatalf("truncated poll question length = %d, want 300", n)
	}
}

func TestUploadRetriesReplayableMediaFromStartingOffsets(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("decode media request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for field, want := range map[string]string{"f0": "first", "f1": "second"} {
			f, _, err := r.FormFile(field)
			if err != nil {
				t.Errorf("missing %s: %v", field, err)
				continue
			}
			got, err := io.ReadAll(f)
			f.Close()
			if err != nil || string(got) != want {
				t.Errorf("file %s = %q, %v", field, got, err)
			}
		}
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"ok":false,"error_code":503,"description":"Unavailable"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":[]}`)
	}))
	defer srv.Close()

	first := strings.NewReader("skipfirst")
	if _, err := first.Seek(4, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	b := NewBotWithBase("test", srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	err := b.SendMediaGroup(ctx, 1, []MediaItem{
		{Type: "photo", File: File{Name: "first.png", Reader: first}},
		{Type: "photo", File: File{Name: "second.png", Reader: strings.NewReader("second")}},
	}, SendOptions{})
	if err != nil || hits.Load() != 2 {
		t.Fatalf("album requests = %d, error = %v", hits.Load(), err)
	}
}

func TestUploadDoesNotReplayOneShotReaderOrLogToken(t *testing.T) {
	const token = "123:EXAMPLE"
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"ok":false,"error_code":503,"description":%q}`, "failed for "+token)
	}))
	defer srv.Close()

	b := NewBotWithBase(token, srv.URL)
	f := File{Name: "file.txt", Reader: struct{ io.Reader }{strings.NewReader("one shot")}}
	err := b.SendDocument(context.Background(), 1, f, "")
	var api *APIError
	if !errors.As(err, &api) || api.Code != 503 || strings.Contains(err.Error(), token) || hits.Load() != 1 {
		t.Fatalf("attempts = %d, error = %v", hits.Load(), err)
	}
}

func TestLongFloodControlDelayDoesNotRetryEarly(t *testing.T) {
	err := &APIError{Code: http.StatusTooManyRequests, RetryAfter: 45}
	if got := retryDelay(err, 0); got != 0 {
		t.Fatalf("delay %v would send ahead of the server's 45s interval", got)
	}
}
