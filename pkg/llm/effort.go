package llm

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	EffortLow    = "low"
	EffortMedium = "medium"
	EffortHigh   = "high"
	EffortUltra  = "ultracode"
)

var Efforts = []string{EffortLow, EffortMedium, EffortHigh, EffortUltra}

func ValidEffort(e string) bool { return e == "" || slices.Contains(Efforts, e) }

type effortCapKey struct{}

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

type effortKey struct{}

const EffortNone = "none"

func WithEffort(ctx context.Context, level string) context.Context {
	if level != EffortNone && !slices.Contains(Efforts, level) {
		return ctx
	}
	return context.WithValue(ctx, effortKey{}, level)
}

func effortOf(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(effortKey{}).(string)
	return v, ok
}

func (c *Client) effortFor(req Request) string {
	if req.effortSet {
		if req.effortExact == EffortNone {
			return ""
		}
		return req.effortExact
	}
	level := c.Effort
	if level != "" && req.effortCap != "" && slices.Index(Efforts, req.effortCap) < slices.Index(Efforts, level) {
		return req.effortCap
	}
	return level
}

const shortAnswer = 4096

type EffortSupport int

const (
	EffortMaybe EffortSupport = iota
	EffortYes
	EffortNo
)

var (
	claudeRe    = regexp.MustCompile(`claude-(?:opus|sonnet|haiku|fable|mythos)-(\d+)(?:-(\d{1,2}))?(?:\b|[-@_.])`)
	claudeOldRe = regexp.MustCompile(`claude-(?:\d|instant)`)
	gptRe       = regexp.MustCompile(`^gpt-(\d+)(?:\.(\d+))?`)
)

func modelBase(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return m
}

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

func effortLadder(api, model, level string) []string {
	if level == "" {
		return nil
	}
	m := modelBase(model)
	if api == APIAnthropic {
		major, minor, family, ok := claudeVersion(m)
		switch {
		case !ok:
		case major < 4 || (major == 4 && minor < 5) || (major == 4 && minor == 5 && family != "opus"):
			return nil
		case major == 4 && minor == 5:
			if level == EffortUltra {
				return []string{EffortHigh}
			}
			return []string{level}
		case major == 4 && minor == 6:
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
		return nil
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
	return !c.noEffort.Swap(true)
}
