package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

func TestTraceShowsSubAgentsAsBranches(t *testing.T) {
	now := time.Now()
	var tr trace
	music := tr.subStart("Music search & download", now)
	tr.start(0, "generate_image", now)
	tr.start(music, "search_music", now)
	tr.end(music, "search_music", false)
	tr.start(music, tools.ToolDownloadMedia, now)
	tr.end(0, "generate_image", false)
	tr.end(music, tools.ToolDownloadMedia, false)
	tr.start(music, toolSendFile, now)
	tr.end(music, toolSendFile, false)
	tr.subEnd(music, false)
	tr.start(0, toolSendFile, now)
	tr.end(0, toolSendFile, false)
	want := traceTitle + " · step 5 · 1:20\n" + traceRule + `
✓ Sub-agent: Music search & download (3 steps)
  ├─ ✓ Music Search
  ├─ ✓ Download
  └─ ✓ Attach File
✓ Generate Image
✓ Attach File`
	if out := tr.render(now.Add(80*time.Second), now, false); out != want {
		t.Fatalf("render:\n%s\nwant:\n%s", out, want)
	}

	var two trace
	a, b := two.subStart("Research A", now), two.subStart("Research B", now)
	two.start(a, "web_search", now)
	two.start(b, "web_fetch", now)
	two.start(a, "web_fetch", now)
	two.end(b, "web_fetch", true)
	two.subEnd(b, true)
	out := two.render(now.Add(5*time.Second), now, false)
	wantTwo := `◆ Sub-agent: Research A …
  ├─ ◆ Search …
  └─ ◆ Read Page …
✗ Sub-agent: Research B (1 step)
  └─ ✗ Read Page`
	if !strings.HasSuffix(out, wantTwo) {
		t.Fatalf("parallel sub-agents:\n%s\nwant the end:\n%s", out, wantTwo)
	}

	var long trace
	s := long.subStart("Many files", now)
	for _, name := range []string{"read_file", "write_file", "read_file", "write_file", "read_file", "write_file", "read_file"} {
		long.start(s, name, now)
		long.end(s, name, false)
	}
	out = long.render(now, now, false)
	if !strings.Contains(out, "  ├─ … +3\n") || strings.Count(out, "─ ✓") != traceBranch-1 || !strings.Contains(out, "  └─ ✓ Read File") {
		t.Fatalf("a long branch:\n%s", out)
	}
}

func TestCompactionIsAnEventInTheTerminal(t *testing.T) {
	now := time.Now()
	l := &liveView{terminal: true}
	l.context(160000, 256000)
	l.compacted(160000, 80000, 256000, true)
	l.compacted(150000, 118000, 256000, false)
	out := l.tr.render(now, now, false)
	for _, want := range []string{"🗜️ Context compacted: 160k ➔ 80k (-50%)", "🗜️ Old outputs trimmed: 150k ➔ 118k (-21%)", "ctx ▰▰▰▰▱▱▱▱ 46% 118k/256k"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestFilesOfOverlappingCallsGoToTheirOwner(t *testing.T) {
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ft := newFileTracker(sb, "s")
	ft.start("delegate_task")
	ft.start("generate_image")
	_ = sb.WriteFile("s", "song.mp3", []byte("la"))
	_ = sb.WriteFile("s", "sunset.png", []byte("img"))
	_ = sb.WriteFile("s", "data.png", []byte("scratch"))
	ft.end("generate_image", "Image created and queued for delivery.\n- File: sunset.png (1 KB)")
	ft.end("delegate_task", "Sub-agent report (3 steps): done")
	if got := strings.Join(ft.made(), ","); got != "sunset.png" {
		t.Fatalf("a shared window gives the picture tool only the file its result names: %s", got)
	}
	if got := strings.Join(ft.deliveries([]string{"song.mp3"}), ","); got != "song.mp3" {
		t.Fatalf("the song the sub-agent sent: %s", got)
	}
	if !names("- File: out/a.png", "out/a.png") || names("- File: data.png", "a.png") {
		t.Fatal("names matches whole file names")
	}
}

func TestSubAgentShowsAsABranchInTheLiveTerminal(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		switch n {
		case 0:
			return toolCall("delegate_task", `{"title":"Notes","goal":"write notes.md"}`), 0
		case 1:
			return toolCall("write_file", `{"filename":"notes.md","content":"- Go"}`), 0
		case 2:
			time.Sleep(700 * time.Millisecond)
			return map[string]any{"role": "assistant", "content": "notes.md written"}, 0
		}
		time.Sleep(700 * time.Millisecond)
		return map[string]any{"role": "assistant", "content": "یادداشت‌ها آماده‌ست."}, 0
	}
	h.send(private(20, "یه یادداشت درباره Go بنویس"))
	all := liveFrames(h)
	joined := strings.Join(all, "\n=====\n")
	for _, want := range []string{"Sub-agent: Notes", "└─ ✓ Write File"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the terminal misses %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "Sub-agent ✅") || strings.Contains(joined, "**Sub-agent**") {
		t.Errorf("the delegate call itself is not a second line:\n%s", joined)
	}
	h.wantText("یادداشت‌ها آماده‌ست.")
}
