package agent

import (
	"slices"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

type Suspended struct {
	messages []llm.Message
	lay      layout
	plan     []PlanItem
	at       time.Time
	size     int
}

func (s *Suspended) Age() time.Duration { return time.Since(s.at) }

func (s *Suspended) Size() int { return s.size }

func suspend(messages []llm.Message, lay layout, plan []PlanItem) *Suspended {
	size := 0
	for _, m := range messages {
		size += len(m.Content)
		for _, p := range m.Parts {
			size += len(p.Text) + len(p.ImageURL)
		}
		for _, c := range m.ToolCalls {
			size += len(c.Function.Arguments)
		}
	}
	return &Suspended{messages: slices.Clone(messages), lay: lay, plan: slices.Clone(plan), at: time.Now(), size: size}
}

const resumeAnswer = "[The user answered your question. The job goes on from where it stopped: the tool results above are still valid.]\n"
