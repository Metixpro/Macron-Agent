package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const StateFileName = ".macron_state.json"

// TaskState records the structured progress of an in-flight job so it can be resumed after a crash or restart.
type TaskState struct {
	SessionID   string     `json:"session_id"`
	Goal        string     `json:"goal"`
	UserPrompt  string     `json:"user_prompt"`
	Plan        []PlanItem `json:"plan"`
	Step        int        `json:"step"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Files       []string   `json:"files,omitempty"`
	Summary     string     `json:"summary,omitempty"`
	Interrupted bool       `json:"interrupted,omitempty"`
}

func statePath(ws string) string {
	return filepath.Join(ws, StateFileName)
}

// SaveTaskState writes the task progress state atomically to the workspace.
func SaveTaskState(ws string, s TaskState) error {
	if ws == "" {
		return nil
	}
	s.UpdatedAt = time.Now()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = s.UpdatedAt
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := statePath(ws) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath(ws))
}

// LoadTaskState reads the checkpoint state from the workspace if present.
func LoadTaskState(ws string) (*TaskState, error) {
	if ws == "" {
		return nil, nil
	}
	data, err := os.ReadFile(statePath(ws))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s TaskState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ClearTaskState removes the checkpoint file once a job completes successfully.
func ClearTaskState(ws string) {
	if ws != "" {
		_ = os.Remove(statePath(ws))
		_ = os.Remove(statePath(ws) + ".tmp")
	}
}

// ResumeNotice formats a clear continuation note for the agent prompt.
func (s *TaskState) ResumeNotice() string {
	if s == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("[System: Resuming interrupted task after restart/crash]\n")
	if s.Goal != "" {
		sb.WriteString(fmt.Sprintf("Original Goal: %s\n", s.Goal))
	}
	if len(s.Files) > 0 {
		sb.WriteString(fmt.Sprintf("Existing workspace files already produced: %s\n", strings.Join(s.Files, ", ")))
	}
	if len(s.Plan) > 0 {
		sb.WriteString("Current Plan status:\n")
		for i, item := range s.Plan {
			sb.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, item.Status, item.Text))
		}
	}
	if s.Summary != "" {
		sb.WriteString(fmt.Sprintf("Summary of completed work: %s\n", s.Summary))
	}
	sb.WriteString("Do NOT redo finished steps or rewrite existing working files. Continue directly with the remaining pending steps.")
	return sb.String()
}
