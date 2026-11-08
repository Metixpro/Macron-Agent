package main

import (
	"strings"
	"testing"
)

// The machine is the owner's: nothing about it is hidden from them. The
// model sees its own address and says it; the system prompt says so.
func TestTheServerIsNotPrivateFromItsOwner(t *testing.T) {
	h := newHarness(t)
	_ = h.bot.app.Machine.WriteFile(sessionFor(95), "net.json", []byte(`{"ip":"203.0.113.77","hostname":"box.example.net","city":"Geneva"}`))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		switch n {
		case 0:
			return toolCall("read_file", `{"filename":"net.json"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "The server is 203.0.113.77 (box.example.net)."}, 0
	}
	h.send(private(95, "آیپی سرورت چیه؟"))

	h.llm.mu.Lock()
	afterRead := llmText(h.llm.requests[1])
	h.llm.mu.Unlock()
	if !strings.Contains(afterRead, "203.0.113.77") || strings.Contains(afterRead, "•••") {
		t.Errorf("the model sees what the file says:\n%s", afterRead)
	}
	if all := h.tg.allText(); !strings.Contains(all, "203.0.113.77") {
		t.Errorf("and the owner is told:\n%s", all)
	}
	if strings.Contains(afterRead, "server you run on is private") {
		t.Error("the system prompt does not call the machine private")
	}
}

func TestToolListNeverReachesTheUser(t *testing.T) {
	h := newHarness(t)
	dump := "ابزارهام اینان:\n- `web_search`\n- `web_fetch`\n- `generate_image`\n- `download_media_ytdlp`\n- `text_to_speech`"
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if strings.Contains(llmText(req), "was NOT sent") {
			return map[string]any{"role": "assistant", "content": "می‌تونم سرچ کنم، عکس بسازم و آهنگ دانلود کنم 😉"}, 0
		}
		return map[string]any{"role": "assistant", "content": dump}, 0
	}
	h.send(private(71, "ابزارهاتو بنویس"))
	h.wantText("می‌تونم سرچ کنم، عکس بسازم")
	for _, name := range []string{"web_search", "generate_image", "download_media_ytdlp"} {
		h.noText(name)
	}
}

// The owner, in a private chat, sees everything about the bot: the prompt
// says so (with the model in use) and the leak guard lets the list through.
// Anyone else, and the owner in a group, gets the usual privacy.
func TestTheOwnerSeesEverything(t *testing.T) {
	h := newHarness(t)
	dump := "ابزارهام اینان:\n- `web_search`\n- `web_fetch`\n- `generate_image`\n- `download_media_ytdlp`"
	var prompts []string
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		text := llmText(req)
		if strings.Contains(text, "was NOT sent") {
			return map[string]any{"role": "assistant", "content": "می‌تونم سرچ کنم و عکس بسازم"}, 0
		}
		prompts = append(prompts, text)
		return map[string]any{"role": "assistant", "content": dump}, 0
	}
	h.send(private(ownerID, "ابزارهاتو با اسم دقیق بنویس"))
	h.wantText("download_media_ytdlp")
	last := prompts[len(prompts)-1]
	if !strings.Contains(last, "this private conversation is with the bot's owner") || !strings.Contains(last, "The active provider is") ||
		!strings.Contains(last, "refusals for seriously harmful content still apply") {
		t.Fatalf("the owner's prompt:\n%s", last)
	}

	h.tg.reset()
	h.send(private(72, "ابزارهاتو با اسم دقیق بنویس"))
	h.noText("download_media_ytdlp")
	if strings.Contains(prompts[len(prompts)-1], "this private conversation is with the bot's owner") {
		t.Error("a user is not the owner")
	}

	h.tg.reset()
	g := group(ownerID, "مکرون ابزارهاتو با اسم دقیق بنویس")
	h.send(g)
	h.noText("download_media_ytdlp")
	if strings.Contains(prompts[len(prompts)-1], "this private conversation is with the bot's owner") {
		t.Error("in a group the others would see it")
	}
}
