package machine

import (
	"strings"
	"testing"
)

func clearExecEnv(t *testing.T) {
	t.Helper()
	t.Setenv("MACRON_EXECUTION_MODE", "")
	t.Setenv("MACRON_SANDBOX", "")
}

func TestNew_LocalBypassesSandbox(t *testing.T) {
	clearExecEnv(t)

	m, err := New(Config{
		BaseDir:       t.TempDir(),
		Sandbox:       SandboxBwrap,
		ExecutionMode: "local",
	})
	if err != nil {
		t.Fatalf("expected local mode to succeed, got error: %v", err)
	}
	if m.ExecutionMode() != "local" {
		t.Fatalf("expected execution mode %q, got %q", "local", m.ExecutionMode())
	}
	if m.Sandboxed() {
		t.Fatalf("expected sandbox to be bypassed in local mode")
	}
}

func TestNew_StrictSandboxFailsClosedWhenSandboxOff(t *testing.T) {
	clearExecEnv(t)

	_, err := New(Config{
		BaseDir:       t.TempDir(),
		Sandbox:       SandboxOff,
		ExecutionMode: "sandbox",
	})
	if err == nil {
		t.Fatalf("expected strict sandbox mode to fail closed when sandbox is off")
	}
	if !strings.Contains(err.Error(), "sandbox setting is 'off'") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestNew_StrictSandboxFailsClosedWhenBwrapMissing(t *testing.T) {
	clearExecEnv(t)
	t.Setenv("PATH", t.TempDir())

	_, err := New(Config{
		BaseDir:       t.TempDir(),
		ExecutionMode: "sandbox",
	})
	if err == nil {
		t.Fatalf("expected strict sandbox mode to fail closed when bwrap is missing")
	}
	if !strings.Contains(err.Error(), "bwrap is not installed") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestNew_StrictSandboxFailsClosedOnUnknownBackend(t *testing.T) {
	clearExecEnv(t)

	_, err := New(Config{
		BaseDir:       t.TempDir(),
		Sandbox:       "unknown-backend",
		ExecutionMode: "sandbox",
	})
	if err == nil {
		t.Fatalf("expected strict sandbox mode to fail closed on unknown backend")
	}
	if !strings.Contains(err.Error(), "strict sandbox mode is required") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestNew_BestEffortFallsBackWhenBwrapMissing(t *testing.T) {
	clearExecEnv(t)
	t.Setenv("PATH", t.TempDir())

	m, err := New(Config{
		BaseDir:       t.TempDir(),
		Sandbox:       SandboxBwrap,
		ExecutionMode: "best-effort",
	})
	if err != nil {
		t.Fatalf("expected best-effort mode to fall back without error, got: %v", err)
	}
	if m.ExecutionMode() != "best-effort" {
		t.Fatalf("expected execution mode %q, got %q", "best-effort", m.ExecutionMode())
	}
	if m.Sandboxed() {
		t.Fatalf("expected unsandboxed fallback when bwrap is missing")
	}
}

func TestNew_InvalidExecutionModeRejected(t *testing.T) {
	clearExecEnv(t)

	_, err := New(Config{
		BaseDir:       t.TempDir(),
		ExecutionMode: "invalid-mode",
	})
	if err == nil {
		t.Fatalf("expected error for invalid execution mode")
	}
	if !strings.Contains(err.Error(), "invalid execution mode") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestSetExecutionMode_FailClosedPreservesPreviousState(t *testing.T) {
	clearExecEnv(t)
	t.Setenv("PATH", t.TempDir())

	m, err := New(Config{
		BaseDir:       t.TempDir(),
		ExecutionMode: "local",
	})
	if err != nil {
		t.Fatalf("unexpected init error: %v", err)
	}

	if _, err := m.SetExecutionMode("sandbox"); err == nil {
		t.Fatalf("expected runtime switch to strict sandbox to fail when bwrap is missing")
	}
	if m.ExecutionMode() != "local" {
		t.Fatalf("expected previous mode %q to be preserved after failed switch, got %q", "local", m.ExecutionMode())
	}
	if m.Sandboxed() {
		t.Fatalf("expected sandbox state to remain false after failed switch")
	}
}

func TestSetExecutionMode_SwitchAndValidation(t *testing.T) {
	clearExecEnv(t)

	m, err := New(Config{
		BaseDir:       t.TempDir(),
		Sandbox:       SandboxOff,
		ExecutionMode: "best-effort",
	})
	if err != nil {
		t.Fatalf("unexpected init error: %v", err)
	}

	note, err := m.SetExecutionMode("local")
	if err != nil {
		t.Fatalf("expected switch to local to succeed, got: %v", err)
	}
	if !strings.Contains(note, "LOCAL") {
		t.Fatalf("expected local bypass note, got %q", note)
	}
	if m.ExecutionMode() != "local" {
		t.Fatalf("expected execution mode %q, got %q", "local", m.ExecutionMode())
	}

	if _, err := m.SetExecutionMode("banana"); err == nil {
		t.Fatalf("expected invalid mode switch to fail")
	}
	if m.ExecutionMode() != "local" {
		t.Fatalf("expected mode to remain %q after invalid switch, got %q", "local", m.ExecutionMode())
	}

	if _, err := m.SetExecutionMode("best-effort"); err != nil {
		t.Fatalf("expected switch back to best-effort to succeed, got: %v", err)
	}
	if m.ExecutionMode() != "best-effort" {
		t.Fatalf("expected execution mode %q, got %q", "best-effort", m.ExecutionMode())
	}
}
