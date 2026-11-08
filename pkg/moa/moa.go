// Package moa is Mixture-of-Agents: several models give their view of a
// request and one model, the aggregator, does the work with their views in
// front of it.
//
//   - A preset is reference models (the advisors) and an aggregator.
//   - The aggregator is the real model: it writes the answer and calls the
//     tools; the tool loop, the transcript and the session are as with any
//     model, and the whole run is billed to its provider.
//   - The advisors get no tools and no system prompt: only the conversation's
//     text (user and assistant turns). What they say reaches the aggregator as
//     private context, in a user message of its own at the end, never mixed
//     into the user's message.
//   - Fanout says when the advisors are asked: once per user message
//     (user_turn, the cheapest), at every tool round (per_iteration), or at the
//     first round and every N after (every_n:N; the rounds between reuse what
//     they said).
//   - An advisor that fails does not fail the turn: its error is in the
//     reference block and the others go on.
//
// The prompt caches are kept: the advisors' block is a separate last message
// (the prefix before it is untouched), and their view is a pure function of
// the stable history (its own prefix caches too). A provider whose chat
// template insists on alternating roles («Conversation roles must
// alternate», llama.cpp and vLLM templates, some OpenRouter routes) gets the
// two user messages merged: the failed request once, and from then on, from
// the start, for that destination only.
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

	"github.com/Metixpro/macron-agent/pkg/llm"
)

// Fanouts.
const (
	FanoutUserTurn     = "user_turn"
	FanoutPerIteration = "per_iteration"
	fanoutEveryN       = "every_n:"
)

// Privacy filters.
const (
	PrivacyNone    = ""
	PrivacyDisplay = "display" // the user's surfaces and the trace are cleaned
	PrivacyFull    = "full"    // the advisors' text in the aggregator's prompt too
)

// Slot is one model of a preset: a provider by its name (or id), and how
// hard it thinks ("" its provider's own; none, low, medium, high, ultracode).
type Slot struct {
	Provider string `json:"provider"`
	Effort   string `json:"reasoning_effort,omitempty"`
}

// Preset is a named mixture.
type Preset struct {
	Name          string `json:"name"`
	References    []Slot `json:"reference_models"`
	Aggregator    Slot   `json:"aggregator"`
	Fanout        string `json:"fanout,omitempty"`
	PrivacyFilter string `json:"privacy_filter,omitempty"`
	Enabled       *bool  `json:"enabled,omitempty"` // false: the aggregator alone
}

// On reports whether the advisors are asked (enabled is on unless set false).
func (p Preset) On() bool { return p.Enabled == nil || *p.Enabled }

// Every is how many tool rounds go between two fanouts.
func (p Preset) Every() int {
	switch {
	case p.Fanout == FanoutPerIteration:
		return 1
	case strings.HasPrefix(p.Fanout, fanoutEveryN):
		if n, err := strconv.Atoi(strings.TrimPrefix(p.Fanout, fanoutEveryN)); err == nil && n >= 1 {
			return n
		}
	}
	return 0 // user_turn (and anything unknown): once per user message
}

// FanoutName is the fanout as it is kept (an invalid one reads as user_turn).
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

// ParsePresets reads the presets (a JSON list).
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

// Find is the preset of a name ("" the first).
func Find(list []Preset, name string) (Preset, bool) {
	for _, p := range list {
		if name == "" || strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return Preset{}, false
}

// Problems lists what is wrong with a preset: a slot whose provider does not
// resolve or has no key, an aggregator that is itself a mixture.
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

// Caller sends one request to one slot's model (no failover: a slot that
// fails says so).
type Caller func(ctx context.Context, slot Slot, req llm.Request) (*llm.Response, error)

// Advice is what one advisor said.
type Advice struct {
	Slot Slot
	Text string
	Err  error
}

// Client is a preset as a chat client, for one request: each Complete is one
// round of the agent's loop.
type Client struct {
	Preset Preset
	Call   Caller
	// Strict is shared by the requests of a session: the destinations
	// (base_url + model) that insist on alternating roles.
	Strict *Strict
	// Dest names a slot's destination (base_url + model).
	Dest func(Slot) string
	// OnAdvice reports the advisors' views each time they are asked (the trace).
	OnAdvice func(advice []Advice)

	mu      sync.Mutex
	round   int
	cached  []Advice
	Fanouts int // how many times the advisors were asked (for tests and the log)
}

// Strict remembers the destinations that need merged user messages.
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

// fanNow reports whether this round asks the advisors.
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

// Complete is one round: the advisors when the fanout says so (or what they
// said last), then the aggregator.
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
		// The template wants user and assistant in turn: this request once
		// more with the two user messages merged, and that shape from now on
		// for this destination only.
		c.Strict.mark(dest)
		agg.Messages = MergeUsers(msgs)
		resp, err = c.Call(ctx, c.Preset.Aggregator, agg)
	}
	return resp, err
}

// View is what an advisor sees: the conversation's text only, no system
// prompt, no tools, no tool results; the same history gives the same view.
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
	// A view ends with the user's turn: what follows is the agent's own work.
	for len(out) > 0 && out[len(out)-1].Role == "assistant" {
		out = out[:len(out)-1]
	}
	return MergeUsers(out)
}

// advise asks every advisor at once.
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

// block is the advisors' views as the aggregator reads them ("" none).
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

// MergeUsers joins adjacent user messages (for templates that insist on
// alternating roles).
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

// Alternation reports a provider refusing two user messages in a row.
func Alternation(err error) bool {
	var api *llm.APIError
	if errors.As(err, &api) && api.Status != 400 && api.Status != 422 {
		return false
	}
	return err != nil && alternationRe.MatchString(err.Error())
}

// What Scrub takes out: emails, and phone numbers written as phone numbers
// (an international prefix, or an Iranian mobile). Line numbers, hashes,
// dates, addresses and versions stay.
var (
	emailRe = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}\b`)
	phoneRe = regexp.MustCompile(`(?:^|[^\w.:/-])((?:\+|00)[1-9]\d{0,2}[ -]?\(?\d{2,4}\)?[ -]?\d{3,4}[ -]?\d{3,4}|09\d{2}[ -]?\d{3}[ -]?\d{4})(?:$|[^\w.:/-])`)
)

// Scrub hides emails and phone numbers.
func Scrub(s string) string {
	s = emailRe.ReplaceAllString(s, "[email]")
	return phoneRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := phoneRe.FindStringSubmatch(m)
		return strings.Replace(m, sub[1], "[phone]", 1)
	})
}
