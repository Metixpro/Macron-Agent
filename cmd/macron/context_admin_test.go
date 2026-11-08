package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/store"
)

func TestParseTokens(t *testing.T) {
	for in, want := range map[string]int{"256000": 256000, "256k": 256000, "۲۵۶k": 256000, "1m": 1000000, "1.5M": 1500000, "128,000": 128000, "0": 0} {
		if got, ok := parseTokens(in); !ok || got != want {
			t.Errorf("parseTokens(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "abc", "-5", "100", "20m"} {
		if _, ok := parseTokens(bad); ok {
			t.Errorf("parseTokens(%q) must fail", bad)
		}
	}
}

func TestContextWindowFromThePanel(t *testing.T) {
	h := newHarness(t)
	w := contextWindow(h.st, h.bot.prov, 0)
	if got := w(); got != agent.ContextWindow("m") || got != agent.DefaultContextWindow {
		t.Fatalf("guessed from the model: %d", got)
	}
	if got := contextWindow(h.st, h.bot.prov, 200000)(); got != 200000 {
		t.Fatalf("the environment: %d", got)
	}
	h.click(ownerID, ownerID, 9, "a:st:window")
	h.send(private(ownerID, "256k"))
	if got := contextWindow(h.st, h.bot.prov, 200000)(); got != 256000 {
		t.Fatalf("the panel wins: %d", got)
	}
	h.wantText("<b>256k</b>")
	_ = h.st.SetSetting(context.Background(), settingContextWindow, "0")
	if got := w(); got != agent.DefaultContextWindow {
		t.Fatalf("0 is automatic again: %d", got)
	}
	_ = store.ErrNotFound
	if !strings.Contains(lastMarkup(h), "a:st:window") {
		t.Fatal("the settings have the button")
	}
}

func TestAProvidersOwnWindowComesFirst(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_ = h.st.SetSetting(ctx, settingContextWindow, "300000")
	list, _ := h.st.Providers(ctx)
	p := list[0]
	p.ContextWindow = 128000
	if err := h.st.UpdateProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	_ = h.bot.prov.Reload(ctx)
	if got := contextWindow(h.st, h.bot.prov, 0)(); got != 128000 {
		t.Errorf("the active provider's window wins: %d", got)
	}
	h.click(ownerID, ownerID, 900, fmt.Sprintf("a:pv:%d:window", p.ID))
	h.send(private(ownerID, "1m"))
	if q, _ := h.st.Provider(ctx, p.ID); q.ContextWindow != 1000000 {
		t.Errorf("set in the panel: %d", q.ContextWindow)
	}
}

func TestProviderPageWarnsAboutAnUnknownModelsWindowAndShowsItsCache(t *testing.T) {
	h := newHarness(t)
	ps, _ := h.st.Providers(bg)
	p := ps[0]
	p.Model = "deep"
	if err := h.st.UpdateProvider(bg, p); err != nil {
		t.Fatal(err)
	}
	view := h.bot.viewProvider(bg, fa, p.ID).html
	if !strings.Contains(view, "این اسم مدل رو نمی‌شناسم") || !strings.Contains(view, "کامبو") {
		t.Errorf("the unknown name is flagged:\n%s", view)
	}
	if strings.Contains(view, "کش پرامپت") {
		t.Errorf("no cache line before anything was measured:\n%s", view)
	}

	p.Model = "gemini-3-flash"
	_ = h.st.UpdateProvider(bg, p)
	if view = h.bot.viewProvider(bg, fa, p.ID).html; strings.Contains(view, "نمی‌شناسم") {
		t.Errorf("a known model has no warning:\n%s", view)
	}

	for range triageCacheSamples {
		h.bot.noteCache(p.ID, llm.Usage{PromptTokens: 18000, CachedTokens: 600})
	}
	view = h.bot.viewProvider(bg, fa, p.ID).html
	if !strings.Contains(view, "📦 کش پرامپت: ۳٪") || !strings.Contains(view, "کمه") {
		t.Errorf("a provider that caches little says so:\n%s", view)
	}
	if en := h.bot.viewProvider(bg, en, p.ID).html; !strings.Contains(en, "Prompt cache: 3%") || persianLeft(strings.ReplaceAll(en, p.Name, "")) {
		t.Errorf("in English:\n%s", en)
	}
}
