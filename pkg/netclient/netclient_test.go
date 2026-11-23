package netclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClientDefaultTimeoutAndNoTotalTimeout(t *testing.T) {
	c := NewClient(Options{})
	if c.Timeout != DefaultTimeout {
		t.Fatalf("expected DefaultTimeout=%v, got %v", DefaultTimeout, c.Timeout)
	}
	stream := NewClient(Options{NoTotalTimeout: true})
	if stream.Timeout != 0 {
		t.Fatalf("expected Timeout=0 with NoTotalTimeout, got %v", stream.Timeout)
	}
}

func TestWithRequestTimeoutClampsAndCancels(t *testing.T) {
	ctx, cancel := WithRequestTimeout(context.Background(), 0, 15, 60)
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline on default timeout")
	}
	rem := time.Until(dl)
	if rem < 13*time.Second || rem > 16*time.Second {
		t.Fatalf("unexpected default remaining duration: %v", rem)
	}

	ctx2, cancel2 := WithRequestTimeout(context.Background(), 999, 15, 60)
	defer cancel2()
	dl2, _ := ctx2.Deadline()
	if time.Until(dl2) > 61*time.Second {
		t.Fatalf("expected timeout clamped to 60s, got %v", time.Until(dl2))
	}
}

func TestLinkRequestTimesOutOnSlowServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
			_, _ = w.Write([]byte("too late"))
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	c := NewClient(Options{Timeout: 200 * time.Millisecond})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	_, err := c.Do(req)
	if err == nil {
		t.Fatal("expected timeout error from slow server")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "timeout") && !strings.Contains(strings.ToLower(err.Error()), "deadline") {
		t.Fatalf("expected timeout/deadline error, got: %v", err)
	}
}
