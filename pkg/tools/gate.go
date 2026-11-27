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

func (r *Registry) gateCode(ctx context.Context, sessionID, language, code string) string {
	if r.Gate == nil {
		return ""
	}
	if v := r.Gate.CheckCode(ctx, sessionID, language, code); !v.Allow {
		return v.Message
	}
	return ""
}

func (r *Registry) gatePath(ctx context.Context, sessionID, tool, name string) string {
	return r.gateFile(ctx, sessionID, "tee", tool, name)
}

func (r *Registry) gateRead(ctx context.Context, sessionID, tool, name string) string {
	return r.gateFile(ctx, sessionID, "cat", tool, name)
}

func resolveWithAncestor(abs string) (string, error) {
	cur := filepath.Clean(abs)
	var tail []string
	for {
		realPath, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				realPath = filepath.Join(realPath, tail[i])
			}
			return realPath, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

func (r *Registry) ensureWorkspaceRel(sessionID, raw string) (string, error) {
	rel, err := cleanPath(raw)
	if err != nil {
		return "", err
	}
	abs, inside, err := r.Machine.HostPath(sessionID, rel)
	if err != nil || !inside {
		return "", fmt.Errorf("path %q must stay inside the workspace", raw)
	}
	ws, err := r.Machine.Workspace(sessionID)
	if err != nil {
		return "", err
	}
	realWS, err := filepath.EvalSymlinks(ws)
	if err != nil {
		realWS = filepath.Clean(ws)
	}
	realTarget, err := resolveWithAncestor(abs)
	if err != nil {
		return "", fmt.Errorf("invalid workspace path %q: %w", raw, err)
	}
	back, err := filepath.Rel(realWS, realTarget)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q resolves outside the workspace", raw)
	}
	return rel, nil
}

var blockedSecretPrefixes = []string{
	"MACRON_", "OPENAI_", "ANTHROPIC_", "GEMINI_", "OPENROUTER_", "DEEPSEEK_",
	"TELEGRAM_", "BALE_", "RUBIKA_", "EITAA_", "AWS_", "AZURE_", "S3_", "WEBDAV_",
	"SLACK_", "DISCORD_", "WHATSAPP_", "GMAIL_", "SMTP_", "NOTION_", "TRELLO_",
	"TODOIST_", "WAZUH_",
}

func isBlockedSecretEnv(name string) bool {
	u := strings.ToUpper(strings.TrimSpace(name))
	if u == "" {
		return true
	}
	if u == "GITHUB_TOKEN" || u == "GH_TOKEN" || u == "SUDO_PASSWORD" {
		return true
	}
	for _, p := range blockedSecretPrefixes {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return false
}

func (r *Registry) gateFile(ctx context.Context, sessionID, verb, tool, name string) string {
	if r.Gate == nil {
		return ""
	}
	abs, inside, err := r.Machine.HostPath(sessionID, name)
	if err != nil {
		return ""
	}
	if inside {
		if ws, err := r.Machine.Workspace(sessionID); err == nil {
			realWS, err1 := filepath.EvalSymlinks(ws)
			if err1 != nil {
				realWS = filepath.Clean(ws)
			}
			realTarget, err2 := resolveWithAncestor(abs)
			if err2 == nil {
				if rel, err := filepath.Rel(realWS, realTarget); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return ""
				}
				abs = realTarget
			}
		}
	}
	return r.gateCommand(ctx, sessionID, verb+" -- "+shellQuote(filepath.ToSlash(abs))+" # "+tool)
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
	cmd := machine.Hide(exec.CommandContext(checkpointCtx, "tar", args...))
	cmd.Dir = ws

	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8"}
	if err := cmd.Run(); err != nil {
		_ = os.Remove(file)
		return fmt.Sprintf("checkpoint failed: %v", err)
	}
	return "checkpoint kept in " + file
}
