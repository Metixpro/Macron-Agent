package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

type NodeStatus string

const (
	NodePending  NodeStatus = "pending"
	NodeReady    NodeStatus = "ready"
	NodeRunning  NodeStatus = "running"
	NodeDone     NodeStatus = "done"
	NodeFailed   NodeStatus = "failed"
	NodeSkipped  NodeStatus = "skipped"
)

// DAGNode represents one step in a task dependency graph.
type DAGNode struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Goal      string     `json:"goal"`
	DependsOn []string   `json:"depends_on,omitempty"`
	Status    NodeStatus `json:"status"`
	Toolsets  []string   `json:"toolsets,omitempty"`
	Output    string     `json:"output,omitempty"`
	Error     string     `json:"error,omitempty"`
	StartedAt time.Time  `json:"started_at,omitempty"`
	Duration  time.Duration `json:"duration,omitempty"`
}

// TaskDAG manages a directed acyclic graph of task execution.
type TaskDAG struct {
	mu    sync.Mutex
	Nodes map[string]*DAGNode `json:"nodes"`
	Order []string            `json:"order"`
}

func NewDAG() *TaskDAG {
	return &TaskDAG{
		Nodes: make(map[string]*DAGNode),
	}
}

// AddNode adds a step to the DAG and validates dependencies.
func (d *TaskDAG) AddNode(node *DAGNode) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if node.ID == "" {
		return fmt.Errorf("node id cannot be empty")
	}
	if _, exists := d.Nodes[node.ID]; exists {
		return fmt.Errorf("node %s already exists in DAG", node.ID)
	}
	if node.Status == "" {
		node.Status = NodePending
	}
	d.Nodes[node.ID] = node
	d.Order = append(d.Order, node.ID)
	return nil
}

// Ready returns all nodes that have all dependencies fulfilled and can run now.
func (d *TaskDAG) Ready() []*DAGNode {
	d.mu.Lock()
	defer d.mu.Unlock()

	var ready []*DAGNode
	for _, id := range d.Order {
		n := d.Nodes[id]
		if n.Status != NodePending {
			continue
		}
		satisfied := true
		for _, depID := range n.DependsOn {
			dep, ok := d.Nodes[depID]
			if !ok || dep.Status != NodeDone {
				satisfied = false
				break
			}
		}
		if satisfied {
			n.Status = NodeReady
			ready = append(ready, n)
		}
	}
	return ready
}

// MarkRunning transitions a node to running.
func (d *TaskDAG) MarkRunning(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n, ok := d.Nodes[id]; ok {
		n.Status = NodeRunning
		n.StartedAt = time.Now()
	}
}

// MarkDone marks a node completed and records its duration and output.
func (d *TaskDAG) MarkDone(id string, output string, dur time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n, ok := d.Nodes[id]; ok {
		n.Status = NodeDone
		n.Output = output
		n.Duration = dur
	}
}

// MarkFailed marks a node failed and skips dependent downstream nodes.
func (d *TaskDAG) MarkFailed(id string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n, ok := d.Nodes[id]; ok {
		n.Status = NodeFailed
		if err != nil {
			n.Error = err.Error()
		}
	}
	d.skipDownstream(id)
}

func (d *TaskDAG) skipDownstream(failedID string) {
	for _, id := range d.Order {
		n := d.Nodes[id]
		if n.Status != NodePending && n.Status != NodeReady {
			continue
		}
		for _, dep := range n.DependsOn {
			if dep == failedID {
				n.Status = NodeSkipped
				d.skipDownstream(id)
				break
			}
		}
	}
}

// Progress returns completed count, total count, and completion percentage (0..100).
func (d *TaskDAG) Progress() (done, total int, pct float64) {
	d.mu.Lock()
	defer d.mu.Unlock()

	total = len(d.Order)
	if total == 0 {
		return 0, 0, 100.0
	}
	for _, id := range d.Order {
		n := d.Nodes[id]
		switch n.Status {
		case NodeDone:
			done++
		case NodeRunning:
			pct += 0.5
		}
	}
	pct = float64(done) / float64(total) * 100.0
	return done, total, pct
}

// ETA estimates time remaining based on completed nodes' average duration.
func (d *TaskDAG) ETA() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()

	var totalDur time.Duration
	completed := 0
	remaining := 0

	for _, id := range d.Order {
		n := d.Nodes[id]
		if n.Status == NodeDone && n.Duration > 0 {
			totalDur += n.Duration
			completed++
		} else if n.Status == NodePending || n.Status == NodeReady || n.Status == NodeRunning {
			remaining++
		}
	}
	if completed == 0 || remaining == 0 {
		return 0
	}
	avg := totalDur / time.Duration(completed)
	return avg * time.Duration(remaining)
}

// IsComplete reports whether every node has reached a terminal status (Done, Failed, or Skipped).
func (d *TaskDAG) IsComplete() bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, id := range d.Order {
		switch d.Nodes[id].Status {
		case NodeDone, NodeFailed, NodeSkipped:
		default:
			return false
		}
	}
	return true
}

// HasFailures reports whether any node has failed.
func (d *TaskDAG) HasFailures() bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, id := range d.Order {
		if d.Nodes[id].Status == NodeFailed {
			return true
		}
	}
	return false
}

// ToPlanItems converts DAG nodes into the standard PlanItem slice for UI rendering.
func (d *TaskDAG) ToPlanItems() []PlanItem {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]PlanItem, len(d.Order))
	for i, id := range d.Order {
		n := d.Nodes[id]
		st := PlanPending
		switch n.Status {
		case NodeDone:
			st = PlanDone
		case NodeRunning, NodeReady:
			st = PlanInProgress
		}
		title := n.Title
		if title == "" {
			title = n.Goal
		}
		out[i] = PlanItem{Text: title, Status: st}
	}
	return out
}

// Render returns a readable progress visualization of the DAG.
func (d *TaskDAG) Render() string {
	d.mu.Lock()
	defer d.mu.Unlock()

	var sb strings.Builder
	done, total, pct := 0, len(d.Order), 0.0
	for _, id := range d.Order {
		if d.Nodes[id].Status == NodeDone {
			done++
		}
	}
	if total > 0 {
		pct = float64(done) / float64(total) * 100.0
	}
	sb.WriteString(fmt.Sprintf("DAG Progress: %.0f%% (%d/%d steps completed)\n", pct, done, total))
	for i, id := range d.Order {
		n := d.Nodes[id]
		icon := map[NodeStatus]string{
			NodePending: "[ ]",
			NodeReady:   "[·]",
			NodeRunning: "[>]",
			NodeDone:    "[x]",
			NodeFailed:  "[!]",
			NodeSkipped: "[-]",
		}[n.Status]
		deps := ""
		if len(n.DependsOn) > 0 {
			deps = fmt.Sprintf(" (depends on: %s)", strings.Join(n.DependsOn, ", "))
		}
		sb.WriteString(fmt.Sprintf("%s %d. %s: %s%s\n", icon, i+1, n.ID, n.Title, deps))
	}
	return sb.String()
}

func (d *TaskDAG) MarshalJSON() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return json.Marshal(map[string]any{
		"nodes": d.Nodes,
		"order": d.Order,
	})
}
