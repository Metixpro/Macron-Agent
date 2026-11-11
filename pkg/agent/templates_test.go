package agent

import (
	"testing"
)

func TestWorkflowTemplates(t *testing.T) {
	// Test matching code review
	tmpl := MatchTemplate("لطفاً یک کد ریویو کامل روی این پوشه انجام بده")
	if tmpl == nil || tmpl.ID != "code_review" {
		t.Fatalf("expected code_review template, got %v", tmpl)
	}

	dag := tmpl.BuildDAG("pkg/agent")
	if len(dag.Order) != 4 {
		t.Errorf("code_review DAG steps = %d, want 4", len(dag.Order))
	}

	// Test matching deep research
	tmpl = MatchTemplate("Please do a deep research on quantum computing")
	if tmpl == nil || tmpl.ID != "deep_research" {
		t.Fatalf("expected deep_research template, got %v", tmpl)
	}

	// Test matching refactor
	tmpl = MatchTemplate("این ماژول رو ریفکتور کن")
	if tmpl == nil || tmpl.ID != "refactor" {
		t.Fatalf("expected refactor template, got %v", tmpl)
	}

	// Unknown request
	tmpl = MatchTemplate("امروز هوا چطوره؟")
	if tmpl != nil {
		t.Errorf("expected nil for small talk, got %v", tmpl)
	}
}
