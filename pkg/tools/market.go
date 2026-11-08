package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// currency_gold_price: the free-market rates of Tehran in Toman (the dollar,
// euro, dirham, pound…), 18 and 24 karat gold, the mithqal, the world ounce
// and every coin. tgju.org answers first (two mirrors, with the day's
// change); bonbast.com, a separate source, fills in when it does not. Rates
// are kept for a minute, so a busy group does not ask the sources again and
// again.

const (
	ToolMarketPrice = "currency_gold_price"
	marketTTL       = time.Minute
)

var (
	tgjuAPIs   = []string{"https://call1.tgju.org/ajax.json", "https://call2.tgju.org/ajax.json"}
	bonbastURL = "https://bonbast.com"
	bonbastRe  = regexp.MustCompile(`param:\s*"([^"]{8,200})"`)
)

type marketItem struct {
	key, fa, code, emoji string
	tgju, bonbast        string
	usd                  bool // priced in dollars, not Toman
}

var marketItems = []marketItem{
	{"usd", "دلار آمریکا", "USD", "💵", "price_dollar_rl", "usd1", false},
	{"eur", "یورو", "EUR", "💶", "price_eur", "eur1", false},
	{"aed", "درهم امارات", "AED", "🇦🇪", "price_aed", "aed1", false},
	{"gbp", "پوند انگلیس", "GBP", "💷", "price_gbp", "gbp1", false},
	{"try", "لیر ترکیه", "TRY", "🇹🇷", "price_try", "try1", false},
	{"cny", "یوان چین", "CNY", "🇨🇳", "price_cny", "cny1", false},
	{"cad", "دلار کانادا", "CAD", "🇨🇦", "price_cad", "cad1", false},
	{"aud", "دلار استرالیا", "AUD", "🇦🇺", "price_aud", "aud1", false},
	{"usdt", "تتر", "USDT", "₮", "crypto-tether-irr", "", false},
	{"gold18", "طلای ۱۸ عیار (هر گرم)", "", "🥇", "geram18", "gol18", false},
	{"gold24", "طلای ۲۴ عیار (هر گرم)", "", "🥇", "geram24", "", false},
	{"mithqal", "مثقال طلا (آبشده)", "", "🥇", "mesghal", "mithqal", false},
	{"ounce", "انس جهانی طلا", "", "🌍", "ons", "ounce", true},
	{"emami", "سکه امامی", "", "🪙", "sekee", "emami1", false},
	{"bahar", "سکه تمام بهار آزادی", "", "🪙", "sekeb", "azadi1", false},
	{"nim", "نیم سکه", "", "🪙", "nim", "azadi1_2", false},
	{"rob", "ربع سکه", "", "🪙", "rob", "azadi1_4", false},
	{"gerami", "سکه گرمی", "", "🪙", "gerami", "azadi1g", false},
}

var marketGroups = map[string][]string{
	"default":    {"usd", "eur", "aed", "gbp", "gold18", "mithqal", "ounce", "emami", "bahar", "nim", "rob", "gerami"},
	"currencies": {"usd", "eur", "aed", "gbp", "try", "cny", "cad", "aud", "usdt"},
	"gold":       {"gold18", "gold24", "mithqal", "ounce"},
	"coins":      {"emami", "bahar", "nim", "rob", "gerami"},
}

// quote is one rate: Toman (or dollars for the ounce) and the day's change.
type quote struct {
	value, change float64
	hasChange     bool
}

type marketSnapshot struct {
	quotes  map[string]quote // by item key
	source  string
	updated string // the source's own time, when it gives one
	at      time.Time
}

type marketCache struct {
	mu    sync.Mutex
	board *marketSnapshot
}

func (r *Registry) registerMarketTool() {
	r.add(ToolMarketPrice,
		"Live Tehran free-market rates in Toman: dollar, euro, dirham, pound and more; 18/24k gold, mithqal, the world ounce; Emami, "+
			"Bahar Azadi, half, quarter and gram coins.",
		map[string]any{"items": prop("string", "e.g. 'usd,gold18,emami' or currencies, gold, coins, all")},
		nil, r.marketPrice)
}

