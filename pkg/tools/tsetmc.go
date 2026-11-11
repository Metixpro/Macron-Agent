package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	ToolTSETMCStock = "tsetmc_stock"
	tseAPI          = "https://cdn.tsetmc.com/api"
)

var arabicLetters = strings.NewReplacer("ي", "ی", "ك", "ک", "‌", " ")

type tseObject map[string]any

func (o tseObject) num(keys ...string) (float64, bool) {
	for _, k := range keys {
		switch v := o[k].(type) {
		case float64:
			return v, true
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

func (o tseObject) str(keys ...string) string {
	for _, k := range keys {
		if v, ok := o[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(arabicLetters.Replace(v))
		}
	}
	return ""
}

func (o tseObject) obj(key string) tseObject {
	if m, ok := o[key].(map[string]any); ok {
		return m
	}
	return tseObject{}
}

func (o tseObject) list(key string) []tseObject {
	raw, _ := o[key].([]any)
	out := make([]tseObject, 0, len(raw))
	for _, it := range raw {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func (r *Registry) tseGet(ctx context.Context, path string) (tseObject, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, tseAPI+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124 Safari/537.36")
	req.Header.Set("Accept", "application/json")
	resp, err := r.cloudClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("TSETMC did not answer (it often refuses connections from outside Iran): %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TSETMC answered %s", resp.Status)
	}
	var out tseObject
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("TSETMC sent something that is not JSON: %.200s", body)
	}
	return out, nil
}

type tseInstrument struct {
	code, symbol, name, market string
}

func (r *Registry) tseFind(ctx context.Context, symbol string) (tseInstrument, []tseInstrument, error) {
	res, err := r.tseGet(ctx, "/Instrument/GetInstrumentSearch/"+url.PathEscape(symbol))
	if err != nil {
		return tseInstrument{}, nil, err
	}
	want := strings.TrimSpace(arabicLetters.Replace(symbol))
	var all []tseInstrument
	for _, it := range res.list("instrumentSearch") {
		in := tseInstrument{code: it.str("insCode"), symbol: it.str("lVal18AFC"), name: it.str("lVal30"), market: it.str("flowTitle")}
		if in.code == "" {
			if n, ok := it.num("insCode"); ok {
				in.code = strconv.FormatFloat(n, 'f', 0, 64)
			}
		}
		if in.code != "" {
			all = append(all, in)
		}
	}
	for _, in := range all {
		if in.symbol == want {
			return in, all, nil
		}
	}
	if len(all) == 0 {
		return tseInstrument{}, nil, fmt.Errorf("TSETMC knows no symbol %q", symbol)
	}
	return all[0], all, nil
}

func groupDigits(f float64) string {
	neg := f < 0
	s := strconv.FormatInt(int64(math.Round(math.Abs(f))), 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func signed(f float64, pct bool) string {
	sign := "+"
	if f < 0 {
		sign = ""
	}
	if pct {
		return fmt.Sprintf("%s%.2f%%", sign, f)
	}
	return sign + groupDigits(f)
}

func (r *Registry) tseIndices(ctx context.Context, sb *strings.Builder) error {
	res, err := r.tseGet(ctx, "/Index/GetIndexB1LastAll/SelectedIndexes/1")
	if err != nil {
		return err
	}
	items := res.list("indexB1")
	if len(items) == 0 {
		return fmt.Errorf("TSETMC sent no index values")
	}
	sb.WriteString("Tehran Stock Exchange indices:")
	for _, it := range items {
		v, ok := it.num("xDrNivJIdx004")
		if !ok {
			continue
		}
		line := "\n" + it.str("lVal30") + ": " + groupDigits(v)
		if ch, ok := it.num("indexChange"); ok {
			line += " (" + signed(ch, false)
			if p, ok := it.num("xVarIdxJRfV"); ok {
				line += ", " + signed(p, true)
			}
			line += ")"
		}
		sb.WriteString(line)
	}
	return nil
}

func (r *Registry) QueryTSETMC(ctx context.Context, symbol string) (string, error) {
	symbol = strings.TrimSpace(symbol)
	var sb strings.Builder
	if symbol == "" || symbol == "شاخص" || strings.EqualFold(symbol, "index") || strings.EqualFold(symbol, "all") {
		if err := r.tseIndices(ctx, &sb); err != nil {
			return "", err
		}
		sb.WriteString("\n(source: TSETMC, " + time.Now().Format("15:04") + ")")
		return sb.String(), nil
	}
	in, all, err := r.tseFind(ctx, symbol)
	if err != nil {
		return "", err
	}
	res, err := r.tseGet(ctx, "/ClosingPrice/GetClosingPriceInfo/"+url.PathEscape(in.code))
	if err != nil {
		return "", err
	}
	cp := res.obj("closingPriceInfo")
	last, okLast := cp.num("pDrCotVal")
	closing, okClose := cp.num("pClosing")
	if !okLast && !okClose {
		return "", fmt.Errorf("TSETMC sent no prices for %s", in.symbol)
	}
	fmt.Fprintf(&sb, "%s (%s)", in.symbol, in.name)
	if in.market != "" {
		sb.WriteString(" · " + in.market)
	}
	if st := cp.obj("instrumentState").str("cEtavalTitle"); st != "" {
		sb.WriteString(" · " + st)
	}
	yesterday, okY := cp.num("priceYesterday")
	line := "\nlast trade " + groupDigits(last)
	if okClose {
		line += " · closing price " + groupDigits(closing)
	}
	if okY && yesterday > 0 {
		line += fmt.Sprintf(" · yesterday %s · closing change %s (%s)", groupDigits(yesterday), signed(closing-yesterday, false), signed((closing-yesterday)/yesterday*100, true))
	} else if ch, ok := cp.num("priceChange"); ok {
		line += " · change " + signed(ch, false)
	}
	sb.WriteString(line + " (rial)")
	var stats []string
	if lo, ok := cp.num("priceMin"); ok {
		if hi, ok := cp.num("priceMax"); ok {
			stats = append(stats, "day range "+groupDigits(lo)+"–"+groupDigits(hi))
		}
	}
	if n, ok := cp.num("zTotTran"); ok {
		stats = append(stats, "trades "+groupDigits(n))
	}
	if n, ok := cp.num("qTotTran5J"); ok {
		stats = append(stats, "volume "+groupDigits(n))
	}
	if n, ok := cp.num("qTotCap"); ok {
		stats = append(stats, fmt.Sprintf("value %.1f billion rial", n/1e9))
	}
	if len(stats) > 0 {
		sb.WriteString("\n" + strings.Join(stats, " · "))
	}
	if info, err := r.tseGet(ctx, "/Instrument/GetInstrumentInfo/"+url.PathEscape(in.code)); err == nil {
		eps := info.obj("instrumentInfo").obj("eps")
		if e, ok := eps.num("epsValue", "estimatedEPS"); ok && e != 0 {
			price := closing
			if !okClose {
				price = last
			}
			fmt.Fprintf(&sb, "\nEPS %s · P/E %.1f", groupDigits(e), price/e)
			if spe, ok := eps.num("sectorPE"); ok && spe > 0 {
				fmt.Fprintf(&sb, " · sector P/E %.1f", spe)
			}
		}
	}
	if bl, err := r.tseGet(ctx, "/BestLimits/"+url.PathEscape(in.code)); err == nil {
		for _, lv := range bl.list("bestLimits") {
			if n, _ := lv.num("number"); n != 1 {
				continue
			}
			bid, _ := lv.num("pMeDem")
			ask, _ := lv.num("pMeOf")
			bq, _ := lv.num("qTitMeDem")
			aq, _ := lv.num("qTitMeOf")
			fmt.Fprintf(&sb, "\nbest bid %s × %s · best ask %s × %s", groupDigits(bid), groupDigits(bq), groupDigits(ask), groupDigits(aq))
		}
	}
	if cd, err := r.tseGet(ctx, "/Codal/GetPreparedDataByInsCode/5/"+url.PathEscape(in.code)); err == nil {
		letters := cd.list("preparedData")
		if len(letters) > 0 {
			sb.WriteString("\nCodal (latest):")
		}
		for _, l := range letters {
			title := l.str("title", "Title", "letterTitle")
			when := l.str("publishDateTime", "sentDateTime", "PublishDateTime")
			link := l.str("url", "Url")
			if link != "" && !strings.HasPrefix(link, "http") {
				link = "https://www.codal.ir" + link
			}
			fmt.Fprintf(&sb, "\n- %s %s %s", when, title, link)
		}
	}
	if len(all) > 1 && all[0].symbol != in.symbol {
		var others []string
		for _, o := range all[:min(len(all), 5)] {
			others = append(others, o.symbol)
		}
		sb.WriteString("\n(other matches: " + strings.Join(others, "، ") + ")")
	}
	sb.WriteString("\n(source: TSETMC, " + time.Now().Format("15:04") + ")")
	return sb.String(), nil
}
