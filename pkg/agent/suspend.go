package agent

import (
	"slices"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

// Suspended is a job that stopped to wait for the user (ask_user): its whole
// conversation with the model, the tool results included, and its plan. The
// bot keeps it, and the user's answer resumes the job where it stopped,
// instead of starting over from the conversation's notes.
type Suspended struct {
	messages []llm.Message
	lay      layout
	plan     []PlanItem
	at       time.Time
	size     int
}

// Age is how long it has waited.
func (s *Suspended) Age() time.Duration { return time.Since(s.at) }

// Size estimates what it holds, in bytes.
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

// resumeAnswer is how the user's answer joins a resumed job.
const resumeAnswer = "[The user answered your question. The job goes on from where it stopped: the tool results above are still valid.]\n"
