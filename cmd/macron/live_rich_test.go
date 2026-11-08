package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func TestPrivateRichCardUsesNativeBlocksAndReusesMessage(t *testing.T) {
	h := newHarness(t)
	l := h.bot.newLive(private(ownerID, "کار"), ownerID)
	close(l.finished)
	l.stopData = "run:stop:1000:test"
	l.tr.plan = []agent.PlanItem{{Text: "<b>متن کار</b>", Status: agent.PlanInProgress}, {Text: "گزارش", Status: agent.PlanDone}}
	l.toolStart(0, "list_files")
	l.flush(bg)
	initial := lastOf(h, "sendRichMessage")
	rm := initial.body["rich_message"].(map[string]any)
	if rm["markdown"] != nil || rm["html"] != nil || rm["is_rtl"] != true {
		t.Fatalf("native card: %v", rm)
	}
	raw, _ := json.Marshal(rm)
	for _, typ := range []string{"heading", "paragraph", "list", "details", "pre", "footer"} {
		if !strings.Contains(string(raw), `"type":"`+typ+`"`) {
			t.Errorf("missing %s: %s", typ, raw)
		}
	}
	if strings.Contains(string(raw), `"type":"thinking"`) || !strings.Contains(string(raw), `"has_checkbox":true`) || !strings.Contains(string(raw), `"is_checked":true`) {
		t.Fatal("invalid persistent card/checklist")
	}
	if !hasButton(buttons(initial), "→ "+l.stopData) {
		t.Fatal("stop must be below the card")
	}
	id := l.traceMsg
	l.toolEnd(0, "list_files", "done")
	l.lastEdit = time.Time{}
	l.flush(bg)
	if lastOf(h, "editMessageText").body["message_id"] != float64(id) {
		t.Fatal("progress created another message")
	}
	if err := l.replace(bg, telegram.RichMessage{Markdown: "**تمام شد**", IsRTL: true}, nil); err != nil {
		t.Fatal(err)
	}
	final := lastOf(h, "editMessageText")
	if final.body["message_id"] != float64(id) || len(buttons(final)) != 0 || l.traceMsg != 0 {
		t.Fatal("final message or stop cleanup is incorrect")
	}
	l.discard()
	if len(h.tg.of("deleteMessage")) != 0 {
		t.Fatal("final answer was deleted")
	}
}

func TestPrivateRichCompatibilityFallbackKeepsStop(t *testing.T) {
	h := newHarness(t)
	h.tg.fail["sendRichMessage"] = 404
	h.tg.desc["sendRichMessage"] = "method not found"
	l := h.bot.newLive(private(ownerID, "کار"), ownerID)
	l.stopData = "run:stop:1000:test"
	l.toolStart(0, "list_files")
	l.flush(bg)
	sent := lastOf(h, "sendMessage")
	if l.rich || l.traceMsg == 0 || !hasButton(buttons(sent), "→ "+l.stopData) || !strings.Contains(fmt.Sprint(sent.body["text"]), "<pre><code") {
		t.Fatal("unsupported rich API lost classic card or stop")
	}
}

func TestPrivateRichTransientFailureDoesNotDisableRich(t *testing.T) {
	h := newHarness(t)
	h.tg.fail["sendRichMessage"] = 500
	l := h.bot.newLive(private(ownerID, "کار"), ownerID)
	l.flush(bg)
	if !l.rich || !l.dirty || h.bot.richOff.Load() {
		t.Fatal("transient failure disabled rich")
	}
	delete(h.tg.fail, "sendRichMessage")
	l.lastEdit = time.Time{}
	l.flush(bg)
	if l.traceMsg == 0 || len(h.tg.of("sendMessage")) != 0 {
		t.Fatal("rich was not retried")
	}
}
