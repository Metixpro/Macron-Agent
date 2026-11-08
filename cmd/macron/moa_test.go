package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/store"
)

// withAdvisor adds a second provider ("gpt", model gpt-x) beside the main one
// ("m"), and a preset of the two.
func withAdvisor(t *testing.T, h *harness, fanout string, enabled bool) {
	t.Helper()
	main, _ := h.st.Providers(bg)
	if _, err := h.st.AddProvider(bg, store.Provider{Name: "gpt", BaseURL: main[0].BaseURL, APIKey: "sk-gpt-1", Model: "gpt-x", Priority: 20, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_ = h.bot.prov.Reload(bg)
	on := ""
	if !enabled {
		on = `,"enabled":false`
	}
	_ = h.st.SetSetting(bg, settingMoaPresets, fmt.Sprintf(`[{"name":"duo","aggregator":{"provider":%q},"reference_models":[{"provider":"gpt"}],"fanout":%q%s}]`,
		main[0].Name, fanout, on))
}

// advisorCalls counts the requests that went to the advisor's model.
func advisorCalls(h *harness) int {
	h.llm.mu.Lock()
	defer h.llm.mu.Unlock()
	n := 0
	for _, r := range h.llm.requests {
		if r["model"] == "gpt-x" {
			n++
		}
	}
	return n
}

// moaReply: the advisor gives its view; the aggregator works (tools calls
// list_files until it has n results) and answers.
func moaReply(h *harness, n int, advisorStatus int) {
	h.llm.reply = func(req map[string]any, k int) (map[string]any, int) {
		if req["model"] == "gpt-x" {
			if advisorStatus != 0 {
				return map[string]any{"error": "invalid api key"}, advisorStatus
			}
			return map[string]any{"role": "assistant", "content": "ADVICE: split the job in two"}, 0
		}
		if toolResults(req) < n {
			return toolCall("list_files", fmt.Sprintf(`{"path":"p%d"}`, toolResults(req))), 0
		}
		return map[string]any{"role": "assistant", "content": "جواب نهایی ✅"}, 0
	}
}

func TestMoaRunsOneRequest(t *testing.T) {
	h := newHarness(t)
	withAdvisor(t, h, "user_turn", true)
	moaReply(h, 0, 0)
	h.send(private(ownerID, "/moa یه کار سخت"))
	h.wantText("جواب نهایی")
	if advisorCalls(h) != 1 {
		t.Fatalf("the advisor was asked %d times", advisorCalls(h))
	}
	h.wantText("[+] gpt")
	h.wantText("[*] جمع‌بندی")
	if len(h.tg.of("deleteMessage")) == 0 {
		t.Error("the trace is removed after the answer")
	}
	r := jobRequests(h, "یه کار سخت")
	if len(r) == 0 || !strings.Contains(llmText(r[len(r)-1]), "reference model=") || !strings.Contains(llmText(r[len(r)-1]), "ADVICE: split") {
		t.Fatalf("the aggregator sees the advice: %v", len(r))
	}
	// The next request is the usual model alone.
	h.send(private(ownerID, "یه سؤال معمولی"))
	if advisorCalls(h) != 1 {
		t.Error("/moa does not switch for good")
	}
}

// every_n:3: the advisors at the first round and every third after.
func TestMoaEveryThirdRound(t *testing.T) {
	h := newHarness(t)
	_ = h.st.SetSetting(bg, store.SettingAutoContinue, "0")
	withAdvisor(t, h, "every_n:3", true)
	moaReply(h, 6, 0) // 6 tool rounds, then the answer: 7 rounds
	h.send(private(ownerID, "/moa یه کار چندمرحله‌ای"))
	if n := advisorCalls(h); n != 3 {
		t.Fatalf("advisors asked %d times, want 3 (rounds 1, 4, 7)", n)
	}
}

func TestMoaFailingAdvisorKeepsTheTurn(t *testing.T) {
	h := newHarness(t)
	withAdvisor(t, h, "user_turn", true)
	moaReply(h, 0, 401)
	h.send(private(ownerID, "/moa کار"))
	h.wantText("جواب نهایی")
	r := jobRequests(h, "کار")
	if len(r) == 0 || !strings.Contains(llmText(r[len(r)-1]), `error=`) {
		t.Fatal("the error is in the reference block")
	}
	h.wantText("[!] gpt")
}

func TestMoaDisabledIsTheAggregatorAlone(t *testing.T) {
	h := newHarness(t)
	withAdvisor(t, h, "per_iteration", false)
	moaReply(h, 2, 0)
	h.send(private(ownerID, "/moa کار"))
	h.wantText("جواب نهایی")
	if advisorCalls(h) != 0 {
		t.Fatalf("advisors asked %d times", advisorCalls(h))
	}
}

func TestMoaPanel(t *testing.T) {
	h := newHarness(t)
	main, _ := h.st.Providers(bg)
	_, _ = h.st.AddProvider(bg, store.Provider{Name: "gpt", BaseURL: main[0].BaseURL, APIKey: "k", Model: "gpt-x", Priority: 20, Enabled: true})
	_ = h.bot.prov.Reload(bg)
	h.click(ownerID, ownerID, 9, "a:ai")
	if !hasButton(buttons(lastOf(h, "editMessageText")), "→ a:moa") {
		t.Fatal("no door from the agent")
	}
	h.click(ownerID, ownerID, 9, "a:moa:new")
	h.send(private(ownerID, "duo"))
	list := h.bot.moaPresets()
	if len(list) != 1 || list[0].Name != "duo" || list[0].Aggregator.Provider != main[0].Name {
		t.Fatalf("created: %+v", list)
	}
	ps, _ := h.st.Providers(bg)
	gpt := ps[len(ps)-1]
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:moa:ref:0:%d", gpt.ID))
	h.click(ownerID, ownerID, 9, "a:moa:fan:0")
	h.click(ownerID, ownerID, 9, "a:moa:eff:0:0")
	h.click(ownerID, ownerID, 9, "a:moa:priv:0")
	h.click(ownerID, ownerID, 9, "a:moa:active:0")
	p := h.bot.moaPresets()[0]
	if len(p.References) != 1 || p.References[0].Provider != "gpt" || p.FanoutName() != "per_iteration" || p.References[0].Effort != "none" ||
		p.PrivacyFilter != "display" {
		t.Fatalf("edited: %+v", p)
	}
	if v, _ := h.st.Setting(settingMoaActive); v != "duo" {
		t.Fatal("every request")
	}
	h.wantText("صورتحساب روی پرووایدر همین مدله")
	// A recursive or broken preset is reported.
	_ = h.st.SetSetting(bg, settingMoaPresets, `[{"name":"loop","aggregator":{"provider":"moa"}}]`)
	if probs := h.bot.moaProblems(h.bot.moaPresets()[0]); len(probs) == 0 {
		t.Fatal("a mixture cannot aggregate")
	}
}
