package main

import (
	"fmt"
	"github.com/Metixpro/macron-agent/pkg/store"
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
	frames := strings.Join(liveFrames(h), "\n")
	if !strings.Contains(frames, "<pre><code") || !strings.Contains(frames, "Files") {
		t.Fatalf("classic trace missing: %s", frames)
	}
	if len(h.tg.of("sendRichMessageDraft")) != 0 {
		t.Fatal("private chat used a draft instead of a live message")
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

func TestPrivateLiveMessageKeepsCodeAndStopWhenPremiumIsRejected(t *testing.T) {
	h := newHarness(t)
	_ = h.st.SetSetting(bg, store.SettingPremiumEmoji, "true")
	_ = h.st.SetPremiumEmoji(bg, "💭", "99999")
	h.bot.reloadEmoji(bg)
	h.tg.reject = func(method string, body map[string]any) string {
		if (method == "sendMessage" || method == "editMessageText") && strings.Contains(fmt.Sprint(body["text"]), "tg-emoji") {
			return "Bad Request: custom emoji not allowed"
		}
		return ""
	}
	l := h.bot.newLive(private(ownerID, "x"), ownerID)
	l.stopData = "run:stop:1:test"
	l.toolStart(0, "list_files")
	l.flush(bg)
	if l.traceMsg == 0 {
		t.Fatal("premium rejection hid the live message")
	}
	shown := lastOf(h, "sendMessage")
	if !strings.Contains(fmt.Sprint(shown.body["text"]), "<pre><code") || !hasButton(buttons(shown), "→ "+l.stopData) {
		t.Fatal("fallback lost code formatting or stop keyboard")
	}
	l.toolEnd(0, "list_files", "Files listed")
	l.lastEdit = time.Time{}
	l.flush(bg)
	edited := lastOf(h, "editMessageText")
	if strings.Contains(fmt.Sprint(edited.body["text"]), "tg-emoji") || !hasButton(buttons(edited), "→ "+l.stopData) {
		t.Fatal("edit did not fall back while keeping stop")
	}
}
