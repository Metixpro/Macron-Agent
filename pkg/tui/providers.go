package tui

import (
	"context"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"time"
)

type ProviderChoice struct {
	ID              int64
	Name, Model     string
	Active, Enabled bool
}

func (m model) providerPicker() (tea.Model, tea.Cmd) {
	if m.running || m.busy != "" {
		return m, m.say("Finish or stop the current request first")
	}
	e, ok := m.e.(interface {
		Providers(context.Context) ([]ProviderChoice, error)
		SetProvider(context.Context, int64) error
	})
	if !ok {
		return m, m.say("Use /connect model to configure a provider")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	providers, err := e.Providers(ctx)
	if err != nil {
		return m, m.say(errText(err))
	}
	if len(providers) == 0 {
		return m, m.say("No provider yet · /connect model")
	}
	labels := []string{}
	ids := map[string]int64{}
	current := ""
	for _, p := range providers {
		label := fmt.Sprintf("%s · %s", p.Name, p.Model)
		if !p.Enabled {
			label += " (off)"
		}
		labels = append(labels, label)
		ids[label] = p.ID
		if p.Active {
			current = label
		}
	}
	m.pick = &picker{title: "Provider", items: labels, current: current, sel: max(indexOf(labels, current), 0), pick: func(label string) tea.Cmd {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := e.SetProvider(ctx, ids[label]); err != nil {
				return noticeMsg{err: err}
			}
			return noticeMsg{text: "Provider: " + label}
		}
	}}
	return m, nil
}
