package tools

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const ToolAcademicSearch = "academic_search"

type AcademicPaper struct {
	Source    string   `json:"source"`
	Title     string   `json:"title"`
	Authors   []string `json:"authors,omitempty"`
	Year      string   `json:"year,omitempty"`
	Abstract  string   `json:"abstract,omitempty"`
	URL       string   `json:"url"`
	Citations int      `json:"citations,omitempty"`
}

func SearchArxiv(ctx context.Context, client *http.Client, query string, limit int) ([]AcademicPaper, error) {
	if client == nil {
		client = http.DefaultClient
	}
	u := fmt.Sprintf("https://export.arxiv.org/api/query?search_query=all:%s&start=0&max_results=%d",
		url.QueryEscape(query), limit)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Macron-Agent/1.5 (academic-search)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("arxiv API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	type arxivEntry struct {
		Title     string `xml:"title"`
		Summary   string `xml:"summary"`
		Published string `xml:"published"`
		ID        string `xml:"id"`
		Author    []struct {
			Name string `xml:"name"`
		} `xml:"author"`
	}
	type arxivFeed struct {
		Entry []arxivEntry `xml:"entry"`
	}

	var feed arxivFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, err
	}

	var papers []AcademicPaper
	for _, e := range feed.Entry {
		year := ""
		if len(e.Published) >= 4 {
			year = e.Published[:4]
		}
		var authors []string
		for _, a := range e.Author {
			authors = append(authors, strings.TrimSpace(a.Name))
		}
		papers = append(papers, AcademicPaper{
			Source:   "arXiv",
			Title:    strings.TrimSpace(textutil.OneLine(e.Title)),
			Authors:  authors,
			Year:     year,
			Abstract: textutil.TruncateRunes(strings.TrimSpace(e.Summary), 400, "…"),
			URL:      strings.TrimSpace(e.ID),
		})
	}
	return papers, nil
}

func SearchSemanticScholar(ctx context.Context, client *http.Client, query string, limit int) ([]AcademicPaper, error) {
	if client == nil {
		client = http.DefaultClient
	}
	u := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/paper/search?query=%s&limit=%d&fields=title,authors,year,abstract,url,citationCount",
		url.QueryEscape(query), limit)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Macron-Agent/1.5")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("semantic scholar API status %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			Title         string `json:"title"`
			Year          int    `json:"year"`
			Abstract      string `json:"abstract"`
			URL           string `json:"url"`
			CitationCount int    `json:"citationCount"`
			Authors       []struct {
				Name string `json:"name"`
			} `json:"authors"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var papers []AcademicPaper
	for _, d := range result.Data {
		var authors []string
		for _, a := range d.Authors {
			authors = append(authors, a.Name)
		}
		y := ""
		if d.Year > 0 {
			y = fmt.Sprint(d.Year)
		}
		u := d.URL
		if u == "" {
			u = "https://www.semanticscholar.org/paper/" + url.QueryEscape(d.Title)
		}
		papers = append(papers, AcademicPaper{
			Source:    "Semantic Scholar",
			Title:     d.Title,
			Authors:   authors,
			Year:      y,
			Abstract:  textutil.TruncateRunes(strings.TrimSpace(d.Abstract), 400, "…"),
			URL:       u,
			Citations: d.CitationCount,
		})
	}
	return papers, nil
}

func (r *Registry) SearchAcademic(ctx context.Context, query, source string, limit int) ([]AcademicPaper, error) {
	if limit <= 0 {
		limit = 5
	}
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" {
		source = "all"
	}

	client := r.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	var (
		mu     sync.Mutex
		papers []AcademicPaper
		wg     sync.WaitGroup
	)

	runArxiv := source == "all" || source == "arxiv"
	runScholar := source == "all" || source == "semantic_scholar" || source == "scholar"

	if runArxiv {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if res, err := SearchArxiv(sctx, client, query, limit); err == nil {
				mu.Lock()
				papers = append(papers, res...)
				mu.Unlock()
			}
		}()
	}

	if runScholar {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if res, err := SearchSemanticScholar(sctx, client, query, limit); err == nil {
				mu.Lock()
				papers = append(papers, res...)
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	return papers, nil
}

func FormatAcademicPapers(papers []AcademicPaper) string {
	if len(papers) == 0 {
		return "No academic papers found matching the query."
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("### Academic Papers (%d found)\n\n", len(papers)))
	for i, p := range papers {
		authorLine := strings.Join(p.Authors, ", ")
		if len(p.Authors) > 3 {
			authorLine = strings.Join(p.Authors[:3], ", ") + " et al."
		}
		yearStr := ""
		if p.Year != "" {
			yearStr = fmt.Sprintf(" (%s)", p.Year)
		}
		sb.WriteString(fmt.Sprintf("%d. **[%s](%s)**%s — *%s*\n", i+1, p.Title, p.URL, yearStr, p.Source))
		if authorLine != "" {
			sb.WriteString(fmt.Sprintf("   *Authors:* %s\n", authorLine))
		}
		if p.Abstract != "" {
			sb.WriteString(fmt.Sprintf("   *Abstract:* %s\n", p.Abstract))
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}
