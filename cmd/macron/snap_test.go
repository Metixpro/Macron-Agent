package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/snap"
	"github.com/Metixpro/macron-agent/pkg/store"
)

func snapTriage(kind string, tools, work float64, context string) snap.Triage {
	return snap.Triage{Kind: kind, KindConf: 0.9, NeedsTools: tools, Work: work, Context: context, ContextConf: 0.9}
}

// ---- a fake SubQ ----------------------------------------------------------------

const snapTestKey = "sk-proj-SNAP-SECRET-9876"

type fakeSnap struct {
	mu      sync.Mutex
	calls   int
	states  []string
	auth    []string
	answers map[string]any
	status  int
	delay   time.Duration
}

func (f *fakeSnap) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		State     string         `json:"state"`
		Questions map[string]any `json:"questions"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.calls++
	f.states = append(f.states, body.State)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	answers, status, delay := f.answers, f.status, f.delay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if status != 0 {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"detail":{"error_type":"server_error","message":"down"}}`))
		return
	}
	if _, ok := body.Questions["kind"]; !ok { // the key test asks one question
		answers = map[string]any{"greeting": map[string]any{"type": "noul", "noul": 0.9}}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"model": "subq-snap-1.1", "answers": answers, "usage": map[string]any{"input_tokens": 700}})
}

func (f *fakeSnap) set(answers map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = answers
}

func (f *fakeSnap) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// triage is Snap's answer to the triage questions.
func triage(kind string, tools, work float64, context string, groups map[string]float64) map[string]any {
	a := map[string]any{
		"needs_tools": map[string]any{"type": "noul", "noul": tools},
		"kind":        map[string]any{"type": "choice", "choice": kind, "confidence": 0.93},
		"work":        map[string]any{"type": "score", "score": work, "confidence": 0.8},
		"context":     map[string]any{"type": "choice", "choice": context, "confidence": 0.9},
	}
	for g, p := range groups {
		a["g_"+g] = map[string]any{"type": "noul", "noul": p}
	}
	return a
}

// withSnap points the bot at a fake SubQ, with a key; on turns it on.
func withSnap(t *testing.T, h *harness, on bool) *fakeSnap {
	t.Helper()
	f := &fakeSnap{answers: triage("chat", 0.02, 0.2, "standalone", nil)}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	h.bot.app.Config.SnapURL = srv.URL + "/v1"
	_ = h.st.SetSecretSetting(bg, settingSnapKey, snapTestKey)
	_ = h.st.SetSetting(bg, settingSnapOn, fmt.Sprint(on))
	return f
}

// jobRequests are the model calls of a request (its text in them).
func jobRequests(h *harness, marker string) []map[string]any {
	h.llm.mu.Lock()
	defer h.llm.mu.Unlock()
	var out []map[string]any
	for _, r := range h.llm.requests {
		if strings.Contains(llmText(r), marker) && r["tools"] != nil {
			out = append(out, r)
		}
	}
	return out
}

