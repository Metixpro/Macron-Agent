package tools

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

const ToolDeployService = "deploy_service"

// DeployWithRollback manages an automated deployment lifecycle with instant rollback on healthcheck failure.
func (r *Registry) DeployWithRollback(ctx context.Context, sessionID, deployCmd, healthURL, rollbackCmd string) (string, error) {
	deployCmd = strings.TrimSpace(deployCmd)
	if deployCmd == "" {
		return "", fmt.Errorf("deploy_cmd is required")
	}

	client := r.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	// 1. Run deploy command
	deployOpts := machine.RunOptions{
		Shell:   deployCmd,
		Timeout: 3 * time.Minute,
	}
	res := r.Machine.Run(ctx, sessionID, deployOpts)
	if res.ExitCode != 0 {
		return fmt.Sprintf("❌ Deployment command failed (exit %d): %s\n%s", res.ExitCode, res.Error, res.Stderr), nil
	}

	// 2. Healthcheck polling (if healthURL provided)
	if healthURL != "" {
		healthy := false
		var lastErr string
		deadline := time.Now().Add(30 * time.Second)

		for time.Now().Before(deadline) {
			if ctx.Err() != nil {
				break
			}
			hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			req, err := http.NewRequestWithContext(hctx, "GET", healthURL, nil)
			if err == nil {
				resp, err := client.Do(req)
				if err == nil {
					resp.Body.Close()
					if resp.StatusCode >= 200 && resp.StatusCode < 300 {
						healthy = true
						cancel()
						break
					}
					lastErr = fmt.Sprintf("HTTP %d", resp.StatusCode)
				} else {
					lastErr = err.Error()
				}
			}
			cancel()
			time.Sleep(2 * time.Second)
		}

		// 3. Rollback on healthcheck failure
		if !healthy {
			rollbackNote := "No rollback command configured."
			if rollbackCmd != "" {
				rbOpts := machine.RunOptions{
					Shell:   rollbackCmd,
					Timeout: 2 * time.Minute,
				}
				rbRes := r.Machine.Run(ctx, sessionID, rbOpts)
				rollbackNote = fmt.Sprintf("Automatic rollback executed (exit %d). Output: %s", rbRes.ExitCode, rbRes.Stdout)
			}
			return fmt.Sprintf("🚨 Healthcheck failed at %s (%s). %s", healthURL, lastErr, rollbackNote), nil
		}
	}

	return fmt.Sprintf("🚀 Deployment completed successfully!\nCommand: `%s`\nHealthcheck: Verified healthy (%s).", deployCmd, healthURL), nil
}
