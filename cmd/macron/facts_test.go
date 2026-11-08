package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
)

func TestLongTermMemory(t *testing.T) {
	h := newHarness(t)
	const uid = 81
	ctx := context.Background()
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if lastRole(req) == "user" && strings.Contains(llmText(req), "گیاهخوارم") {
			return map[string]any{"role": "assistant", "tool_calls": []any{
				map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": toolRemember, "arguments": `{"fact":"Vegetarian"}`}},
				map[string]any{"id": "c2", "type": "function", "function": map[string]any{"name": toolRemember, "arguments": `{"fact":"Card number 6037 9911 2233 4455"}`}},
			}}, 0
		}
		return map[string]any{"role": "assistant", "content": "حتماً 🌱"}, 0
	}
	h.send(private(uid, "یادت باشه من گیاهخوارم"))
	facts, _ := h.bot.brain.User.List()
	if len(facts) != 1 || facts[0].Text != "Vegetarian" {
		t.Fatalf("facts: %+v", facts)
	}
	if raw, _ := os.ReadFile(h.bot.brain.User.Path); !strings.Contains(string(raw), "- Vegetarian") {
		t.Errorf("the fact is a line of USER.md:\n%s", raw)
	}
	if !strings.Contains(llmText(h.llm.last()), "looks like a secret") {
		t.Error("a card number is never saved")
	}

	h.send(private(uid, "/new"))
	h.send(private(uid, "یه غذا برای شام پیشنهاد بده"))
	if req := llmText(h.llm.last()); !strings.Contains(req, fmt.Sprintf("[user #%d] Vegetarian", facts[0].ID)) {
		t.Errorf("the model knows what it remembers: %s", req[:min(len(req), 400)])
	}

	h.tg.reset()
	h.send(private(uid, "/memory"))
	panel := lastOf(h, "sendMessage")
	if !strings.Contains(panel.body["text"].(string), "Vegetarian") {
		t.Errorf("panel: %s", panel.body["text"])
	}
	del := fmt.Sprintf("m:d:%d:u:%d:0", uid, facts[0].ID)
	if btns := buttons(panel); !hasButton(btns, "🗑 ۱ → "+del) || !hasButton(btns, "m:t:") || !hasButton(btns, "m:c:") {
		t.Errorf("panel buttons: %v", btns)
	}
	h.click(99, uid, 900, del)
	if f, _ := h.bot.brain.User.List(); len(f) != 1 {
		t.Error("only the owner can delete")
	}
	h.click(uid, uid, 900, del)
	if f, _ := h.bot.brain.User.List(); len(f) != 0 {
		t.Error("the fact is deleted")
	}

	_, _, _ = h.bot.brain.User.Add("Likes jazz")
	h.click(uid, uid, 900, fmt.Sprintf("m:t:%d:0", uid))
	if v, _ := h.st.UserPref(ctx, uid, store.PrefFactsOff); v == "" {
		t.Fatal("memory turned off")
	}
	h.send(private(uid, "یه سوال دارم"))
	if req := h.llm.last(); strings.Contains(llmText(req), "Likes jazz") || strings.Contains(toolNames(req), ","+toolRemember+",") {
		t.Error("a switched-off memory is neither used nor written")
	}
	h.click(uid, uid, 900, fmt.Sprintf("m:t:%d:0", uid))
	h.send(private(uid, "یه سوال دارم"))
	if req := h.llm.last(); !strings.Contains(llmText(req), "Likes jazz") || !strings.Contains(toolNames(req), ","+toolRemember+",") {
		t.Error("turned back on")
	}

	h.send(group(uid, "مکرون سلام"))
	if !strings.Contains(llmText(h.llm.last()), "never reveal private ones") {
		t.Error("group note")
	}
}

func TestLooksSecret(t *testing.T) {
	for s, want := range map[string]bool{
		"Card number 6037-9911-2233-4455": true, "رمز وای‌فای خونه 1234": true, "شماره ملی ۰۰۱۲۳۴۵۶۷۸": true,
		"Born in 1998": false, "Has 2 cats": false, "Phone brand is Samsung": false,
	} {
		if got := looksSecret(s); got != want {
			t.Errorf("looksSecret(%q) = %v", s, got)
		}
	}
}

