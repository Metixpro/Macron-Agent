package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWebhook(t *testing.T) {
	h := newHarness(t)
	newUser(h, ownerID)
	h.llm.reply = say("رویداد رسید و گزارشش اینه")
	srv := httptest.NewServer(h.bot.webhookHandler())
	t.Cleanup(srv.Close)
	secret := h.bot.secretSetting(bg, settingHookSecret)
	post := func(path, body, ts, sig string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		req.Header.Set("X-Macron-Timestamp", ts)
		req.Header.Set("X-Macron-Signature", sig)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	now := strconv.FormatInt(time.Now().Unix(), 10)
	body := `{"event":"deploy","status":"failed"}`
	if c := post("/hook/ci", body, now, signHook(secret, now, []byte(body))); c != 200 {
		t.Fatalf("a signed event: %d", c)
	}
	h.bot.wg.Wait()
	h.wantText("رویداد رسید")
	if c := post("/hook/ci", body, now, signHook(secret, now, []byte(body))); c != http.StatusConflict {
		t.Errorf("a replay: %d", c)
	}
	if c := post("/hook/ci", body, now, signHook("wrong", now, []byte(body))); c != http.StatusUnauthorized {
		t.Errorf("a bad signature: %d", c)
	}
	old := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	if c := post("/hook/ci", body, old, signHook(secret, old, []byte(body))); c != http.StatusUnauthorized {
		t.Errorf("a stale event: %d", c)
	}
	if c := post("/hook/../etc", body, now, signHook(secret, now, []byte(body))); c != http.StatusNotFound {
		t.Errorf("a bad name: %d", c)
	}

	tasks, _ := h.st.UserBgTasks(bg, ownerID, 5)
	if len(tasks) != 1 || !strings.HasPrefix(tasks[0].Title, "webhook: ci") || !strings.Contains(tasks[0].Goal, "<event>") {
		t.Fatalf("tasks: %+v", tasks)
	}
}

func TestDashboard(t *testing.T) {
	h := newHarness(t)
	srv := httptest.NewServer(h.bot.dashboardHandler())
	t.Cleanup(srv.Close)
	resp, _ := http.Get(srv.URL + "/")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	token := h.bot.secretSetting(bg, settingDashToken)
	resp, err := http.Get(srv.URL + "/?token=" + token)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("with the token: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()
	page := h.bot.dashboardHTML(bg)
	for _, want := range []string{"Providers", "Command gate", "Learning", "Skills"} {
		if !strings.Contains(page, want) {
			t.Errorf("no %s", want)
		}
	}
	for addr, want := range map[string]bool{"127.0.0.1:8788": true, "localhost:1": true, "[::1]:9": true, "0.0.0.0:8788": false, ":8788": false, "10.0.0.5:80": false} {
		if loopback(addr) != want {
			t.Errorf("loopback(%q) != %v", addr, want)
		}
	}
}
