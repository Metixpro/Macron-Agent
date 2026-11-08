package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExecRunsToolRoundWithoutTelegram(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MACRON_HOME", home)
	t.Setenv("MACRON_ENV_FILE", "")
	t.Setenv("MACRON_MODEL", "local-test")
	t.Setenv("MACRON_API_KEY", "test-key")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing configured credential")
		}
		var req struct {
			Messages []struct{ Role, Content string }           `json:"messages"`
			Tools    []struct{ Function struct{ Name string } } `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if len(req.Tools) != 1 || req.Tools[0].Function.Name != "write_file" {
			t.Errorf("unexpected offered tools: %+v", req.Tools)
		}
		message := map[string]any{"role": "assistant", "content": "Verified local task."}
		if calls.Add(1) == 1 {
			message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "write-1", "type": "function", "function": map[string]any{"name": "write_file", "arguments": `{"filename":"proof.txt","content":"actual tool execution"}`}}}}
		} else {
			found := false
			for _, m := range req.Messages {
				if m.Role == "tool" {
					found = true
				}
			}
			if !found {
				t.Error("tool result was not returned to the model")
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": "stop"}}})
	}))
	defer srv.Close()
	t.Setenv("MACRON_API_URL", srv.URL+"/v1")
	term, out := testIO("")
	handled, code := Run([]string{"exec", "--tools", "write_file", "write proof.txt and verify it"}, term)
	if !handled || code != 0 {
		t.Fatalf("exec: %d %s", code, out)
	}
	if !strings.Contains(out.String(), "Verified local task.") {
		t.Fatalf("missing reply: %s", out)
	}
	paths, err := filepath.Glob(filepath.Join(home, "workspaces", "exec-*", "proof.txt"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("actual tool output missing: %v %v", paths, err)
	}
	b, err := os.ReadFile(paths[0])
	if err != nil || string(b) != "actual tool execution" {
		t.Fatalf("output: %q %v", b, err)
	}
}

func TestExecRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{}, {"--timeout", "0s", "task"}, {"--max-steps", "0", "task"}} {
		term, _ := testIO("")
		if execTask(term, args) != 2 {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestExecDeadlineReturnsFailure(t *testing.T) {
	t.Setenv("MACRON_HOME", t.TempDir())
	t.Setenv("MACRON_ENV_FILE", "")
	t.Setenv("MACRON_MODEL", "local-test")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	}))
	defer srv.Close()
	t.Setenv("MACRON_API_URL", srv.URL)
	term, out := testIO("")
	if code := execTask(term, []string{"--timeout", "30ms", "test deadline"}); code != 1 {
		t.Fatalf("deadline: code=%d output=%s", code, out)
	}
}