func TestMemoryFilesAreEditedByHandAndByTheAgent(t *testing.T) {
	h := newHarness(t)
	const uid = 82
	ctx := context.Background()
	if err := os.WriteFile(h.bot.brain.User.Path, []byte("# my notes\n\n- Lives in Mashhad\nA comment of mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := h.bot.factsAbout(ctx, uid, false); !strings.Contains(got, "[user #1] Lives in Mashhad") {
		t.Fatalf("a hand edit is seen: %q", got)
	}
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if lastRole(req) == "user" && strings.Contains(llmText(req), "دیپلوی") {
			return map[string]any{"role": "assistant", "tool_calls": []any{
				map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": toolRemember, "arguments": `{"fact":"Deploys with make ship","about":"work"}`}},
			}}, 0
		}
		return map[string]any{"role": "assistant", "content": "باشه"}, 0
	}
	h.send(private(uid, "یادت باشه دیپلوی با make ship انجام میشه"))
	work, _ := h.bot.brain.Memory.List()
	if len(work) != 1 || work[0].Text != "Deploys with make ship" {
		t.Fatalf("MEMORY.md: %+v", work)
	}
	if users, _ := h.bot.brain.User.List(); len(users) != 1 {
		t.Errorf("USER.md is not touched by a note about work: %+v", users)
	}
	if raw, _ := os.ReadFile(h.bot.brain.User.Path); !strings.Contains(string(raw), "A comment of mine") {
		t.Error("the owner's own lines survive")
	}
	h.tg.reset()
	h.send(private(uid, "/memory"))
	text := lastOf(h, "sendMessage").body["text"].(string)
	if !strings.Contains(text, "Lives in Mashhad") || !strings.Contains(text, "Deploys with make ship") || !strings.Contains(text, "MEMORY.md") {
		t.Errorf("the panel shows both files: %s", text)
	}
	del := fmt.Sprintf("m:d:%d:w:1:0", uid)
	h.click(uid, uid, 901, del)
	if w, _ := h.bot.brain.Memory.List(); len(w) != 0 {
		t.Error("a note about work is deleted from the panel")
	}

	h.tg.reset()
	h.click(uid, uid, 901, fmt.Sprintf("m:f:%d", uid))
	docs := h.tg.of("sendDocument")
	var names []string
	for _, c := range docs {
		names = append(names, fmt.Sprint(c.body["document_name"]))
	}
	if len(docs) != 3 || names[0] != "SOUL.md" || names[1] != "USER.md" || names[2] != "MEMORY.md" {
		t.Errorf("SOUL.md, USER.md and MEMORY.md are sent: %v", names)
	}
}

func TestPastChatsAreSearchable(t *testing.T) {
	h := newHarness(t)
	const uid = 83
	h.llm.reply = say("سرور هتزنر رو با دستور passwd عوض می‌کنی")
	h.send(private(uid, "رمز سرور هتزنر رو چطور عوض کنم؟"))
	h.send(private(uid, "/new"))
	hits, err := h.st.SearchArchive(context.Background(), uid, "هتزنر", time.Time{}, 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("the exchange is in the archive after /new: %+v %v", hits, err)
	}
	tool := h.bot.recallTool(&store.User{ID: uid})
	out, err := tool.Handler(context.Background(), map[string]any{"query": "هتزنر"})
	if err != nil || !strings.Contains(out.Text, "هتزنر") || !strings.Contains(out.Text, "owner:") {
		t.Fatalf("search_past_chats: %q %v", out.Text, err)
	}
	if out, _ := tool.Handler(context.Background(), map[string]any{"query": "کلمه‌ای که هیچ‌وقت نبوده"}); !strings.Contains(out.Text, "No earlier message") {
		t.Errorf("no match: %q", out.Text)
	}
}
