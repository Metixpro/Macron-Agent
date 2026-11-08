package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func lastMarkup(h *harness) string {
	h.t.Helper()
	h.tg.mu.Lock()
	defer h.tg.mu.Unlock()
	for i := len(h.tg.calls) - 1; i >= 0; i-- {
		if m, ok := h.tg.calls[i].body["reply_markup"]; ok {
			return fmt.Sprint(m)
		}
	}
	return ""
}

func TestPanelAccess(t *testing.T) {
	h := newHarness(t)
	h.send(private(ownerID, "/admin"))
	h.wantText("پنل مدیریت مکرون")
	kb := lastMarkup(h)
	for _, want := range []string{"a:stats", "a:pv", "a:ai", "a:learn", "a:st"} {
		if !strings.Contains(kb, want) {
			t.Errorf("panel misses %s: %s", want, kb)
		}
	}
	for _, gone := range []string{"a:users", "a:bc", "a:shop", "a:ad", "a:gr", "a:fj"} {
		if strings.Contains(kb, gone) {
			t.Errorf("the personal panel has no %s: %s", gone, kb)
		}
	}
	// A normal user clicking an admin button gets nothing.
	h.tg.reset()
	h.click(55, 55, 1, "a:stats")
	if len(h.tg.of("editMessageText")) != 0 {
		t.Error("non-admins must not see panel screens")
	}
}

func TestStatsScreens(t *testing.T) {
	h := newHarness(t)
	h.send(private(30, "سلام"))
	h.send(private(31, "درود"))
	h.tg.reset()
	h.click(ownerID, ownerID, 9, "a:stats")
	h.wantText("آمار کامل")
	h.wantText("امروز: <b>2</b>") // two users so far
	h.click(ownerID, ownerID, 9, "a:top")
	h.wantText("فعال‌ترین کاربرهای امروز")
}

func TestSettingsScreen(t *testing.T) {
	h := newHarness(t)
	h.click(ownerID, ownerID, 9, "a:st:ctx")
	h.wantText("خودکار")
	h.send(private(ownerID, "1500")) // under the least that makes sense
	h.wantText("یه عدد بین")
	h.send(private(ownerID, "12000"))
	if h.st.SettingInt(store.SettingHistoryTokens, 0) != 12000 || h.bot.historyTokens() != 12000 {
		t.Fatal("history window setting")
	}
	h.wantText("گفتگوی همراه هر درخواست: <b>12.0K</b>")
	h.click(ownerID, ownerID, 9, "a:st:react")
	if h.st.SettingBool(store.SettingReactions, true) {
		t.Fatal("reactions toggle")
	}
	// /cancel leaves a dialog.
	h.click(ownerID, ownerID, 9, "a:st:ctx")
	h.send(private(ownerID, "/cancel"))
	if h.bot.pending(ownerID) != nil {
		t.Error("dialog not cancelled")
	}
}

