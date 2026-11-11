package agent

import (
	"errors"
	"testing"
	"time"
)

func TestTaskDAGExecution(t *testing.T) {
	dag := NewDAG()

	// Add 3 nodes: A -> B -> C
	_ = dag.AddNode(&DAGNode{ID: "A", Title: "Scaffold", Goal: "Scaffold project"})
	_ = dag.AddNode(&DAGNode{ID: "B", Title: "Implement", Goal: "Implement feature", DependsOn: []string{"A"}})
	_ = dag.AddNode(&DAGNode{ID: "C", Title: "Test", Goal: "Test feature", DependsOn: []string{"B"}})

	// Initially, only A should be ready
	ready := dag.Ready()
	if len(ready) != 1 || ready[0].ID != "A" {
		t.Fatalf("expected ready=[A], got %v", ready)
	}

	// Progress at start
	done, total, pct := dag.Progress()
	if done != 0 || total != 3 || pct != 0 {
		t.Errorf("progress = %d/%d (%.1f%%), want 0/3 (0%%)", done, total, pct)
	}

	// Mark A done
	dag.MarkDone("A", "scaffolded", 10*time.Second)

	// Now B should be ready
	ready = dag.Ready()
	if len(ready) != 1 || ready[0].ID != "B" {
		t.Fatalf("expected ready=[B], got %v", ready)
	}

	// Mark B done
	dag.MarkDone("B", "implemented", 10*time.Second)

	// Check ETA
	eta := dag.ETA()
	if eta != 10*time.Second {
		t.Errorf("ETA = %v, want 10s", eta)
	}

	// Now C should be ready
	ready = dag.Ready()
	if len(ready) != 1 || ready[0].ID != "C" {
		t.Fatalf("expected ready=[C], got %v", ready)
	}

	// Mark C done
	dag.MarkDone("C", "tested", 5*time.Second)

	if !dag.IsComplete() {
		t.Error("expected DAG to be complete")
	}
	if dag.HasFailures() {
		t.Error("DAG should have no failures")
	}

	items := dag.ToPlanItems()
	if len(items) != 3 {
		t.Fatalf("plan items len = %d, want 3", len(items))
	}
	for _, it := range items {
		if it.Status != PlanDone {
			t.Errorf("item %s status = %s, want done", it.Text, it.Status)
		}
	}
}

func TestDAGFailureCascade(t *testing.T) {
	dag := NewDAG()
	_ = dag.AddNode(&DAGNode{ID: "step1", Title: "Download", Goal: "Download asset"})
	_ = dag.AddNode(&DAGNode{ID: "step2", Title: "Process", Goal: "Process asset", DependsOn: []string{"step1"}})

	ready := dag.Ready()
	if len(ready) != 1 || ready[0].ID != "step1" {
		t.Fatalf("ready = %v", ready)
	}

	// Fail step 1
	dag.MarkFailed("step1", errors.New("download 404"))

	if !dag.HasFailures() {
		t.Error("expected DAG to report failures")
	}

	// step 2 should be automatically skipped
	if dag.Nodes["step2"].Status != NodeSkipped {
		t.Errorf("step2 status = %s, want skipped", dag.Nodes["step2"].Status)
	}
	if !dag.IsComplete() {
		t.Error("DAG with failed/skipped nodes should be complete")
	}
}
