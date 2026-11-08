package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEffortForEachModel(t *testing.T) {
	cases := []struct{ api, model, level, want string }{
		{APIAnthropic, "claude-opus-5-5", EffortUltra, "[max xhigh high]"},
		{APIAnthropic, "claude-fable-5-1", EffortLow, "[low]"},
		{APIAnthropic, "claude-opus-4-7", EffortUltra, "[max xhigh high]"},
		{APIAnthropic, "claude-sonnet-4-6", EffortUltra, "[max high]"}, // xhigh came with Opus 4.7
		{APIAnthropic, "claude-opus-4-5-20251101", EffortUltra, "[high]"},
		{APIAnthropic, "claude-opus-4-5", EffortMedium, "[medium]"},
		{APIAnthropic, "claude-sonnet-4-5-20250929", EffortHigh, "[]"}, // before effort
		{APIAnthropic, "claude-haiku-4-5@20251001", EffortHigh, "[]"},
		{APIAnthropic, "claude-opus-4-1-20250805", EffortHigh, "[]"},
		{APIAnthropic, "claude-opus-4-20250514", EffortHigh, "[]"},
		{APIAnthropic, "claude-3-7-sonnet-latest", EffortHigh, "[]"},
		{APIAnthropic, "my-gateway-model", EffortUltra, "[max xhigh high]"}, // unknown: try
		{APIOpenAI, "gpt-5.2", EffortUltra, "[xhigh high]"},
		{APIOpenAI, "openai/gpt-6", EffortUltra, "[xhigh high]"},
		{APIOpenAI, "gpt-5", EffortUltra, "[high]"},
		{APIOpenAI, "o3", EffortLow, "[low]"},
		{APIOpenAI, "gpt-4o", EffortHigh, "[]"}, // does not reason
		{APIOpenAI, "gpt-4.1-mini", EffortLow, "[]"},
		{APIOpenAI, "deepseek-reasoner", EffortMedium, "[medium]"},
		{APIOpenAI, "gpt-5", "", "[]"},
	}
	for _, c := range cases {
		if got := fmt.Sprint(effortLadder(c.api, c.model, c.level)); got != c.want {
			t.Errorf("%s %s %s: %s, want %s", c.api, c.model, c.level, got, c.want)
		}
	}
	if p, v, s := EffortWire(APIAnthropic, "claude-opus-5-5", EffortUltra); p != "output_config.effort" || v != "max" || s != EffortYes {
		t.Errorf("Claude: %s %s %v", p, v, s)
	}
	if p, v, s := EffortWire(APIOpenAI, "gpt-4o", EffortHigh); p != "reasoning_effort" || v != "" || s != EffortNo {
		t.Errorf("a model without reasoning: %s %q %v", p, v, s)
	}
	if _, v, s := EffortWire(APIOpenAI, "qwen3-max", EffortHigh); v != "high" || s != EffortMaybe {
		t.Errorf("an unknown model: %q %v", v, s)
	}
	for _, e := range append([]string{""}, Efforts...) {
		if !ValidEffort(e) {
			t.Errorf("%q is a level", e)
		}
	}
	if ValidEffort("max") || ValidEffort("ULTRA") {
		t.Error("only the four levels")
	}
}

