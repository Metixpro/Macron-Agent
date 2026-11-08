package tools

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCryptoPriceFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/blocked/"):
			http.Error(w, `{"code":0,"msg":"Service unavailable from a restricted location"}`, http.StatusUnavailableForLegalReasons)
		case r.URL.Path == "/mirror/api/v3/ticker/24hr" && r.URL.Query().Get("symbol") == "BTCUSDT":
			fmt.Fprint(w, `{"symbol":"BTCUSDT","lastPrice":"83780.12","priceChangePercent":"0.92"}`)
		case r.URL.Path == "/mirror/api/v3/ticker/24hr":
			http.Error(w, `{"code":-1121,"msg":"Invalid symbol."}`, http.StatusBadRequest)
		case r.URL.Path == "/okx/api/v5/market/ticker" && r.URL.Query().Get("instId") == "NOT-USDT":
			fmt.Fprint(w, `{"code":"0","data":[{"instId":"NOT-USDT","last":"0.0021","open24h":"0.0020"}]}`)
		case r.URL.Path == "/okx/api/v5/market/ticker":
			fmt.Fprint(w, `{"code":"51001","msg":"Instrument ID does not exist","data":[]}`)
		case strings.HasPrefix(r.URL.Path, "/ramzinex/"):
			http.Error(w, "geo", http.StatusForbidden)
		case r.URL.Path == "/nobitex/market/stats":
			fmt.Fprint(w, `{"status":"ok","stats":{"usdt-rls":{"latest":"1023450","bestBuy":"1023000"}}}`)
		case r.URL.Path == "/wallex/v1/trades":
			fmt.Fprint(w, `{"result":{"latestTrades":[{"symbol":"USDTTMN","price":"99999"}]},"success":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := []string{okxAPI, ramzinexAPI, nobitexAPI, wallexAPI}
	oldBinance := binanceAPIs
	binanceAPIs = []string{srv.URL + "/blocked", srv.URL + "/mirror"}
	okxAPI, ramzinexAPI, nobitexAPI, wallexAPI = srv.URL+"/okx", srv.URL+"/ramzinex", srv.URL+"/nobitex", srv.URL+"/wallex"
	defer func() {
		binanceAPIs = oldBinance
		okxAPI, ramzinexAPI, nobitexAPI, wallexAPI = old[0], old[1], old[2], old[3]
	}()

	r := newRegistry(t, true, nil)
	out := call(t, r, "s", "crypto_currency_price", map[string]any{"symbols": "BTC, NOT, ZZZ"})
	for _, want := range []string{
		"USDT/Toman: 102,345 Toman (Nobitex)",                  // Ramzinex refused; Nobitex comes before Wallex
		"BTC: $83,780.12 (+0.92% / 24h) ≈ 8,574,476,381 Toman", // Binance refused this country: its mirror answered
		"NOT: $0.0021 (+5.00% / 24h)",                          // only OKX lists it
		"Not found: ZZZ",
		"Source: Binance, OKX",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	binanceAPIs, okxAPI = []string{srv.URL + "/blocked"}, srv.URL+"/blocked"
	if out := call(t, r, "s", "crypto_currency_price", map[string]any{"symbols": "BTC"}); !strings.Contains(out, "could not fetch prices") {
		t.Errorf("no source: %s", out)
	}
}

func TestFmtPrice(t *testing.T) {
	for in, want := range map[float64]string{83780.12: "83,780.12", 1999.999: "2,000.00", 12.345: "12.35", 0.5: "0.5000", 0.0021: "0.0021", 0.00000123: "0.00000123"} {
		if got := fmtPrice(in); got != want {
			t.Errorf("fmtPrice(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestWhoisFallsBackToRDAP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/domain/example.com" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"objectClassName":"domain","ldhName":"EXAMPLE.COM","status":["client transfer prohibited"],
			"events":[{"eventAction":"registration","eventDate":"1995-08-14T04:00:00Z"},{"eventAction":"expiration","eventDate":"2027-08-13T04:00:00Z"}],
			"entities":[{"roles":["registrar"],"vcardArray":["vcard",[["version",{},"text","4.0"],["fn",{},"text","RESERVED-Internet Assigned Numbers Authority"]]]}],
			"nameservers":[{"ldhName":"A.IANA-SERVERS.NET"},{"ldhName":"B.IANA-SERVERS.NET"}]}`)
	}))
	defer srv.Close()
	old := rdapAPI
	rdapAPI = srv.URL + "/domain/"
	defer func() { rdapAPI = old }()

	r := newRegistry(t, true, nil)
	lines := strings.Join(r.rdapLookup(t.Context(), "example.com"), "\n")
	for _, want := range []string{"Domain Name: EXAMPLE.COM", "Registrar: RESERVED-Internet Assigned Numbers Authority", "Creation Date: 1995-08-14",
		"Expiry Date: 2027-08-13", "Status: client transfer prohibited", "Name Server: a.iana-servers.net"} {
		if !strings.Contains(lines, want) {
			t.Errorf("missing %q in:\n%s", want, lines)
		}
	}
	if got := r.rdapLookup(t.Context(), "nic.ir"); got != nil {
		t.Errorf("a registry without RDAP: %v", got)
	}
}
