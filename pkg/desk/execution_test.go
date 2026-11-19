package desk

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubExecutionHost struct {
	Host
	mode      string
	sandboxed bool
	failErr   error
}

func (h *stubExecutionHost) Info() Info {
	return Info{
		ExecutionMode: h.mode,
		Sandboxed:     h.sandboxed,
	}
}

func (h *stubExecutionHost) SetExecutionMode(mode string) (string, error) {
	if h.failErr != nil {
		return "", h.failErr
	}
	h.mode = mode
	h.sandboxed = mode == "sandbox"
	return "", nil
}

func TestExecutionAPI_SuccessAndFailClosed(t *testing.T) {
	host := &stubExecutionHost{mode: "best-effort"}
	s := &Server{host: host}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/execution", strings.NewReader(`{"mode":"local"}`))
	w := httptest.NewRecorder()
	s.executionAPI(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Info Info `json:"info"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Info.ExecutionMode != "local" || resp.Info.Sandboxed {
		t.Fatalf("unexpected info payload: %+v", resp.Info)
	}

	// Simulate fail-closed rejection when strict sandbox is unavailable.
	host.failErr = errors.New("bwrap is not installed, but strict sandbox mode is required")
	req2 := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/execution", strings.NewReader(`{"mode":"sandbox"}`))
	w2 := httptest.NewRecorder()
	s.executionAPI(w2, req2)

	if w2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request on fail-closed error, got %d", w2.Code)
	}
	if host.mode != "local" {
		t.Fatalf("expected previous mode %q to be preserved, got %q", "local", host.mode)
	}
}
