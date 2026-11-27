package desk

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"testing"
)

func TestRemoteProxyPreservesAuthenticatedSameOriginRequest(t *testing.T) {
	local := &Server{token: "local"}
	remote := &Server{token: "remote"}
	dst := remote.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/mode" || r.Header.Get("Cookie") != "" {
			t.Errorf("forwarded path = %q, Cookie = %q", r.URL.Path, r.Header.Get("Cookie"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	proxy := (&remotes{}).proxyFor("fixture", "127.0.0.1:3001", remote.token, nil)
	handler := local.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := r.Clone(r.Context())
		proxy.Rewrite(&httputil.ProxyRequest{In: r, Out: out})
		dst.ServeHTTP(w, out)
	}))
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:3000/remote/fixture/api/mode", nil)
	req.Header.Set("Origin", "http://127.0.0.1:3000")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Authorization", "Bearer "+local.token)
	req.Header.Set("X-Macron", "1")
	req.Header.Set("Cookie", "local-session=removed-by-proxy")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("same-origin remote request returned %d: %s", rec.Code, rec.Body.String())
	}
}
