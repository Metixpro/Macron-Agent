package tools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFaNumber(t *testing.T) {
	for v, want := range map[float64]string{272200: "۲۷۲٬۲۰۰", 1234567890: "۱٬۲۳۴٬۵۶۷٬۸۹۰", 4091.38: "۴٬۰۹۱٫۳۸", 12: "۱۲"} {
		dec := 0
		if v != float64(int64(v)) {
			dec = 2
		}
		if got := faNumber(v, dec); got != want {
			t.Errorf("%v: %s, want %s", v, got, want)
		}
	}
}

func TestMarketPrice(t *testing.T) {
	var tgjuHits, bonbastHits atomic.Int32
	tgjuUp := true
	tgju := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		tgjuHits.Add(1)
		if !tgjuUp {
			http.Error(w, "down", 502)
			return
		}
		_, _ = w.Write([]byte(`{"current":{
			"price_dollar_rl":{"p":"2,722,000","dp":1.04,"dt":"high","ts":"2026-10-22 14:04:49"},
			"price_eur":{"p":"3,058,400","dp":0.79,"dt":"low"},
			"geram18":{"p":"271,940,000","dp":0.51,"dt":"high"},
			"ons":{"p":"4,091.38","dp":1.01,"dt":"low"},
			"sekee":{"p":"2,738,100,000","dp":0,"dt":""}}}`))
	}))
	defer tgju.Close()
	bonbast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		bonbastHits.Add(1)
		switch {
		case req.Method == http.MethodGet:
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "1"})
			_, _ = w.Write([]byte(`<script>$.post('/json', {param: "abc123def456,X,2026"}, function(json){})</script>`))
		case req.URL.Path == "/json" && req.FormValue("param") == "abc123def456,X,2026" && req.Header.Get("X-Requested-With") != "":
			_, _ = w.Write([]byte(`{"usd1":"272400","eur1":"305900","aed1":"74150","gbp1":"365200","gol18":"27175769","mithqal":"117720000",
				"ounce":"4087.55","emami1":"274000000","azadi1":"265000000","azadi1_2":"144000000","azadi1_4":"77000000","azadi1g":"39000000",
				"hour":"14","minute":"5"}`))
		default:
			http.Error(w, "bad", 400)
		}
	}))
	defer bonbast.Close()
	oldT, oldB := tgjuAPIs, bonbastURL
	tgjuAPIs, bonbastURL = []string{tgju.URL, tgju.URL}, bonbast.URL
	defer func() { tgjuAPIs, bonbastURL = oldT, oldB }()

	r := newRegistry(t, true, nil)
	out := call(t, r, "m", ToolMarketPrice, map[string]any{"items": "usd,eur,gold18,ounce,emami"})
	for _, want := range []string{"💵 دلار آمریکا (USD): ۲۷۲٬۲۰۰ ▲۱٫۰۴٪", "💶 یورو (EUR): ۳۰۵٬۸۴۰ ▼۰٫۷۹٪", "🥇 طلای ۱۸ عیار (هر گرم): ۲۷٬۱۹۴٬۰۰۰ ▲۰٫۵۱٪",
		"🌍 انس جهانی طلا: ۴٬۰۹۱٫۳۸ دلار ▼۱٫۰۱٪", "🪙 سکه امامی: ۲۷۳٬۸۱۰٬۰۰۰\n", "منبع: tgju.org · به‌روز ۱۴:۰۴"} {
		if !strings.Contains(out, want) {
			t.Errorf("misses %q:\n%s", want, out)
		}
	}
	hits := tgjuHits.Load()
	call(t, r, "m", ToolMarketPrice, map[string]any{"items": "دلار"})
	if tgjuHits.Load() != hits {
		t.Error("a minute-old answer comes from the cache")
	}

	out = call(t, r, "m", ToolMarketPrice, map[string]any{"items": "coins"})
	if !strings.Contains(out, "🪙 ربع سکه: ۷۷٬۰۰۰٬۰۰۰") || !strings.Contains(out, "🪙 سکه امامی: ۲۷۳٬۸۱۰٬۰۰۰") || !strings.Contains(out, "tgju.org + bonbast.com") {
		t.Errorf("filled in:\n%s", out)
	}

	tgjuUp = false
	r.market.board = nil
	out = call(t, r, "m", ToolMarketPrice, nil)
	if !strings.Contains(out, "💵 دلار آمریکا (USD): ۲۷۲٬۴۰۰\n") || !strings.Contains(out, "منبع: bonbast.com · به‌روز ۱۴:۰۵") || !strings.Contains(out, "🪙 سکه گرمی: ۳۹٬۰۰۰٬۰۰۰") {
		t.Errorf("the fallback:\n%s", out)
	}
	if out := call(t, r, "m", ToolMarketPrice, map[string]any{"items": "bitcoin"}); !strings.HasPrefix(out, "error: unknown items bitcoin") {
		t.Errorf("unknown: %s", out)
	}
	keys, unknown := marketKeys("سکه, طلا، usd, xyz")
	if strings.Join(keys, ",") != "emami,bahar,nim,rob,gerami,gold18,usd" || strings.Join(unknown, ",") != "xyz" {
		t.Errorf("keys %v unknown %v", keys, unknown)
	}
}
