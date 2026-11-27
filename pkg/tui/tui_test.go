package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Metixpro/macron-agent/pkg/cmdgate"
)

func TestFitInputAndPreservesCursor(t *testing.T) {
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.SetWidth(10)
	ta.SetValue("hello world extra text")
	for ta.Line() > 0 {
		ta.CursorUp()
	}
	ta.SetCursor(0)

	m := model{width: 80, height: 24, input: ta}
	m.fitInput()
	if got, want := m.input.Height(), m.input.LineInfo().Height; got != want {
		t.Fatalf("fitInput height = %d, textarea renders %d lines", got, want)
	}
	if m.input.Line() != 0 || m.input.LineInfo().ColumnOffset != 0 {
		t.Fatalf("cursor moved during fitInput: line=%d col=%d", m.input.Line(), m.input.LineInfo().ColumnOffset)
	}

	m.input.SetValue(strings.Repeat("line\n", 12))
	m.fitInput()
	if got := m.input.Height(); got != 6 {
		t.Fatalf("fitInput clamped height = %d, want 6", got)
	}

	m.input.Reset()
	m.fitInput()
	if got := m.input.Height(); got != 1 {
		t.Fatalf("fitInput empty height = %d, want 1", got)
	}
}

func TestFitInputFollowsWordWrapAndShowsFullDraftAfterResize(t *testing.T) {
	input := textarea.New()
	input.ShowLineNumbers = false
	input.Prompt = ""
	input.SetWidth(10)
	input.Focus()
	input.SetValue("abcdef ghijkl mnopqr")
	m := model{height: 24, input: input}
	m.fitInput()
	if got := m.input.Height(); got != 3 {
		t.Fatalf("fitInput height for word-wrapped draft = %d, want 3", got)
	}

	m.height = 8
	m.input.SetWidth(8)
	m.input.SetValue("first second third fourth")
	m.fitInput()
	_ = m.input.View()
	m.input, _ = m.input.Update(nil)

	m.input.SetWidth(30)
	m.fitInput()
	view := m.input.View()
	if m.input.Height() != 1 || !strings.Contains(view, "first second third fourth") {
		t.Fatalf("resized draft height = %d, view = %q", m.input.Height(), view)
	}
	if info := m.input.LineInfo(); info.StartColumn+info.ColumnOffset != len("first second third fourth") {
		t.Fatalf("caret changed during resize: %+v", info)
	}
}

func TestCleanStreamStripsNotesPreservesCode(t *testing.T) {
	in := "Done.\n<files_sent>a.txt</files_sent>\n<work_notes\npartial"
	if got := cleanStream(in); got != "Done." {
		t.Fatalf("cleanStream = %q, want %q", got, "Done.")
	}
	code := "Keep ```\n<work_notes>x</work_notes>\n```"
	if got := cleanStream(code); got != code {
		t.Fatalf("cleanStream touched code block: %q", got)
	}
}

func TestAskStateNonBlocking(t *testing.T) {
	var nilAsk *askState
	nilAsk.answer(cmdgate.Deny)

	a := &askState{reply: make(chan cmdgate.Choice, 1)}
	a.answer(cmdgate.Once)
	a.answer(cmdgate.Deny) // must not block when buffered reply already sent
	if got := <-a.reply; got != cmdgate.Once {
		t.Fatalf("got %v, want Once", got)
	}
}

func TestCtrlCInterruptsBusyOp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ta := textarea.New()
	m := model{
		busy:   "Compacting",
		cancel: cancel,
		input:  ta,
	}
	next, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	got := next.(model)
	if !got.interrupted || ctx.Err() == nil {
		t.Fatal("ctrl+c did not interrupt active busy operation")
	}
}

func TestAvatarTickStopsWhenIdleWorkspace(t *testing.T) {
	applyTheme(resolveTheme(""))
	m := model{
		home:         false,
		running:      false,
		busy:         "",
		avatarActive: true,
		input:        textarea.New(),
		log:          &transcript{},
		history:      newViewport(),
		histCache:    &historyCache{},
		md:           &markdown{style: markdownStyle()},
	}
	next, cmd := m.Update(avatarTickMsg{})
	gm := next.(model)
	if gm.avatarActive || cmd != nil {
		t.Fatalf("idle workspace kept avatar ticking (active=%v cmd=%v)", gm.avatarActive, cmd != nil)
	}

	gm.busy = "Connecting"
	next, cmd = gm.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	gm = next.(model)
	if !gm.avatarActive || cmd == nil {
		t.Fatal("entering busy state did not restart avatarTick")
	}
}

func TestHistoryViewCachePreservesScroll(t *testing.T) {
	applyTheme(resolveTheme(""))
	m := model{
		width:     80,
		height:    20,
		log:       &transcript{},
		history:   newViewport(),
		histCache: &historyCache{},
		md:        &markdown{style: markdownStyle()},
		follow:    false,
	}
	for i := 0; i < 30; i++ {
		m.log.add(transcriptBlock{text: "message line"})
	}
	_ = m.historyView(6)
	m.history.SetYOffset(4)
	_ = m.historyView(6)
	if m.history.YOffset != 4 {
		t.Fatalf("historyView reset manual scroll offset to %d", m.history.YOffset)
	}
	if m.histCache.text == "" || m.histCache.height != 6 {
		t.Fatalf("historyCache not populated: %+v", *m.histCache)
	}
}

func TestHistoryViewClampsScrolledOffsetAfterGrowing(t *testing.T) {
	m := model{
		width:     80,
		log:       &transcript{},
		history:   newViewport(),
		histCache: &historyCache{},
		md:        &markdown{style: markdownStyle()},
	}
	m.log.add(transcriptBlock{text: "first\nsecond\nthird\nfourth"})
	_ = m.historyView(2)
	m.history.SetYOffset(2)
	_ = m.historyView(4)
	if m.history.YOffset != 0 {
		t.Fatalf("all content fits after resize, scroll offset = %d", m.history.YOffset)
	}
}
