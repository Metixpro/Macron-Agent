package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/router"
	"github.com/Metixpro/macron-agent/pkg/store"
)

func testTriage(kind string, tools, work float64, context string) router.Triage {
	return router.Triage{Kind: router.Kind(kind), KindConf: 0.9, NeedsTools: tools, Work: work, Context: context, ContextConf: 0.9}
}

type fakeTriage struct {
	mu     sync.Mutex
	answer string
	status int
	calls  int
}

func decision(kind string, tools, work float64, context string, groups map[string]float64) string {
	b, _ := json.Marshal(map[string]any{"needs_tools": tools, "kind": kind, "kind_confidence": 0.93, "work": work,
		"context": context, "context_confidence": 0.9, "groups": groups})
	return string(b)
}

func (f *fakeTriage) set(answer string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer = answer
}

func (f *fakeTriage) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func withTriage(t *testing.T, h *harness, mode string) *fakeTriage {
	t.Helper()
	f := &fakeTriage{answer: decision("chat", 0.02, 0.2, "standalone", nil)}
	h.llm.mu.Lock()
	h.llm.triage = func(map[string]any) (map[string]any, int) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		if f.status != 0 {
			return nil, f.status
		}
		return map[string]any{"role": "assistant", "content": f.answer}, 0
	}
	h.llm.mu.Unlock()
	_ = h.st.SetSetting(bg, settingTriage, mode)
	return f
}

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

func TestTriageOffChangesNothing(t *testing.T) {
	h := newHarness(t)
	f := withTriage(t, h, triageOff)
	_ = h.st.SetSetting(bg, settingTriage, "")
	if h.bot.triageMode() != triageOff {
		t.Fatalf("default mode %q", h.bot.triageMode())
	}
	h.send(private(501, "یه شعر کوتاه درباره پاییز بگو"))
	if f.count() != 0 {
		t.Fatalf("the model was asked to triage %d times while off", f.count())
	}
	reqs := jobRequests(h, "پاییز")
	if len(reqs) == 0 || !strings.Contains(toolNames(reqs[0]), ",web_search,") {
		t.Fatalf("the request has every tool: %v", reqs)
	}
}

func TestTriageSendsAConversationWithoutTools(t *testing.T) {
	h := newHarness(t)
	f := withTriage(t, h, triageModel)
	h.send(private(501, "به نظرت آدم باید همیشه دروغ نگه؟"))
	if reqs := jobRequests(h, "دروغ نگه"); len(reqs) != 1 || !strings.Contains(toolNames(reqs[0]), ",web_search,") {
		t.Fatalf("by default a conversation keeps its tools: %v", reqs)
	}
	_ = h.st.SetSetting(bg, settingTriageChat, "true")
	h.send(private(502, "به نظرت آدم باید همیشه راستشو بگه؟"))
	h.llm.mu.Lock()
	prompts := append([]string(nil), h.llm.triageLog...)
	h.llm.mu.Unlock()
	if f.count() != 2 || !strings.Contains(prompts[1], "به نظرت آدم باید همیشه راستشو بگه؟") {
		t.Fatalf("the triage call: %d %q", f.count(), prompts)
	}
	reqs := jobRequests(h, "راستشو بگه")
	if len(reqs) != 1 || toolNames(reqs[0]) != ",use_tools," {
		t.Fatalf("a conversation goes without tools: %v", toolNames(reqs[0]))
	}
}