func (r *Registry) marketPrice(ctx context.Context, _ string, args map[string]any) (string, error) {
	keys, unknown := marketKeys(strArg(args, "items"))
	if len(keys) == 0 {
		return "error: unknown items " + strings.Join(unknown, ", ") + "; use usd, eur, aed, gbp, try, cny, cad, aud, usdt, gold18, gold24, " +
			"mithqal, ounce, emami, bahar, nim, rob, gerami, or currencies, gold, coins, all", nil
	}
	board, err := r.marketSnapshot(ctx, keys)
	if err != nil {
		return "could not fetch the rates right now (" + err.Error() + "); try again in a minute", nil
	}
	return formatMarket(board, keys, unknown), nil
}

// marketKeys reads the requested items, groups and Persian names included.
func marketKeys(raw string) (keys, unknown []string) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return marketGroups["default"], nil
	}
	aliases := map[string]string{"dollar": "usd", "دلار": "usd", "euro": "eur", "یورو": "eur", "dirham": "aed", "درهم": "aed",
		"pound": "gbp", "پوند": "gbp", "lira": "try", "لیر": "try", "yuan": "cny", "تتر": "usdt", "tether": "usdt", "gold": "gold18",
		"طلا": "gold18", "مثقال": "mithqal", "انس": "ounce", "امامی": "emami", "بهار": "bahar", "نیم": "nim", "ربع": "rob",
		"گرمی": "gerami", "سکه": "coins", "ارز": "currencies"}
	seen := map[string]bool{}
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '،' || r == ';' }) {
		if a, ok := aliases[part]; ok {
			part = a
		}
		switch {
		case part == "all":
			for _, it := range marketItems {
				add(it.key)
			}
		case marketGroups[part] != nil:
			for _, k := range marketGroups[part] {
				add(k)
			}
		case marketItemByKey(part) != nil:
			add(part)
		default:
			unknown = append(unknown, part)
		}
	}
	return keys, unknown
}

func marketItemByKey(key string) *marketItem {
	for i := range marketItems {
		if marketItems[i].key == key {
			return &marketItems[i]
		}
	}
	return nil
}

// marketSnapshot returns fresh rates: from the cache when they are less than
// a minute old and hold every item asked for, otherwise from the sources.
func (r *Registry) marketSnapshot(ctx context.Context, keys []string) (*marketSnapshot, error) {
	r.market.mu.Lock()
	defer r.market.mu.Unlock()
	if s := r.market.board; s != nil && time.Since(s.at) < marketTTL && s.has(keys) {
		return s, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	bonbast := make(chan *marketSnapshot, 1)
	go func() { bonbast <- r.bonbastRates(ctx) }() // asked at once: it is slower, and tgju may not answer
	board := r.tgjuRates(ctx)
	if board == nil || !board.has(keys) {
		if b := <-bonbast; b != nil {
			if board == nil {
				board = b
			} else {
				for k, q := range b.quotes {
					if _, ok := board.quotes[k]; !ok {
						board.quotes[k] = q
					}
				}
				board.source += " + " + b.source
			}
		}
	}
	if board == nil {
		return nil, errors.New("tgju.org and bonbast.com did not answer")
	}
	r.market.board = board
	return board, nil
}

func (s *marketSnapshot) has(keys []string) bool {
	for _, k := range keys {
		if _, ok := s.quotes[k]; !ok {
			return false
		}
	}
	return true
}

func (r *Registry) tgjuRates(ctx context.Context) *marketSnapshot {
	for _, api := range tgjuAPIs {
		var data struct {
			Current map[string]struct {
				P  any    `json:"p"`
				DP any    `json:"dp"`
				DT string `json:"dt"`
				TS string `json:"ts"`
			} `json:"current"`
		}
		if err := r.getJSONWithin(ctx, 8*time.Second, api, &data); err != nil || len(data.Current) == 0 {
			continue
		}
		board := &marketSnapshot{quotes: map[string]quote{}, source: "tgju.org", at: time.Now()}
		for _, it := range marketItems {
			c, ok := data.Current[it.tgju]
			if !ok {
				continue
			}
			v := marketNumber(c.P)
			if v <= 0 {
				continue
			}
			if !it.usd {
				v /= 10 // rial → Toman
			}
			q := quote{value: v}
			if dp := marketNumber(c.DP); c.DT == "high" || c.DT == "low" {
				q.change, q.hasChange = dp, true
				if c.DT == "low" {
					q.change = -dp
				}
			}
			board.quotes[it.key] = q
			if it.key == "usd" && len(c.TS) >= 16 {
				board.updated = c.TS[11:16]
			}
		}
		if len(board.quotes) > 0 {
			return board
		}
	}
	return nil
}

// bonbastRates reads bonbast.com, whose page carries the token its JSON asks for.
func (r *Registry) bonbastRates(ctx context.Context) *marketSnapshot {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, bonbastURL+"/", nil)
	req.Header.Set("User-Agent", browserUA)
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil
	}
	page, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	resp.Body.Close()
	m := bonbastRe.FindSubmatch(page)
	if resp.StatusCode != http.StatusOK || m == nil {
		return nil
	}
	form := url.Values{"param": {string(m[1])}}
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, bonbastURL+"/json", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Referer", bonbastURL+"/")
	req.Header.Set("User-Agent", browserUA)
	for _, c := range resp.Cookies() {
		req.AddCookie(c)
	}
	resp, err = r.HTTP.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var data map[string]any
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data) != nil {
		return nil
	}
	board := &marketSnapshot{quotes: map[string]quote{}, source: "bonbast.com", at: time.Now()}
	for _, it := range marketItems {
		if it.bonbast == "" {
			continue
		}
		if v := marketNumber(data[it.bonbast]); v > 0 {
			board.quotes[it.key] = quote{value: v} // already Toman
		}
	}
	if h, mi := marketNumber(data["hour"]), marketNumber(data["minute"]); h > 0 || mi > 0 {
		board.updated = fmt.Sprintf("%02d:%02d", int(h), int(mi))
	}
	if len(board.quotes) == 0 {
		return nil
	}
	return board
}

