package main

import (
	"context"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/tools"
)

// A sub-agent's send_file is honoured once its workspace is handed over:
// the file reaches the chat under its name in the main workspace.
func TestASubAgentsFileReachesTheChat(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		switch n {
		case 0:
			return toolCall("delegate_task", `{"goal":"write a short poem into poem.md and send it to the user","toolsets":["documents"]}`), 0
		case 1:
			return toolCall("write_file", `{"filename":"poem.md","content":"گل‌ها و باران"}`), 0
		case 2:
			return toolCall(toolSendFile, `{"filename":"poem.md","caption":"یه شعر کوتاه"}`), 0
		case 3:
			return map[string]any{"role": "assistant", "content": "Wrote poem.md and sent it."}, 0
		}
		return map[string]any{"role": "assistant", "content": "شعرت آماده‌ست."}, 0
	}
	h.send(private(62, "یه شعر بنویس و بفرست"))
	rich := h.tg.of("sendRichMessage")
	if len(rich) != 1 || rich[0].body["m0_name"] != "poem.md" {
		t.Fatalf("the sub-agent's file goes with the answer: %v", rich)
	}
	if _, err := h.bot.app.Machine.Stat(sessionFor(62), "poem.md"); err != nil {
		t.Fatal("it is in the user's workspace")
	}
}

// What a sub-agent's picture or music tool made counts as the agent's own
// once handed over; its other files stay work in progress.
func TestHandedOverFilesKeepWhatMadeThem(t *testing.T) {
	h := newHarness(t)
	sb := h.bot.app.Machine
	main, sub := newFileTracker(sb, "s"), newFileTracker(sb, "s::d1")
	sub.auto, sub.scratch = []string{"song.mp3"}, []string{"notes.txt"}
	_ = sb.WriteFile("s", "song-2.mp3", []byte("mp3"))
	_ = sb.WriteFile("s", "notes.txt", []byte("n"))
	main.adopt(sub, map[string]string{"song.mp3": "song-2.mp3", "notes.txt": "notes.txt"})
	if got := main.made(); len(got) != 1 || got[0] != "song-2.mp3" {
		t.Fatalf("made: %v", got)
	}
}

// The background check rests a cookie whose account was refused, and leaves
// one that failed for another reason (the network, the video) alone.
func TestTheBackgroundCheckRestsOnlyRefusedCookies(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	line := ".youtube.com\tTRUE\t/\tTRUE\t0\tSID\t"
	dead, _ := h.st.AddCookie(ctx, "dead", []string{"youtube.com"}, line+"dead\n")
	flaky, _ := h.st.AddCookie(ctx, "flaky", []string{"youtube.com"}, line+"flaky\n")
	good, _ := h.st.AddCookie(ctx, "good", []string{"youtube.com"}, line+"good\n")
	test := func(_ context.Context, data, host string) tools.CookieTest {
		switch {
		case strings.Contains(data, "dead"):
			return tools.CookieTest{Live: true, Refused: true, Detail: "Sign in to confirm you're not a bot"}
		case strings.Contains(data, "flaky"):
			return tools.CookieTest{Live: true, Detail: "connection reset by peer"}
		}
		return tools.CookieTest{OK: true, Live: true, Detail: "YouTube Turns 20"}
	}
	if n := h.bot.cookies.checkAll(ctx, test); n != 1 {
		t.Fatalf("rested %d", n)
	}
	now := h.st.Now()
	for id, want := range map[int64]bool{dead: true, flaky: false, good: false} {
		c, _ := h.st.Cookie(ctx, id)
		if c.Resting(now) != want {
			t.Errorf("cookie %d resting: %v", id, c.Resting(now))
		}
	}
	if n := h.bot.cookies.checkAll(ctx, test); n != 0 {
		t.Errorf("a resting cookie is not tried again: %d", n)
	}
}
