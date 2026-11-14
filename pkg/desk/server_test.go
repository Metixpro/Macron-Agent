package desk

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDesktopGuardAuthHostAndWrites(t *testing.T) {
	s := &Server{token: "local-test-token"}
	handler := s.guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		name, method, host, token, csrf string
		want                            int
	}{
		{"unauthenticated", "GET", "127.0.0.1", "", "", 401},
		{"wrong token", "GET", "127.0.0.1", "wrong", "", 401},
		{"read", "GET", "127.0.0.1", s.token, "", 204},
		{"write without CSRF header", "POST", "127.0.0.1", s.token, "", 403},
		{"write", "POST", "127.0.0.1", s.token, "1", 204},
		{"untrusted host", "GET", "evil.example", s.token, "", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://"+tc.host+"/api/state", nil)
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.csrf != "" {
				r.Header.Set("X-Macron", tc.csrf)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
		})
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1/?t="+s.token, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	cookies := w.Result().Cookies()
	if w.Code != 302 || w.Header().Get("Location") != "/" || len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("token login did not set a protected cookie and remove the token URL")
	}
	r = httptest.NewRequest("GET", "http://127.0.0.1/api/state", nil)
	r.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal("authenticated cookie refused")
	}
}
