package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
)

func TestLiveModelSwitchesFromCompletedTaskToAPK(t *testing.T) {
	endpoint, key, model := os.Getenv("WAZE_TEST_LLM_URL"), os.Getenv("WAZE_TEST_LLM_KEY"), os.Getenv("WAZE_TEST_LLM_MODEL")
	if endpoint == "" || key == "" || model == "" {
		t.Skip("live model credentials not configured")
	}
	h := newHarness(t)
	proxy := &labProxy{target: strings.TrimRight(endpoint, "/"), key: key}
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	providers, err := h.st.Providers(bg)
	if err != nil || len(providers) == 0 {
		t.Fatal("test provider unavailable", err)
	}
	provider := providers[0]
	provider.BaseURL = server.URL + "/v1"
	provider.APIKey = "sk-local-test-proxy"
	provider.Model = model
	provider.Name = "task-context-live"
	if err := h.st.UpdateProvider(bg, provider); err != nil {
		t.Fatal(err)
	}
	if err := h.bot.prov.Reload(bg); err != nil {
		t.Fatal(err)
	}
	h.bot.runTimeout = 4 * time.Minute
	_ = h.st.SetSetting(bg, store.SettingAgentSteps, "20")
	h.send(private(ownerID, "فقط داخل فضای کاری یک اسکریپت hello.sh بساز که Hello چاپ کند، اجراش کن و نتیجه را بگو. هیچ بسته سیستمی نصب نکن."))
	next := private(ownerID, "یه اپلیکیشن دارم میخوام فایلش رو بررسی کنی؛ الان فایل رو میفرستم.")
	next.MessageID = 88
	h.send(next)
	proxy.mu.Lock()
	start := len(proxy.raw)
	proxy.mu.Unlock()
	h.tg.reset()
	h.send(apkMessage(t, h, ownerID, 90, "این فایل APK رو همین الان باز کن، فهرست فایل های داخلش و نام پکیج اپ را بگو. فقط بررسی کن؛ اپ را نصب یا اجرا نکن."))
	proxy.mu.Lock()
	raw := append([]string(nil), proxy.raw[start:]...)
	proxy.mu.Unlock()
	inspected := false
	for _, wire := range raw {
		request, _, _ := strings.Cut(wire, "\n\n>>> REPLY\n")
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal([]byte(request), &body) != nil {
			continue
		}
		for _, message := range body.Messages {
			if message.Role != "tool" {
				continue
			}
			output := fmt.Sprint(message.Content)
			if strings.Contains(output, "AndroidManifest.xml") && strings.Contains(output, "classes.dex") {
				inspected = true
			}
		}
	}
	if !inspected {
		t.Fatalf("live model did not inspect the current APK; visible response: %s", strings.Join(shown(h), "\n"))
	}
	if memory, err := h.bot.mem.Load(bg, ownerID); err != nil || memory.Messages < 6 {
		t.Fatal("task switch reset the conversation history", err)
	}
	t.Logf("live model %s inspected APK after a completed task and a topic change; API requests for APK: %d", model, len(raw))
	t.Logf("APK answer: %s", strings.Join(shown(h), "\n"))
}
