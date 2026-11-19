package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/config"
)

const (
	settingExecutionMode = "execution_mode"
	envExecutionMode     = "MACRON_EXECUTION_MODE"
)

func normalizeExecutionMode(v string) (string, bool) {
	switch m := strings.ToLower(strings.TrimSpace(v)); m {
	case "local", "sandbox", "best-effort":
		return m, true
	default:
		return "", false
	}
}

func executionModeFlagSet(fs *flag.FlagSet) bool {
	if fs == nil {
		return false
	}
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "execution-mode" {
			set = true
		}
	})
	return set
}

// applySavedExecutionMode loads the persisted execution mode from the settings
// store only when MACRON_EXECUTION_MODE is not explicitly set in the environment.
func applySavedExecutionMode(cfg *config.Config, getSetting func(string) (string, bool)) {
	if cfg == nil || getSetting == nil {
		return
	}
	if strings.TrimSpace(os.Getenv(envExecutionMode)) != "" {
		return
	}
	saved, ok := getSetting(settingExecutionMode)
	if !ok {
		return
	}
	if mode, valid := normalizeExecutionMode(saved); valid {
		cfg.ExecutionMode = mode
	}
}

func (t *terminal) ExecutionMode() string {
	if t.app != nil && t.app.Machine != nil {
		return t.app.Machine.ExecutionMode()
	}
	if t.st != nil {
		if v, _ := t.st.Setting(settingExecutionMode); v != "" {
			return v
		}
	}
	return "best-effort"
}

func (t *terminal) Sandboxed() bool {
	return t.app != nil && t.app.Machine != nil && t.app.Machine.Sandboxed()
}

func (t *terminal) executionStatus() string {
	mode := t.ExecutionMode()
	if t.Sandboxed() {
		return mode + " (sandbox active)"
	}
	return mode + " (unsandboxed)"
}

func (t *terminal) SetExecutionMode(mode string) (string, error) {
	if t.app == nil || t.app.Machine == nil {
		return "", errors.New("execution machine is not initialized")
	}
	note, err := t.app.Machine.SetExecutionMode(mode)
	if err != nil {
		return "", err
	}
	active := t.app.Machine.ExecutionMode()
	if t.app.Config != nil {
		t.app.Config.ExecutionMode = active
	}
	if t.st != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := t.st.SetSetting(ctx, settingExecutionMode, active); err != nil {
			return note, err
		}
	}
	return note, nil
}
