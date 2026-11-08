package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const dollarPage = `<html><body>
<nav><a>خانه</a> <a>ارز</a> <a>طلا</a></nav>
<p>به سایت ما خوش آمدید؛ این‌جا جدیدترین اخبار اقتصادی، تحلیل بازار و قیمت‌های روز را پیدا می‌کنید.</p>
<p>قيمت دلار آزاد امروز در بازار تهران به ۹۰ هزار تومان رسید که نسبت به دیروز ۲ درصد افزایش داشته است.</p>
<p>کارشناسان می‌گویند دلیل افزایش قیمت دلار، تقاضای بالا در روزهای پایانی ماه است و این روند ادامه دارد.</p>
<p>در خبرهای ورزشی، تیم ملی والیبال برای مسابقات جهانی آماده می‌شود و تمرینات خود را آغاز کرده است.</p>
</body></html>`

func TestKeyPassagesMatchTheQuery(t *testing.T) {
	doc := `Go 1.24 was released in February 2025 with generic type aliases and faster maps.
Menu Home About Contact
The weather in Tehran is sunny this week, with temperatures reaching thirty degrees in the afternoon.
Generic type aliases let a type alias have type parameters, a long-awaited feature of the Go language.`
	got := keyPassages(doc, queryTerms("What is new in Go 1.24 generics?"))
	if len(got) != 2 || !strings.Contains(got[0], "released in February") || !strings.Contains(got[1], "type parameters") {
		t.Fatalf("%q", got)
	}

	fa := keyPassages("به سایت ما خوش آمدید و اخبار روز را این‌جا بخوانید، همه چیز درباره اقتصاد.\n"+
		"قيمت دلار آزاد امروز در بازار تهران به ۹۰ هزار تومان رسید که بیشتر از دیروز است.", queryTerms("قیمت دلار امروز چنده؟"))
	if len(fa) != 1 || !strings.Contains(fa[0], "۹۰ هزار تومان") {
		t.Fatalf("persian: %q", fa)
	}

	lead := keyPassages(strings.Repeat("A long first paragraph about something else entirely. ", 4), queryTerms("zebra"))
	if len(lead) != 1 {
		t.Errorf("lead: %q", lead)
	}
}

func TestSearchResultsCarryKeyPassages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			time.Sleep(2 * time.Second)
		case "/blocked":
			w.WriteHeader(403)
			return
		}
		w.Write([]byte(dollarPage))
	}))
	defer srv.Close()
	old := passageBudget
	passageBudget = 700 * time.Millisecond
	defer func() { passageBudget = old }()

	results := []SearchResult{{Title: "قیمت دلار", URL: srv.URL + "/dollar"}, {URL: srv.URL + "/slow"}, {URL: srv.URL + "/blocked"}, {URL: srv.URL + "/fourth"}}
	start := time.Now()
	testEngine().AddPassages(context.Background(), "قیمت دلار امروز", results, 3)
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Errorf("a slow page must not hold the search: %s", took)
	}
	p := results[0].Passages
	if len(p) != 2 || !strings.Contains(p[0], "۹۰ هزار تومان") || !strings.Contains(p[1], "تقاضای بالا") || strings.Contains(strings.Join(p, " "), "والیبال") {
		t.Errorf("the paragraphs about the dollar, in order: %q", p)
	}
	if results[1].Passages != nil || results[2].Passages != nil || results[3].Passages != nil {
		t.Error("slow, blocked and later pages have none")
	}
}
