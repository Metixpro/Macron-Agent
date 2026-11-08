package llm

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

// What every API accepts of a conversation with tools: each result right
// after the call it answers, every call answered, an id on each, and no
// tool turns at all in a request that offers no tools. Gateways that turn
// one API into another are the strictest: one that drops the calls of a
// request without tools still sends their results, and the API behind it
// answers "No tool call found for function call output with call_id …".

// normalizeTranscript returns the conversation fixed for sending; msgs is
// not changed.
func normalizeTranscript(msgs []Message, toolsOffered bool) []Message {
	out := make([]Message, 0, len(msgs)+2)
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			calls := make([]ToolCall, len(m.ToolCalls))
			copy(calls, m.ToolCalls)
			orig := make([]string, len(calls))
			seen := map[string]bool{}
			for j := range calls {
				orig[j] = calls[j].ID
				if calls[j].ID == "" || seen[calls[j].ID] {
					calls[j].ID = fmt.Sprintf("call_%d_%d", i, j)
				}
				seen[calls[j].ID] = true
				if calls[j].Type == "" {
					calls[j].Type = "function"
				}
			}
			m.ToolCalls = calls
			results := make([]*Message, len(calls))
			var between, orphans []Message
			k := i + 1
			for ; k < len(msgs) && msgs[k].Role != "assistant"; k++ {
				t := msgs[k]
				if t.Role != "tool" {
					between = append(between, t)
					continue
				}
				slot := -1
				for j := range calls {
					if results[j] == nil && t.ToolCallID != "" && (t.ToolCallID == calls[j].ID || t.ToolCallID == orig[j]) {
						slot = j
						break
					}
				}
				if slot < 0 && t.ToolCallID == "" { // no id: the first call not answered yet
					for j := range calls {
						if results[j] == nil {
							slot = j
							break
						}
					}
				}
				if slot < 0 {
					orphans = append(orphans, orphanResult(t))
					continue
				}
				t.ToolCallID = calls[slot].ID
				if t.Name == "" {
					t.Name = calls[slot].Function.Name
				}
				results[slot] = &t
			}
			out = append(out, m)
			for j, r := range results {
				if r == nil {
					r = &Message{Role: "tool", ToolCallID: calls[j].ID, Name: calls[j].Function.Name, Content: "(no result: this call did not finish)"}
				}
				out = append(out, *r)
			}
			out = append(out, orphans...)
			out = append(out, between...)
			i = k - 1
		case m.Role == "tool": // a result with no call before it
			out = append(out, orphanResult(m))
		default:
			out = append(out, m)
		}
	}
	if !toolsOffered {
		return flattenTools(out)
	}
	return out
}

// orphanResult is a tool result whose call is gone, kept as a note.
func orphanResult(t Message) Message {
	name := t.Name
	if name == "" {
		name = "a tool"
	}
	return Message{Role: "user", Content: "(Earlier result of " + name + ":)\n" + t.Content}
}

// flattenTools writes the tool calls and results as plain conversation, for
// a request that offers no tools or a gateway that cannot pair them.
func flattenTools(msgs []Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			var sb strings.Builder
			sb.WriteString(strings.TrimSpace(m.Content))
			for _, c := range m.ToolCalls {
				args := strings.TrimSpace(c.Function.Arguments)
				if utf8.RuneCountInString(args) > 300 {
					args = string([]rune(args)[:300]) + "…"
				}
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				fmt.Fprintf(&sb, "(I used %s %s)", c.Function.Name, args)
			}
			out = append(out, Message{Role: "assistant", Content: sb.String()})
		case m.Role == "tool":
			name := m.Name
			if name == "" {
				name = "the tool"
			}
			content := m.Content
			if strings.TrimSpace(content) == "" {
				content = "(no output)"
			}
			out = append(out, Message{Role: "user", Content: "(Result of " + name + ":)\n" + content})
		default:
			out = append(out, m)
		}
	}
	return out
}

// hasToolTurns reports tool calls or results in a conversation.
func hasToolTurns(msgs []Message) bool {
	for _, m := range msgs {
		if m.Role == "tool" || len(m.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

// toolPairingError recognises an API refusing how calls and results are paired.
func toolPairingError(err error) bool {
	var api *APIError
	if !errors.As(err, &api) || IsContextOverflow(err) {
		return false
	}
	body := strings.ToLower(api.Body)
	for _, p := range []string{"no tool call found", "no tool output found", "function call output", "function_call_output", "tool_call_id",
		"tool_use_id", "tool_result", "preceding message with 'tool_calls'", "tool_calls must be followed", "role 'tool'", "unexpected tool",
		"tool call id", "toolcallid", "tool_use ids", "tool_use` blocks", "messages with role tool"} {
		if strings.Contains(body, p) {
			return true
		}
	}
	return false
}

// gatewayErrorRe is an error a gateway sent as the answer's text (HTTP 200):
// "[Error] …", "[upstream error] …". No model starts an answer like that.
var gatewayErrorRe = regexp.MustCompile(`(?is)^\s*\[\s*(?:error|err|upstream error|api error|proxy error)\s*\]`)

// knownFailures are the API errors gateways pass on as text.
var knownFailures = []string{"no tool call found for function call output", "no tool output found for function call",
	"invalid 'input[", "upstream request failed", "upstream_error", "the upstream server"}

// looksLikeGatewayError reports an answer text that is really an error.
func looksLikeGatewayError(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" || len(t) > 1500 {
		return false
	}
	if gatewayErrorRe.MatchString(t) {
		return true
	}
	low := strings.ToLower(t)
	if len(t) > 400 {
		return false
	}
	for _, p := range knownFailures {
		if strings.HasPrefix(low, p) || strings.HasPrefix(low, "error: "+p) || strings.HasPrefix(low, "error "+p) {
			return true
		}
	}
	return false
}

// gatewayError turns an answer that is an error into one.
func gatewayError(resp *Response) error {
	if resp == nil || len(resp.Message.ToolCalls) > 0 || !looksLikeGatewayError(resp.Message.Content) {
		return nil
	}
	return &APIError{Status: http.StatusBadGateway, Body: strings.TrimSpace(resp.Message.Content)}
}

// errorPrefix reports streamed text that is, or may still become, an error
// passed on as text: it is not shown while it might be one.
func errorPrefix(text string) bool {
	t := strings.ToLower(strings.TrimLeft(text, " \t\r\n"))
	if t == "" {
		return false
	}
	if gatewayErrorRe.MatchString(t) {
		return true
	}
	if t[0] != '[' {
		return false
	}
	for _, w := range []string{"[error]", "[err]", "[upstream error]", "[api error]", "[proxy error]"} {
		if len(t) < len(w) && strings.HasPrefix(w, strings.ReplaceAll(t, " ", "")) {
			return true // "[Err" so far
		}
	}
	return false
}
