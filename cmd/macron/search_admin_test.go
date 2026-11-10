package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/netclient"
)

func TestTheTregTokenFromThePanel(t *testing.T) {
	h := newHarness(t)
	var searches []string
	treg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Treg-Token") != "treg-good-token" {
			w.WriteHeader(401)
			w.Write([]byte(`{"detail":"not authenticated"}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		searches = append(searches, body["q"].(string))
		w.Header().Set("X-Treg-Served-By", "tinyfish.web.search")
		w.Write([]byte(`{"output":{"results":[{"title":"Telegram Bot API","url":"https://core.telegram.org/bots/api","snippet":"The Bot API"}]},"_treg":{"served_by":"tinyfish.web.search"}}`))
	}))
	t.Cleanup(treg.Close)
	h.bot.app.Search.Treg.BaseURL = treg.URL
	h.bot.app.Search.HTTPClient = netclient.NewClient(netclient.Options{})

	h.click(ownerID, ownerID, 900, "a:ai")
	if !hasButton(buttons(lastOf(h, "editMessageText")), "a:sr") {
		t.Fatal("the agent's tools have a web search page")
	}
	h.click(ownerID, ownerID, 900, "a:sr")
	if v := lastOf(h, "editMessageText").body["text"].(string); !strings.Contains(v, "treg.to") || !strings.Contains(v, "خاموش") {
		t.Fatalf("off without a token: %s", v)
	}

	h.click(ownerID, ownerID, 900, "a:sr:token")
	bad := private(ownerID, "treg-bad-token")
	bad.MessageID = 5001
	h.send(bad)
	h.wantText("توکن رو قبول نکرد")
	if h.bot.app.Search.Treg.Token() != "" {
		t.Fatal("a refused token is not saved")
	}

	h.click(ownerID, ownerID, 900, "a:sr:token")
	good := private(ownerID, "treg-good-token")
	good.MessageID = 5002
	h.send(good)
	h.wantText("توکن treg ذخیره شد")
	if h.bot.app.Search.Treg.Token() != "treg-good-token" {
		t.Fatal("the panel's token is used")
	}
	for _, id := range []float64{5001, 5002} {
		deleted := false
		for _, c := range h.tg.of("deleteMessage") {
			deleted = deleted || c.body["message_id"].(float64) == id
		}
		if !deleted {
			t.Errorf("the message with a token (%v) must be deleted", id)
		}
	}
	var raw string
	_ = h.st.DB().QueryRow("SELECT v FROM settings WHERE k = ?", settingTregToken).Scan(&raw)
	if raw == "" || strings.Contains(raw, "treg-good-token") {
		t.Errorf("the token is stored encrypted: %q", raw)
	}

	res, err := h.bot.app.Search.Search(context.Background(), "macron", 3)
	if err != nil || len(res) != 1 || res[0].URL != "https://core.telegram.org/bots/api" || searches[len(searches)-1] != "macron" {
		t.Fatalf("%v %+v %v", err, res, searches)
	}
	if strings.Contains(h.bot.redact("token treg-good-token here"), "treg-good-token") {
		t.Error("the token never reaches a chat")
	}

	h.click(ownerID, ownerID, 900, "a:sr:del")
	if h.bot.app.Search.Treg.Token() != "" {
		t.Error("deleted")
	}
}
