package search

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

var longText = strings.Repeat("Waze reads blocked pages through a reader. ", 20)

func site(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/blocked":
			w.WriteHeader(403)
			w.Write([]byte("<html><title>Attention Required! | Cloudflare</title></html>"))
		case "/challenge":
			w.Write([]byte(`<html><head><title>Just a moment...</title><script src="/cdn-cgi/challenge-platform/x.js"></script></head><body>Checking your browser</body></html>`))
		case "/spa":
			w.Write([]byte(`<html><body><div id="root"></div><script src="/app.js"></script></body></html>`))
		case "/short":
			w.Write([]byte(`<html><body><p>Short and real.</p></body></html>`))
		case "/missing":
			w.WriteHeader(404)
		default:
			w.Write([]byte("<html><body><p>" + longText + "</p></body></html>"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func reader(t *testing.T, ok bool) (*httptest.Server, *atomic.Int32) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !ok {
			w.WriteHeader(422)
			return
		}
		target := strings.TrimPrefix(r.URL.Path, "/")
		w.Write([]byte("Title: Read by the reader\n\nURL Source: " + target + "\n\nMarkdown Content:\n" + longText))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestBlockedPagesAreReadThroughTheReader(t *testing.T) {
	s := site(t)
	rd, calls := reader(t, true)
	e := testEngine()
	e.ReaderURL = rd.URL + "/"
	for _, path := range []string{"/blocked", "/challenge", "/spa"} {
		p, err := e.Fetch(context.Background(), s.URL+path)
		if err != nil || p.Via != "reader" || p.Title != "Read by the reader" || !strings.Contains(p.Text, "blocked pages") {
			t.Errorf("%s: %+v %v", path, p, err)
		}
	}
	text, err := e.FetchPage(context.Background(), s.URL+"/blocked", 8000)
	if err != nil || !strings.Contains(text, "read through a reader") {
		t.Errorf("the model is told how the page was read: %v\n%s", err, text)
	}
	before := calls.Load()
	for _, path := range []string{"/", "/short"} {
		if p, err := e.Fetch(context.Background(), s.URL+path); err != nil || p.Via != "" {
			t.Errorf("%s is read directly: %+v %v", path, p, err)
		}
	}
	if _, err := e.Fetch(context.Background(), s.URL+"/missing"); err == nil || calls.Load() != before {
		t.Errorf("a missing page is not retried elsewhere: %v", err)
	}
}

func TestTregReadsWhenTheReaderCannot(t *testing.T) {
	s := site(t)
	rd, _ := reader(t, false)
	treg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/call/treg.web.extract" || r.Header.Get("X-Treg-Token") != "tok" || r.Header.Get("X-Treg-Route-Prefer") != "tinyfish" {
			t.Errorf("request: %s %v", r.URL.Path, r.Header)
		}
		w.Write([]byte(`{"output":{"title":"Via treg","url":"x","markdown":"` + longText + `"},"_treg":{"served_by":"tinyfish.web.fetch"}}`))
	}))
	t.Cleanup(treg.Close)
	e := testEngine()
	e.ReaderURL = rd.URL + "/"
	e.Treg = NewTreg(func() string { return "tok" })
	e.Treg.BaseURL = treg.URL
	p, err := e.Fetch(context.Background(), s.URL+"/blocked")
	if err != nil || p.Via != "treg" || p.Title != "Via treg" || !strings.Contains(p.Text, "blocked pages") {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestTheBrowserIsTheLastResort(t *testing.T) {
	s := site(t)
	e := testEngine()
	var rendered string
	e.Render = func(ctx context.Context, url string) (*Page, error) {
		rendered = url
		return &Page{Title: "Rendered", Text: longText}, nil
	}
	p, err := e.Fetch(context.Background(), s.URL+"/spa")
	if err != nil || p.Via != "browser" || rendered != s.URL+"/spa" {
		t.Fatalf("%+v %v", p, err)
	}

	e.Render = func(ctx context.Context, url string) (*Page, error) { return nil, errors.New("no") }
	if _, err := e.Fetch(context.Background(), s.URL+"/blocked"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("the original error: %v", err)
	}
}