func TestProviderWizardSwitchAndDelete(t *testing.T) {
	h := newHarness(t)
	h.click(ownerID, ownerID, 9, "a:pv")
	h.wantText("<b>main</b>")
	h.click(ownerID, ownerID, 9, "a:pv:add")
	h.send(private(ownerID, "backup"))
	h.send(private(ownerID, "not a url"))
	h.wantText("آدرس معتبر نیست")
	llmURL := ""
	if ps, _ := h.st.Providers(bg); len(ps) > 0 {
		llmURL = ps[0].BaseURL
	}
	h.send(private(ownerID, llmURL+"/"))
	keyMsg := private(ownerID, "sk-backup-key-9999")
	keyMsg.MessageID = 4242
	h.send(keyMsg)
	deleted := false
	for _, c := range h.tg.of("deleteMessage") {
		if c.body["message_id"].(float64) == 4242 {
			deleted = true
		}
	}
	if !deleted {
		t.Error("the message with the API key must be deleted")
	}
	h.send(private(ownerID, "model-b"))
	h.wantText("وصل شد")

	ps, _ := h.st.Providers(bg)
	if len(ps) != 2 || ps[1].Name != "backup" || ps[1].APIKey != "sk-backup-key-9999" || ps[1].Model != "model-b" || ps[1].BaseURL != llmURL {
		t.Fatalf("saved provider: %+v", ps)
	}
	h.noText("sk-backup-key-9999") // the key is never echoed back
	h.wantText("sk-…9999")

	// Switch the active provider.
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:activate", ps[1].ID))
	if h.bot.prov.ActiveID() != ps[1].ID {
		t.Fatal("switch active provider")
	}
	h.send(private(40, "سلام"))
	if !strings.Contains(fmt.Sprint(h.llm.last()["model"]), "model-b") {
		t.Errorf("requests must go to the new provider: %v", h.llm.last()["model"])
	}
	// Test button.
	h.tg.reset()
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:test", ps[1].ID))
	h.wantText("جواب داد")
	// Disable, then delete with confirmation.
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:toggle", ps[1].ID))
	if h.bot.prov.ActiveID() == ps[1].ID {
		t.Error("a disabled provider is not used")
	}
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:delete", ps[1].ID))
	h.wantText("حذف بشه؟")
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:deleteok", ps[1].ID))
	if ps, _ := h.st.Providers(bg); len(ps) != 1 {
		t.Error("delete provider")
	}

	// A failing test offers to save anyway.
	h.click(ownerID, ownerID, 9, "a:pv:add")
	h.send(private(ownerID, "broken"))
	h.send(private(ownerID, "http://127.0.0.1:1/v1"))
	h.send(private(ownerID, "-"))
	h.send(private(ownerID, "x"))
	h.wantText("تست ناموفق بود")
	h.click(ownerID, ownerID, 9, "a:pv:savepending")
	if ps, _ := h.st.Providers(bg); len(ps) != 2 {
		t.Error("save anyway")
	}
}

