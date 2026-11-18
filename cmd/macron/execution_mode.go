package main

import (
	"context"
	"errors"
	"time"
)

const settingExecutionMode = "execution_mode"

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