package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestClassicTraceIgnoresRemovedDisplaySettings(t *testing.T) {
	h := newHarness(t)
	for key, value := range map[string]string{"trace_style": "", "work_log": "true", "live_mode": "dots"} {
		if err := h.st.SetSetting(bg, key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.st.SetUserPref(bg, ownerID, "live", "msg"); err != nil {
		t.Fatal(err)
	}
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall("list_files", `{}`), 0
		}
		time.Sleep(900 * time.Millisecond)
		return map[string]any{"role": "assistant", "content": "فایل‌ها بررسی شد"}, 0
	}
	h.send(private(ownerID, "فایل‌های پوشه را بررسی کن"))
	var frames strings.Builder
	for _, m := range h.tg.of("sendRichMessageDraft") {
		frames.WriteString(fmt.Sprint(m.body["rich_message"]))
	}
	if !strings.Contains(frames.String(), "```bash") || !strings.Contains(frames.String(), "Files") {
		t.Fatalf("classic trace missing: %s", frames.String())
	}
	h.noText("گزارش کار")
}

func TestRemovedDisplayControlsStayAbsentInBothLanguages(t *testing.T) {
	h := newHarness(t)
	for _, l := range []lang{fa, en} {
		for _, v := range []panelView{h.bot.viewShowWork(l), h.bot.viewAgent(l)} {
			all := v.html + fmt.Sprint(v.kb)
			for _, removed := range []string{"a:st:tstyle", "a:st:worklog", "a:st:live", "سهم هر سطح", "VIP", "Terminal look", "Work log above"} {
				if strings.Contains(all, removed) {
					t.Errorf("removed control/text still present: %s", removed)
				}
			}
		}
		kb := fmt.Sprint(h.bot.modeKeyboard(l, false))
		if strings.Contains(kb, "mode:live:") {
			t.Fatal("private-view options still present")
		}
	}
}
