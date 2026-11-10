package netclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateURL(t *testing.T) {
	for _, ok := range []string{"http://example.com", "https://example.com/a?b=c", " https://x.org "} {
		if _, err := ValidateURL(ok); err != nil {
			t.Errorf("%q should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ftp://x.com", "file:///etc/passwd", "javascript:alert(1)", "//example.com", "http://", "gopher://x"} {
		if _, err := ValidateURL(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestAStalledBodyIsGivenUp(t *testing.T) {
	old := bodyIdle
	bodyIdle = 200 * time.Millisecond
	defer func() { bodyIdle = old }()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("start"))
		w.(http.Flusher).Flush()
		<-release
	}))
	defer srv.Close()
	defer close(release)
	resp, err := NewClient(Options{}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = io.ReadAll(resp.Body)
	if err == nil || !strings.Contains(err.Error(), "stopped sending") || time.Since(start) > 3*time.Second {
		t.Fatalf("a stalled body: %v after %s", err, time.Since(start))
	}
}

func TestAClientReachesALocalService(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	resp, err := NewClient(Options{}).Get(srv.URL)
	if err != nil {
		t.Fatalf("a client reaches a local service: %v", err)
	}
	resp.Body.Close()
}

func TestRedirectToAnOddSchemeFails(t *testing.T) {
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
	}))
	defer redir.Close()
	if _, err := NewClient(Options{}).Get(redir.URL); err == nil {
		t.Fatal("redirect to file:// must fail")
	}
}
