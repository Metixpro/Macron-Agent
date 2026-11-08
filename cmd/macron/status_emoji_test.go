package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func TestTheBotMakesItsOwnStatusEmoji(t *testing.T) {
	h := newHarness(t)
	h.click(ownerID, ownerID, 1, "a:em:make")
	made := h.tg.of("createNewStickerSet")
	if len(made) != 1 {
		t.Fatalf("createNewStickerSet calls: %d\n%s", len(made), h.tg.allText())
	}
	c := made[0].body
	if c["name"] != "status_v2_by_WazeBot" || c["sticker_type"] != "custom_emoji" || c["needs_repainting"] != "false" ||
		c["user_id"] != fmt.Sprint(ownerID) || c["emoji0_name"] != "emoji0.tgs" || c["emoji1_name"] != "emoji1.tgs" {
		t.Fatalf("the set: %v", c)
	}
	var stickers []map[string]any
	_ = json.Unmarshal([]byte(c["stickers"].(string)), &stickers)
	if len(stickers) != 2 || stickers[0]["format"] != "animated" || stickers[0]["sticker"] != "attach://emoji0" ||
		fmt.Sprint(stickers[1]["emoji_list"]) != "[🎧]" {
		t.Fatalf("stickers: %v", stickers)
	}
	for key, want := range map[string]string{"listen": "7000", "transcribe": "7001"} {
		if id, _ := h.st.Setting(settingStatusEmoji + key); id != want {
			t.Errorf("slot %s = %q, want %s", key, id, want)
		}
	}
	h.wantText("t.me/addemoji/status_v2_by_WazeBot")
	h.wantText("ایموجی پریمیوم خاموشه")
	h.wantText(`<tg-emoji emoji-id="7000">🎙</tg-emoji> دارم می‌شنوم…`)

	// The set is made once; the next time it is found.
	h.click(ownerID, ownerID, 1, "a:em:make")
	if n := len(h.tg.of("createNewStickerSet")); n != 1 {
		t.Errorf("made %d times", n)
	}
}

func TestAStatusSlotTakesAnyPremiumEmoji(t *testing.T) {
	h := newHarness(t)
	h.click(ownerID, ownerID, 1, "a:em:slot:transcribe")
	m := private(ownerID, "⭐")
	m.Entities = []telegram.MessageEntity{{Type: "custom_emoji", Offset: 0, Length: 1, CustomEmojiID: "5368"}}
	h.send(m)
	if id, _ := h.st.Setting(settingStatusEmoji + "transcribe"); id != "5368" {
		t.Fatalf("slot = %q\n%s", id, h.tg.allText())
	}
	h.click(ownerID, ownerID, 1, "a:em:slot:transcribe")
	h.send(private(ownerID, "0"))
	if id, ok := h.st.Setting(settingStatusEmoji + "transcribe"); ok || id != "" {
		t.Errorf("0 empties the slot: %q", id)
	}
}

// The voice card's «listening» wears its own premium emoji; 🎙 anywhere
// else keeps the general mapping.
func TestAStatusWearsItsOwnEmoji(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_ = h.st.SetSetting(ctx, store.SettingPremiumEmoji, "true")
	_ = h.st.SetPremiumEmoji(ctx, "🎙", "111")
	h.bot.reloadEmoji(ctx)
	_ = h.st.SetSetting(ctx, settingStatusEmoji+"listen", "999")
	stt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(900 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "سلام"})
	}))
	defer stt.Close()
	raw, _ := json.Marshal(sttSetting{URL: stt.URL + "/v1"})
	_ = h.st.SetSecretSetting(ctx, settingSTT, string(raw))
	h.tg.files["ov"] = []byte("OggS own voice")
	m := private(33, "")
	m.MessageID, m.Voice = 701, &telegram.Voice{FileID: "ov", Duration: 2}
	h.send(m)
	var drafts []string
	for _, c := range h.tg.of("sendRichMessageDraft") {
		if rm, ok := c.body["rich_message"].(map[string]any); ok {
			drafts = append(drafts, fmt.Sprint(rm["html"]))
		}
	}
	all := strings.Join(drafts, "\n")
	if !strings.Contains(all, `<tg-thinking><tg-emoji emoji-id="999">🎙</tg-emoji> دارم می‌شنوم…</tg-thinking>`) || strings.Contains(all, `"111"`) {
		t.Fatalf("drafts:\n%s", all)
	}
	if got := h.bot.premium().HTML("🎙 صدا به متن"); !strings.Contains(got, `emoji-id="111"`) {
		t.Errorf("elsewhere 🎙 keeps the general mapping: %s", got)
	}
}
