package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type transcriptBlock struct {
	kind, text, name, args string
	shown                  string
	shownWidth             int
}
type transcript struct {
	blocks                          []transcriptBlock
	width, version, renderedVersion int
	cached                          string
}

func (t *transcript) add(b transcriptBlock) {
	t.blocks = append(t.blocks, b)
	if len(t.blocks) > 1500 {
		t.blocks = t.blocks[len(t.blocks)-1500:]
	}
	t.version++
}

func (t *transcript) invalidate() {
	for i := range t.blocks {
		t.blocks[i].shownWidth = 0
	}
	t.version++
}

func (t *transcript) render(md *markdown, width int) string {
	if t.width == width && t.renderedVersion == t.version {
		return t.cached
	}
	parts := make([]string, len(t.blocks))
	for i := range t.blocks {
		b := &t.blocks[i]
		if b.shownWidth != width {
			var s string
			switch b.kind {
			case "user":
				s = userBlock(b.text, width)
			case "assistant":
				s = withBullet(md.render(b.text, width), "●")
			case "tool":
				s = toolBlock(b.name, b.args, b.text, width)
			default:
				s = ansi.Wrap(b.text, width, "")
			}
			b.shown, b.shownWidth = terminalText(s), width
		}
		parts[i] = b.shown
	}
	t.cached = strings.Join(parts, "\n\n")
	t.width = width
	t.renderedVersion = t.version
	return t.cached
}
func newViewport() *viewport.Model          { v := viewport.New(80, 20); return &v }
func (m *model) addBlock(b transcriptBlock) { m.home = false; m.log.add(b) }
func (m *model) scrollHistory(lines int) {
	m.follow = false
	if lines < 0 {
		m.history.ScrollUp(-lines)
	} else {
		m.history.ScrollDown(lines)
	}
	if m.history.AtBottom() {
		m.follow = true
	}
}
func (m model) historyView(height int) string {
	w := m.contentWidth()
	m.history.Width = w
	m.history.Height = max(height, 1)
	text := m.log.render(m.md, w)
	if m.running && strings.TrimSpace(m.stream) != "" {
		live := m.streamView
		if live == "" {
			live = m.renderStream()
		}
		text += "\n\n" + terminalText(live)
	}
	m.history.SetContent(text)
	if m.follow {
		m.history.GotoBottom()
	}
	return lipgloss.NewStyle().Width(w).Height(height).MaxHeight(height).Render(m.history.View())
}
