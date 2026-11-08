package main

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/guard"
)

// waitFor polls until the chat shows text (the owners' alert goes out beside
// the request, not inside it).
func (h *harness) waitFor(sub string) {
	h.t.Helper()
	for i := 0; i < 100; i++ {
		if strings.Contains(h.tg.allText(), sub) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("no message contains %q; sent:\n%s", sub, h.tg.allText())
}

// When no model answers, the owners hear of it once (with what each provider
// said), and «🔁 دوباره امتحان کن» makes every provider a first choice again
// before it tries: what failed minutes ago may work now.
func TestAModelOutageReachesTheOwnersAndRetryStartsFresh(t *testing.T) {
	h := newHarness(t)
	modelDownAlerts = guard.NewRateLimiter(1, 10*time.Minute)
	const uid = 71
	h.llm.reply = func(map[string]any, int) (map[string]any, int) {
		return map[string]any{"error": "quota exceeded for this key"}, 503
	}
	for i := 0; i < 3; i++ {
		h.send(private(uid, "یه کار سنگین"))
	}
	h.wantText("به مغزم دسترسی ندارم")
	h.waitFor("مدل جواب نمی‌ده")
	all := h.tg.allText()
	if n := strings.Count(all, "مدل جواب نمی‌ده"); n != 1 {
		t.Errorf("the owners are told once, not %d times", n)
	}
	for _, want := range []string{fmt.Sprintf("<code>%d</code>", uid), "main", "quota exceeded"} {
		if !strings.Contains(all, want) {
			t.Errorf("the alert says who, which provider and why: misses %q\n%s", want, all)
		}
	}
	if list := h.bot.prov.Snapshot(); len(list) == 0 || !list[0].Cooling {
		t.Fatalf("three failures put the provider on cooldown: %+v", list)
	}

	// The user presses «try again»: the cooldown is gone when the request goes out.
	var coolingAtRetry atomic.Bool
	h.llm.reply = func(map[string]any, int) (map[string]any, int) {
		for _, p := range h.bot.prov.Snapshot() {
			coolingAtRetry.Store(coolingAtRetry.Load() || p.Cooling)
		}
		return map[string]any{"role": "assistant", "content": "بفرما، درست شد ✅"}, 0
	}
	h.tg.reset()
	h.click(uid, uid, 901, fmt.Sprintf("run:go:%d:77", uid))
	h.wantText("بفرما، درست شد")
	if coolingAtRetry.Load() {
		t.Error("a provider was still on cooldown when «try again» sent the request")
	}
	if list := h.bot.prov.Snapshot(); list[0].Cooling {
		t.Errorf("and it answered, so it is healthy: %+v", list)
	}
}