func snapWeek(t *testing.T, h *harness) store.SnapStats {
	t.Helper()
	h.bot.wg.Wait()
	st, err := h.st.SnapSince(bg, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// ---- the tests ------------------------------------------------------------------

// Off (the default), nothing is asked and every request goes as before.
func TestSnapOffChangesNothing(t *testing.T) {
	h := newHarness(t)
	f := withSnap(t, h, false)
	h.send(private(501, "یه شعر کوتاه درباره پاییز بگو"))
	if f.count() != 0 {
		t.Fatalf("Snap was asked %d times while off", f.count())
	}
	reqs := jobRequests(h, "پاییز")
	if len(reqs) == 0 || !strings.Contains(toolNames(reqs[0]), ",web_search,") {
		t.Fatalf("the request has every tool: %v", reqs)
	}
	if st := snapWeek(t, h); st.Calls != 0 {
		t.Fatalf("logged: %+v", st)
	}
}

// Snap's reply without tools is the admins' choice: by default a request Snap
// reads as a conversation keeps every tool (one read as chat could not look
// anything up). Turned on, it goes with the light prompt and no tools; Snap
// gets the message with the key, and the decision is logged without the text.
func TestSnapSendsAConversationWithoutTools(t *testing.T) {
	h := newHarness(t)
	f := withSnap(t, h, true)
	h.send(private(501, "به نظرت آدم باید همیشه دروغ نگه؟"))
	if reqs := jobRequests(h, "دروغ نگه"); len(reqs) != 1 || !strings.Contains(toolNames(reqs[0]), ",web_search,") {
		t.Fatalf("by default a conversation keeps its tools: %v", reqs)
	}
	_ = h.st.SetSetting(bg, settingSnapChat, "true")
	h.send(private(502, "به نظرت آدم باید همیشه راستشو بگه؟"))
	if f.count() != 2 || !strings.Contains(f.states[1], "به نظرت آدم باید همیشه راستشو بگه؟") || f.auth[1] != "Bearer "+snapTestKey {
		t.Fatalf("Snap's call: %d %q %v", f.count(), f.states, f.auth)
	}
	reqs := jobRequests(h, "راستشو بگه")
	if len(reqs) != 1 || toolNames(reqs[0]) != ",use_tools," {
		t.Fatalf("a conversation goes without tools: %v", toolNames(reqs[0]))
	}
	st := snapWeek(t, h)
	if st.Calls != 2 || st.Failures != 0 || st.Applied[snapDidChat] != 1 || st.Kinds["chat"] != 2 || st.SnapTokens != 1400 || st.SavedTokens <= 0 {
		t.Fatalf("the log: %+v", st)
	}
	if strings.Contains(h.tg.allText(), snapTestKey) {
		t.Fatal("the key showed in a message")
	}
}

// Snap's reading routes: a job it calls a project goes to the strong model
// although its words say little; a conversation stays with the light one.
func TestSnapRoutesByWhatItUnderstood(t *testing.T) {
	h := newHarness(t)
	main, _ := h.st.Providers(bg)
	strong, err := h.st.AddProvider(bg, store.Provider{Name: "strong", BaseURL: main[0].BaseURL, APIKey: "sk-strong-1", Model: "big", Priority: 20, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.bot.prov.Reload(bg)
	_ = h.st.SetSetting(bg, settingRouterStrong+string(tierOwner), fmt.Sprint(strong))
	f := withSnap(t, h, true)

	f.set(triage("project", 0.97, 2.8, "standalone", map[string]float64{"servers": 0.9}))
	h.send(private(503, "همون کاری که گفتم رو انجام بده")) // its words show no job
	if h.lastModel() != "big" {
		t.Fatalf("a project goes to the strong model: %q", h.lastModel())
	}
	f.set(triage("chat", 0.02, 0.1, "standalone", nil))
	h.send(private(504, "مرسی رفیق"))
	if h.lastModel() != "m" {
		t.Fatalf("a thank-you stays light: %q", h.lastModel())
	}
	// Snap never moves a request down: code the words send to the strong
	// model stays there, whatever Snap calls it.
	f.set(triage("chat", 0.02, 0.1, "standalone", nil))
	h.send(private(505, "CODE-JOB یه تابع پایتون بنویس که یه فایل csv رو بخونه و جمع ستون دوم رو برگردونه"))
	if h.lastModel() != "big" {
		t.Fatalf("Snap took a code request down to %q", h.lastModel())
	}
	if r := jobRequests(h, "CODE-JOB"); len(r) == 0 || toolNames(r[0]) == ",use_tools," {
		t.Fatal("a request whose words show a job keeps its tools, whatever Snap says")
	}
	if st := snapWeek(t, h); st.Applied[snapRouteStrong] != 1 || st.Applied[snapDidChat] != 0 {
		t.Fatalf("the routes: %+v", st.Applied)
	}
}

// Only a conversation thinks lightly: any job keeps its provider's effort,
// however small Snap thinks it is.
func TestSnapCapsOnlyAConversationsEffort(t *testing.T) {
	h := newHarness(t)
	ps, _ := h.st.Providers(bg)
	p := ps[0]
	p.Effort = llm.EffortHigh
	_ = h.st.UpdateProvider(bg, p)
	_ = h.bot.prov.Reload(bg)
	f := withSnap(t, h, true)
	_ = h.st.SetSetting(bg, settingSnapChat, "true") // the reply without tools is the admins' choice

	f.set(triage("quick", 0.9, 0.3, "standalone", map[string]float64{"prices": 0.95}))
	h.send(private(505, "قیمت دلار امروز چنده؟"))
	if reqs := jobRequests(h, "قیمت دلار"); len(reqs) == 0 || reqs[0]["reasoning_effort"] != "high" {
		t.Fatalf("a quick job keeps its effort: %v", reqs)
	}
	f.set(triage("chat", 0.02, 0.2, "standalone", nil))
	h.send(private(506, "به نظرت پاییز قشنگ‌تره یا بهار؟"))
	reqs := jobRequests(h, "پاییز قشنگ")
	if len(reqs) == 0 || reqs[0]["reasoning_effort"] != "low" || toolNames(reqs[0]) != ",use_tools," {
		t.Fatalf("a conversation goes light: %v", reqs)
	}
	if st := snapWeek(t, h); st.Applied[snapDidEffort+"low"] != 1 || st.Applied[snapDidChat] != 1 {
		t.Fatalf("logged: %+v", st.Applied)
	}
}

// In the middle of a job (the last turns used tools), the next message gets
// every tool and the whole history, whatever Snap thinks of its words: «make
// it better» is more of the job, not a conversation.
func TestSnapLeavesAJobAlone(t *testing.T) {
	h := newHarness(t)
	f := withSnap(t, h, true)
	_ = h.st.SetSetting(bg, settingSnapTrim, snapTrimAlways)
	const uid = 512
	_ = h.bot.app.Machine.WriteFile(sessionFor(uid), "sticker.json", []byte(`{"v":"5.5.2"}`))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if lastRole(req) == "user" && strings.Contains(llmText(req), "JOB-START") && !strings.Contains(llmText(req), "JOB-NEXT") {
			return toolCall("read_file", `{"path":"sticker.json"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "ساختمش ✅"}, 0
	}
	f.set(triage("media", 0.95, 2.0, "standalone", map[string]float64{"media": 0.9}))
	h.send(private(uid, "JOB-START یه استیکر انیمیشنی بساز"))
	f.set(triage("chat", 0.02, 0.2, "standalone", nil))
	h.send(private(uid, "JOB-NEXT بهترش کن"))
	reqs := jobRequests(h, "JOB-NEXT")
	if len(reqs) == 0 {
		t.Fatal("no request")
	}
	names := toolNames(reqs[0])
	if names == ",use_tools," || strings.Contains(names, ","+toolRecallHistory+",") || !strings.Contains(llmText(reqs[0]), "JOB-START") {
		t.Fatalf("a job in progress keeps its tools and history: %s", names)
	}
	if st := snapWeek(t, h); st.Applied[snapDidChat] != 0 || st.Applied[snapDidTrim] != 0 {
		t.Fatalf("logged: %+v", st.Applied)
	}
}

// Where the provider caches nothing, a request that stands on its own goes
// with the last turns and the tools it needs; recall_history brings the
// rest back. Until the cache is measured, nothing is trimmed.
func TestSnapTrimsWhereNothingIsCached(t *testing.T) {
	h := newHarness(t)
	f := withSnap(t, h, true)
	_ = h.st.SetSetting(bg, settingSnapTrim, snapTrimAuto) // off by default
	f.set(triage("research", 0.9, 1.5, "earlier", map[string]float64{"research": 0.9}))
	long := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 120)
	h.llm.reply = say(long)
	const uid = 507
	h.send(private(uid, "MARKER-OLD درباره زرافه‌ها تحقیق کن"))
	for i := 0; i < snapCacheSamples; i++ {
		h.send(private(uid, fmt.Sprintf("ادامه تحقیق شماره %d", i)))
	}
	ps, _ := h.st.Providers(bg)
	if r, n, known := h.bot.providerCaches(ps[0].ID); !known || r != 0 {
		t.Fatalf("the cache is measured: %.2f of %d", r, n)
	}
	for _, r := range jobRequests(h, "ادامه تحقیق شماره") {
		if strings.Contains(toolNames(r), ","+toolRecallHistory+",") {
			t.Fatal("trimmed before the cache was known")
		}
	}

	f.set(triage("writing", 0.6, 1.0, "standalone", map[string]float64{"documents": 0.8}))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if strings.Contains(toolNames(req), ","+toolRecallHistory+",") && lastRole(req) == "user" {
			return toolCall(toolRecallHistory, `{"query":"MARKER-OLD"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "تموم شد"}, 0
	}
	h.send(private(uid, "NEW-ONE یه متن کوتاه برای معرفی شرکت بنویس"))
	reqs := jobRequests(h, "NEW-ONE")
	if len(reqs) < 2 {
		t.Fatalf("the job's calls: %d", len(reqs))
	}
	first := reqs[0]
	names := toolNames(first)
	if strings.Contains(llmText(first), "MARKER-OLD") || !strings.Contains(names, ","+toolRecallHistory+",") ||
		strings.Contains(names, ",more_tools,") || !strings.Contains(names, ",download_media_ytdlp,") {
		t.Fatalf("the history is trimmed, never the tools: %s", names)
	}
	if !strings.Contains(llmText(reqs[1]), "MARKER-OLD") {
		t.Fatal("recall_history brings the older turns back")
	}
	if !strings.Contains(llmText(first), "[history]") || !strings.Contains(llmText(first), toolRecallHistory) {
		t.Fatal("the model is told what was left out")
	}
	if st := snapWeek(t, h); st.Applied[snapDidTrim] != 1 || st.Applied[snapDidFocus] != 0 {
		t.Fatalf("logged: %+v", st.Applied)
	}

	// Off, the history and tools go in full again.
	_ = h.st.SetSetting(bg, settingSnapTrim, snapTrimOff)
	h.llm.reply = say("باشه")
	h.send(private(uid, "THIRD یه متن دیگه بنویس"))
	if r := jobRequests(h, "THIRD"); len(r) == 0 || !strings.Contains(llmText(r[0]), "MARKER-OLD") {
		t.Fatal("trimming off keeps the history")
	}
}

// Where the provider caches its prompts, auto trims nothing: the unchanged
// prompt is the cheaper one.
func TestSnapLeavesACachingProviderAlone(t *testing.T) {
	h := newHarness(t)
	f := withSnap(t, h, true)
	_ = h.st.SetSetting(bg, settingSnapTrim, snapTrimAuto)
	h.llm.mu.Lock()
	h.llm.cached = 90
	h.llm.mu.Unlock()
	f.set(triage("research", 0.9, 1.5, "earlier", map[string]float64{"research": 0.9}))
	const uid = 511
	for i := 0; i <= snapCacheSamples; i++ {
		h.send(private(uid, fmt.Sprintf("CACHED-%d درباره زرافه‌ها تحقیق کن", i)))
	}
	ps, _ := h.st.Providers(bg)
	if r, _, known := h.bot.providerCaches(ps[0].ID); !known || r < 0.85 {
		t.Fatalf("the cache is measured: %.2f", r)
	}
	f.set(triage("writing", 0.6, 1.0, "standalone", map[string]float64{"documents": 0.8}))
	h.send(private(uid, "CACHED-NEW یه متن کوتاه بنویس"))
	r := jobRequests(h, "CACHED-NEW")
	if len(r) == 0 || !strings.Contains(llmText(r[0]), "CACHED-0") || strings.Contains(toolNames(r[0]), ","+toolRecallHistory+",") ||
		!strings.Contains(toolNames(r[0]), ",download_media_ytdlp,") {
		t.Fatalf("a caching provider gets the whole history and every tool: %d %v %s", len(r), len(r) > 0 && strings.Contains(llmText(r[0]), "CACHED-0"), toolNames(r[0]))
	}
}

// Snap failing or late leaves each request as it would be without it; after
// a few failures in a row it rests, and its errors never carry the key.
func TestSnapFailingChangesNothing(t *testing.T) {
	h := newHarness(t)
	f := withSnap(t, h, true)
	f.mu.Lock()
	f.status = http.StatusInternalServerError
	f.mu.Unlock()
	for i := 0; i < snapBreakAfter; i++ {
		h.send(private(508, fmt.Sprintf("FAIL-%d یه شعر بگو", i)))
		if r := jobRequests(h, fmt.Sprintf("FAIL-%d", i)); len(r) == 0 || !strings.Contains(toolNames(r[0]), ",web_search,") {
			t.Fatalf("request %d goes as without Snap", i)
		}
	}
	h.send(private(508, "REST یه شعر بگو"))
	if f.count() != snapBreakAfter {
		t.Fatalf("resting: asked %d times", f.count())
	}
	st := snapWeek(t, h)
	if st.Calls != snapBreakAfter || st.Failures != snapBreakAfter {
		t.Fatalf("failures logged: %+v", st)
	}

	// Late: the request waits no longer than the timeout.
	h.bot.snapReset()
	_ = h.st.SetSetting(bg, settingSnapTimeout, "300")
	f.mu.Lock()
	f.status, f.delay = 0, 3*time.Second
	f.mu.Unlock()
	start := time.Now()
	h.send(private(509, "LATE یه شعر بگو"))
	if took := time.Since(start); took > 2500*time.Millisecond {
		t.Fatalf("waited %s for a late Snap", took)
	}
	if r := jobRequests(h, "LATE"); len(r) == 0 || !strings.Contains(toolNames(r[0]), ",web_search,") {
		t.Fatal("a late decision changes nothing")
	}
	if strings.Contains(h.tg.allText(), snapTestKey) {
		t.Fatal("the key showed in a message")
	}
}

// The panel: a door from the providers, the key checked and saved sealed
// (its message deleted), the switch, the decisions, and a test.
func TestSnapPanel(t *testing.T) {
	h := newHarness(t)
	f := withSnap(t, h, false)
	_ = h.st.SetSecretSetting(bg, settingSnapKey, "")
	h.send(private(ownerID, "/admin"))
	h.click(ownerID, ownerID, 9, "a:pv")
	if !hasButton(buttons(lastOf(h, "editMessageText")), "→ a:sn") {
		t.Fatal("no door to Snap in the providers")
	}
	h.click(ownerID, ownerID, 9, "a:sn")
	if v := fmt.Sprint(lastOf(h, "editMessageText").body["text"]); !strings.Contains(v, "تنظیم نشده") || !strings.Contains(v, "حریم خصوصی") {
		t.Fatalf("the panel: %s", v)
	}
	h.click(ownerID, ownerID, 9, "a:sn:on")
	if a := lastOf(h, "answerCallbackQuery"); a.body["show_alert"] != true || h.st.SettingBool(settingSnapOn, false) {
		t.Fatal("no switching on without a key")
	}

	h.click(ownerID, ownerID, 9, "a:sn:key")
	key := private(ownerID, snapTestKey)
	key.MessageID = 555
	h.send(key)
	deleted := false
	for _, c := range h.tg.of("deleteMessage") {
		deleted = deleted || fmt.Sprint(c.body["message_id"]) == "555"
	}
	raw, _ := h.st.Setting(settingSnapKey)
	if !deleted || strings.Contains(raw, snapTestKey) || h.bot.snapKey() != snapTestKey || f.count() != 1 {
		t.Fatalf("the key: deleted %v, sealed %v, read %v, checked %d", deleted, !strings.Contains(raw, snapTestKey), h.bot.snapKey() == snapTestKey, f.count())
	}
	if strings.Contains(h.tg.allText(), snapTestKey) {
		t.Fatal("the key showed in a message")
	}

	h.click(ownerID, ownerID, 9, "a:sn:on")
	h.click(ownerID, ownerID, 9, "a:sn:d:route")
	h.click(ownerID, ownerID, 9, "a:sn:trim:always")
	h.click(ownerID, ownerID, 9, "a:sn:to:800")
	h.click(ownerID, ownerID, 9, "a:sn:model:2")
	h.click(ownerID, ownerID, 9, "a:sn:to:999") // not offered
	if !h.st.SettingBool(settingSnapOn, false) || h.st.SettingBool(settingSnapRoute, true) || h.bot.snapTrimMode() != snapTrimAlways ||
		h.bot.snapTimeout() != 800*time.Millisecond || h.bot.snapModel() != "subq-snap-preview" {
		t.Fatal("the settings")
	}

	f.set(triage("code", 0.97, 2.0, "standalone", map[string]float64{"code": 0.9}))
	h.click(ownerID, ownerID, 9, "a:sn:test")
	h.tg.reset()
	h.send(private(ownerID, "یه اسکریپت پایتون برای بکاپ دیتابیس بنویس"))
	v := fmt.Sprint(lastOf(h, "sendMessage").body["text"])
	if !strings.Contains(v, "کد") || !strings.Contains(v, "۹۷٪") || !strings.Contains(v, "code") || !strings.Contains(v, "فقط ابزارهای لازم") {
		t.Fatalf("the test: %s", v)
	}

	h.click(ownerID, ownerID, 9, "a:sn:del")
	if h.bot.snapKey() != "" || h.st.SettingBool(settingSnapOn, false) {
		t.Fatal("removing the key turns Snap off")
	}
}

// What Snap reads keeps no secret: keys, passwords, private keys, the bot's own.
func TestSnapStateHidesSecrets(t *testing.T) {
	h := newHarness(t)
	f := withSnap(t, h, true)
	secrets := []string{"sk-proj-abcdefghijklmnopqrstuvwx", "AKIAABCDEFGHIJKLMNOP", "xoxb-1234567890-abcdefghij", "hunter2-Pass!", "رمزخیلی‌مخفی",
		"MIIEowIBAAKCAQEA", "sk-provider-secret-1234", "s3cr3tpw"}
	msg := "این کلید منه sk-proj-abcdefghijklmnopqrstuvwx و کلید آمازون AKIAABCDEFGHIJKLMNOP و اسلک xoxb-1234567890-abcdefghij\n" +
		"password: hunter2-Pass! رمز: رمزخیلی‌مخفی\nssh://root:s3cr3tpw@1.2.3.4\n" +
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----\n" +
		"کلید پرووایدر sk-provider-secret-1234 رو روی سرور بذار"
	h.send(private(510, msg))
	if f.count() != 1 {
		t.Fatalf("Snap was asked %d times: %s", f.count(), h.tg.allText())
	}
	for _, s := range secrets {
		if strings.Contains(f.states[0], s) {
			t.Errorf("Snap saw %q:\n%s", s, f.states[0])
		}
	}
	if !strings.Contains(f.states[0], "روی سرور بذار") {
		t.Errorf("the words stay: %s", f.states[0])
	}
}

// The decisions themselves.
func TestSnapDecisions(t *testing.T) {
	chat := func(tools, work float64, ctx string) bool {
		return snapChat(snapTriage("chat", tools, work, ctx), "مرسی")
	}
	if !chat(0.05, 0.3, "standalone") || chat(0.5, 0.3, "standalone") || chat(0.05, 0.3, "earlier") || chat(0.05, 0.3, "recent") ||
		chat(0.05, 2, "standalone") {
		t.Error("chat")
	}
	if snapChat(snapTriage("chat", 0.05, 0.3, "standalone"), "یه تابع پایتون بنویس") {
		t.Error("words that show a job keep the tools")
	}
	for _, k := range []string{"code", "writing", "quick", "media"} {
		if snapChat(snapTriage(k, 0.05, 0.5, "standalone"), "x") {
			t.Errorf("%s keeps its tools", k)
		}
	}
	if snapChat(snapTriage("chat", 0.05, 0.3, "standalone"), strings.Repeat("کلمه ", 80)) {
		t.Error("a long message is more than a conversation")
	}
	if snapEffort(true) != llm.EffortLow || snapEffort(false) != "" {
		t.Error("effort")
	}
	if !jobInProgress([]llm.Message{{Role: "user", Content: "x"}, {Role: "assistant", Content: "done\n\n<work_notes>\nread_file(a) → ok\n</work_notes>"}}) ||
		jobInProgress([]llm.Message{{Role: "user", Content: "سلام"}, {Role: "assistant", Content: "سلام!"}}) {
		t.Error("jobInProgress")
	}
	if !effortLowered(llm.EffortHigh, llm.EffortLow) || effortLowered("", llm.EffortLow) || effortLowered(llm.EffortLow, llm.EffortMedium) {
		t.Error("effortLowered")
	}
	if v := snapVerdict(snapTriage("writing", 0.5, 2.0, "standalone")); v == nil || !v.Hard {
		t.Error("a long piece of writing is hard")
	}
	if v := snapVerdict(snapTriage("nonsense", 0.5, 2.0, "standalone")); v != nil {
		t.Error("an unknown kind does not route")
	}

	// splitHistory keeps whole turns from a user message on.
	var h []llm.Message
	for i := 0; i < 10; i++ {
		h = append(h, llm.Message{Role: "user", Content: strings.Repeat("u ", 200)}, llm.Message{Role: "assistant", Content: strings.Repeat("a ", 200)})
	}
	kept, dropped := splitHistory(h, 500)
	if len(kept) == 0 || kept[0].Role != "user" || len(kept)+len(dropped) != len(h) || len(dropped) == 0 {
		t.Fatalf("split: %d kept, %d dropped", len(kept), len(dropped))
	}
	out, _ := recallTool(dropped).Handler(context.Background(), map[string]any{"query": "nothing-like-it"})
	if !strings.Contains(out.Text, "Nothing") {
		t.Fatalf("recall: %s", out.Text)
	}
}

// TestLiveSnapTriage asks the real API (set WAZE_TEST_SNAP_KEY) the bot's
// own questions about a few messages, and checks what the bot would do.
func TestLiveSnapTriage(t *testing.T) {
	key := os.Getenv("WAZE_TEST_SNAP_KEY")
	if key == "" {
		t.Skip("WAZE_TEST_SNAP_KEY is not set")
	}
	b := &Bot{}
	c := &snap.Client{}
	for _, tc := range []struct {
		msg   string
		hist  []snap.Turn
		kind  string // "a|b": either
		chat  bool
		any   bool // either way is right (a thanks is small talk without Snap too)
		group string
	}{
		{msg: "مرسی داداش خیلی خوب بود", kind: "chat", any: true},
		{msg: "به نظرت آدم باید همیشه راستشو بگه؟", kind: "chat", chat: true},
		{msg: "یه شعر کوتاه درباره پاییز بگو", kind: "writing"}, // writing keeps its tools
		{msg: "قیمت دلار امروز چنده؟", kind: "quick", group: "prices"},
		{msg: "این آهنگو برام دانلود کن https://youtu.be/dQw4w9WgXcQ", kind: "media", group: "media"},
		{msg: "تو sev1 که پروژه پایتون آپلودر وجود داره می‌خوام کلا همه جاشو دکمه کیبورد عادی بزن", kind: "project|code", group: "servers"},
		{msg: "یه تابع پایتون بنویس که فایل csv بخونه و میانگین ستون دوم رو بده", kind: "code", group: "code"},
		{msg: "آره همینو ادامه بده", hist: []snap.Turn{{Role: "user", Text: "یه ربات تلگرام با پایتون برام بساز"},
			{Role: "assistant", Text: "فایل bot.py رو ساختم و دستور start رو اضافه کردم. می‌خوای دکمه‌های منو رو هم اضافه کنم؟"}}},
	} {
		res, err := c.Decide(context.Background(), key, "", snap.TriageState(tc.hist, "", nil, tc.msg), b.snapQuestions())
		if err != nil {
			t.Fatal(err)
		}
		tri := snap.ReadTriage(res)
		t.Logf("%s\n    → %s · chat %v · keep %d · groups %v · %d tokens", tc.msg, tri, snapChat(tri, tc.msg), snapKeep(tri),
			tri.Picked(snapGroupAt), tri.InputTokens)
		if tc.kind != "" && !slices.Contains(strings.Split(tc.kind, "|"), tri.Kind) {
			t.Errorf("%q: kind %s", tc.msg, tri.Kind)
		}
		if !tc.any && snapChat(tri, tc.msg) != tc.chat {
			t.Errorf("%q: chat %v", tc.msg, snapChat(tri, tc.msg))
		}
		if tc.group != "" && tri.Groups[tc.group] < snapGroupAt {
			t.Errorf("%q: group %s at %.2f", tc.msg, tc.group, tri.Groups[tc.group])
		}
		if tc.hist != nil && tri.Context == snap.ContextStandalone {
			t.Errorf("%q leans on the conversation: %s", tc.msg, tri.Context)
		}
	}
}
