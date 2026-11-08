package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/sysmon"
)

func TestServerPanel(t *testing.T) {
	h := newHarness(t)
	h.send(private(55, "سلام"))
	s := h.bot.sampler()
	s.Sample()
	time.Sleep(60 * time.Millisecond)
	s.Sample()

	h.click(ownerID, ownerID, 9, "a:stats")
	if v := lastOf(h, "editMessageText"); !hasButton(buttons(v), "a:sys:1h") {
		t.Fatalf("📊 the stats have the server button: %v", buttons(v))
	}
	h.tg.reset()
	h.click(ownerID, ownerID, 9, "a:sys:1h")
	v := lastOf(h, "editMessageText")
	txt := v.body["text"].(string)
	for _, want := range []string{"سرور", "CPU", "RAM", "دیسک", "خود ربات", "دستورها", "پرمصرف‌ترین‌ها (RAM)"} {
		if !strings.Contains(txt, want) {
			t.Errorf("the panel lacks %q:\n%s", want, txt)
		}
	}
	for _, b := range []string{"a:sys:1h", "a:sys:6h", "a:sys:24h", "a:syschart:1h", "a:sysal"} {
		if !hasButton(buttons(v), b) {
			t.Errorf("button %s: %v", b, buttons(v))
		}
	}
	h.click(ownerID, ownerID, 9, "a:sys:24h")
	if v := lastOf(h, "editMessageText"); !strings.Contains(fmt.Sprint(buttons(v)), "✓ ۲۴ ساعت") {
		t.Errorf("the chosen stretch is marked: %v", buttons(v))
	}
	h.click(ownerID, ownerID, 9, "a:stats")
	if v := lastOf(h, "editMessageText"); !strings.Contains(v.body["text"].(string), "سرور: CPU") || !hasButton(buttons(v), "a:sys:1h") {
		t.Errorf("the stats panel sums the server up and links to it: %v", v.body["text"])
	}

	h.click(ownerID, ownerID, 9, "a:sysal")
	if v := lastOf(h, "editMessageText"); !strings.Contains(v.body["text"].(string), "90٪") {
		t.Errorf("the default limits: %v", v.body["text"])
	}
	h.click(ownerID, ownerID, 9, "a:sysal:cpu")
	if n := h.st.SettingInt(store.SettingSysCPU, 90); n != 95 {
		t.Errorf("a tap steps the limit: %d", n)
	}
	h.click(ownerID, ownerID, 9, "a:sysal:cpu")
	if n := h.st.SettingInt(store.SettingSysCPU, 90); n != 0 {
		t.Errorf("past the top it is off: %d", n)
	}
	if l := h.bot.sysLimits(); l.CPU != 0 || l.RAM != 90 || l.Disk != 90 {
		t.Errorf("limits: %+v", l)
	}
	h.click(ownerID, ownerID, 9, "a:sysal:t")
	if l := h.bot.sysLimits(); l != (sysmon.Limits{}) {
		t.Errorf("alerts off: %+v", l)
	}

	h.tg.reset()
	h.bot.sysAlert(sysmon.Alert{Kind: sysmon.AlertCPU, Value: 97.4, Limit: 90, Top: []sysmon.Proc{{Name: "ffmpeg", CPU: 180}}})
	h.wantText("CPU سرور بالاست")
	h.wantText("ffmpeg")
	h.tg.reset()
	h.bot.sysAlert(sysmon.Alert{Kind: sysmon.AlertDisk, Value: 93, Limit: 90, Path: "/"})
	h.wantText("دیسک سرور داره پر می‌شه")
	h.tg.reset()
	h.bot.sysAlert(sysmon.Alert{Kind: sysmon.AlertRAM, Value: 50, Limit: 90, Recovered: true})
	h.wantText("به حالت عادی برگشت")

	h.tg.reset()
	h.click(55, 55, 1, "a:sys:1h")
	if len(h.tg.of("editMessageText")) != 0 {
		t.Error("only admins open the server panel")
	}
}

func TestServerPanelHasNoSSHUserManagement(t *testing.T) {
	h := newHarness(t)
	for _, l := range []lang{fa, en} {
		v := h.bot.viewServer(l, "1h")
		for _, row := range v.kb.InlineKeyboard {
			for _, button := range row {
				if button.CallbackData == "a:ssh" || strings.Contains(button.Text, "SSH") {
					t.Fatalf("obsolete SSH user button: %+v", button)
				}
			}
		}
	}
	h.click(ownerID, ownerID, 9, "a:ssh")
	v := lastOf(h, "editMessageText")
	if !hasButton(buttons(v), "→ a:stats") || hasButton(buttons(v), "→ a:ssh") {
		t.Fatal("old SSH callback must refresh the server screen")
	}
	if len(h.tg.of("answerCallbackQuery")) == 0 {
		t.Fatal("old callback not acknowledged")
	}
}
