package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type dryRunKey struct{}

// WithDryRun injects the dry-run flag into context.
func WithDryRun(ctx context.Context, dryRun bool) context.Context {
	return context.WithValue(ctx, dryRunKey{}, dryRun)
}

// IsDryRun returns true if the context has dry-run mode enabled.
func IsDryRun(ctx context.Context) bool {
	v, _ := ctx.Value(dryRunKey{}).(bool)
	return v
}

// DryRunAction records what a tool would do without modifying system or external state.
type DryRunAction struct {
	Tool       string `json:"tool"`
	Target     string `json:"target"`
	Summary    string `json:"summary"`
	Mutating   bool   `json:"mutating"`
	Simulation string `json:"simulation"`
}

// SimulateToolCall produces a preview outcome for mutating tools in dry-run mode.
func SimulateToolCall(name, argsJSON string) (DryRunAction, bool) {
	var args map[string]any
	_ = json.Unmarshal([]byte(argsJSON), &args)
	if args == nil {
		args = make(map[string]any)
	}

	switch name {
	case "write_file":
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		return DryRunAction{
			Tool:       name,
			Target:     path,
			Summary:    fmt.Sprintf("Would write %d bytes to %s", len(content), path),
			Mutating:   true,
			Simulation: fmt.Sprintf("[DRY-RUN PREVIEW] Successfully wrote %s (%d bytes). No changes were made on disk.", path, len(content)),
		}, true

	case "edit_file":
		path, _ := args["path"].(string)
		oldS, _ := args["old_string"].(string)
		newS, _ := args["new_string"].(string)
		return DryRunAction{
			Tool:       name,
			Target:     path,
			Summary:    fmt.Sprintf("Would replace %d bytes with %d bytes in %s", len(oldS), len(newS), path),
			Mutating:   true,
			Simulation: fmt.Sprintf("[DRY-RUN PREVIEW] Successfully replaced target text in %s. No changes were made on disk.", path),
		}, true

	case "bash_execute":
		cmd, _ := args["command"].(string)
		cmd = strings.TrimSpace(cmd)
		return DryRunAction{
			Tool:       name,
			Target:     cmd,
			Summary:    fmt.Sprintf("Would execute command: %s", cmd),
			Mutating:   cmdMutates(cmd),
			Simulation: fmt.Sprintf("[DRY-RUN PREVIEW] Simulated command execution: %s\n(Dry-run mode: command was not executed on host/sandbox)", cmd),
		}, true

	case "apply_patch":
		path, _ := args["file"].(string)
		return DryRunAction{
			Tool:       name,
			Target:     path,
			Summary:    fmt.Sprintf("Would apply diff patch to %s", path),
			Mutating:   true,
			Simulation: fmt.Sprintf("[DRY-RUN PREVIEW] Patch applied cleanly to %s. No changes made on disk.", path),
		}, true

	case "send_file":
		file, _ := args["filename"].(string)
		return DryRunAction{
			Tool:       name,
			Target:     file,
			Summary:    fmt.Sprintf("Would deliver file %s to user", file),
			Mutating:   false,
			Simulation: fmt.Sprintf("[DRY-RUN PREVIEW] File %s would be delivered to the chat.", file),
		}, true
	}
	return DryRunAction{}, false
}