func TestTriageRoutesByWhatItUnderstood(t *testing.T) {
	h := newHarness(t)
	main, _ := h.st.Providers(bg)
	strong, err := h.st.AddProvider(bg, store.Provider{Name: "strong", BaseURL: main[0].BaseURL, APIKey: "sk-strong-1", Model: "big", Priority: 20, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.bot.prov.Reload(bg)
	_ = h.st.SetSetting(bg, settingRouterStrong+string(tierOwner), fmt.Sprint(strong))
	f := withTriage(t, h, triageModel)

	f.set(decision("project", 0.97, 2.8, "standalone", map[string]float64{"servers": 0.9}))
	h.send(private(503, "همون کاری که گفتم رو انجام بده"))
	if h.lastModel() != "big" {
		t.Fatalf("a project goes to the strong model: %q", h.lastModel())
	}
	f.set(decision("chat", 0.02, 0.1, "standalone", nil))
	h.send(private(504, "مرسی رفیق"))
	if h.lastModel() != "m" {
		t.Fatalf("a thank-you stays light: %q", h.lastModel())
	}

	f.set(decision("chat", 0.02, 0.1, "standalone", nil))
	h.send(private(505, "CODE-JOB یه تابع پایتون بنویس که یه فایل csv رو بخونه و جمع ستون دوم رو برگردونه"))
	if h.lastModel() != "big" {
		t.Fatalf("the triage took a code request down to %q", h.lastModel())
	}
	if r := jobRequests(h, "CODE-JOB"); len(r) == 0 || toolNames(r[0]) == ",use_tools," {
		t.Fatal("a request whose words show a job keeps its tools, whatever the triage says")
	}
}

func TestTriageCapsOnlyAConversationsEffort(t *testing.T) {
	h := newHarness(t)
	ps, _ := h.st.Providers(bg)
	p := ps[0]
	p.Effort = llm.EffortHigh
	_ = h.st.UpdateProvider(bg, p)
	_ = h.bot.prov.Reload(bg)
	f := withTriage(t, h, triageModel)
	_ = h.st.SetSetting(bg, settingTriageChat, "true")

	f.set(decision("quick", 0.9, 0.3, "standalone", map[string]float64{"prices": 0.95}))
	h.send(private(505, "قیمت دلار امروز چنده؟"))
	if reqs := jobRequests(h, "قیمت دلار"); len(reqs) == 0 || reqs[0]["reasoning_effort"] != "high" {
		t.Fatalf("a quick job keeps its effort: %v", reqs)
	}
	f.set(decision("chat", 0.02, 0.2, "standalone", nil))
	h.send(private(506, "به نظرت پاییز قشنگ‌تره یا بهار؟"))
	reqs := jobRequests(h, "پاییز قشنگ")
	if len(reqs) == 0 || reqs[0]["reasoning_effort"] != "low" || toolNames(reqs[0]) != ",use_tools," {
		t.Fatalf("a conversation goes light: %v", reqs)
	}
}

func TestTriageLeavesAJobAlone(t *testing.T) {
	h := newHarness(t)
	f := withTriage(t, h, triageModel)
	_ = h.st.SetSetting(bg, settingTriageTrim, triageTrimAlways)
	_ = h.st.SetSetting(bg, settingTriageChat, "true")
	const uid = 512
	_ = h.bot.app.Machine.WriteFile(sessionFor(uid), "sticker.json", []byte(`{"v":"5.5.2"}`))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if lastRole(req) == "user" && strings.Contains(llmText(req), "JOB-START") && !strings.Contains(llmText(req), "JOB-NEXT") {
			return toolCall("read_file", `{"path":"sticker.json"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "ساختمش ✅"}, 0
	}
	f.set(decision("media", 0.95, 2.0, "standalone", map[string]float64{"media": 0.9}))
	h.send(private(uid, "JOB-START یه استیکر انیمیشنی بساز"))
	f.set(decision("chat", 0.02, 0.2, "standalone", nil))
	h.send(private(uid, "JOB-NEXT بهترش کن"))
	reqs := jobRequests(h, "JOB-NEXT")
	if len(reqs) == 0 {
		t.Fatal("no request")
	}
	names := toolNames(reqs[0])
	if names == ",use_tools," || strings.Contains(names, ","+toolRecallHistory+",") || !strings.Contains(llmText(reqs[0]), "JOB-START") {
		t.Fatalf("a job in progress keeps its tools and history: %s", names)
	}
}

func TestTriageTrimsWhereNothingIsCached(t *testing.T) {
	h := newHarness(t)
	f := withTriage(t, h, triageModel)
	_ = h.st.SetSetting(bg, settingTriageTrim, triageTrimAuto)
	f.set(decision("research", 0.9, 1.5, "earlier", map[string]float64{"research": 0.9}))
	long := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 120)
	h.llm.reply = say(long)
	const uid = 507
	h.send(private(uid, "MARKER-OLD درباره زرافه‌ها تحقیق کن"))
	for i := 0; i < triageCacheSamples; i++ {
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

	f.set(decision("writing", 0.6, 1.0, "standalone", map[string]float64{"documents": 0.8}))
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

	_ = h.st.SetSetting(bg, settingTriageTrim, triageTrimOff)
	h.llm.reply = say("باشه")
	h.send(private(uid, "THIRD یه متن دیگه بنویس"))
	if r := jobRequests(h, "THIRD"); len(r) == 0 || !strings.Contains(llmText(r[0]), "MARKER-OLD") {
		t.Fatal("trimming off keeps the history")
	}
}

func TestTriageLeavesACachingProviderAlone(t *testing.T) {
	h := newHarness(t)
	f := withTriage(t, h, triageModel)
	_ = h.st.SetSetting(bg, settingTriageTrim, triageTrimAuto)
	h.llm.mu.Lock()
	h.llm.cached = 90
	h.llm.mu.Unlock()
	f.set(decision("research", 0.9, 1.5, "earlier", map[string]float64{"research": 0.9}))
	const uid = 511
	for i := 0; i <= triageCacheSamples; i++ {
		h.send(private(uid, fmt.Sprintf("CACHED-%d درباره زرافه‌ها تحقیق کن", i)))
	}
	ps, _ := h.st.Providers(bg)
	if r, _, known := h.bot.providerCaches(ps[0].ID); !known || r < 0.85 {
		t.Fatalf("the cache is measured: %.2f", r)
	}
	f.set(decision("writing", 0.6, 1.0, "standalone", map[string]float64{"documents": 0.8}))
	h.send(private(uid, "CACHED-NEW یه متن کوتاه بنویس"))
	r := jobRequests(h, "CACHED-NEW")
	if len(r) == 0 || !strings.Contains(llmText(r[0]), "CACHED-0") || strings.Contains(toolNames(r[0]), ","+toolRecallHistory+",") ||
		!strings.Contains(toolNames(r[0]), ",download_media_ytdlp,") {
		t.Fatalf("a caching provider gets the whole history and every tool")
	}
}

func TestTriageModelFailingFallsBackToLocal(t *testing.T) {
	h := newHarness(t)
	f := withTriage(t, h, triageModel)
	f.mu.Lock()
	f.status = http.StatusInternalServerError
	f.mu.Unlock()
	for i := 0; i < triageBreakAfter; i++ {
		h.send(private(508, fmt.Sprintf("FAIL-%d یه شعر بگو", i)))
		if r := jobRequests(h, fmt.Sprintf("FAIL-%d", i)); len(r) == 0 || !strings.Contains(toolNames(r[0]), ",web_search,") {
			t.Fatalf("request %d goes with its tools", i)
		}
	}
	asked := f.count()
	h.send(private(508, "REST یه شعر بگو"))
	if f.count() != asked {
		t.Fatalf("resting: asked %d more times", f.count()-asked)
	}
	if r := jobRequests(h, "REST"); len(r) == 0 || !strings.Contains(toolNames(r[0]), ",web_search,") {
		t.Fatal("a resting triage changes nothing")
	}
}

func TestLocalTriageRoutesTheThreeKinds(t *testing.T) {
	h := newHarness(t)
	f := withTriage(t, h, triageLocal)
	_ = h.st.SetSetting(bg, settingTriageTrim, triageTrimAlways)
	const uid = 520
	h.send(private(uid, "سلام"))
	if r := jobRequests(h, "سلام"); len(r) == 0 || toolNames(r[0]) != ",use_tools," {
		t.Fatalf("a greeting goes without tools")
	}
	h.send(private(uid, "MULTI این ویدیو رو از یوتیوب دانلود کن، صداش رو mp3 کن و برام بفرست https://youtu.be/x"))
	if r := jobRequests(h, "MULTI"); len(r) == 0 || !strings.Contains(toolNames(r[0]), ",download_media_ytdlp,") ||
		!strings.Contains(toolNames(r[0]), ",media_convert,") {
		t.Fatalf("a multi-tool job keeps its tools: %s", toolNames(jobRequests(h, "MULTI")[0]))
	}
	h.send(private(uid, "LEANS اون پروژه‌ای که دیروز گفتم رو ادامه بده"))
	if r := jobRequests(h, "LEANS"); len(r) == 0 || !strings.Contains(llmText(r[0]), "MULTI") ||
		strings.Contains(toolNames(r[0]), ","+toolRecallHistory+",") {
		t.Fatal("a request that leans on the past keeps the whole history")
	}
	if f.count() != 0 {
		t.Fatalf("the local triage asked the model %d times", f.count())
	}
}

func TestTriagePanel(t *testing.T) {
	h := newHarness(t)
	f := withTriage(t, h, triageOff)
	h.send(private(ownerID, "/settings"))
	h.click(ownerID, ownerID, 9, "a:rt")
	if !hasButton(buttons(lastOf(h, "editMessageText")), "→ a:tri") {
		t.Fatal("no door to the triage from smart routing")
	}
	h.click(ownerID, ownerID, 9, "a:tri")
	if v := fmt.Sprint(lastOf(h, "editMessageText").body["text"]); !strings.Contains(v, "بدون درخواست به مدل") {
		t.Fatalf("the panel: %s", v)
	}
	h.click(ownerID, ownerID, 9, "a:tri:mode:model")
	h.click(ownerID, ownerID, 9, "a:tri:chat")
	h.click(ownerID, ownerID, 9, "a:tri:trim:always")
	h.click(ownerID, ownerID, 9, "a:tri:mode:nonsense")
	if h.bot.triageMode() != triageModel || !h.bot.triageChatOn() || h.bot.triageTrimMode() != triageTrimAlways {
		t.Fatal("the settings")
	}

	f.set(decision("code", 0.97, 2.0, "standalone", map[string]float64{"code": 0.9}))
	h.click(ownerID, ownerID, 9, "a:tri:test")
	h.tg.reset()
	h.send(private(ownerID, "یه اسکریپت پایتون برای بکاپ دیتابیس بنویس"))
	v := fmt.Sprint(lastTextDelivery(h).body["text"])
	if !strings.Contains(v, "کد") || !strings.Contains(v, "۹۳٪") || !strings.Contains(v, "code") || !strings.Contains(v, "فقط ابزارهای لازم") {
		t.Fatalf("the test: %s", v)
	}
	h.click(ownerID, ownerID, 9, "a:tri:mode:off")
	if h.bot.triageMode() != triageOff {
		t.Fatal("off")
	}
}

func TestTriageDecisions(t *testing.T) {
	chat := func(tools, work float64, ctx string) bool {
		return triageChat(testTriage("chat", tools, work, ctx), "مرسی")
	}
	if !chat(0.05, 0.3, "standalone") || chat(0.5, 0.3, "standalone") || chat(0.05, 0.3, "earlier") || chat(0.05, 0.3, "recent") ||
		chat(0.05, 2, "standalone") {
		t.Error("chat")
	}
	if triageChat(testTriage("chat", 0.05, 0.3, "standalone"), "یه تابع پایتون بنویس") {
		t.Error("words that show a job keep the tools")
	}
	for _, k := range []string{"code", "writing", "quick", "media"} {
		if triageChat(testTriage(k, 0.05, 0.5, "standalone"), "x") {
			t.Errorf("%s keeps its tools", k)
		}
	}
	if triageChat(testTriage("chat", 0.05, 0.3, "standalone"), strings.Repeat("کلمه ", 80)) {
		t.Error("a long message is more than a conversation")
	}
	if triageChat(router.Local(router.TriageInput{Input: router.Input{Text: "مرسی"}}), "مرسی") {
		t.Error("a local triage is never sure enough to drop the tools")
	}
	if triageEffort(true) != llm.EffortLow || triageEffort(false) != "" {
		t.Error("effort")
	}
	if !jobInProgress([]llm.Message{{Role: "user", Content: "x"}, {Role: "assistant", Content: "done\n\n<work_notes>\nread_file(a) → ok\n</work_notes>"}}) ||
		jobInProgress([]llm.Message{{Role: "user", Content: "سلام"}, {Role: "assistant", Content: "سلام!"}}) {
		t.Error("jobInProgress")
	}
	if !effortLowered(llm.EffortHigh, llm.EffortLow) || effortLowered("", llm.EffortLow) || effortLowered(llm.EffortLow, llm.EffortMedium) {
		t.Error("effortLowered")
	}
	if v := triageVerdict(testTriage("writing", 0.5, 2.0, "standalone")); v == nil || !v.Hard {
		t.Error("a long piece of writing is hard")
	}
	if v := triageVerdict(testTriage("nonsense", 0.5, 2.0, "standalone")); v != nil {
		t.Error("an unknown kind does not route")
	}

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
