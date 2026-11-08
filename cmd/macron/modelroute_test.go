package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/store"
)

func TestSmartModelRouting(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	main, _ := h.st.Providers(ctx)
	strong, err := h.st.AddProvider(ctx, store.Provider{Name: "strong", BaseURL: main[0].BaseURL, APIKey: "sk-strong-secret-5678", Model: "big",
		Priority: 20, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.bot.prov.Reload(ctx)
	const uid = 402
	const code = "یه تابع پایتون بنویس که یه فایل csv رو بخونه و جمع ستون دوم رو برگردونه"

	h.send(private(uid, code))
	if h.lastModel() != "m" {
		t.Fatalf("no strong model yet: %q", h.lastModel())
	}

	h.send(private(ownerID, "/admin"))
	h.click(ownerID, ownerID, 9, "a:pv")
	if !hasButton(buttons(lastOf(h, "editMessageText")), "→ a:rt") {
		t.Fatal("no door to the routing in the providers")
	}
	h.click(ownerID, ownerID, 9, "a:rt")
	if v := lastOf(h, "editMessageText"); !strings.Contains(fmt.Sprint(v.body["text"]), "انتخاب نشده") {
		t.Fatalf("the routing: %v", v.body["text"])
	}
	h.click(ownerID, ownerID, 9, "a:rt:pick:strong:owner")
	set := fmt.Sprintf("a:rt:set:strong:owner:%d", strong)
	if !hasButton(buttons(lastOf(h, "editMessageText")), "→ "+set) {
		t.Fatalf("the providers to pick: %v", buttons(lastOf(h, "editMessageText")))
	}
	h.click(ownerID, ownerID, 9, set)
	if v := fmt.Sprint(lastOf(h, "editMessageText").body["text"]); !strings.Contains(v, "big") {
		t.Fatalf("the strong model is shown: %s", v)
	}

	route := func(text, want string) {
		t.Helper()
		h.send(private(uid, text))
		if h.lastModel() != want {
			t.Errorf("%q went to %q, want %q", text, h.lastModel(), want)
		}
	}
	route("سلام خوبی؟", "m")
	route(code, "big")
	route("آره", "big")
	route("قیمت دلار چنده؟", "m")

	h.click(ownerID, ownerID, 9, "a:rt:kinds")
	h.click(ownerID, ownerID, 9, "a:rt:kind:code")
	route("یه تابع پایتون بنویس", "m")
	h.click(ownerID, ownerID, 9, "a:rt:kreset")
	route("یه تابع پایتون بنویس", "big")

	h.click(ownerID, ownerID, 9, "a:rt:test")
	h.tg.reset()
	h.send(private(ownerID, "یه مقاله کامل و حرفه‌ای درباره تاریخ ایران بنویس با منابع معتبر"))
	v := fmt.Sprint(lastTextDelivery(h).body["text"])
	if !strings.Contains(v, "تحقیق") || !strings.Contains(v, "مدل قوی") {
		t.Errorf("the test: %s", v)
	}

	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if req["model"] == "big" {
			return map[string]any{"role": "assistant", "content": "انجام شد"}, 0
		}
		return toolCall("read_file", fmt.Sprintf(`{"path": "missing-%d.txt"}`, n)), 0
	}
	h.tg.reset()
	from := h.llm.count()
	h.send(private(uid, "قیمت طلا رو از فایل قیمت‌ها بخون"))
	h.wantText("انجام شد")
	var models []any
	h.llm.mu.Lock()
	for _, r := range h.llm.requests[from:] {
		models = append(models, r["model"])
	}
	h.llm.mu.Unlock()
	if len(models) < 3 || models[0] != "m" || models[1] != "m" || models[2] != "big" {
		t.Errorf("two light steps, then the strong model: %v", models)
	}
	rows, _ := h.st.TopStats(ctx, store.StatRoute, 1, 50)
	ways := map[string]int64{}
	for _, r := range rows {
		way, _, _ := strings.Cut(r.Name, ":")
		ways[way] += r.Count
	}
	if ways["escalated"] != 1 || ways["strong"] < 3 || ways["light"] < 3 {
		t.Errorf("the statistics: %v", rows)
	}

	h.llm.reply = say("ok")
	h.click(ownerID, ownerID, 9, "a:rt:on")
	route(code, "m")
}
