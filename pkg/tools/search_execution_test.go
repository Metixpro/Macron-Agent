package tools

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchToolsExecuteThroughRegistry(t *testing.T) {
	var site *httptest.Server
	site = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch req.URL.Path {
		case "/lite":
			fmt.Fprintf(w, `<table><tr><td><a class="result-link" href="%s/song">Sample search result</a></td></tr><tr><td class="result-snippet">A sample song and documentation result</td></tr></table>`, site.URL)
		case "/song":
			fmt.Fprintf(w, `<html><head><title>Sample song</title></head><body><h1>Sample song</h1><p>%s</p><audio src="/sample.mp3"></audio></body></html>`, strings.Repeat("A detailed sample page about the song and its recording. ", 20))
		default:
			http.NotFound(w, req)
		}
	}))
	defer site.Close()
	r := newRegistry(t, true, nil)
	r.Search.SetEndpoints(site.URL+"/lite", site.URL+"/html")
	for _, name := range []string{"web_search", "search_music"} {
		t.Run(name, func(t *testing.T) {
			out := call(t, r, "search", name, map[string]any{"query": "Sample song", "read_top": 1})
			if !strings.Contains(out, "Sample search result") || !strings.Contains(out, site.URL+"/song") {
				t.Fatalf("search result lost: %s", out)
			}
			if name == "search_music" && !strings.Contains(out, site.URL+"/sample.mp3") {
				t.Fatalf("music URL lost: %s", out)
			}
			if empty := call(t, r, "search", name, map[string]any{}); empty != "error: query is required" {
				t.Fatalf("empty query: %s", empty)
			}
		})
	}
}
