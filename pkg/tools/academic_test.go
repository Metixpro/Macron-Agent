package tools

import (
	"strings"
	"testing"
)

func TestFormatAcademicPapers(t *testing.T) {
	papers := []AcademicPaper{
		{
			Source:   "arXiv",
			Title:    "Attention Is All You Need",
			Authors:  []string{"Ashish Vaswani", "Noam Shazeer", "Niki Parmar", "Jakob Uszkoreit"},
			Year:     "2017",
			Abstract: "The dominant sequence transduction models are based on complex recurrent or convolutional neural networks...",
			URL:      "https://arxiv.org/abs/1706.03762",
		},
		{
			Source:   "Semantic Scholar",
			Title:    "BERT: Pre-training of Deep Bidirectional Transformers for Language Understanding",
			Authors:  []string{"Jacob Devlin", "Ming-Wei Chang"},
			Year:     "2018",
			Abstract: "We introduce a new language representation model called BERT...",
			URL:      "https://www.semanticscholar.org/paper/df2b0e26d0599ce3e70df8a9dae157f709f00cfb",
		},
	}

	formatted := FormatAcademicPapers(papers)
	if !strings.Contains(formatted, "Attention Is All You Need") {
		t.Errorf("missing paper 1 title: %s", formatted)
	}
	if !strings.Contains(formatted, "et al.") {
		t.Errorf("expected et al. for >3 authors: %s", formatted)
	}
	if !strings.Contains(formatted, "arXiv") || !strings.Contains(formatted, "Semantic Scholar") {
		t.Errorf("missing source names: %s", formatted)
	}
}
