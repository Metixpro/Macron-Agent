package search

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/netclient"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	PassagePages    = 3
	passagesPerPage = 3
	passageRunes    = 750
	minParagraph    = 60
	maxParagraph    = 700
)

var passageBudget = 6 * time.Second

func (e *Engine) AddPassages(ctx context.Context, query string, results []SearchResult, n int) {
	if n <= 0 || len(results) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, passageBudget)
	defer cancel()
	terms := queryTerms(query)
	var wg sync.WaitGroup
	for i := range results[:min(n, len(results))] {
		wg.Add(1)
		go func(r *SearchResult) {
			defer wg.Done()
			defer guard.Recover("reading a search result")
			u, err := netclient.ValidateURL(r.URL)
			if err != nil {
				return
			}
			page, err := e.fetchDirect(ctx, u)
			if err != nil || page == nil {
				return
			}
			r.Passages = keyPassages(page.Text, terms)
		}(&results[i])
	}
	wg.Wait()
}

func keyPassages(text string, terms []string) []string {
	type para struct {
		text  string
		pos   int
		score float64
	}
	var paras []para
	for i, p := range paragraphs(text) {
		norm := normalize(p)
		hits, seen := 0, map[string]bool{}
		for _, t := range terms {
			if c := strings.Count(norm, t); c > 0 {
				hits += c
				seen[t] = true
			}
		}
		if len(seen) == 0 {
			paras = append(paras, para{text: p, pos: i})
			continue
		}

		score := float64(len(seen))*3 + float64(min(hits, 6)) - float64(len([]rune(p)))/1500
		paras = append(paras, para{text: p, pos: i, score: score})
	}
	if len(paras) == 0 {
		return nil
	}
	sorted := append([]para(nil), paras...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].score > sorted[b].score })
	var picked []para
	if sorted[0].score == 0 {

		for _, p := range paras {
			if len([]rune(p.text)) >= 120 {
				picked = append(picked, p)
				break
			}
		}
	} else {

		budget, floor := passageRunes, sorted[0].score/2
		for _, p := range sorted {
			if p.score <= 0 || p.score < floor || len(picked) == passagesPerPage || budget <= 0 {
				break
			}
			p.text = textutil.TruncateRunes(p.text, budget, "…")
			budget -= len([]rune(p.text))
			picked = append(picked, p)
		}
	}
	sort.Slice(picked, func(a, b int) bool { return picked[a].pos < picked[b].pos })
	out := make([]string, len(picked))
	for i, p := range picked {
		out[i] = p.text
	}
	return out
}

func paragraphs(text string) []string {
	var out []string
	for _, block := range strings.Split(text, "\n") {
		block = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(block), "•#>*-"))
		n := len([]rune(block))
		if n < minParagraph || strings.Count(block, " ") < 6 {
			continue
		}
		out = append(out, textutil.TruncateRunes(block, maxParagraph, "…"))
	}
	return out
}

var stopWords = map[string]bool{
	"و": true, "در": true, "به": true, "از": true, "که": true, "این": true, "آن": true, "را": true, "با": true, "است": true,
	"برای": true, "یک": true, "چی": true, "چیه": true, "چه": true, "کجا": true, "کی": true, "چطور": true, "چگونه": true,
	"هست": true, "بود": true, "می": true, "های": true, "ها": true, "تا": true, "یا": true, "هم": true, "بر": true, "اون": true,
	"the": true, "a": true, "an": true, "of": true, "in": true, "on": true, "to": true, "and": true, "or": true, "is": true,
	"are": true, "what": true, "how": true, "for": true, "with": true, "by": true, "at": true, "from": true, "about": true,
	"which": true, "who": true, "when": true, "why": true, "does": true, "do": true, "it": true, "its": true, "be": true,
}

func queryTerms(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(normalize(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if len([]rune(w)) < 2 || stopWords[w] {
			continue
		}
		w = stem(w)
		if seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

func stem(w string) string {
	n := len([]rune(w))
	switch {
	case n >= 6 && strings.HasSuffix(w, "های"):
		return strings.TrimSuffix(w, "های")
	case n >= 5 && strings.HasSuffix(w, "ها"):
		return strings.TrimSuffix(w, "ها")
	case n >= 5 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return strings.TrimSuffix(w, "s")
	}
	return w
}

var normalizer = strings.NewReplacer("ي", "ی", "ك", "ک", "ة", "ه", "‌", " ", "أ", "ا", "إ", "ا", "ۀ", "ه")

func normalize(s string) string {
	return normalizer.Replace(strings.ToLower(textutil.Fold(s)))
}