// marketNumber reads "2,722,000", "4,091.38", 1.04 or "1.04".
func marketNumber(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(x), ",", ""), 64)
		if err == nil {
			return f
		}
	}
	return 0
}

func formatMarket(s *marketSnapshot, keys, unknown []string) string {
	var sb strings.Builder
	sb.WriteString("💱 نرخ لحظه‌ای بازار آزاد تهران (تومان)\n━━━━━━━━━━━━━━━━━━\n")
	var missing []string
	group := ""
	for _, k := range keys {
		it := marketItemByKey(k)
		q, ok := s.quotes[k]
		if !ok {
			missing = append(missing, it.fa)
			continue
		}
		g := "fx"
		switch it.emoji {
		case "🥇", "🌍":
			g = "gold"
		case "🪙":
			g = "coin"
		}
		if group != "" && g != group {
			sb.WriteString("\n")
		}
		group = g
		label := it.fa
		if it.code != "" {
			label += " (" + it.code + ")"
		}
		value := faNumber(math.Round(q.value), 0)
		if it.usd {
			value = faNumber(q.value, 2) + " دلار"
		}
		fmt.Fprintf(&sb, "%s %s: %s", it.emoji, label, value)
		switch {
		case q.hasChange && q.change > 0:
			fmt.Fprintf(&sb, " ▲%s٪", faNumber(q.change, 2))
		case q.hasChange && q.change < 0:
			fmt.Fprintf(&sb, " ▼%s٪", faNumber(-q.change, 2))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("━━━━━━━━━━━━━━━━━━\n")
	if len(missing) > 0 {
		sb.WriteString("⚠️ فعلاً در دسترس نیست: " + strings.Join(missing, "، ") + "\n")
	}
	if len(unknown) > 0 {
		sb.WriteString("⚠️ ناشناخته: " + strings.Join(unknown, "، ") + "\n")
	}
	sb.WriteString("منبع: " + s.source)
	if s.updated != "" {
		sb.WriteString(" · به‌روز " + faDigits(s.updated))
	}
	return sb.String()
}

// faNumber writes a number with Persian digits, ٬ between thousands and ٫ before decimals.
func faNumber(v float64, decimals int) string {
	s := strconv.FormatFloat(v, 'f', decimals, 64)
	if decimals > 0 {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	intPart, frac, _ := strings.Cut(s, ".")
	neg := strings.HasPrefix(intPart, "-")
	intPart = strings.TrimPrefix(intPart, "-")
	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteRune('٬')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if frac != "" {
		out += "٫" + frac
	}
	if neg {
		out = "-" + out
	}
	return faDigits(out)
}

func faDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return '۰' + (r - '0')
		}
		return r
	}, s)
}
