package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPictureMakingKeyInThePanel(t *testing.T) {
	h := newHarness(t)
	var asked atomic.Int32
	studio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["api_key"] != "studio-secret-key" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"success":false,"error":"bad key"}`)
			return
		}
		fmt.Fprint(w, `{"success":true,"total":2,"data":[{"id":"anime","name":"انیمه"},{"id":"neon","name":"نئون"}]}`)
	}))
	defer studio.Close()
	h.bot.app.Config.ImageAPI = studio.URL
	h.bot.app.Registry.SetImageAPI(studio.URL)
	h.bot.app.Registry.HTTP = &http.Client{}

	h.click(ownerID, ownerID, 900, "a:ig")
	if p := lastOf(h, "editMessageText"); !strings.Contains(fmt.Sprint(p.body["text"]), "تنظیم نشده") || !hasButton(buttons(p), "a:ig:key") || hasButton(buttons(p), "a:ig:del") {
		t.Fatalf("the panel without a key: %v", p.body["text"])
	}
	if h.bot.app.Registry.StudioReady() {
		t.Fatal("no key yet")
	}

	h.click(ownerID, ownerID, 900, "a:ig:key")
	h.tg.reset()
	bad := private(ownerID, "wrong-key")
	bad.MessageID = 4200
	h.send(bad)
	h.wantText("قبول نکرد")
	if h.bot.app.Registry.StudioReady() {
		t.Fatal("a refused key is not kept")
	}

	h.click(ownerID, ownerID, 900, "a:ig:key")
	h.tg.reset()
	good := private(ownerID, "studio-secret-key")
	good.MessageID = 4201
	h.send(good)
	h.wantText("ذخیره شد")
	if del := h.tg.of("deleteMessage"); len(del) == 0 || del[0].body["message_id"].(float64) != 4201 {
		t.Error("the message with the key is deleted")
	}
	var raw string
	if err := h.st.DB().QueryRow("SELECT v FROM settings WHERE k = ?", settingImageKey).Scan(&raw); err != nil || raw == "" || strings.Contains(raw, "studio-secret-key") {
		t.Errorf("the key is stored encrypted: %q %v", raw, err)
	}
	if !h.bot.app.Registry.StudioReady() || h.bot.app.Registry.ImageKey() != "studio-secret-key" {
		t.Fatal("the studio is ready at once")
	}
	for _, s := range h.tg.texts() {
		if strings.Contains(s, "studio-secret-key") {
			t.Fatalf("the key shows in the chat: %s", s)
		}
	}

	h.tg.reset()
	h.click(ownerID, ownerID, 900, "a:ig:test")
	h.wantText("کلید معتبره")
	h.tg.reset()
	h.click(ownerID, ownerID, 900, "a:ig:styles")
	h.wantText("neon")
	h.click(ownerID, ownerID, 900, "a:ig:del")
	if h.bot.app.Registry.StudioReady() {
		t.Fatal("deleted")
	}
}

func TestPictureMakingKeyFromTheEnvironment(t *testing.T) {
	h := newHarness(t)
	key := imageGenKey(h.st, "env-key")
	if key() != "env-key" {
		t.Fatal("the environment's key is used without one in the panel")
	}
	if err := h.st.SetSecretSetting(context.Background(), settingImageKey, "panel-key"); err != nil {
		t.Fatal(err)
	}
	if key() != "panel-key" {
		t.Fatal("the panel's key wins")
	}
}
