package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

// The admin picks a provider's effort on its card; the requests go with it,
// and ultracode gives the job its bigger budget and standard.
func TestAdminChoosesAProvidersEffort(t *testing.T) {
	h := newHarness(t)
	ps, _ := h.st.Providers(bg)
	id := ps[0].ID
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d", id))
	if v := lastOf(h, "editMessageText"); !strings.Contains(v.body["text"].(string), "effort: <b>⚙️ خودکار</b>") || !hasButton(buttons(v), fmt.Sprintf("a:pv:%d:eff", id)) {
		t.Fatalf("the provider's card shows its effort and the button: %v %v", v.body["text"], buttons(v))
	}
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:eff", id))
	v := lastOf(h, "editMessageText")
	for _, code := range []string{"auto", "low", "medium", "high", "ultracode"} {
		if !hasButton(buttons(v), fmt.Sprintf("a:pv:%d:eff:%s", id, code)) {
			t.Errorf("a button for %s: %v", code, buttons(v))
		}
	}
	if !hasButton(buttons(v), fmt.Sprintf("a:pv:%d", id)) || !strings.Contains(v.body["text"].(string), "Ultracode") {
		t.Fatalf("the levels and the way back: %v", v.body["text"])
	}
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:eff:max", id)) // not a level
	if p, _ := h.st.Provider(bg, id); p.Effort != "" {
		t.Fatalf("only the levels: %q", p.Effort)
	}
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:eff:ultracode", id))
	if p, _ := h.st.Provider(bg, id); p.Effort != llm.EffortUltra {
		t.Fatalf("saved: %q", p.Effort)
	}
	if v := lastOf(h, "editMessageText"); !hasButton(buttons(v), "✅ 🚀") || !strings.Contains(v.body["text"].(string), "reasoning_effort = high") {
		t.Errorf("the choice is marked, with what this model gets: %v %v", v.body["text"], buttons(v))
	}

	h.send(private(50, "یه اسکریپت پایتون بنویس که عددهای اول تا صد رو چاپ کنه و توضیح بده چطور کار می‌کنه"))
	var job map[string]any
	h.llm.mu.Lock()
	for _, r := range h.llm.requests {
		if strings.Contains(llmText(r), "عددهای اول") && r["tools"] != nil {
			job = r
		}
	}
	h.llm.mu.Unlock()
	if job == nil {
		t.Fatal("no request of the job")
	}
	if job["reasoning_effort"] != "high" {
		t.Errorf("the model gets its effort: %v", job["reasoning_effort"])
	}
	if !strings.Contains(llmText(job), "Ultracode") || !strings.Contains(llmText(job), "up to 8 tool rounds") {
		t.Errorf("ultracode: twice the steps and the standard to work to:\n%s", llmText(job))
	}

	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:eff:auto", id))
	if p, _ := h.st.Provider(bg, id); p.Effort != "" {
		t.Errorf("back to the model's own: %q", p.Effort)
	}
}
