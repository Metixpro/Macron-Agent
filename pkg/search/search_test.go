package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/text/encoding/charmap"

	"github.com/Metixpro/macron-agent/pkg/netclient"
)

func testEngine() *Engine {
	e := NewEngine(netclient.NewClient(netclient.Options{}))
	e.ReaderURL = ""                 // tests never reach the real reader service
	e.SetEngines("", "", "", "", "") // nor the real search engines
	return e
}

const liteFixture = `<html><body><table>
<tr><td valign="top">1.&nbsp;</td><td><a rel="nofollow" href="https://example.com/a" class='result-link'>First <b>Result</b></a></td></tr>
<tr><td>&nbsp;</td><td class='result-snippet'>Snippet <b>one</b> here</td></tr>
<tr><td>&nbsp;</td><td><span class='link-text'>example.com</span></td></tr>
<tr><td valign="top">2.&nbsp;</td><td><a class="result-link" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.org%2Fb%3Fx%3D1&amp;rut=abc">Second</a></td></tr>
<tr><td>&nbsp;</td><td class="result-snippet">Second snippet</td></tr>
<tr><td><a class='result-link' href="https://example.com/a">Duplicate</a></td></tr>
<tr><td><a class='result-link' href="https://duckduckgo.com/ads">Ad</a></td></tr>
<tr><td><a class='result-link' href="https://example.net/c">Third</a></td></tr>
</table></body></html>`

func TestParseLite(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(liteFixture))
	got := parseLite(doc, 10)
	if len(got) != 3 {
		t.Fatalf("want 3 results, got %d: %+v", len(got), got)
	}
	if got[0].Title != "First Result" || got[0].URL != "https://example.com/a" || got[0].Snippet != "Snippet one here" {
		t.Errorf("result 0: %+v", got[0])
	}
	if got[1].URL != "https://example.org/b?x=1" || got[1].Snippet != "Second snippet" {
		t.Errorf("redirect link not unwrapped: %+v", got[1])
	}
	if got[2].Snippet != "Third" {
		t.Errorf("missing snippet should fall back to title: %+v", got[2])
	}
	if lim := parseLite(doc, 2); len(lim) != 2 {
		t.Errorf("limit not honoured: %d", len(lim))
	}
}

func TestParseHTMLResults(t *testing.T) {
	const page = `<div class="results"><div class="result results_links"><div class="result__body">
	<h2 class="result__title"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc&rut=x">Go Docs</a></h2>
	<a class="result__snippet" href="x">The <b>Go</b> language</a></div></div></div>`
	doc, _ := html.Parse(strings.NewReader(page))
	got := parseHTMLResults(doc, 5)
	if len(got) != 1 || got[0].URL != "https://go.dev/doc" || got[0].Title != "Go Docs" || got[0].Snippet != "The Go language" {
		t.Fatalf("got %+v", got)
	}
}

func TestSearchFallsBackToHTMLEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lite":
			http.Error(w, "blocked", http.StatusForbidden)
		case "/html":
			w.Write([]byte(`<div class="result__body"><a class="result__a" href="https://ok.example/">OK test</a><a class="result__snippet">snip</a></div>`))
		}
	}))
	defer srv.Close()
	e := testEngine()
	e.liteURL, e.htmlURL = srv.URL+"/lite", srv.URL+"/html"
	got, err := e.Search(context.Background(), "test", 3)
	if err != nil || len(got) != 1 || got[0].URL != "https://ok.example/" {
		t.Fatalf("got %+v err %v", got, err)
	}
	if _, err := e.Search(context.Background(), "  ", 3); err == nil {
		t.Fatal("empty query must fail")
	}
}

func TestFetchHTMLStructureAndMedia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>My  Page</title><script>var x=1</script></head><body>
		<nav>MENU</nav><h1>Heading</h1><p>First paragraph.</p><p>Second   paragraph</p>
		<ul><li>one</li><li>two</li></ul>
		<a href="/files/song.MP3">dl</a> <a href="https://cdn.example/a.zip">zip</a> <a href="javascript:x.mp3">bad</a>
		<audio src="clip.ogg"></audio><a href="/files/song.MP3">dup</a><footer>FOOT</footer></body></html>`))
	}))
	defer srv.Close()
	p, err := testEngine().Fetch(context.Background(), srv.URL+"/dir/page")
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "My Page" {
		t.Errorf("title %q", p.Title)
	}
	for _, bad := range []string{"MENU", "FOOT", "var x"} {
		if strings.Contains(p.Text, bad) {
			t.Errorf("text should not contain %q: %q", bad, p.Text)
		}
	}
	for _, want := range []string{"Heading\n", "First paragraph.\n", "Second paragraph", "• one\n", "• two"} {
		if !strings.Contains(p.Text, want) {
			t.Errorf("text missing %q:\n%s", want, p.Text)
		}
	}
	wantMedia := []string{srv.URL + "/files/song.MP3", "https://cdn.example/a.zip", srv.URL + "/dir/clip.ogg"}
	if strings.Join(p.Media, "|") != strings.Join(wantMedia, "|") {
		t.Errorf("media = %v, want %v", p.Media, wantMedia)
	}
}

func TestFetchDecodesLegacyPersianCharset(t *testing.T) {
	text := "سلام دنيا" // Arabic yeh: the only form windows-1256 has
	enc, err := charmap.Windows1256.NewEncoder().String(text)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=windows-1256")
		w.Write([]byte("<html><body><p>" + enc + "</p></body></html>"))
	}))
	defer srv.Close()
	p, err := testEngine().Fetch(context.Background(), srv.URL)
	if err != nil || !strings.Contains(p.Text, text) {
		t.Fatalf("text %q err %v", p.Text, err)
	}
}

func TestFetchContentTypes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"a":1}`))
		case "/bin":
			w.Header().Set("Content-Type", "application/pdf")
			w.Write([]byte("%PDF-1.4"))
		case "/404":
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	e := testEngine()
	if p, err := e.Fetch(context.Background(), srv.URL+"/json"); err != nil || p.Text != `{"a":1}` {
		t.Errorf("json: %+v %v", p, err)
	}
	if _, err := e.Fetch(context.Background(), srv.URL+"/bin"); err == nil || !strings.Contains(err.Error(), "download_file") {
		t.Errorf("binary should suggest download_file, got %v", err)
	}
	if _, err := e.Fetch(context.Background(), srv.URL+"/404"); err == nil {
		t.Error("404 should fail")
	}
	if _, err := e.Fetch(context.Background(), "file:///etc/passwd"); err == nil {
		t.Error("file:// must be rejected")
	}
}

func TestFetchPageTruncatesOnRuneBoundary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<html><body><p>" + strings.Repeat("فارسی ", 500) + "</p></body></html>"))
	}))
	defer srv.Close()
	out, err := testEngine().FetchPage(context.Background(), srv.URL, 100)
	if err != nil || !strings.Contains(out, "[truncated]") {
		t.Fatalf("%q %v", out, err)
	}
	if strings.ContainsRune(out, '\uFFFD') {
		t.Fatal("truncation split a multi-byte character")
	}
}