// Each API gets its own parameter; a short answer thinks at low effort; a
// refused value steps down once and is remembered.
func TestEffortIsSentAndRefusalsAreLearned(t *testing.T) {
	var bodies []map[string]any
	refuse := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		sent := fmt.Sprint(body["reasoning_effort"])
		if oc, ok := body["output_config"].(map[string]any); ok {
			sent = fmt.Sprint(oc["effort"])
		}
		if refuse[sent] {
			w.WriteHeader(400)
			if strings.HasSuffix(r.URL.Path, "/messages") {
				fmt.Fprintf(w, `{"type":"error","error":{"type":"invalid_request_error","message":"output_config.effort: %q is not supported by this model"}}`, sent)
			} else {
				fmt.Fprint(w, `{"error":{"message":"Unrecognized request argument supplied: reasoning_effort","type":"invalid_request_error"}}`)
			}
			return
		}
		if strings.HasSuffix(r.URL.Path, "/messages") {
			fmt.Fprint(w, `{"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		fmt.Fprint(w, okBody())
	}))
	defer srv.Close()
	ask := func(c *Client, maxTokens int) []string {
		bodies = nil
		if _, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "x"}}, MaxTokens: maxTokens}); err != nil {
			t.Fatal(err)
		}
		var sent []string
		for _, b := range bodies {
			v := fmt.Sprint(b["reasoning_effort"])
			if oc, ok := b["output_config"].(map[string]any); ok {
				v = fmt.Sprint(oc["effort"])
			}
			sent = append(sent, v)
		}
		return sent
	}

	c := fastClient(srv.URL)
	c.Model = "gpt-5.2"
	if got := fmt.Sprint(ask(c, 0)); got != "[<nil>]" {
		t.Errorf("no effort chosen, none sent: %s", got)
	}
	c.Effort = EffortUltra
	if got := fmt.Sprint(ask(c, 0)); got != "[xhigh]" {
		t.Errorf("OpenAI's ultracode: %s", got)
	}
	if got := fmt.Sprint(ask(c, 300)); got != "[low]" {
		t.Errorf("a short answer thinks briefly: %s", got)
	}
	refuse["xhigh"] = true
	if got := fmt.Sprint(ask(c, 0)); got != "[xhigh high]" {
		t.Errorf("a refused xhigh steps down to high: %s", got)
	}
	if got := fmt.Sprint(ask(c, 0)); got != "[high]" {
		t.Errorf("and remembers it: %s", got)
	}

	c = fastClient(srv.URL)
	c.Model, c.Effort = "deepseek-chat", EffortMedium
	refuse["medium"] = true
	if got := fmt.Sprint(ask(c, 0)); got != "[medium <nil>]" {
		t.Errorf("a model that takes no effort goes again without it: %s", got)
	}
	if got := fmt.Sprint(ask(c, 0)); got != "[<nil>]" {
		t.Errorf("and never gets it again: %s", got)
	}

	cl := fastClient(srv.URL)
	cl.API, cl.Model, cl.Effort = APIAnthropic, "claude-opus-5-5", EffortUltra
	refuse = map[string]bool{"max": true}
	if got := fmt.Sprint(ask(cl, 0)); got != "[max xhigh]" {
		t.Errorf("Claude's ultracode: max, refused, then xhigh: %s", got)
	}
	if bodies[len(bodies)-1]["max_tokens"] != float64(16000) {
		t.Errorf("a non-streamed answer at high effort has room to think: %v", bodies[len(bodies)-1]["max_tokens"])
	}
	cl = fastClient(srv.URL)
	cl.API, cl.Model, cl.Effort = APIAnthropic, "claude-sonnet-4-5", EffortHigh
	if got := fmt.Sprint(ask(cl, 0)); got != "[<nil>]" {
		t.Errorf("a Claude model before effort gets none: %s", got)
	}
}

// TestAnEffortCapLowersOnlyAChosenEffort: WithEffortCap lowers the effort
// sent for a light job; it never raises one, and a model left to its own
// default still gets nothing.
func TestAnEffortCapLowersOnlyAChosenEffort(t *testing.T) {
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent = append(sent, fmt.Sprint(body["reasoning_effort"]))
		fmt.Fprint(w, okBody())
	}))
	defer srv.Close()
	c := fastClient(srv.URL)
	c.Model = "gpt-5.2"
	ask := func(ctx context.Context) string {
		sent = nil
		if _, err := c.Complete(ctx, Request{Messages: []Message{{Role: "user", Content: "x"}}}); err != nil {
			t.Fatal(err)
		}
		return sent[0]
	}
	c.Effort = EffortHigh
	if got := ask(WithEffortCap(context.Background(), EffortLow)); got != "low" {
		t.Errorf("capped: %s", got)
	}
	if got := ask(WithEffortCap(context.Background(), EffortUltra)); got != "high" {
		t.Errorf("a cap never raises: %s", got)
	}
	if got := ask(context.Background()); got != "high" {
		t.Errorf("no cap: %s", got)
	}
	if got := ask(WithEffortCap(WithEffortCap(context.Background(), EffortLow), "")); got != "high" {
		t.Errorf("a lifted cap: %s", got)
	}
	c.Effort = ""
	if got := ask(WithEffortCap(context.Background(), EffortLow)); got != "<nil>" {
		t.Errorf("the model's own default stays: %s", got)
	}
}

// TestAGatewaysThinkingRefusalDropsTheEffort: a gateway that turns
// reasoning_effort into its model's own switch passes on the model's
// refusal («property 'enable_thinking' is unsupported»). The request goes
// again without an effort, and later ones go without one.
func TestAGatewaysThinkingRefusalDropsTheEffort(t *testing.T) {
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent = append(sent, fmt.Sprint(body["reasoning_effort"]))
		if body["reasoning_effort"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"[400]: {\"error\":{\"message\":\"property 'enable_thinking' is unsupported\",\"type\":\"invalid_request_error\"}}\n","type":"invalid_request_error","code":"bad_request"}}`)
			return
		}
		fmt.Fprint(w, okBody())
	}))
	defer srv.Close()
	c := fastClient(srv.URL)
	c.Effort = EffortHigh
	for i := 0; i < 2; i++ {
		if _, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if strings.Join(sent, ",") != "high,<nil>,<nil>" {
		t.Fatalf("sent: %v", sent)
	}
}
