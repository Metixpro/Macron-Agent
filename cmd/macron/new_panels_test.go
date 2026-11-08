package main

import (
	"fmt"
	"strings"
	"testing"
)

// Exercise the Telegram callback dispatcher, not just the view builders.
func TestNewPanelNavigationAndBack(t *testing.T) {
	h := newHarness(t)
	withRelease(t, fakeGitHub(t, "99.0.0", false))
	for _, tc := range []struct{ page, back string }{
		{"a:ai", "a:home"}, {"a:ws", "a:ai"}, {"a:gate", "a:ai"},
		{"a:moa", "a:ai"}, {"a:learn", "a:home"},
		{"a:learn:skills", "a:learn"}, {"a:learn:log:0", "a:learn"},
		{"a:learn:cfg", "a:learn"}, {"a:pv", "a:home"},
		{"a:rt", "a:pv"}, {"a:rt:kinds", "a:rt"}, {"a:tri", "a:rt"},
		{"a:upd", "a:home"},
	} {
		t.Run(tc.page, func(t *testing.T) {
			h.tg.reset()
			h.click(ownerID, ownerID, 9, tc.page)
			if len(h.tg.of("answerCallbackQuery")) == 0 || len(h.tg.of("editMessageText")) == 0 {
				t.Fatalf("callback neither acknowledged nor rendered: %s", tc.page)
			}
			page := lastOf(h, "editMessageText")
			if !hasButton(buttons(page), "→ "+tc.back) {
				t.Fatalf("no return path %s: %v", tc.back, buttons(page))
			}
			if text := fmt.Sprint(page.body["text"]); strings.TrimSpace(text) == "" || strings.Contains(text, "%!") {
				t.Fatalf("invalid page: %s", text)
			}
			h.tg.reset()
			h.click(ownerID, ownerID, 9, tc.back)
			if len(h.tg.of("editMessageText")) == 0 {
				t.Fatal("back button did not open a page")
			}
		})
	}
}

func TestNewPanelTogglesPersistAndRejectOtherUsers(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		callback, key string
		initial       bool
	}{
		{"a:gate:ckpt", settingGateCheckpoints, false},
		{"a:learn:auto", settingLearnAuto, true},
		{"a:learn:weekly", settingWeeklyReport, true},
		{"a:rt:on", settingRouterOn, true},
		{"a:rt:esc", settingRouterEsc, true},
		{"a:tri:chat", settingTriageChat, false},
	} {
		t.Run(tc.callback, func(t *testing.T) {
			for i := 0; i < 2; i++ {
				before := h.st.SettingBool(tc.key, tc.initial)
				h.tg.reset()
				h.click(555, 555, 9, tc.callback)
				if h.st.SettingBool(tc.key, tc.initial) != before || len(h.tg.of("editMessageText")) != 0 {
					t.Fatal("non-admin changed settings")
				}
				if lastOf(h, "answerCallbackQuery").body["show_alert"] != true {
					t.Fatal("non-admin was not denied")
				}
				h.click(ownerID, ownerID, 9, tc.callback)
				if h.st.SettingBool(tc.key, tc.initial) == before {
					t.Fatal("owner's change was not persisted")
				}
			}
		})
	}
}

func TestNewPanelFormsCancelCleanly(t *testing.T) {
	h := newHarness(t)
	for _, cb := range []string{"a:moa:new", "a:tri:test", "a:learn:teach"} {
		t.Run(cb, func(t *testing.T) {
			h.tg.reset()
			h.click(ownerID, ownerID, 9, cb)
			if h.bot.pending(ownerID) == nil {
				t.Fatal("form did not start")
			}
			if !hasButton(buttons(lastOf(h, "editMessageText")), "→ a:cancelinput") {
				t.Fatal("no cancel button")
			}
			h.click(ownerID, ownerID, 9, "a:cancelinput")
			if h.bot.pending(ownerID) != nil {
				t.Fatal("cancel left a pending input")
			}
			if len(h.bot.moaPresets()) != 0 {
				t.Fatal("cancel created a preset")
			}
		})
	}
}
