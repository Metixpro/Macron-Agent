package agent

import (
	"context"
	"strings"
	"testing"
)

func TestDryRunContext(t *testing.T) {
	ctx := context.Background()
	if IsDryRun(ctx) {
		t.Error("expected default IsDryRun to be false")
	}

	dryCtx := WithDryRun(ctx, true)
	if !IsDryRun(dryCtx) {
		t.Error("expected IsDryRun(dryCtx) to be true")
	}
}

func TestSimulateToolCall(t *testing.T) {
	// 1. write_file simulation
	action, ok := SimulateToolCall("write_file", `{"path": "app.py", "content": "print('hi')"}`)
	if !ok || !action.Mutating || !strings.Contains(action.Simulation, "DRY-RUN PREVIEW") {
		t.Errorf("write_file simulation failed: %+v", action)
	}

	// 2. bash_execute simulation
	action, ok = SimulateToolCall("bash_execute", `{"command": "rm -rf /tmp/data"}`)
	if !ok || !action.Mutating || !strings.Contains(action.Simulation, "DRY-RUN PREVIEW") {
		t.Errorf("bash_execute simulation failed: %+v", action)
	}

	// 3. non-mutating command simulation
	action, ok = SimulateToolCall("bash_execute", `{"command": "ls -la"}`)
	if !ok || action.Mutating {
		t.Errorf("ls -la should not be mutating: %+v", action)
	}

	// 4. unknown tool
	_, ok = SimulateToolCall("unknown_tool", "")
	if ok {
		t.Error("unknown tool should return ok=false")
	}
}
