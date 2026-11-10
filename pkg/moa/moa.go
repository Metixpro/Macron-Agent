package moa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/llm"
)

const (
	FanoutUserTurn     = "user_turn"
	FanoutPerIteration = "per_iteration"
	fanoutEveryN       = "every_n:"
)

const (
	PrivacyNone    = ""
	PrivacyDisplay = "display"
	PrivacyFull    = "full"
)

type Slot struct {
	Provider string `json:"provider"`
	Effort   string `json:"reasoning_effort,omitempty"`
}

type Preset struct {
	Name          string `json:"name"`
	References    []Slot `json:"reference_models"`
	Aggregator    Slot   `json:"aggregator"`
	Fanout        string `json:"fanout,omitempty"`
	PrivacyFilter string `json:"privacy_filter,omitempty"`
	Enabled       *bool  `json:"enabled,omitempty"`
}

func (p Preset) On() bool { return p.Enabled == nil || *p.Enabled }

func (p Preset) Every() int {
	switch {
	case p.Fanout == FanoutPerIteration:
		return 1
	case strings.HasPrefix(p.Fanout, fanoutEveryN):
		if n, err := strconv.Atoi(strings.TrimPrefix(p.Fanout, fanoutEveryN)); err == nil && n >= 1 {
			return n
		}
	}
	return 0
}

func (p Preset) FanoutName() string {
	switch n := p.Every(); {
	case n == 0:
		return FanoutUserTurn
	case n == 1 && p.Fanout == FanoutPerIteration:
		return FanoutPerIteration
	default:
		return fanoutEveryN + strconv.Itoa(n)
	}
}

func ParsePresets(raw string) ([]Preset, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []Preset
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("moa.presets is not a JSON list of presets: %w", err)
	}
	return out, nil
}

func Find(list []Preset, name string) (Preset, bool) {
	for _, p := range list {
		if name == "" || strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return Preset{}, false
}

func Problems(p Preset, resolve func(provider string) (ok, hasKey, isMoA bool)) []string {
	var out []string
	if strings.TrimSpace(p.Name) == "" {
		out = append(out, "a preset has no name")
	}
	if p.Aggregator.Provider == "" {
		out = append(out, p.Name+": no aggregator")
	} else if ok, key, moa := resolve(p.Aggregator.Provider); !ok {
		out = append(out, fmt.Sprintf("%s: the aggregator %q is not a provider", p.Name, p.Aggregator.Provider))
	} else if moa {
		out = append(out, fmt.Sprintf("%s: the aggregator cannot be a mixture itself", p.Name))
	} else if !key {
		out = append(out, fmt.Sprintf("%s: the aggregator %q has no API key", p.Name, p.Aggregator.Provider))
	}
	for _, s := range append([]Slot{p.Aggregator}, p.References...) {
		if s.Effort != "" && s.Effort != llm.EffortNone && !llm.ValidEffort(s.Effort) {
			out = append(out, fmt.Sprintf("%s: unknown reasoning_effort %q", p.Name, s.Effort))
		}
	}
	for _, s := range p.References {
		if ok, _, _ := resolve(s.Provider); !ok {
			out = append(out, fmt.Sprintf("%s: the reference model %q is not a provider", p.Name, s.Provider))
		}
	}
	return out
}

type Caller func(ctx context.Context, slot Slot, req llm.Request) (*llm.Response, error)

type Advice struct {
	Slot Slot
	Text string
	Err  error
}

type Client struct {
	Preset Preset
	Call   Caller

	Strict *Strict

	Dest func(Slot) string

	OnAdvice func(advice []Advice)

	mu      sync.Mutex
	round   int
	cached  []Advice
	Fanouts int
}

type Strict struct {
	mu   sync.Mutex
	dest map[string]bool
}

func (s *Strict) is(d string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dest[d]
}

func (s *Strict) mark(d string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dest == nil {
		s.dest = map[string]bool{}
	}
	s.dest[d] = true
}

func (c *Client) fanNow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.round
	c.round++
	if !c.Preset.On() || len(c.Preset.References) == 0 {
		return false
	}
	if r == 0 || c.cached == nil {
		return true
	}
	n := c.Preset.Every()
	return n > 0 && r%n == 0
}

