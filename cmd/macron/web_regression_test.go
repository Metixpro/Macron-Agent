package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConcurrentWebSecretInitializationIsStable(t *testing.T) {
	h := newHarness(t)
	start := make(chan struct{})
	values := make(chan string, 32)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; values <- h.bot.secretSetting(bg, settingDashToken) }()
	}
	close(start)
	wg.Wait()
	close(values)
	saved, err := h.st.SecretSetting(settingDashToken)
	if err != nil {
		t.Fatal(err)
	}
	for value := range values {
		if value == "" || value != saved {
			t.Fatal("concurrent requests received different dashboard credentials")
		}
	}
}

func TestWebhookCanRetryAfterTaskWasNotAccepted(t *testing.T) {
	h := newHarness(t)
	newUser(h, ownerID)
	h.llm.reply = say("رویداد دریافت شد")
	secret := h.bot.secretSetting(bg, settingHookSecret)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	body := `{"event":"retry"}`
	handler := h.bot.webhookHandler()
	post := func() int {
		r := httptest.NewRequest(http.MethodPost, "/hook/test", strings.NewReader(body))
		r.Header.Set("X-Macron-Timestamp", ts)
		r.Header.Set("X-Macron-Signature", signHook(secret, ts, []byte(body)))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	owners := h.bot.owner
	h.bot.owner = nil
	if code := post(); code != http.StatusServiceUnavailable {
		t.Fatalf("without owner: %d", code)
	}
	h.bot.owner = owners
	if code := post(); code != http.StatusOK {
		t.Fatalf("retry of unaccepted task: %d", code)
	}
	h.bot.wg.Wait()
	if code := post(); code != http.StatusConflict {
		t.Fatalf("accepted event replay: %d", code)
	}
}
