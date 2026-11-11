package agent

import (
	"context"
	"strings"
	"testing"
)

func TestConfidentialGate(t *testing.T) {
	ctx := context.Background()

	// 1. When confidential mode is off, all tools are permitted
	_, blocked := CheckConfidentialGate(ctx, "web_search")
	if blocked {
		t.Error("web_search should not be blocked when confidential mode is off")
	}

	// 2. When confidential mode is on
	confCtx := WithConfidential(ctx, true)
	if !IsConfidential(confCtx) {
		t.Fatal("expected IsConfidential to be true")
	}

	// Internal tools (read_file, run_code, write_file) must NOT be blocked
	for _, localTool := range []string{"read_file", "write_file", "run_code", "bash_execute"} {
		_, blocked := CheckConfidentialGate(confCtx, localTool)
		if blocked {
			t.Errorf("internal tool %s should not be blocked in confidential mode", localTool)
		}
	}

	// External network tools (web_search, cloud_storage, iran_messenger) MUST be blocked
	for _, netTool := range []string{"web_search", "web_fetch", "cloud_storage", "iran_messenger", "external_messenger"} {
		msg, blocked := CheckConfidentialGate(confCtx, netTool)
		if !blocked {
			t.Errorf("external tool %s MUST be blocked in confidential mode", netTool)
		}
		if !strings.Contains(msg, "محرمانه") && !strings.Contains(msg, "Confidential") {
			t.Errorf("unexpected blocking message: %s", msg)
		}
	}
}