func (c *Client) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if c.fanNow() {
		advice := c.advise(ctx, req.Messages)
		c.mu.Lock()
		c.cached, c.Fanouts = advice, c.Fanouts+1
		c.mu.Unlock()
		if c.OnAdvice != nil {
			c.OnAdvice(advice)
		}
	}
	c.mu.Lock()
	advice := c.cached
	c.mu.Unlock()
	msgs := req.Messages
	if block := c.block(advice); block != "" {
		msgs = append(append([]llm.Message(nil), msgs...), llm.Message{Role: "user", Content: block})
	}
	agg := req
	dest := ""
	if c.Dest != nil {
		dest = c.Dest(c.Preset.Aggregator)
	}
	if c.Strict.is(dest) {
		msgs = MergeUsers(msgs)
	}
	agg.Messages = msgs
	resp, err := c.Call(ctx, c.Preset.Aggregator, agg)
	if err != nil && Alternation(err) && !c.Strict.is(dest) {

		c.Strict.mark(dest)
		agg.Messages = MergeUsers(msgs)
		resp, err = c.Call(ctx, c.Preset.Aggregator, agg)
	}
	return resp, err
}

func View(msgs []llm.Message) []llm.Message {
	var out []llm.Message
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		text := strings.TrimSpace(m.Content)
		if text == "" && len(m.Parts) > 0 {
			var parts []string
			for _, p := range m.Parts {
				if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
					parts = append(parts, p.Text)
				}
			}
			text = strings.Join(parts, "\n")
		}
		if text == "" || strings.HasPrefix(text, "[system notice]") || strings.HasPrefix(text, "[tools]") {
			continue
		}
		out = append(out, llm.Message{Role: m.Role, Content: text})
	}

	for len(out) > 0 && out[len(out)-1].Role == "assistant" {
		out = out[:len(out)-1]
	}
	return MergeUsers(out)
}

func (c *Client) advise(ctx context.Context, msgs []llm.Message) []Advice {
	view := View(msgs)
	out := make([]Advice, len(c.Preset.References))
	if len(view) == 0 {
		return out
	}
	var wg sync.WaitGroup
	for i, s := range c.Preset.References {
		wg.Add(1)
		go func(i int, s Slot) {
			defer wg.Done()
			defer guard.Failed("a model's turn in the vote", &out[i].Err)
			out[i].Slot = s
			resp, err := c.Call(ctx, s, llm.Request{Messages: view, MaxTokens: 2000})
			switch {
			case err != nil:
				out[i].Err = err
			case strings.TrimSpace(resp.Message.Content) == "":
				out[i].Err = errors.New("an empty answer")
			default:
				out[i].Text = strings.TrimSpace(resp.Message.Content)
			}
		}(i, s)
	}
	wg.Wait()
	return out
}

func (c *Client) block(advice []Advice) string {
	if len(advice) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("[private context, not from the user] Other models answered the conversation above, as references for you. " +
		"Use what is right in them, ignore what is wrong, and do the work yourself (only you have the tools). Do not mention them " +
		"unless the user asks.\n")
	for _, a := range advice {
		name := a.Slot.Provider
		if a.Err != nil {
			fmt.Fprintf(&sb, "\n<reference model=%q error=%q/>\n", name, shortErr(a.Err))
			continue
		}
		text := a.Text
		if c.Preset.PrivacyFilter == PrivacyFull {
			text = Scrub(text)
		}
		fmt.Fprintf(&sb, "\n<reference model=%q>\n%s\n</reference>\n", name, text)
	}
	return sb.String()
}

func shortErr(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

func MergeUsers(msgs []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		if n := len(out); n > 0 && m.Role == "user" && out[n-1].Role == "user" && len(m.Parts) == 0 && len(out[n-1].Parts) == 0 {
			out[n-1].Content = strings.TrimRight(out[n-1].Content, "\n") + "\n\n" + m.Content
			continue
		}
		out = append(out, m)
	}
	return out
}

var alternationRe = regexp.MustCompile(`(?i)roles must alternate|must alternate between|alternating user/assistant|expected (?:an? )?(?:user|assistant) message`)

func Alternation(err error) bool {
	var api *llm.APIError
	if errors.As(err, &api) && api.Status != 400 && api.Status != 422 {
		return false
	}
	return err != nil && alternationRe.MatchString(err.Error())
}

var (
	emailRe = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}\b`)
	phoneRe = regexp.MustCompile(`(?:^|[^\w.:/-])((?:\+|00)[1-9]\d{0,2}[ -]?\(?\d{2,4}\)?[ -]?\d{3,4}[ -]?\d{3,4}|09\d{2}[ -]?\d{3}[ -]?\d{4})(?:$|[^\w.:/-])`)
)

func Scrub(s string) string {
	s = emailRe.ReplaceAllString(s, "[email]")
	return phoneRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := phoneRe.FindStringSubmatch(m)
		return strings.Replace(m, sub[1], "[phone]", 1)
	})
}
