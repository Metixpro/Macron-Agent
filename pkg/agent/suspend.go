package agent

import (
	"encoding/json"
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

func (s *Suspended) Age() time.Duration {
	if s == nil {
		return 0
	}
	return time.Since(s.at)
}

func (s *Suspended) Size() int {
	if s == nil {
		return 0
	}
	return s.size
}

func (s *Suspended) Plan() []PlanItem {
	if s == nil {
		return nil
	}
	return slices.Clone(s.plan)
}

func (s *Suspended) Messages() []llm.Message {
	if s == nil {
		return nil
	}
	return slices.Clone(s.messages)
}

type suspendedPayload struct {
	Messages []llm.Message `json:"messages"`
	HistFrom int           `json:"hist_from"`
	HistTo   int           `json:"hist_to"`
	Input    int           `json:"input"`
	Plan     []PlanItem    `json:"plan"`
	At       time.Time     `json:"at"`
	Size     int           `json:"size"`
}

func (s *Suspended) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("null"), nil
	}
	p := suspendedPayload{
		Messages: s.messages,
		HistFrom: s.lay.histFrom,
		HistTo:   s.lay.histTo,
		Input:    s.lay.input,
		Plan:     s.plan,
		At:       s.at,
		Size:     s.size,
	}
	return json.Marshal(p)
}

func (s *Suspended) UnmarshalJSON(data []byte) error {
	var p suspendedPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	s.messages = p.Messages
	s.lay = layout{histFrom: p.HistFrom, histTo: p.HistTo, input: p.Input}
	s.plan = p.Plan
	s.at = p.At
	s.size = p.Size
	return nil
}

func (s *Suspended) Bytes() ([]byte, error) {
	return json.Marshal(s)
}

func SuspendedFromBytes(data []byte) (*Suspended, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var s Suspended
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

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
