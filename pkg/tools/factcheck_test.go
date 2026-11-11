package tools

import (
	"strings"
	"testing"
)

func TestFactCheckFormat(t *testing.T) {
	res := &FactCheckResult{
		Claim:           "Python 3.12 was released in October 2023.",
		Verdict:         VerdictTrue,
		ConfidenceScore: 98,
		Explanation:     "Official Python release notes confirm 3.12.0 final release date was October 2, 2023.",
		SupportingSources: []string{
			"https://www.python.org/downloads/release/python-3120/",
		},
	}

	formatted := res.Format()
	if !strings.Contains(formatted, "✅") || !strings.Contains(formatted, "True") {
		t.Errorf("missing True badge/verdict: %s", formatted)
	}
	if !strings.Contains(formatted, "98%") {
		t.Errorf("missing confidence score: %s", formatted)
	}
	if !strings.Contains(formatted, "python.org") {
		t.Errorf("missing supporting source URL: %s", formatted)
	}
}
