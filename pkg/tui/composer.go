package tui

import (
	"context"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const fileSuggestMax = 40

func lastToken(v string) string {
	if i := strings.LastIndexAny(v, " \n\t"); i >= 0 {
		return v[i+1:]
	}
	return v
}

func lastSentence(text string) string {
	lines := strings.Split(strings.TrimSpace(clean(text)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			if r := []rune(t); len(r) > 200 {
				t = "…" + string(r[len(r)-200:])
			}
			return t
		}
	}
	return ""
}

func (m *model) projectFiles() []string {
	if m.files != nil && time.Since(m.filesAt) < 30*time.Second {
		return m.files
	}
	if e, ok := m.e.(interface{ Files() []string }); ok {
		m.files, m.filesAt = e.Files(), time.Now()
		if m.files == nil {
			m.files = []string{}
		}
	}
	return m.files
}

func (m *model) fileSuggestions(query string) []command {
	files := m.projectFiles()
	q := strings.ToLower(query)
	type hit struct {
		name  string
		score int
	}
	var hits []hit
	for _, f := range files {
		lf, base := strings.ToLower(f), strings.ToLower(path.Base(f))
		score := -1
		switch {
		case q == "":
			score = 3
		case strings.HasPrefix(base, q):
			score = 0
		case strings.HasPrefix(lf, q):
			score = 1
		case strings.Contains(base, q):
			score = 2
		case strings.Contains(lf, q):
			score = 3
		}
		if score >= 0 {
			hits = append(hits, hit{f, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score < hits[j].score
		}
		return len(hits[i].name) < len(hits[j].name)
	})
	out := make([]command, 0, min(len(hits), fileSuggestMax))
	for _, h := range hits[:min(len(hits), fileSuggestMax)] {
		out = append(out, command{name: h.name, about: "file"})
	}
	return out
}

func (m *model) complete() {
	c := m.sugg[m.suggAt]
	m.sugg = nil
	if m.suggKind == "file" {
		v := m.input.Value()
		cut := strings.LastIndexAny(v, " \n\t") + 1
		m.input.SetValue(v[:cut] + "@" + c.name + " ")
		m.fitInput()
		return
	}
	m.input.SetValue(c.name + " ")
}

func (m model) shell(command string) (tea.Model, tea.Cmd) {
	sh, ok := m.e.(interface {
		Shell(context.Context, string) string
	})
	if !ok {
		return m, m.say("Shell commands are not available here")
	}
	if m.running || m.busy != "" {
		return m, m.say("Wait until Macron finishes, or press esc")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.busy, m.started, m.cancel, m.interrupted = "Running "+clip(oneLine(command), 40), time.Now(), cancel, false
	return m, tea.Batch(func() tea.Msg {
		defer cancel()
		return shellMsg{command: command, out: sh.Shell(ctx, command)}
	}, m.spin.Tick)
}

func (m model) export() (string, error) {
	var b strings.Builder
	b.WriteString("# Macron conversation · " + time.Now().Format("2006-01-02 15:04") + "\n")
	for _, blk := range m.log.blocks {
		switch blk.kind {
		case "user":
			b.WriteString("\n## You\n\n" + strings.TrimSpace(blk.text) + "\n")
		case "assistant":
			b.WriteString("\n## Macron\n\n" + strings.TrimSpace(blk.text) + "\n")
		case "tool":
			b.WriteString("\n**" + ansi.Strip(toolTitle(blk.name, blk.args, 200)) + "**\n\n```\n" + strings.TrimSpace(clean(blk.text)) + "\n```\n")
		default:
			if t := strings.TrimSpace(ansi.Strip(blk.text)); t != "" {
				b.WriteString("\n> " + strings.ReplaceAll(t, "\n", "\n> ") + "\n")
			}
		}
	}
	name := "macron-chat-" + time.Now().Format("20060102-150405") + ".md"
	return name, os.WriteFile(name, []byte(b.String()), 0o600)
}
