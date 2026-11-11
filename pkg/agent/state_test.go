package agent

import (
	"os"
	"strings"
	"testing"
)

func TestTaskStateLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "macron-state-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initially empty
	state, err := LoadTaskState(tmpDir)
	if err != nil || state != nil {
		t.Fatalf("expected nil state, got err=%v, state=%v", err, state)
	}

	// Save task state
	orig := TaskState{
		SessionID:  "test-session",
		Goal:       "Create data visualizer",
		UserPrompt: "build me a dashboard",
		Plan: []PlanItem{
			{Text: "Scaffold server", Status: PlanDone},
			{Text: "Build frontend", Status: PlanInProgress},
		},
		Step:        2,
		Files:       []string{"server.py", "static/index.html"},
		Summary:     "scaffolded python server and generated HTML template",
		Interrupted: true,
	}

	if err := SaveTaskState(tmpDir, orig); err != nil {
		t.Fatalf("SaveTaskState: %v", err)
	}

	loaded, err := LoadTaskState(tmpDir)
	if err != nil || loaded == nil {
		t.Fatalf("LoadTaskState: err=%v, loaded=%v", err, loaded)
	}

	if loaded.Goal != orig.Goal {
		t.Errorf("loaded.Goal = %q, want %q", loaded.Goal, orig.Goal)
	}
	if len(loaded.Plan) != 2 || loaded.Plan[0].Status != PlanDone {
		t.Errorf("loaded.Plan = %+v", loaded.Plan)
	}
	if !loaded.Interrupted {
		t.Error("loaded.Interrupted should be true")
	}

	notice := loaded.ResumeNotice()
	if !strings.Contains(notice, "Create data visualizer") {
		t.Errorf("notice missing goal: %s", notice)
	}
	if !strings.Contains(notice, "Scaffold server") {
		t.Errorf("notice missing plan step: %s", notice)
	}
	if !strings.Contains(notice, "server.py") {
		t.Errorf("notice missing file: %s", notice)
	}

	// Clear task state
	ClearTaskState(tmpDir)
	postClear, err := LoadTaskState(tmpDir)
	if err != nil || postClear != nil {
		t.Errorf("expected state cleared, got err=%v, postClear=%v", err, postClear)
	}
}
