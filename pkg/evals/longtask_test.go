package evals

import (
	"context"
	"testing"
	"time"
)

func TestEvaluateTaskRun(t *testing.T) {
	task := BenchmarkLongTasks[0] // fullstack_todo

	// Test failing run
	resFail := EvaluateTaskRun(context.Background(), task, 1, []string{"only_readme.txt"}, false, 5*time.Second)
	if resFail.Completed || resFail.Score >= 0.5 {
		t.Errorf("expected failing score for incomplete task, got %f", resFail.Score)
	}

	// Test successful verified run
	files := []string{"main.py", "index.html", "test_api.py"}
	resPass := EvaluateTaskRun(context.Background(), task, 6, files, true, 45*time.Second)
	if !resPass.Completed || resPass.Score < 0.8 {
		t.Errorf("expected passing score for verified task, got %f", resPass.Score)
	}
}
