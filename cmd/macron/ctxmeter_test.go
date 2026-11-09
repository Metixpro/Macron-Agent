package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/store"
)

func TestTheContextMeterGoesOnAcrossRequests(t *testing.T) {
	h := newHarness(t)
	big := strings.Repeat("goroutines and channels make concurrency simple. ", 800)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		switch n {
		case 0:
			return toolCall("write_file", fmt.Sprintf(`{"filename":"notes.txt","content":%q}`, big)), 0
		case 1:
			return toolCall("read_file", `{"filename":"notes.txt"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "خوندمش 👍"}, 0
	}
	h.send(private(66, "یه متن بلند بنویس و بخونش"))
	meter := func() (tokens, compressions int) {
		v, _ := h.st.UserPref(bg, 66, store.PrefContextMeter)
		fmt.Sscanf(v, "%d/%d", &tokens, &compressions)
		return
	}
	first, _ := meter()
	if first < 8000 {
		t.Fatalf("the first job's meter: %d", first)
	}
	h.llm.reply = say("سلام 🙂")
	h.send(private(66, "ممنون"))
	if second, _ := meter(); second < first {
		t.Errorf("the meter went back from %d to %d", first, second)
	}

	h.send(private(66, "/new"))
	if mc, _ := h.bot.mem.Load(bg, 66); mc.Messages == 0 {
		if n, _ := h.bot.contextCarry(bg, 66, mc); n != 0 {
			t.Errorf("a reset conversation carries %d", n)
		}
	}
}