func TestProviderAPITypeAndModelPicker(t *testing.T) {
	h := newHarness(t)
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer sk-list" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"model-z"},{"id":"model-a"}]}`)
	}))
	defer models.Close()
	id, err := h.st.AddProvider(bg, store.Provider{Name: "lister", BaseURL: models.URL + "/v1", APIKey: "sk-list", Model: "old", Priority: 50, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.bot.prov.Reload(bg)
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d", id))
	h.wantText("OpenAI-compatible")
	h.wantText("خودکار از روی آدرس")

	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:models", id))
	h.wantText("مدل‌های lister")
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:pick:0", id))
	if p, _ := h.st.Provider(bg, id); p.Model != "model-a" {
		t.Fatalf("the model list is sorted and picked from: %q", p.Model)
	}

	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:api", id))
	if p, _ := h.st.Provider(bg, id); p.API != "responses" {
		t.Fatalf("api toggle: %q", p.API)
	}
	h.wantText("OpenAI Responses")
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:api", id))
	if p, _ := h.st.Provider(bg, id); p.API != "anthropic" {
		t.Fatalf("api toggle again: %q", p.API)
	}
	h.wantText("Anthropic Messages")
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:api", id))
	if p, _ := h.st.Provider(bg, id); p.API != "openai" {
		t.Fatalf("api toggle back: %q", p.API)
	}
}

func u16(s string) int { return len(utf16.Encode([]rune(s))) }

func TestPremiumEmojiMappingAndUse(t *testing.T) {
	h := newHarness(t)
	h.click(ownerID, ownerID, 9, "a:em:add")
	text := "😀 ⭐\n🔥 5368324170671202286\nسلام"
	m := private(ownerID, text)
	m.Entities = []telegram.MessageEntity{{Type: "custom_emoji", Offset: u16("😀 "), Length: u16("⭐"), CustomEmojiID: "777"}}
	h.send(m)
	h.wantText("2 تا ایموجی ذخیره شد")
	h.wantText("سلام")
	got, _ := h.st.PremiumEmoji(bg)
	if got["😀"] != "777" || got["🔥"] != "5368324170671202286" {
		t.Fatalf("mappings: %v", got)
	}

	// Enabled: answers and buttons use premium emoji.
	h.click(ownerID, ownerID, 9, "a:st:premium:em")
	if !h.st.SettingBool(store.SettingPremiumEmoji, false) {
		t.Fatal("toggle premium emoji")
	}
	h.llm.reply = say("عالیه 🔥 بزن بریم")
	h.tg.reset()
	h.send(private(50, "پروژه تموم شد"))
	rich := h.tg.of("sendRichMessage")
	md := rich[len(rich)-1].body["rich_message"].(map[string]any)["markdown"].(string)
	if !strings.Contains(md, "![🔥](tg://emoji?id=5368324170671202286)") {
		t.Fatalf("answer without premium emoji: %s", md)
	}
	h.tg.reset()
	h.st.SetPremiumEmoji(bg, "📄", "888")
	h.bot.reloadEmoji(bg)
	h.send(private(51, "/memory"))
	if kb := lastMarkup(h); !strings.Contains(kb, "888") {
		t.Errorf("buttons must carry icon_custom_emoji_id: %s", kb)
	}

	// If Telegram refuses custom emoji, plain emoji are sent instead.
	h.tg.reset()
	h.tg.fail["sendRichMessage"] = 400
	h.tg.desc["sendRichMessage"] = "Bad Request: custom emoji not allowed"
	h.send(private(52, "again"))
	if len(h.tg.of("sendMessage")) == 0 {
		t.Error("expected a fallback message")
	}
	delete(h.tg.fail, "sendRichMessage")

	// Delete one and clear all.
	h.click(ownerID, ownerID, 9, "a:em:del")
	h.send(private(ownerID, "😀"))
	if got, _ := h.st.PremiumEmoji(bg); got["😀"] != "" {
		t.Error("delete mapping")
	}
	h.click(ownerID, ownerID, 9, "a:em:clearok")
	if got, _ := h.st.PremiumEmoji(bg); len(got) != 0 {
		t.Error("clear mappings")
	}
}

func TestPremiumEmojiDuplicatesAreSkippedAndCleaned(t *testing.T) {
	h := newHarness(t)
	// Stored mappings: a selector twin is extra; different emojis may share a premium emoji.
	h.st.SetPremiumEmoji(bg, "❤️", "10000")
	h.st.SetPremiumEmoji(bg, "😎", "20000")
	h.st.DB().Exec("UPDATE premium_emoji SET created_at = STRFTIME('%Y-%m-%d %H:%M:%f', created_at, '-1 minute')")
	h.st.SetPremiumEmoji(bg, "❤", "10100")
	h.st.SetPremiumEmoji(bg, "🤩", "20000")
	h.bot.reloadEmoji(bg)
	got, _ := h.st.PremiumEmoji(bg)
	if len(got) != 3 || got["❤️"] != "10000" || got["😎"] != "20000" || got["🤩"] != "20000" {
		t.Fatalf("the oldest mapping of each emoji stays; a shared premium emoji too: %v", got)
	}

	// A message with extras: they are skipped, the rest is added.
	h.click(ownerID, ownerID, 9, "a:em:add")
	h.send(private(ownerID, "🔥 30000\n❤ 40000\n🌟 20000\n🔥 50000\n🎉 60000"))
	h.wantText("2 تا ایموجی ذخیره شد")
	h.wantText("3 تا تکراری بود و ثبت نشد")
	h.wantText("«❤»: از قبل ایموجی پریمیوم داره")
	h.wantText("«🌟»: این ایموجی پریمیوم قبلاً برای 😎 ثبت شده")
	h.wantText("«🔥»: توی همین پیام تکراری بود")
	got, _ = h.st.PremiumEmoji(bg)
	if len(got) != 5 || got["🔥"] != "30000" || got["🎉"] != "60000" || got["❤️"] != "10000" || got["🌟"] != "" {
		t.Fatalf("mappings: %v", got)
	}
	// Only extras: nothing to retry, the dialog ends.
	h.tg.reset()
	h.send(private(ownerID, "🔥 70000"))
	if _, waiting := h.bot.inputs.Load(int64(ownerID)); waiting {
		t.Error("a message of extras only ends the dialog")
	}
}

// The admin sets a provider's temperature in the panel: a number, «-» for
// the default; out of range is refused (Claude takes 0–1). The model
// requests carry it.
func TestProviderTemperature(t *testing.T) {
	h := newHarness(t)
	ps, _ := h.st.Providers(bg)
	id := ps[0].ID
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d", id))
	h.wantText("temperature: پیش‌فرض")
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:temp", id))
	h.send(private(ownerID, "۰٫۷"))
	if p, _ := h.st.Provider(bg, id); p.Temperature == nil || *p.Temperature != 0.7 {
		t.Fatalf("stored: %v", p.Temperature)
	}
	h.wantText("temperature: <b>0.7</b>")
	h.send(private(55, "سلام"))
	if got := h.llm.last()["temperature"]; got != 0.7 {
		t.Errorf("requests carry it: %v", got)
	}

	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:temp", id))
	h.send(private(ownerID, "5"))
	h.wantText("یه عدد بین ۰ و ۲ بفرست")
	h.send(private(ownerID, "0"))
	if p, _ := h.st.Provider(bg, id); p.Temperature == nil || *p.Temperature != 0 {
		t.Fatalf("0 is a temperature: %v", p.Temperature)
	}
	h.click(ownerID, ownerID, 9, fmt.Sprintf("a:pv:%d:temp", id))
	h.send(private(ownerID, "-"))
	if p, _ := h.st.Provider(bg, id); p.Temperature != nil {
		t.Fatalf("«-» is the default: %v", *p.Temperature)
	}
}

// Every section of the panel has one door, and its back button leads to the
// hub that door is in.
func TestAdminPanelOneDoorPerSection(t *testing.T) {
	h := newHarness(t)
	callbacks := func(c tgCall) (doors []string, back string) {
		for _, b := range buttons(c) {
			text, data, _ := strings.Cut(b, " → ")
			if strings.HasPrefix(text, "⬅️") {
				back = data
				continue
			}
			doors = append(doors, data)
		}
		return doors, back
	}
	h.send(private(ownerID, "/admin"))
	home, _ := callbacks(lastOf(h, "sendMessage"))
	hubs := map[string][]string{
		"a:stats": {"a:sys:1h"},
		"a:ai":    {"a:sr", "a:dl", "a:stt", "a:ig", "a:ie", "a:mu", "a:soul", "a:st:steps", "a:st:window", "a:st:ctx"},
		"a:st":    {"a:em", "a:st:trace"},
		"a:pv":    {"a:rt"},
	}
	where := map[string]string{}
	for _, d := range home {
		where[d] = "a:home"
	}
	for hub, want := range hubs {
		if where[hub] != "a:home" {
			t.Errorf("%s has no door on the home screen: %v", hub, home)
		}
		h.click(ownerID, ownerID, 9, hub)
		doors, back := callbacks(lastOf(h, "editMessageText"))
		if back != "a:home" {
			t.Errorf("%s goes back to %q", hub, back)
		}
		for _, d := range doors {
			if d == hub || d == "a:home" || strings.Count(d, ":") > 1 && !strings.HasSuffix(d, ":0") && d != "a:sys:1h" {
				continue // its own actions
			}
			if w, ok := where[d]; ok && w != hub {
				t.Errorf("%s has two doors: in %s and in %s", d, w, hub)
			}
			where[d] = hub
		}
		for _, d := range want {
			if !hasButton(buttons(lastOf(h, "editMessageText")), "→ "+d) {
				t.Errorf("%s lacks %s", hub, d)
			}
		}
	}
	for hub, children := range hubs {
		for _, c := range children {
			if strings.Count(c, ":") > 1 && c != "a:sys:1h" {
				continue // a setting, not a section
			}
			h.click(ownerID, ownerID, 9, c)
			if _, back := callbacks(lastOf(h, "editMessageText")); back != hub {
				t.Errorf("%s goes back to %q, not to %s", c, back, hub)
			}
		}
	}
}
