package evals

import (
	"context"
	"fmt"
	"time"
)

type EvalTask struct {
	ID                   string   `json:"id"`
	Title                string   `json:"title"`
	Goal                 string   `json:"goal"`
	ExpectedFiles        []string `json:"expected_files"`
	MinSteps             int      `json:"min_steps"`
	MaxSteps             int      `json:"max_steps"`
	RequiresVerification bool     `json:"requires_verification"`
}

type EvalResult struct {
	TaskID             string        `json:"task_id"`
	StepsTaken         int           `json:"steps_taken"`
	Completed          bool          `json:"completed"`
	VerificationPassed bool          `json:"verification_passed"`
	FilesProduced      []string      `json:"files_produced"`
	Duration           time.Duration `json:"duration"`
	Score              float64       `json:"score"` // 0.0 - 1.0
	Notes              string        `json:"notes,omitempty"`
}

var BenchmarkLongTasks = []EvalTask{
	{
		ID:                   "fullstack_todo",
		Title:                "Full-stack Todo Application",
		Goal:                 "Create a Python FastAPI backend with sqlite and an HTML/JS frontend, run tests, and verify endpoints",
		ExpectedFiles:        []string{"main.py", "index.html", "test_api.py"},
		MinSteps:             4,
		MaxSteps:             20,
		RequiresVerification: true,
	},
	{
		ID:                   "deep_market_research",
		Title:                "Iranian Market Quantitative Analysis",
		Goal:                 "Research TSETMC indices, currency trends, and draft an analytical report with charts and markdown citations",
		ExpectedFiles:        []string{"report.md"},
		MinSteps:             3,
		MaxSteps:             15,
		RequiresVerification: false,
	},
	{
		ID:                   "refactor_and_test",
		Title:                "Module Refactoring and Regression Testing",
		Goal:                 "Refactor data processing pipeline into modular components, ensure 100% test pass rate",
		ExpectedFiles:        []string{"pipeline.py", "test_pipeline.py"},
		MinSteps:             5,
		MaxSteps:             25,
		RequiresVerification: true,
	},
}

// EvaluateTaskRun scores an executed long task against benchmark criteria.
func EvaluateTaskRun(ctx context.Context, task EvalTask, stepsTaken int, filesOnDisk []string, verified bool, dur time.Duration) EvalResult {
	res := EvalResult{
		TaskID:             task.ID,
		StepsTaken:         stepsTaken,
		Duration:           dur,
		VerificationPassed: verified,
		FilesProduced:      filesOnDisk,
	}

	// Calculate score
	score := 0.0
	foundFiles := 0
	diskSet := make(map[string]bool)
	for _, f := range filesOnDisk {
		diskSet[f] = true
	}

	for _, exp := range task.ExpectedFiles {
		if diskSet[exp] {
			foundFiles++
		}
	}

	if len(task.ExpectedFiles) > 0 {
		fileScore := float64(foundFiles) / float64(len(task.ExpectedFiles)) * 0.5
		score += fileScore
	} else {
		score += 0.5
	}

	if task.RequiresVerification {
		if verified {
			score += 0.3
		}
	} else {
		score += 0.3
	}

	if stepsTaken >= task.MinSteps && stepsTaken <= task.MaxSteps {
		score += 0.2
	} else if stepsTaken > 0 {
		score += 0.1
	}

	res.Score = score
	res.Completed = score >= 0.7
	res.Notes = fmt.Sprintf("Score: %.0f%% (files: %d/%d, verified: %v, steps: %d)",
		score*100, foundFiles, len(task.ExpectedFiles), verified, stepsTaken)
	return res
}
