package snap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDecideSendsTypedQuestionsAndReadsAnswers(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"model":"subq-snap-1.1","answers":{
			"needs_tools":{"type":"noul","noul":0.02},
			"kind":{"type":"choice","choice":"chat","probabilities":{"chat":0.97,"code":0.03},"confidence":0.95},
			"work":{"type":"score","score":0.2,"legend":{"0":"a","1":"b"},"probabilities":{"0":0.8,"1":0.2},"confidence":0.7},
			"context":{"type":"choice","choice":"standalone","probabilities":{"standalone":0.9},"confidence":0.88},
			"g_media":{"type":"noul","noul":0.71},"g_code":{"type":"noul","noul":0.05}},
			"usage":{"input_tokens":512,"output_tokens":0}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/v1"}
	qs := TriageQuestions([]Option{{"chat", "Conversation"}, {"code", "Code"}}, []Group{{"media", "music"}, {"code", "code"}})
	res, err := c.Decide(context.Background(), "sk-test-123", "", "state", qs)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer sk-test-123" || got["model"] != DefaultModel || got["state"] != "state" {
		t.Fatalf("request: %q %v", auth, got)
	}
	q := got["questions"].(map[string]any)
	if q["needs_tools"].(map[string]any)["type"] != "noul" || q["work"].(map[string]any)["type"] != "score" ||
		len(q["work"].(map[string]any)["criteria"].([]any)) != 4 || q["kind"].(map[string]any)["type"] != "choice" || q["g_media"] == nil {
		t.Fatalf("questions: %v", q)
	}
	tr := ReadTriage(res)
	if tr.NeedsTools != 0.02 || tr.Kind != "chat" || tr.KindConf != 0.95 || tr.Context != ContextStandalone || tr.Work != 0.2 ||
		tr.InputTokens != 512 || tr.Groups["media"] != 0.71 {
		t.Fatalf("triage: %+v", tr)
	}
	if p := tr.Picked(0.3); len(p) != 1 || p[0] != "media" {
		t.Fatalf("picked: %v", p)
	}
}

func TestMissingAnswersAreTheSafeSide(t *testing.T) {
	tr := ReadTriage(&Result{Answers: map[string]Answer{"kind": {Type: TypeChoice, Choice: "code", Confidence: 0.9}}})
	if tr.NeedsTools != 1 || tr.Context != ContextEarlier || tr.Work != 2 {
		t.Fatalf("defaults: %+v", tr)
	}
}

func TestErrorsNeverCarryTheKey(t *testing.T) {
	const key = "sk-proj-SECRET-KEY-42"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":{"error_type":"authentication_error","message":"bad key ` + key + `"}}`))
	}))
	defer srv.Close()
	_, err := (&Client{BaseURL: srv.URL}).Decide(context.Background(), key, "", "s", map[string]Question{"a": Noul("x", "", "")})
	var e *Error
	if err == nil || !errorsAs(err, &e) || e.Status != 401 || e.Type != "authentication_error" || strings.Contains(err.Error(), key) {
		t.Fatalf("error: %v", err)
	}
	if e.Retryable() {
		t.Fatal("a wrong key is not worth retrying")
	}
	if _, err := (&Client{BaseURL: srv.URL}).Decide(context.Background(), " ", "", "s", nil); err != ErrNoKey {
		t.Fatalf("no key: %v", err)
	}
}

func TestDecideRespectsTheDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := (&Client{BaseURL: srv.URL}).Decide(ctx, "k", "", "s", map[string]Question{"a": Noul("x", "", "")}); err == nil {
		t.Fatal("a slow answer must not wait past the deadline")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("waited %s", time.Since(start))
	}
}

func TestTriageStateIsCompact(t *testing.T) {
	var hist []Turn
	for i := 0; i < 50; i++ {
		hist = append(hist, Turn{Role: "user", Text: strings.Repeat("کلمه ", 200)})
	}
	s := TriageState(hist, "پیام قبلی", []string{"1 photo", "bot.py"}, "این فایل رو درست کن")
	if n := len([]rune(s)); n > stateHistoryRunes+stateReplyRunes+600 {
		t.Fatalf("the state is %d characters", n)
	}
	for _, want := range []string{"Earlier conversation", "replies to: «پیام قبلی»", "Sent with it: 1 photo, bot.py", "Newest message:\nاین فایل رو درست کن"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	if s := TriageState(nil, "", nil, "سلام"); !strings.Contains(s, "no earlier conversation") {
		t.Fatalf("empty history: %s", s)
	}
}

func errorsAs(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}

// TestLiveTriage asks the real API (set WAZE_TEST_SNAP_KEY): the kinds and
// gates of a few messages, in Persian.
func TestLiveTriage(t *testing.T) {
	key := os.Getenv("WAZE_TEST_SNAP_KEY")
	if key == "" {
		t.Skip("WAZE_TEST_SNAP_KEY is not set")
	}
	kinds := []Option{
		{"chat", "Conversation: greetings, thanks, feelings, opinions, advice"},
		{"quick", "A short factual question, a definition, a price or a quick lookup"},
		{"media", "Download, convert or find music, videos, voice or pictures"},
		{"writing", "Write, rewrite, translate or summarize text"},
		{"code", "Write, fix, explain, run or review code"},
		{"project", "A big multi-step job: build an app or site, manage or fix a server, automate a workflow"},
	}
	groups := []Group{{"servers", "the user's own remote servers over SSH"}, {"prices", "currency, gold or crypto prices"}}
	c := &Client{}
	for _, tc := range []struct {
		msg, kind string
		tools     bool
	}{
		{"مرسی داداش خیلی خوب بود", "chat", false},
		{"قیمت دلار امروز چنده؟", "quick", true},
		{"تو sev1 که پروژه پایتون آپلودر وجود داره می‌خوام کلا همه جاشو دکمه کیبورد عادی بزن", "project", true},
		{"یه شعر کوتاه درباره پاییز بگو", "writing", false},
	} {
		res, err := c.Decide(context.Background(), key, "", TriageState(nil, "", nil, tc.msg), TriageQuestions(kinds, groups))
		if err != nil {
			t.Fatal(err)
		}
		tr := ReadTriage(res)
		t.Logf("%s → %s", tc.msg, tr)
		if tr.Kind != tc.kind || (tr.NeedsTools >= 0.5) != tc.tools {
			t.Errorf("%q: %s", tc.msg, tr)
		}
	}
}
