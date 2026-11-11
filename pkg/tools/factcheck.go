package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const ToolFactCheck = "fact_check"

type FactVerdict string

const (
	VerdictTrue        FactVerdict = "True"
	VerdictMostlyTrue  FactVerdict = "Mostly True"
	VerdictMixed       FactVerdict = "Half True / Mixed"
	VerdictMisleading  FactVerdict = "Misleading"
	VerdictFalse       FactVerdict = "False"
	VerdictUnverified  FactVerdict = "Unverified"
)

type FactCheckResult struct {
	Claim             string      `json:"claim"`
	Verdict           FactVerdict `json:"verdict"`
	ConfidenceScore   int         `json:"confidence_score"` // 0 - 100
	Explanation       string      `json:"explanation"`
	SupportingSources []string    `json:"supporting_sources,omitempty"`
	RefutingSources   []string    `json:"refuting_sources,omitempty"`
}

const factCheckSystemPrompt = `You are an impartial, highly rigorous fact-checker.
Analyze the given claim against the scraped evidence sources.
Determine:
1. "verdict": exactly one of ["True", "Mostly True", "Half True / Mixed", "Misleading", "False", "Unverified"]
2. "confidence_score": integer from 0 to 100 reflecting confidence in the verdict based on source credibility and consensus
3. "explanation": 2-4 sentences explaining why, citing specific data or source IDs
4. "supporting_sources": array of source URLs that confirm the claim
5. "refuting_sources": array of source URLs that dispute or contradict the claim

Respond with ONLY valid JSON.`

// RunFactCheck searches independent evidence and assesses the truthfulness of a claim.
func (r *Registry) RunFactCheck(ctx context.Context, claim string) (*FactCheckResult, error) {
	if r.LLM == nil {
		return nil, fmt.Errorf("fact check requires language model")
	}

	searchQueries := []string{
		claim,
		claim + " fact check truth fake true false",
	}

	var (
		mu      sync.Mutex
		sources []ResearchSource
		seen    = map[string]bool{}
		wg      sync.WaitGroup
	)

	for _, q := range searchQueries {
		wg.Add(1)
		go func(query string) {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			results, err := r.Search.Search(sctx, query, 3)
			if err != nil {
				return
			}
			for _, sr := range results {
				mu.Lock()
				dup := seen[sr.URL]
				seen[sr.URL] = true
				mu.Unlock()
				if dup {
					continue
				}
				text, err := r.Search.FetchPage(sctx, sr.URL, 3000)
				if err == nil && len(text) > 80 {
					mu.Lock()
					sources = append(sources, ResearchSource{
						Title:   sr.Title,
						URL:     sr.URL,
						Content: text,
					})
					mu.Unlock()
				}
			}
		}(q)
	}
	wg.Wait()

	var evidence strings.Builder
	for i, s := range sources {
		fmt.Fprintf(&evidence, "<source id=\"%d\" url=%q title=%q>\n%s\n</source>\n\n",
			i+1, s.URL, textutil.OneLine(s.Title), s.Content)
	}

	prompt := fmt.Sprintf("Claim to fact-check:\n%s\n\nEvidence from independent sources:\n%s", claim, evidence.String())
	req := llm.Request{
		Messages: []llm.Message{
			{Role: "system", Content: factCheckSystemPrompt},
			{Role: "user", Content: prompt},
		},
	}

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := a_LLM_Complete(r.LLM, cctx, req)
	if err != nil {
		return nil, err
	}

	ans := strings.TrimSpace(resp.Message.Content)
	if s := strings.Index(ans, "{"); s >= 0 {
		if e := strings.LastIndex(ans, "}"); e > s {
			ans = ans[s : e+1]
		}
	}

	var res FactCheckResult
	if err := json.Unmarshal([]byte(ans), &res); err != nil {
		res = FactCheckResult{
			Claim:           claim,
			Verdict:         VerdictUnverified,
			ConfidenceScore: 30,
			Explanation:     "Could not definitively verify the claim from retrieved web evidence.",
		}
	}
	res.Claim = claim
	return &res, nil
}

func a_LLM_Complete(client llm.ChatClient, ctx context.Context, req llm.Request) (*llm.Response, error) {
	return client.Complete(ctx, req)
}

func (res *FactCheckResult) Format() string {
	var sb strings.Builder
	badge := "🔍"
	switch res.Verdict {
	case VerdictTrue, VerdictMostlyTrue:
		badge = "✅"
	case VerdictFalse:
		badge = "❌"
	case VerdictMisleading, VerdictMixed:
		badge = "⚠️"
	case VerdictUnverified:
		badge = "❓"
	}

	sb.WriteString(fmt.Sprintf("### %s راستی‌آزمایی / Fact Check: %s\n\n", badge, res.Verdict))
	sb.WriteString(fmt.Sprintf("**ادعا (Claim):** %s\n", res.Claim))
	sb.WriteString(fmt.Sprintf("**درصد اطمینان (Confidence):** %d%%\n\n", res.ConfidenceScore))
	sb.WriteString(fmt.Sprintf("**تحلیل و ارزیابی:**\n%s\n\n", res.Explanation))

	if len(res.SupportingSources) > 0 {
		sb.WriteString("**منابع تاییدکننده:**\n")
		for _, s := range res.SupportingSources {
			sb.WriteString(fmt.Sprintf("- %s\n", s))
		}
		sb.WriteString("\n")
	}
	if len(res.RefutingSources) > 0 {
		sb.WriteString("**منابع ردکننده یا متناقض:**\n")
		for _, s := range res.RefutingSources {
			sb.WriteString(fmt.Sprintf("- %s\n", s))
		}
	}
	return strings.TrimSpace(sb.String())
}
