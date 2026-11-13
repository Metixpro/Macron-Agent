package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Metixpro/macron-agent/pkg/config"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/idna"

	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

var (
	symbolRe = regexp.MustCompile(`^[A-Z0-9]{2,10}$`)
	domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9-]{2,63}$`)
	repoRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
)

func (r *Registry) registerInfoTools() {
	r.add("crypto_currency_price",
		"Live crypto prices in USD, and USDT in Toman with a Toman estimate per coin.",
		map[string]any{"symbols": prop("string", "e.g. 'BTC,ETH,TON' (default a few majors)")},
		nil, r.cryptoPrice)
	r.registerMarketTool()

	r.add("whois_lookup",
		"A domain's DNS records and registration details (whois).",
		map[string]any{"domain": prop("string", "")},
		[]string{"domain"}, r.whoisLookup)

	r.add("github_inspect",
		"A public GitHub repository: summary, README, releases or commits.",
		map[string]any{
			"repo":       prop("string", "owner/repo or a link"),
			"query_type": enumProp("", "summary", "readme", "releases", "commits"),
		}, []string{"repo"}, r.githubInspect)

	r.add(ToolEmailClient,
		"Send email via SMTP or Gmail OAuth; search/list and read Gmail messages with configured OAuth access.",
		map[string]any{
			"action":     enumProp("send", "send", "search", "list", "read"),
			"provider":   enumProp("", "smtp", "gmail"),
			"query":      prop("string", "Gmail search query (default in:inbox)"),
			"message_id": prop("string", "Gmail message ID returned by search, required for read"),
			"limit":      prop("integer", "Maximum Gmail search results, 1-20 (default 10)"),
			"to":         prop("string", "Recipient email addresses (comma separated; required for send)"),
			"subject":    prop("string", "Email subject"),
			"body":       prop("string", "Email text content"),
			"host":       prop("string", "SMTP host override"),
			"port":       prop("integer", "SMTP port (default MACRON_SMTP_PORT or 587; 465 uses implicit TLS)"),
		}, nil, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			to := strArg(args, "to")
			sub := strArg(args, "subject")
			body := strArg(args, "body")
			host := strArg(args, "host")
			port := intArg(args, "port", 0, 1, 65535)
			action := strArg(args, "action")
			if action == "" {
				action = "send"
			}
			provider := strArg(args, "provider")
			if provider != "" && provider != "smtp" && provider != "gmail" {
				return "error: email provider must be smtp or gmail", nil
			}
			gmail := provider == "gmail" || provider == "" && gmailConfigured() && host == ""
			if action != "send" {
				if !gmail {
					return "error: search/list/read requires Gmail OAuth (provider=gmail)", nil
				}
				return r.Gmail(ctx, action, to, sub, body, strArg(args, "query"), strArg(args, "message_id"), intArg(args, "limit", 10, 1, 20))
			}
			if refused := r.gateSend(ctx, sessionID, "email "+to, "subject: "+sub+"\n"+body); refused != "" {
				return refused, nil
			}
			if gmail {
				return r.Gmail(ctx, action, to, sub, body, "", "", 0)
			}
			if err := SendEmail(ctx, host, port, "", "", "", to, sub, body); err != nil {
				return "email error: " + err.Error(), nil
			}
			return fmt.Sprintf("✅ Email sent to %s with subject: %s", to, sub), nil
		})

	r.add(ToolCalendarClient,
		"Generate an iCalendar (.ics) event file for appointments and meetings.",
		map[string]any{
			"title":       prop("string", "Event title"),
			"description": prop("string", "Event details"),
			"location":    prop("string", "Event location"),
			"start":       prop("string", "Start, local time: YYYY-MM-DD HH:MM, YYYY-MM-DD or RFC3339 (default: tomorrow at this hour)"),
			"duration":    prop("string", "Duration (e.g. 1h, 30m, 1h30m; default 1h)"),
			"filename":    prop("string", "Output .ics filename (default 'event.ics')"),
		}, []string{"title"}, func(_ context.Context, sessionID string, args map[string]any) (string, error) {
			title := strArg(args, "title")
			desc := strArg(args, "description")
			loc := strArg(args, "location")
			start, err := eventStart(strArg(args, "start"))
			if err != nil {
				return "error: " + err.Error(), nil
			}
			length := time.Hour
			if d := strArg(args, "duration"); d != "" {
				if length, err = time.ParseDuration(d); err != nil || length <= 0 {
					return "error: duration must look like 1h, 30m or 1h30m", nil
				}
			}
			end := start.Add(length)
			fn := strArg(args, "filename")
			if fn == "" {
				fn = "event.ics"
			}
			ics := GenerateICalendar(title, desc, loc, start, end)
			if err := r.Machine.WriteFile(sessionID, fn, []byte(ics)); err != nil {
				return "error: " + err.Error(), nil
			}
			return fmt.Sprintf("Calendar event %q (%s, %s) saved to %s; send it so the user can add it to their calendar.", title, start.Format("2006-01-02 15:04 MST"), length, fn), nil
		})

	r.add(ToolIranMessenger,
		"Send messages to Iranian messengers: Bale (بله), Eitaa (ایتا), and Rubika (روبیکا).",
		map[string]any{
			"messenger": enumProp("bale", "bale", "eitaa", "rubika"),
			"chat_id":   prop("string", "Destination channel or user chat ID"),
			"text":      prop("string", "Message content"),
		}, []string{"messenger", "chat_id", "text"}, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			m := strArg(args, "messenger")
			cid := strArg(args, "chat_id")
			txt := strArg(args, "text")
			if refused := r.gateSend(ctx, sessionID, m+" chat "+cid, txt); refused != "" {
				return refused, nil
			}
			return r.SendIranMessenger(ctx, m, "", cid, txt)
		})

	r.add(ToolExternalMessenger,
		"Send a message to Discord or Slack (webhook or bot from config.env) or WhatsApp (Cloud API).",
		map[string]any{
			"platform": enumProp("discord", "discord", "slack", "whatsapp"),
			"text":     prop("string", "Message body"),
			"channel":  prop("string", "Slack channel ID"),
			"to":       prop("string", "WhatsApp recipient phone number"),
		}, []string{"platform", "text"}, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			p := strArg(args, "platform")
			txt := strArg(args, "text")
			ch := strArg(args, "channel")
			to := strArg(args, "to")
			if refused := r.gateSend(ctx, sessionID, strings.TrimSpace(p+" "+ch+to), txt); refused != "" {
				return refused, nil
			}
			return r.SendExternalMessenger(ctx, p, "", "", ch, to, txt)
		})

	r.add(ToolCloudStorage,
		"Upload, download, or list files on S3, MinIO, R2, or WebDAV (Nextcloud).",
		map[string]any{
			"provider":    enumProp("s3", "s3", "webdav", "minio"),
			"action":      enumProp("upload", "upload", "download", "list"),
			"remote_path": prop("string", "Remote path or key"),
			"local_path":  prop("string", "Local workspace file path"),
			"endpoint":    prop("string", "Custom S3 or WebDAV endpoint URL"),
		}, []string{"action", "remote_path"}, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			prov := strArg(args, "provider")
			act := strArg(args, "action")
			rp := strArg(args, "remote_path")
			lp := strArg(args, "local_path")
			ep := strArg(args, "endpoint")
			if act == "upload" {
				if refused := r.gateSend(ctx, sessionID, strings.TrimSpace(prov+" storage"), "upload "+lp+" → "+rp+" "+ep); refused != "" {
					return refused, nil
				}
			}
			return r.ExecuteCloudStorage(ctx, sessionID, prov, act, rp, lp, ep, "", "")
		})

	r.add(ToolTaskBoard,
		"Create or list tasks on Notion, Trello, and Todoist boards.",
		map[string]any{
			"platform":     enumProp("notion", "notion", "trello", "todoist"),
			"action":       enumProp("create_task", "create_task", "list_tasks"),
			"title":        prop("string", "Task title"),
			"container_id": prop("string", "Database ID (Notion), List ID (Trello), or Project ID (Todoist)"),
			"description":  prop("string", "Task description"),
		}, []string{"platform", "action", "title"}, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			p := strArg(args, "platform")
			act := strArg(args, "action")
			title := strArg(args, "title")
			cid := strArg(args, "container_id")
			desc := strArg(args, "description")
			if act != "list_tasks" {
				if refused := r.gateSend(ctx, sessionID, p+" board", title+"\n"+desc); refused != "" {
					return refused, nil
				}
			}
			return r.ManageTaskBoard(ctx, p, act, "", cid, "", title, desc)
		})
}

var (
	binanceAPIs = []string{"https://api.binance.com", "https://data-api.binance.vision"}
	okxAPI      = "https://www.okx.com"
	ramzinexAPI = "https://publicapi.ramzinex.com"
	nobitexAPI  = "https://api.nobitex.ir"
	wallexAPI   = "https://api.wallex.ir"
)

type coinPrice struct {
	price, change float64
	source        string
}

func (r *Registry) getJSONWithin(ctx context.Context, limit time.Duration, rawURL string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	return r.getJSON(ctx, rawURL, out)
}

func (r *Registry) coinPriceOf(ctx context.Context, sym string) *coinPrice {
	for _, base := range binanceAPIs {
		var t struct {
			LastPrice          string `json:"lastPrice"`
			PriceChangePercent string `json:"priceChangePercent"`
		}
		if r.getJSONWithin(ctx, 6*time.Second, base+"/api/v3/ticker/24hr?symbol="+sym+"USDT", &t) == nil && t.LastPrice != "" {
			price, _ := strconv.ParseFloat(t.LastPrice, 64)
			change, _ := strconv.ParseFloat(t.PriceChangePercent, 64)
			if price > 0 {
				return &coinPrice{price, change, "Binance"}
			}
		}
	}
	var o struct {
		Data []struct {
			Last    string `json:"last"`
			Open24h string `json:"open24h"`
		} `json:"data"`
	}
	if r.getJSONWithin(ctx, 6*time.Second, okxAPI+"/api/v5/market/ticker?instId="+sym+"-USDT", &o) == nil && len(o.Data) > 0 {
		price, _ := strconv.ParseFloat(o.Data[0].Last, 64)
		open, _ := strconv.ParseFloat(o.Data[0].Open24h, 64)
		if price > 0 {
			change := 0.0
			if open > 0 {
				change = (price - open) / open * 100
			}
			return &coinPrice{price, change, "OKX"}
		}
	}
	return nil
}

func (r *Registry) tomanRate(ctx context.Context) (float64, string) {
	type source struct {
		name string
		get  func() float64
	}
	sources := []source{
		{"Ramzinex", func() float64 {
			var d struct {
				Data struct {
					Buy float64 `json:"buy"`
				} `json:"data"`
			}
			if r.getJSON(ctx, ramzinexAPI+"/exchange/api/v1.0/exchange/pairs/11", &d) != nil {
				return 0
			}
			return d.Data.Buy / 10
		}},
		{"Nobitex", func() float64 {
			var d struct {
				Stats map[string]struct {
					Latest string `json:"latest"`
				} `json:"stats"`
			}
			if r.getJSON(ctx, nobitexAPI+"/market/stats?srcCurrency=usdt&dstCurrency=rls", &d) != nil {
				return 0
			}
			v, _ := strconv.ParseFloat(d.Stats["usdt-rls"].Latest, 64)
			return v / 10
		}},
		{"Wallex", func() float64 {
			var d struct {
				Result struct {
					LatestTrades []struct {
						Price string `json:"price"`
					} `json:"latestTrades"`
				} `json:"result"`
			}
			if r.getJSON(ctx, wallexAPI+"/v1/trades?symbol=USDTTMN", &d) != nil || len(d.Result.LatestTrades) == 0 {
				return 0
			}
			v, _ := strconv.ParseFloat(d.Result.LatestTrades[0].Price, 64)
			return v
		}},

		{"tgju.org", func() float64 {
			board, err := r.marketSnapshot(ctx, []string{"usdt"})
			if err != nil {
				return 0
			}
			return board.quotes["usdt"].value
		}},
	}
	rates := make([]float64, len(sources))
	var wg sync.WaitGroup
	for i, s := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer guard.Recover("an exchange-rate source")
			rates[i] = s.get()
		}()
	}
	wg.Wait()
	for i, v := range rates {
		if v > 0 {
			return v, sources[i].name
		}
	}
	return 0, ""
}

func (r *Registry) cryptoPrice(ctx context.Context, _ string, args map[string]any) (string, error) {
	raw := strArg(args, "symbols")
	if raw == "" {
		raw = "BTC,ETH,SOL,TON,DOGE,TRX"
	}
	var symbols []string
	seen := map[string]bool{}
	for _, s := range strings.FieldsFunc(strings.ToUpper(raw), func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if symbolRe.MatchString(s) && !seen[s] {
			seen[s] = true
			symbols = append(symbols, s)
		}
	}
	if len(symbols) == 0 {
		return "error: no valid symbols (use tickers such as BTC,ETH)", nil
	}
	if len(symbols) > 10 {
		symbols = symbols[:10]
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var (
		toman       float64
		tomanSource string
		wg          sync.WaitGroup
		prices      = make([]*coinPrice, len(symbols))
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer guard.Recover("the toman rate")
		tctx, tcancel := context.WithTimeout(ctx, 8*time.Second)
		defer tcancel()
		toman, tomanSource = r.tomanRate(tctx)
	}()
	for i, sym := range symbols {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer guard.Recover("a coin price")
			prices[i] = r.coinPriceOf(ctx, sym)
		}()
	}
	wg.Wait()

	var sb strings.Builder
	sb.WriteString("📊 Live Crypto Rates\n━━━━━━━━━━━━━━━━━━━━━━\n")
	if toman > 0 {
		fmt.Fprintf(&sb, "💵 USDT/Toman: %s Toman (%s)\n", commas(int64(toman)), tomanSource)
	} else {
		sb.WriteString("💵 USDT/Toman: unavailable right now\n")
	}
	ok := 0
	var missing, sources []string
	for i, sym := range symbols {
		p := prices[i]
		if p == nil {
			missing = append(missing, sym)
			continue
		}
		ok++
		if !slices.Contains(sources, p.source) {
			sources = append(sources, p.source)
		}
		line := fmt.Sprintf("• %s: $%s (%+.2f%% / 24h)", sym, fmtPrice(p.price), p.change)
		if toman > 0 {
			line += fmt.Sprintf(" ≈ %s Toman", commas(int64(p.price*toman)))
		}
		sb.WriteString(line + "\n")
	}
	if ok == 0 {
		return "could not fetch prices (Binance and OKX are unreachable from this server, or the symbols are unknown)", nil
	}
	if len(missing) > 0 {
		fmt.Fprintf(&sb, "⚠️ Not found: %s\n", strings.Join(missing, ", "))
	}
	fmt.Fprintf(&sb, "Source: %s\n", strings.Join(sources, ", "))
	return sb.String(), nil
}

func (r *Registry) getJSON(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func fmtPrice(p float64) string {
	switch {
	case p >= 1000:
		cents := int64(math.Round(p * 100))
		return fmt.Sprintf("%s.%02d", commas(cents/100), cents%100)
	case p >= 1:
		return fmt.Sprintf("%.2f", p)
	case p >= 0.01:
		return fmt.Sprintf("%.4f", p)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.8f", p), "0"), ".")
}

func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

func normalizeDomain(in string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(in))
	if i := strings.Index(d, "://"); i >= 0 {
		d = d[i+3:]
	}
	if i := strings.IndexAny(d, "/?#"); i >= 0 {
		d = d[:i]
	}
	if i := strings.LastIndexByte(d, '@'); i >= 0 {
		d = d[i+1:]
	}
	if i := strings.LastIndexByte(d, ':'); i >= 0 {
		d = d[:i]
	}
	d = strings.TrimSuffix(d, ".")
	if d == "" {
		return "", errors.New("domain is required")
	}
	if _, err := netip.ParseAddr(d); err == nil {
		return "", errors.New("IP addresses are not supported, pass a domain name")
	}
	ascii, err := idna.Lookup.ToASCII(d)
	if err != nil || len(ascii) > 253 || !domainRe.MatchString(ascii) {
		return "", fmt.Errorf("%q is not a valid public domain name", in)
	}
	return ascii, nil
}

func (r *Registry) whoisLookup(ctx context.Context, _ string, args map[string]any) (string, error) {
	dom, err := normalizeDomain(strArg(args, "domain"))
	if err != nil {
		return "error: " + err.Error(), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🔍 Domain report: %s\n━━━━━━━━━━━━━━━━━━━━━━\n", dom)

	dnsCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	res := net.DefaultResolver
	if addrs, err := res.LookupHost(dnsCtx, dom); err == nil && len(addrs) > 0 {
		sb.WriteString("🌐 A / AAAA:\n")
		for _, a := range addrs[:min(len(addrs), 10)] {
			fmt.Fprintf(&sb, "  • %s\n", a)
		}
	}
	if nss, err := res.LookupNS(dnsCtx, dom); err == nil && len(nss) > 0 {
		sb.WriteString("🛡️ Name servers:\n")
		for _, ns := range nss[:min(len(nss), 10)] {
			fmt.Fprintf(&sb, "  • %s\n", ns.Host)
		}
	}
	if mxs, err := res.LookupMX(dnsCtx, dom); err == nil && len(mxs) > 0 {
		sb.WriteString("📧 Mail servers:\n")
		for _, mx := range mxs[:min(len(mxs), 10)] {
			fmt.Fprintf(&sb, "  • %s (priority %d)\n", mx.Host, mx.Pref)
		}
	}
	if txts, err := res.LookupTXT(dnsCtx, dom); err == nil && len(txts) > 0 {
		sb.WriteString("📝 TXT:\n")
		for _, t := range txts[:min(len(txts), 8)] {
			fmt.Fprintf(&sb, "  • %s\n", textutil.TruncateRunes(t, 200, "…"))
		}
	}

	sb.WriteString("\n📋 Whois:\n")
	wanted := []string{"domain name:", "domain:", "registrar:", "creation date:", "created:", "registry expiry date:",
		"expire", "updated date:", "holder:", "registrant organization:", "status:", "name server:", "nserver:"}
	count := 0
	for _, line := range strings.Split(whoisText(ctx, dom), "\n") {
		l := strings.TrimSpace(line)
		lower := strings.ToLower(l)
		for _, w := range wanted {
			if strings.HasPrefix(lower, w) {
				fmt.Fprintf(&sb, "  %s\n", textutil.TruncateRunes(l, 160, "…"))
				count++
				break
			}
		}
		if count >= 14 {
			break
		}
	}
	if count == 0 {
		if lines := r.rdapLookup(ctx, dom); len(lines) > 0 {
			for _, l := range lines {
				fmt.Fprintf(&sb, "  %s\n", l)
			}
			sb.WriteString("  (from RDAP)\n")
		} else {
			sb.WriteString("  (no registration data: whois and RDAP gave nothing for this domain)\n")
		}
	}
	return sb.String(), nil
}

func whoisText(ctx context.Context, dom string) string {
	if _, err := exec.LookPath("whois"); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "whois", "--", dom).Output()
	if len(out) > 256<<10 {
		out = out[:256<<10]
	}
	return string(out)
}

var rdapAPI = "https://rdap.org/domain/"

func (r *Registry) rdapLookup(ctx context.Context, dom string) []string {
	var d struct {
		LDHName string   `json:"ldhName"`
		Status  []string `json:"status"`
		Events  []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
		Entities []struct {
			Roles []string `json:"roles"`
			VCard []any    `json:"vcardArray"`
		} `json:"entities"`
		Nameservers []struct {
			LDHName string `json:"ldhName"`
		} `json:"nameservers"`
	}
	if r.getJSONWithin(ctx, 15*time.Second, rdapAPI+dom, &d) != nil || d.LDHName == "" {
		return nil
	}
	lines := []string{"Domain Name: " + d.LDHName}
	for _, e := range d.Entities {
		if slices.Contains(e.Roles, "registrar") {
			if name := vcardName(e.VCard); name != "" {
				lines = append(lines, "Registrar: "+name)
			}
		}
	}
	labels := map[string]string{"registration": "Creation Date", "expiration": "Expiry Date", "last changed": "Updated Date"}
	for _, ev := range d.Events {
		if label, ok := labels[ev.Action]; ok {
			date, _, _ := strings.Cut(ev.Date, "T")
			lines = append(lines, label+": "+date)
		}
	}
	if len(d.Status) > 0 {
		lines = append(lines, "Status: "+strings.Join(d.Status, ", "))
	}
	for i, ns := range d.Nameservers {
		if i == 4 {
			break
		}
		lines = append(lines, "Name Server: "+strings.ToLower(ns.LDHName))
	}
	return lines
}

func vcardName(card []any) string {
	if len(card) < 2 {
		return ""
	}
	props, _ := card[1].([]any)
	for _, p := range props {
		if f, ok := p.([]any); ok && len(f) >= 4 && f[0] == "fn" {
			name, _ := f[3].(string)
			return name
		}
	}
	return ""
}

func normalizeRepo(in string) (string, error) {
	s := strings.TrimSpace(in)
	for _, p := range []string{"https://", "http://"} {
		s = strings.TrimPrefix(s, p)
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "www."), "github.com/")
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("%q is not a valid 'owner/repo'", in)
	}
	owner, name := parts[0], strings.TrimSuffix(parts[1], ".git")
	repo := owner + "/" + name

	if !repoRe.MatchString(repo) || owner == "." || owner == ".." || name == "." || name == ".." {
		return "", fmt.Errorf("%q is not a valid 'owner/repo'", in)
	}
	return repo, nil
}

func (r *Registry) githubGet(ctx context.Context, apiPath, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIURL+apiPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "Macron-Agent")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token := r.githubToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	switch {
	case resp.StatusCode == http.StatusOK:
		return body, nil
	case resp.StatusCode == http.StatusNotFound:
		return nil, errors.New("repository or resource not found (it may be private)")
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return nil, errGitHubLimit
	}
	return nil, fmt.Errorf("GitHub API answered HTTP %d", resp.StatusCode)
}

func (r *Registry) githubInspect(ctx context.Context, _ string, args map[string]any) (string, error) {
	repo, err := normalizeRepo(strArg(args, "repo"))
	if err != nil {
		return "error: " + err.Error(), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	const jsonAccept = "application/vnd.github+json"

	switch qt := strArg(args, "query_type"); qt {
	case "readme":
		b, err := r.githubGet(ctx, "/repos/"+repo+"/readme", "application/vnd.github.raw+json")
		text := strings.ToValidUTF8(string(b), "")
		if errors.Is(err, errGitHubLimit) {
			text, err = r.githubRawReadme(ctx, repo)
		}
		if err != nil {
			return fmt.Sprintf("Could not fetch the README of %s: %v", repo, err), nil
		}
		return textutil.TruncateRunes(text, 6000, "\n... [truncated]"), nil

	case "releases":
		b, err := r.githubGet(ctx, "/repos/"+repo+"/releases?per_page=5", jsonAccept)
		if errors.Is(err, errGitHubLimit) {
			if out, ferr := r.githubFeed(ctx, repo, "releases"); ferr == nil {
				return out, nil
			}
		}
		if err != nil {
			return "GitHub error: " + err.Error(), nil
		}
		var rels []struct {
			Tag        string `json:"tag_name"`
			Name       string `json:"name"`
			Published  string `json:"published_at"`
			Prerelease bool   `json:"prerelease"`
			URL        string `json:"html_url"`
			Body       string `json:"body"`
		}
		if json.Unmarshal(b, &rels) != nil || len(rels) == 0 {
			return fmt.Sprintf("%s has no releases.", repo), nil
		}
		var sb strings.Builder
		for _, x := range rels {
			pre := ""
			if x.Prerelease {
				pre = " (pre-release)"
			}
			fmt.Fprintf(&sb, "🏷️ %s %s%s — %s\n%s\n%s\n\n", x.Tag, x.Name, pre, textutil.TruncateRunes(x.Published, 10, ""), x.URL,
				textutil.TruncateRunes(strings.TrimSpace(x.Body), 400, "…"))
		}
		return sb.String(), nil

	case "commits":
		b, err := r.githubGet(ctx, "/repos/"+repo+"/commits?per_page=10", jsonAccept)
		if errors.Is(err, errGitHubLimit) {
			if out, ferr := r.githubFeed(ctx, repo, "commits"); ferr == nil {
				return out, nil
			}
		}
		if err != nil {
			return "GitHub error: " + err.Error(), nil
		}
		var cs []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
				Author  struct {
					Name string `json:"name"`
					Date string `json:"date"`
				} `json:"author"`
			} `json:"commit"`
		}
		if json.Unmarshal(b, &cs) != nil || len(cs) == 0 {
			return "No commits found.", nil
		}
		var sb strings.Builder
		for _, c := range cs {
			first, _, _ := strings.Cut(c.Commit.Message, "\n")
			fmt.Fprintf(&sb, "• %s %s — %s (%s)\n", textutil.TruncateBytes(c.SHA, 7, ""), textutil.TruncateRunes(first, 100, "…"),
				c.Commit.Author.Name, textutil.TruncateRunes(c.Commit.Author.Date, 10, ""))
		}
		return sb.String(), nil

	default:
		b, err := r.githubGet(ctx, "/repos/"+repo, jsonAccept)
		if errors.Is(err, errGitHubLimit) {
			if out, perr := r.githubPage(ctx, repo); perr == nil {
				return out, nil
			}
		}
		if err != nil {
			return "GitHub error: " + err.Error(), nil
		}
		var d struct {
			FullName    string   `json:"full_name"`
			Description string   `json:"description"`
			Stars       int      `json:"stargazers_count"`
			Forks       int      `json:"forks_count"`
			Language    string   `json:"language"`
			Branch      string   `json:"default_branch"`
			Issues      int      `json:"open_issues_count"`
			URL         string   `json:"html_url"`
			Pushed      string   `json:"pushed_at"`
			Topics      []string `json:"topics"`
			License     *struct {
				Name string `json:"name"`
			} `json:"license"`
			Archived bool `json:"archived"`
		}
		if err := json.Unmarshal(b, &d); err != nil {
			return "GitHub returned an unexpected response", nil
		}
		license := "none"
		if d.License != nil {
			license = d.License.Name
		}
		sort.Strings(d.Topics)
		return fmt.Sprintf("🐙 %s%s\n• URL: %s\n• Description: %s\n• Language: %s | ⭐ %d | Forks: %d | Open issues: %d\n• Default branch: %s | License: %s\n• Last push: %s\n• Topics: %s",
			d.FullName, map[bool]string{true: " (archived)", false: ""}[d.Archived], d.URL, d.Description, d.Language, d.Stars, d.Forks,
			d.Issues, d.Branch, license, textutil.TruncateRunes(d.Pushed, 10, ""), strings.Join(d.Topics, ", ")), nil
	}
}

func eventStart(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Now().Add(24 * time.Hour).Truncate(time.Hour), nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	loc, err := config.Location()
	if err != nil {
		loc = time.Local
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("start %q is not a date: use YYYY-MM-DD HH:MM", v)
}
