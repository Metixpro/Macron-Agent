// Package snap is a client of SubQ's Decisions API (POST /v1/systemone):
// fast, typed judgments instead of generated text. A request carries a state
// (text) and named questions, each a yes/no gate (noul), a pick among named
// options (choice) or a level on an ordered scale (score); the answers come
// back with probabilities in a fraction of a second, and only the input is
// paid for. The bot asks it what a request needs (see triage.go) so the
// language model only does the work.
package snap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultURL is the API's base address.
const DefaultURL = "https://api.subq.ai/v1"

// DefaultModel is the decision model used when none is set.
const DefaultModel = "subq-snap-1.1"

// Models are the decision models offered (GET /v1/models).
var Models = []string{"subq-snap-1.1", "subq-snap-latest", "subq-snap-preview"}

// Question types.
const (
	TypeNoul   = "noul"
	TypeChoice = "choice"
	TypeScore  = "score"
)

// Question is one judgment asked about the state.
type Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Noul is a yes/no gate; yes and no describe the two outcomes.
func Noul(instructions, yes, no string) Question {
	q := Question{Type: TypeNoul, Instructions: instructions}
	if yes != "" || no != "" {
		q.Criteria = map[string]string{"true": yes, "false": no}
	}
	return q
}

// Option is one option of a choice.
type Option struct {
	Key   string
	About string // "" for none
}

// Choice picks one of 2-255 named options.
func Choice(instructions string, options ...Option) Question {
	crit := make(map[string]any, len(options))
	for _, o := range options {
		if o.About == "" {
			crit[o.Key] = nil
		} else {
			crit[o.Key] = o.About
		}
	}
	return Question{Type: TypeChoice, Instructions: instructions, Criteria: crit}
}

// Score places the state on 2-10 ordered levels, lowest first.
func Score(instructions string, levels ...string) Question {
	return Question{Type: TypeScore, Instructions: instructions, Criteria: levels}
}

// Answer is the answer to one question.
type Answer struct {
	Type string `json:"type"`
	// Noul: the probability of yes, 0 to 1.
	Noul float64 `json:"noul"`
	// Choice: the option picked; Score: the probability-weighted level.
	Choice string  `json:"choice"`
	Score  float64 `json:"score"`
	// Choice and score: each option's (or level's) probability, and how sure.
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
	Legend        map[string]string  `json:"legend"`
}

// Result is a decision.
type Result struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   struct {
		InputTokens int `json:"input_tokens"`
	} `json:"usage"`
	Took time.Duration `json:"-"`
}

// Error is an answer of the API other than a decision.
type Error struct {
	Status  int
	Type    string // validation_error, …
	Message string
}

func (e *Error) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("snap: %d %s: %s", e.Status, e.Type, e.Message)
	}
	return fmt.Sprintf("snap: %d %s", e.Status, e.Message)
}

// Retryable reports a failure worth trying again later (rate limit, the
// service down), as opposed to a request or a key that is wrong.
func (e *Error) Retryable() bool { return e.Status == http.StatusTooManyRequests || e.Status >= 500 }

// ErrNoKey is returned when no API key is set.
var ErrNoKey = errors.New("snap: no API key")

// Client calls the Decisions API. The key is passed with each call (it may
// change in the admin panel at any time) and never appears in an error.
type Client struct {
	BaseURL string       // DefaultURL when empty
	HTTP    *http.Client // http.DefaultClient's transport with a timeout when nil
}

// maxResponse caps what is read of an answer.
const maxResponse = 1 << 20

// Decide asks the questions about the state.
func (c *Client) Decide(ctx context.Context, key, model, state string, questions map[string]Question) (*Result, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, ErrNoKey
	}
	if model == "" {
		model = DefaultModel
	}
	body, err := json.Marshal(map[string]any{"model": model, "state": state, "questions": questions})
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = DefaultURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/systemone", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	start := time.Now()
	resp, err := hc.Do(req)
	if err != nil {
		return nil, scrub(err, key)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, scrub(err, key)
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Detail struct {
				ErrorType string `json:"error_type"`
				Message   string `json:"message"`
			} `json:"detail"`
		}
		out := &Error{Status: resp.StatusCode}
		if json.Unmarshal(raw, &e) == nil && (e.Detail.Message != "" || e.Detail.ErrorType != "") {
			out.Type, out.Message = e.Detail.ErrorType, e.Detail.Message
		} else {
			out.Message = strings.TrimSpace(string(raw[:min(len(raw), 200)]))
		}
		out.Message = strings.ReplaceAll(out.Message, key, "•••")
		return nil, out
	}
	var res Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("snap: unreadable answer: %w", err)
	}
	if len(res.Answers) == 0 {
		return nil, errors.New("snap: an answer without decisions")
	}
	res.Took = time.Since(start)
	return &res, nil
}

// scrub keeps the key out of an error (a URL or a proxy may echo it).
func scrub(err error, key string) error {
	if err == nil || key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "•••"))
}
