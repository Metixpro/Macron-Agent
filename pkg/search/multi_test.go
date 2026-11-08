package search

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func bingLink(target string) string {
	return "https://www.bing.com/ck/a?!&amp;&amp;p=abc&amp;u=a1" + base64.RawURLEncoding.EncodeToString([]byte(target)) + "&amp;ntb=1"
}

func bingPage(rows ...[2]string) string {
	var sb strings.Builder
	sb.WriteString(`<html><body><ol id="b_results">`)
	for _, r := range rows {
		fmt.Fprintf(&sb, `<li class="b_algo"><div class="b_tpcn"><a class="tilk" href="%s">site</a></div><h2><a href="%s">%s</a></h2>`+
			`<div class="b_caption"><p class="b_lineclamp2">About %s</p></div></li>`, bingLink(r[0]), bingLink(r[0]), r[1], r[1])
	}
	sb.WriteString(`</ol></body></html>`)
	return sb.String()
}

const ddgLite = `<html><body><table>
<tr><td><a class='result-link' href="https://go.dev/doc/tutorial/generics?utm_source=ddg">Tutorial: generics in golang</a></td></tr>
<tr><td class='result-snippet'>Getting started with generics</td></tr>
<tr><td><a class='result-link' href="https://blog.example/golang-generics">Golang generics explained</a></td></tr>
<tr><td class='result-snippet'>A blog post</td></tr>
<tr><td><a class='result-link' href="https://go.dev/blog/intro-generics">An introduction to generics</a></td></tr>
<tr><td><a class='result-link' href="https://go.dev/ref/spec#generics">The spec on generics</a></td></tr>
</table></body></html>`

type fakeEngines struct {
	srv                       *httptest.Server
	bing, bingNews, ddg, wiki atomic.Int32
	bingBody                  func() string
	bingDelay                 time.Duration
}

func newFakeEngines(t *testing.T) *fakeEngines {
	f := &fakeEngines{}
	f.bingBody = func() string {
		return bingPage([2]string{"https://www.go.dev/doc/tutorial/generics/", "Tutorial: Getting started with generics"},
			[2]string{"https://golangbyexample.com/generics", "Golang generics by example"},
			[2]string{"https://go.dev/blog/when-generics", "When to use generics"})
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lite":
			f.ddg.Add(1)
			w.Write([]byte(ddgLite))
		case "/bing":
			f.bing.Add(1)
			time.Sleep(f.bingDelay)
			w.Write([]byte(f.bingBody()))
		case "/bingnews":
			f.bingNews.Add(1)
			if r.URL.Query().Get("qft") != `interval="8"` {
				t.Errorf("the week's news: %q", r.URL.RawQuery)
			}
			old := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC1123)
			fresh := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC1123)
			fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0" xmlns:News="https://www.bing.com/news/search?q=x"><channel>
