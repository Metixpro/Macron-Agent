package main

import (
	"context"
	"flag"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/machine"
)

func TestApplySavedExecutionMode_LoadsFromStoreWhenEnvUnset(t *testing.T) {
	t.Setenv(envExecutionMode, "")

	cfg := &config.Config{ExecutionMode: "best-effort"}
	applySavedExecutionMode(cfg, func(key string) (string, bool) {
		if key != settingExecutionMode {
			t.Fatalf("unexpected setting key %q", key)
		}
		return "sandbox", true
	})

	if cfg.ExecutionMode != "sandbox" {
		t.Fatalf("expected saved mode %q, got %q", "sandbox", cfg.ExecutionMode)
	}
}

func TestApplySavedExecutionMode_EnvOverridesSavedSetting(t *testing.T) {
	t.Setenv(envExecutionMode, "sandbox")

	called := false
	cfg := &config.Config{ExecutionMode: "sandbox"}
	applySavedExecutionMode(cfg, func(key string) (string, bool) {
		called = true
		return "local", true
	})

	if called {
		t.Fatalf("expected saved setting lookup to be skipped when %s is set", envExecutionMode)
	}
	if cfg.ExecutionMode != "sandbox" {
		t.Fatalf("expected env mode %q to win over saved setting, got %q", "sandbox", cfg.ExecutionMode)
	}
}

func TestApplySavedExecutionMode_IgnoresInvalidOrMissingSetting(t *testing.T) {
	t.Setenv(envExecutionMode, "")

	cfg := &config.Config{ExecutionMode: "best-effort"}
	applySavedExecutionMode(cfg, func(string) (string, bool) {
		return "corrupted-value", true
	})
	if cfg.ExecutionMode != "best-effort" {
		t.Fatalf("expected invalid saved mode to be ignored, got %q", cfg.ExecutionMode)
	}

	applySavedExecutionMode(cfg, func(string) (string, bool) {
		return "", false
	})
	if cfg.ExecutionMode != "best-effort" {
		t.Fatalf("expected missing setting to keep default mode, got %q", cfg.ExecutionMode)
	}
}

func TestExecutionModeFlagSet(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := &config.Config{}
	cfg.Bind(fs)

	if err := fs.Parse(nil); err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if executionModeFlagSet(fs) {
		t.Fatalf("expected execution-mode flag to be unset")
	}

	fs2 := flag.NewFlagSet("test2", flag.ContinueOnError)
	cfg2 := &config.Config{}
	cfg2.Bind(fs2)
	if err := fs2.Parse([]string{"-execution-mode", "sandbox"}); err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !executionModeFlagSet(fs2) {
		t.Fatalf("expected execution-mode flag to be detected as explicitly set")
	}
}

func TestTerminalExecutionStatus(t *testing.T) {
	t.Setenv(envExecutionMode, "")
	t.Setenv("MACRON_SANDBOX", "")

	m, err := machine.New(machine.Config{
		BaseDir:       t.TempDir(),
		Sandbox:       machine.SandboxOff,
		ExecutionMode: "local",
	})
	if err != nil {
		t.Fatalf("unexpected machine init error: %v", err)
	}

	term := &terminal{app: &app.App{Machine: m}}
	if got := term.executionStatus(); got != "local (unsandboxed)" {
		t.Fatalf("expected %q, got %q", "local (unsandboxed)", got)
	}
}

func TestBotSetExecutionMode_FailClosedAndSwitch(t *testing.T) {
	t.Setenv(envExecutionMode, "")
	t.Setenv("MACRON_SANDBOX", "")
	t.Setenv("PATH", t.TempDir())

	m, err := machine.New(machine.Config{
		BaseDir:       t.TempDir(),
		Sandbox:       machine.SandboxOff,
		ExecutionMode: "best-effort",
	})
	if err != nil {
		t.Fatalf("unexpected machine init error: %v", err)
	}

	cfg := &config.Config{ExecutionMode: "best-effort"}
	b := &Bot{app: &app.App{Config: cfg, Machine: m}}

	if _, err := b.setExecutionMode(context.Background(), "sandbox"); err == nil {
		t.Fatalf("expected strict sandbox switch to fail closed when bwrap is missing")
	}
	if b.executionMode() != "best-effort" {
		t.Fatalf("expected previous mode %q to be preserved, got %q", "best-effort", b.executionMode())
	}

	note, err := b.setExecutionMode(context.Background(), "local")
	if err != nil {
		t.Fatalf("expected switch to local to succeed, got: %v", err)
	}
	if !strings.Contains(note, "LOCAL") {
		t.Fatalf("expected local bypass note, got %q", note)
	}
	if b.executionMode() != "local" || cfg.ExecutionMode != "local" {
		t.Fatalf("expected bot and config mode to update to local, got %q / %q", b.executionMode(), cfg.ExecutionMode)
	}
}
