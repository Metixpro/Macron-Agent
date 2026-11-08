package tools

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

// gateCommand asks the command gate whether a command may run here; it
// returns why not ("" when it may), and keeps the checkpoint the gate asks
// for before a destructive command.
func (r *Registry) gateCommand(ctx context.Context, sessionID, cmd string) string {
	if r.Gate == nil {
		return ""
	}
	v := r.Gate.Check(ctx, sessionID, cmd)
	if !v.Allow {
		return v.Message
	}
	if len(v.Snapshot) > 0 && r.CheckpointDir != "" {
		if note := r.checkpoint(ctx, sessionID, v.Snapshot); note != "" {
			log.Printf("gate: %s", note)
		}
	}
	return ""
}

// checkpoint keeps a copy of what a command is about to delete.
func (r *Registry) checkpoint(ctx context.Context, sessionID string, paths []string) string {
	if err := os.MkdirAll(r.CheckpointDir, 0o700); err != nil {
		return "checkpoint: " + err.Error()
	}
	file := filepath.Join(r.CheckpointDir, time.Now().Format("20060102-150405")+"-"+sanitizeBase(sessionID)+".tar.gz")
	quoted := make([]string, 0, len(paths))
	for _, p := range paths {
		quoted = append(quoted, "'"+strings.ReplaceAll(p, "'", `'\''`)+"'")
	}
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Shell: "tar czf '" + strings.ReplaceAll(file, "'", `'\''`) + "' --ignore-failed-read -- " +
		strings.Join(quoted, " ") + " 2>/dev/null || true", Timeout: 2 * time.Minute, InSession: true})
	if res != nil && res.ExitCode != 0 {
		return fmt.Sprintf("checkpoint of %v failed (exit %d)", paths, res.ExitCode)
	}
	return "checkpoint kept in " + file
}
