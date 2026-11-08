package main

import (
	"context"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func TestPersianLaysOutRightToLeft(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const faUser, enUser = 7101, 7102
	h.send(private(faUser, "/start"))
	h.send(englishMsg(enUser, "/start"))
	h.tg.reset()

	html := "VIP فعال است\nسطح: VIP\nلغو با /cancel"
	h.bot.sendHTML(ctx, faUser, html, telegram.SendOptions{})
	h.bot.sendHTML(ctx, enUser, html, telegram.SendOptions{}) // an English reader: left as it is
	h.bot.sendHTML(ctx, -100601, "VIP برای همه\nیه گروه فارسی", telegram.SendOptions{})
	h.bot.sendHTML(ctx, -100602, "Sent to the group of علی", telegram.SendOptions{})
	_ = h.bot.editHTML(ctx, faUser, 5, "Free رایگان است", nil)
	want := []string{
		"‏VIP فعال است\nسطح: VIP\nلغو با ‎/cancel",
		html,
		"‏VIP برای همه\nیه گروه فارسی",
		"Sent to the group of علی",
		"‏Free رایگان است",
	}
	var got []string
	h.tg.mu.Lock()
	for _, c := range h.tg.calls {
		if c.method == "sendMessage" || c.method == "editMessageText" {
			got = append(got, c.body["text"].(string))
		}
	}
	h.tg.mu.Unlock()
	if len(got) != len(want) {
		t.Fatalf("sent %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d = %q, want %q", i, got[i], want[i])
		}
	}

	if b := h.bot.button("VIP بخر و راحت باش", "x", ""); b.Text != "‏VIP بخر و راحت باش" {
		t.Errorf("Persian label: %q", b.Text)
	}
	if b := h.bot.button("Ban علی", "x", ""); b.Text != "Ban علی" {
		t.Errorf("English label: %q", b.Text)
	}
}
