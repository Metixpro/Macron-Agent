package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func TestRemindersCostNoTokens(t *testing.T) {
	h := newHarness(t)
	u := newUser(h, 80)
	_ = h.st.AddTokens(bg, 80, 500)
	m := private(80, "")
	now := time.Now()
	for _, text := range []string{"اولی", "دومی"} {
		if out := h.bot.createSchedule(bg, u, m, map[string]any{"kind": "reminder", "text": text, "in_minutes": 1}, now); !strings.HasPrefix(out, "Scheduled #") {
			t.Fatal(out)
		}
	}
	h.bot.runDueSchedules(bg, now.Add(2*time.Minute))
	h.bot.wg.Wait()

	if all := h.tg.allText(); strings.Count(all, "یادآوری") != 2 {
		t.Errorf("both reminders are delivered:\n%s", all)
	}
	if n, _ := h.st.TokensToday(bg, 80); n != 500 {
		t.Errorf("reminders cost nothing: %d", n)
	}
}

func TestAnAdminMayOpenAnotherUsersFilePanel(t *testing.T) {
	h := newHarness(t)
	newUser(h, 82)
	_ = h.bot.app.Machine.WriteFile(sessionFor(82), "a.txt", []byte("x"))
	h.click(83, 83, 900, "f:h:82")
	if a := h.tg.of("answerCallbackQuery"); len(a) == 0 || !strings.Contains(fmt.Sprint(a[len(a)-1].body), "مال کس دیگه‌ای است") {
		t.Errorf("a user cannot open someone else's files: %v", a)
	}
	h.click(ownerID, ownerID, 900, "f:h:82")
	if a := h.tg.of("answerCallbackQuery"); strings.Contains(fmt.Sprint(a[len(a)-1].body), "مال کس دیگه‌ای است") {
		t.Error("an admin can")
	}
}

func TestThePremiumStopLinkShowsItsIconOnce(t *testing.T) {
	h := newHarness(t)
	_ = h.st.SetSetting(bg, store.SettingPremiumEmoji, "true")
	_ = h.st.SetPremiumEmoji(bg, "⏹", "5260293700088511294")
	h.bot.reloadEmoji(bg)
	l := &liveView{b: h.bot, lng: fa, stopData: "run:stop:1:abcd"}
	row := l.stopRow()
	if !strings.Contains(row, `<tg-emoji emoji-id="5260293700088511294"></tg-emoji> توقف`) || strings.Contains(row, "⏹") {
		t.Errorf("the premium icon alone, no emoji character next to it: %s", row)
	}
	if label, id := h.bot.premium().Button("⏹"); label != "‌" || id == "" {
		t.Errorf("an emoji-only button shows the icon, not the emoji twice: %q %q", label, id)
	}
}

func TestTheGroupThinkingLineIsNotAQuote(t *testing.T) {
	h := newHarness(t)
	h.send(group(84, "مکرون سلام"))
	sent := h.tg.of("sendRichMessage")
	if len(sent) == 0 {
		t.Fatal("no live message")
	}
	md := fmt.Sprint(sent[0].body["rich_message"].(map[string]any)["markdown"])
	if !strings.HasPrefix(md, "💭") {
		t.Errorf("a plain line, no quote bar next to 💭: %q", md)
	}
	_ = telegram.SendOptions{}
}
