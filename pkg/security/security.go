package security

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/netclient"
)

const (
	// MaxOutputChars keeps every security tool response compact and token-efficient
	// while preserving high-signal findings (severities, counts, top indicators).
	MaxOutputChars = 3600

	DefaultTimeoutSec = 60
	MaxTimeoutSec     = 300
)

// Env provides shared runtime dependencies (workspace machine + HTTP client)
// so every security tool works identically across Desktop, TUI, and Telegram.
type Env struct {
	Machine *machine.Machine
	HTTP    *http.Client
}

func (e Env) httpClient() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return netclient.NewClient(netclient.Options{Timeout: 30 * time.Second, UseEnvProxy: true})
}

// ResolvePath resolves a file or directory path relative to the session workspace
// when possible, falling back to an explicit absolute path if it exists on disk.
func (e Env) ResolvePath(sessionID, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("path is required")
	}
	if e.Machine != nil {
		if wsDir, err := e.Machine.Workspace(sessionID); err == nil && wsDir != "" {
			if !filepath.IsAbs(raw) {
				cand := filepath.Clean(filepath.Join(wsDir, raw))
				if _, err := os.Stat(cand); err == nil {
					return cand, nil
				}
				return cand, nil
			}
		}
	}
	return filepath.Clean(raw), nil
}

// WorkspaceDir returns the session workspace directory or current working dir.
func (e Env) WorkspaceDir(sessionID string) string {
	if e.Machine != nil {
		if d, err := e.Machine.Workspace(sessionID); err == nil && d != "" {
			return d
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// RunBin executes a binary with explicit argv (never shell-interpolated) inside
// the session workspace with a bounded timeout.
func (e Env) RunBin(ctx context.Context, sessionID string, timeout time.Duration, bin string, args ...string) (stdout, stderr string, exitCode int, err error) {
	if timeout <= 0 {
		timeout = time.Duration(DefaultTimeoutSec) * time.Second
	}
	if e.Machine != nil {
		res := e.Machine.Run(ctx, sessionID, machine.RunOptions{
			HostExecutable: true,
			Args:           append([]string{bin}, args...),
			Timeout:        timeout,
		})
		if res.Error != "" {
			return res.Stdout, res.Stderr, res.ExitCode, fmt.Errorf("%s", res.Error)
		}
		return res.Stdout, res.Stderr, res.ExitCode, nil
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(rctx, bin, args...)
	var outB, errB strings.Builder
	cmd.Stdout = &outB
	cmd.Stderr = &errB
	runErr := cmd.Run()
	code := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			code = ee.ExitCode()
			runErr = nil
		}
	}
	return outB.String(), errB.String(), code, runErr
}

// FindBinary locates the first available executable name in PATH.
func FindBinary(names ...string) (string, bool) {
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p, true
		}
	}
	return "", false
}

// ClampTimeout converts timeoutSeconds into a bounded time.Duration.
func ClampTimeout(seconds, defSec int) time.Duration {
	if defSec <= 0 {
		defSec = DefaultTimeoutSec
	}
	if seconds <= 0 {
		seconds = defSec
	}
	if seconds > MaxTimeoutSec {
		seconds = MaxTimeoutSec
	}
	return time.Duration(seconds) * time.Second
}

// CapOutput trims output cleanly at MaxOutputChars with a concise notice so
// security tool outputs never bloat the LLM context window.
func CapOutput(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= MaxOutputChars {
		return s
	}
	return strings.TrimSpace(string(r[:MaxOutputChars])) + fmt.Sprintf("\n… (truncated %d chars for token efficiency; narrow filter/limit to inspect more)", len(r)-MaxOutputChars)
}

// KVCount represents a label and its frequency count.
type KVCount struct {
	Key   string
	Count int
}

// TopCounts returns up to limit entries sorted by descending count then key.
func TopCounts(m map[string]int, limit int) []KVCount {
	list := make([]KVCount, 0, len(m))
	for k, v := range m {
		if k == "" || v <= 0 {
			continue
		}
		list = append(list, KVCount{Key: k, Count: v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Count != list[j].Count {
			return list[i].Count > list[j].Count
		}
		return list[i].Key < list[j].Key
	})
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list
}

func truncateLine(s string, maxRunes int) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " "))
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}
