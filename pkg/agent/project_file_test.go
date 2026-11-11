package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

func TestProjectFileRenderAndParse(t *testing.T) {
	pf := &ProjectFile{
		Title:     "Crypto Dashboard",
		Goal:      "Real-time tracking of prices and market caps",
		TechStack: "Python FastAPI + Chart.js",
		Architecture: []string{
			"FastAPI backend fetches from CoinGecko API",
			"HTML frontend displays auto-refreshing charts",
		},
		Files: []ProjectFileEntry{
			{Path: "main.py", Description: "Backend API and static server"},
			{Path: "index.html", Description: "Interactive charting dashboard"},
		},
		Milestones: []PlanItem{
			{Text: "Design API endpoints", Status: PlanDone},
			{Text: "Implement frontend charts", Status: PlanInProgress},
			{Text: "Write unit tests", Status: PlanPending},
		},
		VerifiedFacts: []string{
			"CoinGecko free tier allows 30 req/min",
			"Port 8000 is open in workspace",
		},
	}

	rendered := pf.Render()
	if !strings.Contains(rendered, "# Project: Crypto Dashboard") {
		t.Fatalf("missing title in rendered markdown:\n%s", rendered)
	}
	if !strings.Contains(rendered, "- [x] Design API endpoints") {
		t.Errorf("missing done milestone in rendered markdown:\n%s", rendered)
	}
	if !strings.Contains(rendered, "- [/] Implement frontend charts") {
		t.Errorf("missing in_progress milestone in rendered markdown:\n%s", rendered)
	}
	if !strings.Contains(rendered, "- `main.py`: Backend API and static server") {
		t.Errorf("missing file entry in rendered markdown:\n%s", rendered)
	}

	parsed := ParseProjectFile(rendered)
	if parsed.Title != "Crypto Dashboard" {
		t.Errorf("parsed.Title = %q, want 'Crypto Dashboard'", parsed.Title)
	}
	if len(parsed.Architecture) != 2 {
		t.Errorf("parsed.Architecture len = %d, want 2", len(parsed.Architecture))
	}
	if len(parsed.Files) != 2 || parsed.Files[0].Path != "main.py" {
		t.Errorf("parsed.Files = %+v", parsed.Files)
	}
	if len(parsed.Milestones) != 3 {
		t.Fatalf("parsed.Milestones len = %d, want 3", len(parsed.Milestones))
	}
	if parsed.Milestones[0].Status != PlanDone {
		t.Errorf("parsed.Milestones[0].Status = %s, want done", parsed.Milestones[0].Status)
	}
	if parsed.Milestones[1].Status != PlanInProgress {
		t.Errorf("parsed.Milestones[1].Status = %s, want in_progress", parsed.Milestones[1].Status)
	}
	if parsed.Milestones[2].Status != PlanPending {
		t.Errorf("parsed.Milestones[2].Status = %s, want pending", parsed.Milestones[2].Status)
	}
}

func TestPreserveProjectAcrossCompaction(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "macron-proj-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	pf := &ProjectFile{
		Title: "Test Project",
		Goal:  "Verify project preservation",
		Architecture: []string{
			"Use SQLite for state storage",
		},
	}
	if err := SaveProjectFile(tmpDir, pf); err != nil {
		t.Fatalf("SaveProjectFile: %v", err)
	}

	msgs := []llm.Message{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "Compacted summary of earlier rounds"},
	}

	preserved := PreserveProjectAcrossCompaction(tmpDir, msgs)
	if len(preserved) != 3 {
		t.Fatalf("preserved messages count = %d, want 3", len(preserved))
	}

	found := false
	for _, m := range preserved {
		if strings.Contains(m.Content, "Pinned Project State") && strings.Contains(m.Content, "Test Project") {
			found = true
			break
		}
	}
	if !found {
		t.Error("project note was not injected into preserved messages")
	}
}
