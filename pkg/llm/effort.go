package llm

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Effort is how hard a reasoning model thinks before it answers: one
// setting for every provider, each API given its own parameter (Anthropic
// output_config.effort, OpenAI-compatible reasoning_effort). Ultracode is
// the most a model gives (max, xhigh) for big code and long agentic work.
// A model that refuses a value steps down to the next one it may take; one
// that takes no effort at all is learned once and gets none after that.

const (
	EffortLow    = "low"
	EffortMedium = "medium"
	EffortHigh   = "high"
	EffortUltra  = "ultracode"
)

// Efforts are the levels, lowest first ("" leaves the model its own default).
var Efforts = []string{EffortLow, EffortMedium, EffortHigh, EffortUltra}

// ValidEffort reports a level ("" included).
func ValidEffort(e string) bool { return e == "" || slices.Contains(Efforts, e) }

type effortCapKey struct{}

// WithEffortCap caps how hard the models called under ctx think: a light
// job (a fast classifier said so) does not pay for a provider's high effort.
// It only lowers an effort the provider was given; a model left to its own
// default gets nothing sent, as before. An empty level lifts a cap.
func WithEffortCap(ctx context.Context, level string) context.Context {
	if level != "" && !slices.Contains(Efforts, level) {
		return ctx
	}
	return context.WithValue(ctx, effortCapKey{}, level)
}

func effortCapOf(ctx context.Context) string {
	v, _ := ctx.Value(effortCapKey{}).(string)
	return v
}

// effortFor is the effort of one request: the provider's, under its cap.
func (c *Client) effortFor(req Request) string {
	level := c.Effort
	if level != "" && req.effortCap != "" && slices.Index(Efforts, req.effortCap) < slices.Index(Efforts, level) {
		return req.effortCap
	}
	return level
}

// shortAnswer: a request that allows fewer output tokens than this thinks at
// low effort (a title, a summary, a test): long thinking would leave no room
// for the answer.
const shortAnswer = 4096

// EffortSupport is what is known of a model and effort.
type EffortSupport int

const (
	EffortMaybe EffortSupport = iota // sent; left out from then on if the model refuses it
	EffortYes                        // the model takes it
	EffortNo                         // the model takes none: nothing is sent
)

var (
	claudeRe    = regexp.MustCompile(`claude-(?:opus|sonnet|haiku|fable|mythos)-(\d+)(?:-(\d{1,2}))?(?:\b|[-@_.])`)
	claudeOldRe = regexp.MustCompile(`claude-(?:\d|instant)`)
	gptRe       = regexp.MustCompile(`^gpt-(\d+)(?:\.(\d+))?`)
)

// modelBase is a model's own name: lower case, without a gateway's prefix
// ("anthropic/claude-…", "openai/gpt-…").
func modelBase(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return m
}

// claudeVersion reads a Claude model's version (ok false: not a Claude name).
func claudeVersion(m string) (major, minor int, family string, ok bool) {
	if claudeOldRe.MatchString(m) {
		return 3, 0, "", true
	}
	sub := claudeRe.FindStringSubmatch(m + "-")
	if sub == nil {
		return 0, 0, "", false
	}
	major, _ = strconv.Atoi(sub[1])
	minor, _ = strconv.Atoi(sub[2])
	for _, f := range []string{"opus", "sonnet", "haiku", "fable", "mythos"} {
		if strings.Contains(m, "-"+f+"-") {
			family = f
		}
	}
	return major, minor, family, true
}

// effortLadder is what a level becomes for a model, best first: a refusal
// moves to the next value; nil sends nothing.
func effortLadder(api, model, level string) []string {
	if level == "" {
		return nil
	}
	m := modelBase(model)
	if api == APIAnthropic {
		major, minor, family, ok := claudeVersion(m)
		switch {
		case !ok: // a gateway's own name: try it
		case major < 4 || (major == 4 && minor < 5) || (major == 4 && minor == 5 && family != "opus"):
			return nil // before effort
		case major == 4 && minor == 5: // Opus 4.5: low, medium and high only
			if level == EffortUltra {
				return []string{EffortHigh}
			}
			return []string{level}
		case major == 4 && minor == 6: // no xhigh yet
			if level == EffortUltra {
				return []string{"max", EffortHigh}
			}
			return []string{level}
		}
		if level == EffortUltra {
			return []string{"max", "xhigh", EffortHigh}
		}
		return []string{level}
	}
	if strings.HasPrefix(m, "gpt-4") || strings.HasPrefix(m, "gpt-3") || strings.HasPrefix(m, "chatgpt-") {
		return nil // OpenAI's models that do not reason
	}
	if level != EffortUltra {
		return []string{level}
	}
	if sub := gptRe.FindStringSubmatch(m); sub != nil {
		major, _ := strconv.Atoi(sub[1])
		minor, _ := strconv.Atoi(sub[2])
		if major > 5 || (major == 5 && minor >= 2) || strings.Contains(m, "codex-max") {
			return []string{"xhigh", EffortHigh}
		}
	}
	return []string{EffortHigh}
}

// EffortWire is what a level becomes for a model: the parameter and the
// value sent first ("" when nothing is), and what is known of the model.
func EffortWire(api, model, level string) (param, value string, support EffortSupport) {
	param = "reasoning_effort"
	switch api {
	case APIAnthropic:
		param = "output_config.effort"
	case APIResponses:
		param = "reasoning.effort"
	}
	ladder := effortLadder(api, model, EffortHigh)
	switch m := modelBase(model); {
	case ladder == nil:
		support = EffortNo
	case api == APIAnthropic:
		if _, _, _, ok := claudeVersion(m); ok {
			support = EffortYes
		}
	case reasoningModel(m):
		support = EffortYes
	}
	if l := effortLadder(api, model, level); len(l) > 0 {
		value = l[0]
	}
	return param, value, support
}

// wireEffort is the effort value this request goes with ("" = none): the
// first of the level's ladder the model has not refused.
func (c *Client) wireEffort(req Request) string {
	level := c.effortFor(req)
	if level == "" || c.noEffort.Load() {
		return ""
	}
	if req.MaxTokens > 0 && req.MaxTokens < shortAnswer {
		level = EffortLow
	}
	for _, v := range effortLadder(c.api(), c.Model, level) {
		if (v == "max" && c.noMaxEffort.Load()) || (v == "xhigh" && c.noXHighEffort.Load()) {
			continue
		}
		return v
	}
	return ""
}

// learnEffort reads a refusal of the effort sent; it reports whether the
// request should go again (with the next value, or none). A gateway may turn
// reasoning_effort into its model's own switch and pass on that model's
// refusal («property 'enable_thinking' is unsupported»): that is the effort
// refused too.
func (c *Client) learnEffort(body, sent string) bool {
	b := strings.ToLower(body)
	if sent == "" || !(strings.Contains(b, "effort") || strings.Contains(b, "output_config") || strings.Contains(b, "thinking")) {
		return false
	}
	switch sent {
	case "max":
		return !c.noMaxEffort.Swap(true)
	case "xhigh":
		return !c.noXHighEffort.Swap(true)
	}
	return !c.noEffort.Swap(true) // a plain level refused: the model takes no effort at all
}