<item><title>Old dollar story</title><link>http://www.bing.com/news/apiclick.aspx?ref=FexRss&amp;url=https%%3a%%2f%%2fold.example%%2fa&amp;c=1</link><description>dollar then</description><pubDate>%s</pubDate><News:Source>Old Times</News:Source></item>
<item><title>Dollar rises today</title><link>http://www.bing.com/news/apiclick.aspx?ref=FexRss&amp;url=https%%3a%%2f%%2fnews.example%%2fdollar&amp;c=2</link><description>&lt;b&gt;dollar&lt;/b&gt; up</description><pubDate>%s</pubDate><News:Source>News Example</News:Source></item>
</channel></rss>`, old, fresh)
		case "/gnews":
			mid := time.Now().Add(-5 * time.Hour).UTC().Format(time.RFC1123)
			fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0"><channel><item><title>Dollar steady - Daily Paper</title>
<link>https://news.google.com/rss/articles/CBMi123</link><pubDate>%s</pubDate><source url="https://daily.example">Daily Paper</source></item></channel></rss>`, mid)
		case "/ia":
			if r.URL.Query().Get("q") == "Milad Tower" {
				w.Write([]byte(`{"Heading":"Milad Tower","AbstractText":"The Milad Tower is a tower in Tehran.","AbstractURL":"https://en.wikipedia.org/wiki/Milad_Tower","AbstractSource":"Wikipedia",
					"Infobox":{"content":[{"label":"Height","value":"435 m"},{"label":"Coordinates","value":{"lat":1}}]}}`))
				return
			}
			w.Write([]byte(`{"Heading":"","AbstractText":""}`))
		case "/wiki/fa":
			f.wiki.Add(1)
			w.Write([]byte(`{"query":{"pages":[{"title":"برج آزادی","extract":"برج آزادی نماد تهران است.","fullurl":"https://fa.wikipedia.org/wiki/x"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeEngines) engine() *Engine {
	e := testEngine()
	e.SetEndpoints(f.srv.URL+"/lite", "")
	e.SetEngines(f.srv.URL+"/bing", f.srv.URL+"/bingnews", f.srv.URL+"/gnews", f.srv.URL+"/ia", f.srv.URL+"/wiki")
	return e
}

func TestSearchMergesTheEngines(t *testing.T) {
	f := newFakeEngines(t)
	e := f.engine()
	rep, err := e.SearchWith(context.Background(), "golang generics", SearchOptions{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	var urls []string
	for _, r := range rep.Results {
		urls = append(urls, r.URL)
	}
	if len(urls) != 5 || urls[0] != "https://go.dev/doc/tutorial/generics?utm_source=ddg" || strings.Count(strings.Join(urls, " "), "tutorial/generics") != 1 {
		t.Fatalf("the page both engines found leads, once: %v", urls)
	}
	goDev := 0
	for _, u := range urls[:4] {
		if strings.Contains(u, "go.dev") {
			goDev++
		}
	}
	if goDev > 2 {
		t.Errorf("one site fills the top: %v", urls)
	}
	if len(rep.Engines) != 2 || rep.Results[0].Snippet != "About Tutorial: Getting started with generics" {
		t.Errorf("engines %v, the longer snippet kept: %+v", rep.Engines, rep.Results[0])
	}

	before := f.bing.Load()
	if _, err := e.SearchWith(context.Background(), "golang generics", SearchOptions{Limit: 5}); err != nil || f.bing.Load() != before {
		t.Errorf("cached: %v %d", err, f.bing.Load()-before)
	}
}

func TestSearchLeavesOutAnEngineThatAnswersWithAnything(t *testing.T) {
	f := newFakeEngines(t)
	f.bingBody = func() string {
		return bingPage([2]string{"https://www.microsoft.com/en-us/", "Microsoft - Official Home Page"},
			[2]string{"https://account.microsoft.com/", "Microsoft account"}, [2]string{"https://outlook.live.com/", "Outlook"})
	}
	e := f.engine()
	for i := range 3 {
		rep, err := e.SearchWith(context.Background(), fmt.Sprintf("golang generics %d", i), SearchOptions{Limit: 5})
		if err != nil || len(rep.Results) == 0 || strings.Contains(fmt.Sprint(rep.Results), "microsoft") {
			t.Fatalf("search %d: %v %+v", i, err, rep)
		}
	}
	if f.bing.Load() != 2 {
		t.Errorf("after two junk lists Bing rests: asked %d times", f.bing.Load())
	}
	st := e.EngineStatus()
	if len(st) != 2 || st[0].Name != "bing" || st[0].Fails != 2 || !strings.Contains(st[0].LastErr, "unrelated") || st[1].Hits != 3 {
		t.Errorf("status: %+v", st)
	}

	lists := []engineResult{{engine: webEngine{name: "x", weight: 1}, res: []SearchResult{
		{Title: "Milad Tower", URL: "https://a.example/1"}, {Title: "Milad Tower tour", URL: "https://b.example/2"}, {Title: "Tehran", URL: "https://c.example/3"}}}}
	dropUnrelated(lists, "برج میلاد", func(string) { t.Error("not junk") })
	if len(lists[0].res) != 3 {
		t.Errorf("kept: %+v", lists[0].res)
	}
}

func TestSearchDoesNotWaitForASlowEngine(t *testing.T) {
	f := newFakeEngines(t)
	f.bingDelay = 1500 * time.Millisecond
	old := searchGrace
	searchGrace = 100 * time.Millisecond
	defer func() { searchGrace = old }()
	start := time.Now()
	rep, err := f.engine().SearchWith(context.Background(), "golang generics", SearchOptions{Limit: 3})
	if err != nil || len(rep.Results) != 3 || time.Since(start) > time.Second || len(rep.Engines) != 1 {
		t.Fatalf("%v %+v in %v", err, rep, time.Since(start))
	}
}

func TestNewsSearch(t *testing.T) {
	f := newFakeEngines(t)
	rep, err := f.engine().SearchWith(context.Background(), "dollar", SearchOptions{Limit: 5, News: true, Time: "week"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 2 || rep.Results[0].URL != "https://news.example/dollar" || rep.Results[0].Source != "News Example" ||
		rep.Results[0].Snippet != "dollar up" || rep.Results[1].Title != "Dollar steady" || rep.Results[1].Source != "Daily Paper" {
		t.Fatalf("news: %+v", rep.Results)
	}
	if _, err := time.Parse(time.RFC3339, rep.Results[0].Date); err != nil || f.ddg.Load() != 0 || f.bing.Load() != 0 {
		t.Errorf("dated, from the news feeds only: %q %d %d", rep.Results[0].Date, f.ddg.Load(), f.bing.Load())
	}
}

func TestQuickAnswer(t *testing.T) {
	f := newFakeEngines(t)
	e := f.engine()
	a := e.quickAnswer(context.Background(), "Milad Tower")
	if a == nil || a.Source != "Wikipedia" || a.URL != "https://en.wikipedia.org/wiki/Milad_Tower" || len(a.Facts) != 1 || a.Facts[0] != "Height: 435 m" {
		t.Fatalf("instant answer: %+v", a)
	}
	if a := e.quickAnswer(context.Background(), "برج آزادی تهران"); a == nil || a.Title != "برج آزادی" || f.wiki.Load() != 1 {
		t.Fatalf("Wikipedia: %+v", a)
	}
	if a := e.quickAnswer(context.Background(), "قیمت دلار"); a != nil {
		t.Errorf("an article that is not about the query is no answer: %+v", a)
	}
}

func TestBingLinksAndAddresses(t *testing.T) {
	if got := bingTarget("https://www.bing.com/ck/a?!&&p=x&u=a1" + base64.RawURLEncoding.EncodeToString([]byte("https://fa.wikipedia.org/wiki/x?y=1")) + "&ntb=1"); got != "https://fa.wikipedia.org/wiki/x?y=1" {
		t.Errorf("decoded: %q", got)
	}
	if bingTarget("https://site.example/a") != "https://site.example/a" || bingTarget("https://www.bing.com/ck/a?u=zz") != "" {
		t.Error("plain link kept, a broken one dropped")
	}
	if normURL("https://www.Example.com/a/?utm_source=x&id=2#top") != normURL("http://example.com/a?id=2") ||
		normURL("https://m.example.com/a") != normURL("https://example.com/a") || normURL("https://example.com/a?id=2") == normURL("https://example.com/a?id=3") {
		t.Error("addresses")
	}
}
