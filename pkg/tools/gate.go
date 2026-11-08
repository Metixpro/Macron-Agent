package tools

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

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

func (r *Registry) checkpoint(ctx context.Context, sessionID string, paths []string) string {
	if err := os.MkdirAll(r.CheckpointDir, 0o700); err != nil {
		return "checkpoint: " + err.Error()
	}
	f, err := os.CreateTemp(r.CheckpointDir, "checkpoint-"+sanitizeBase(sessionID)+"-*.tar.gz")
	if err != nil {
		return "checkpoint failed: " + err.Error()
	}
	file := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(file)
		return "checkpoint failed: " + err.Error()
	}
	ws, err := r.Machine.Workspace(sessionID)
	if err != nil {
		_ = os.Remove(file)
		return "checkpoint failed: " + err.Error()
	}
	args := []string{"czf", file, "--"}
	for _, path := range paths {
		if path == machine.MountPoint {
			path = ws
		} else if strings.HasPrefix(path, machine.MountPoint+"/") {
			path = filepath.Join(ws, strings.TrimPrefix(path, machine.MountPoint+"/"))
		}
		args = append(args, path)
	}
	checkpointCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(checkpointCtx, "tar", args...)
	cmd.Dir = ws

	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8"}
	if err := cmd.Run(); err != nil {
		_ = os.Remove(file)
		return fmt.Sprintf("checkpoint failed: %v", err)
	}
	return "checkpoint kept in " + file
}
