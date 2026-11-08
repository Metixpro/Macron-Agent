package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func fakeTreg(t *testing.T, answer func(w http.ResponseWriter, body map[string]any)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/call/treg.web.search" {
			t.Errorf("request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Treg-Token") != "tok-123" {
			w.WriteHeader(401)
			w.Write([]byte(`{"detail":"not authenticated"}`))
			return
		}
		if r.Header.Get("X-Treg-Route-Prefer") != "tinyfish" || r.Header.Get("X-Treg-Route-Max-Cost") != TregMaxCost {
			t.Errorf("free vendor first, with a cost cap: %v", r.Header)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		answer(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func tregEngine(base, token string, ddg *httptest.Server) *Engine {
	e := testEngine()
	e.Treg = NewTreg(func() string { return token })
	e.Treg.BaseURL = base
	if ddg != nil {
		e.SetEndpoints(ddg.URL+"/lite/", ddg.URL+"/html/")
	}
	return e
}

func ddgServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(liteFixture))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestTregIsAskedFirst(t *testing.T) {
	treg, _ := fakeTreg(t, func(w http.ResponseWriter, body map[string]any) {
		if body["q"] != "golang generics" || body["limit"] != float64(8) {
			t.Errorf("body: %v", body)
		}
		w.Header().Set("X-Treg-Served-By", "tinyfish.web.search")
		w.Write([]byte(`{"output":{"results":[
			{"title":"Generics in Go","url":"https://go.dev/doc/tutorial/generics","snippet":"A tutorial\nabout generics"},
			{"name":"Spec","link":"https://go.dev/ref/spec","description":"The Go spec, generics included"},
			{"title":"dup","url":"https://go.dev/ref/spec"},
			{"title":"no link"},
			{"title":"Exa row","url":"https://example.com/x","highlights":["golang one","two"]}
		]},"raw":{},"_treg":{"served_by":"tinyfish.web.search","tried":[]}}`))
	})
	ddg, ddgCalls := ddgServer(t)
	e := tregEngine(treg.URL, "tok-123", ddg)
	got, err := e.Search(context.Background(), "golang generics", 3)
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %+v", err, got)
	}
	if got[0].Title != "Generics in Go" || got[0].Snippet != "A tutorial about generics" || got[1].URL != "https://go.dev/ref/spec" ||
		got[1].Snippet != "The Go spec, generics included" || got[2].Snippet != "golang one … two" {
		t.Errorf("rows: %+v", got)
	}
	if ddgCalls.Load() == 0 {
		t.Error("DuckDuckGo is asked at the same time")
	}
	if s := e.Treg.Status(); !s.On || s.Hits != 1 || s.LastServedBy != "tinyfish.web.search" {
		t.Errorf("status: %+v", s)
	}
}

func TestDuckDuckGoAnswersWhenTregCannot(t *testing.T) {
	for name, answer := range map[string]func(w http.ResponseWriter, body map[string]any){
		"nothing found": func(w http.ResponseWriter, _ map[string]any) {
			w.Write([]byte(`{"output":{"results":[]},"_treg":{"served_by":"tinyfish.web.search"}}`))
		},
		"no balance": func(w http.ResponseWriter, _ map[string]any) {
			w.WriteHeader(402)
			w.Write([]byte(`{"error":"insufficient_balance","balance_micro":0}`))
		},
		"still running": func(w http.ResponseWriter, _ map[string]any) { w.WriteHeader(202); w.Write([]byte(`{}`)) },
		"down": func(w http.ResponseWriter, _ map[string]any) {
			w.WriteHeader(503)
			w.Write([]byte(`{"treg_saturated":true}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			treg, _ := fakeTreg(t, answer)
			ddg, ddgCalls := ddgServer(t)
			got, err := tregEngine(treg.URL, "tok-123", ddg).Search(context.Background(), "x", 5)
			if err != nil || len(got) == 0 || ddgCalls.Load() == 0 {
				t.Errorf("DuckDuckGo must answer: %v %+v", err, got)
			}
		})
	}
}

func TestNoTokenNoTreg(t *testing.T) {
	treg, calls := fakeTreg(t, func(w http.ResponseWriter, _ map[string]any) {})
	ddg, _ := ddgServer(t)
	e := tregEngine(treg.URL, "  ", ddg)
	if got, err := e.Search(context.Background(), "x", 5); err != nil || len(got) == 0 {
		t.Fatalf("%v %+v", err, got)
	}
	if calls.Load() != 0 || e.Treg.Status().On {
		t.Error("without a token treg is never called")
	}
	var nilEngine Engine
	if nilEngine.Treg.Token() != "" {
		t.Error("a nil treg has no token")
	}
}

func TestTregRefusesABadToken(t *testing.T) {
	treg, _ := fakeTreg(t, func(w http.ResponseWriter, _ map[string]any) {})
	e := tregEngine(treg.URL, "wrong", nil)
	_, _, err := e.Treg.Search(context.Background(), e.HTTPClient, "wrong", "x", 3)
	var te *TregError
	if !errors.As(err, &te) || !te.Unauthorized() || !strings.Contains(te.Error(), "not authenticated") {
		t.Errorf("%v", err)
	}
}
