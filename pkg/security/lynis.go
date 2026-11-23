package security

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// LynisParams configures Lynis host/Dockerfile hardening audit or report parsing.
type LynisParams struct {
	Action     string // audit_system, audit_dockerfile, parse_report, status
	Path       string // lynis-report.dat or Dockerfile path
	Filter     string // keyword filter (e.g. ssh, kernel, firewall)
	Limit      int
	TimeoutSec int
}

// RunLynis executes Lynis audits when installed or parses lynis-report.dat in pure Go.
func (e Env) RunLynis(ctx context.Context, sessionID string, p LynisParams) (string, error) {
	action := strings.ToLower(strings.TrimSpace(p.Action))
	if action == "" {
		if strings.TrimSpace(p.Path) != "" && strings.Contains(strings.ToLower(p.Path), "dockerfile") {
			action = "audit_dockerfile"
		} else if strings.TrimSpace(p.Path) != "" {
			action = "parse_report"
		} else {
			action = "status"
		}
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 80 {
		limit = 80
	}

	lynisBin, hasLynis := FindBinary("lynis")
	switch action {
	case "status":
		if hasLynis {
			out, _, _, _ := e.RunBin(ctx, sessionID, 10*time.Second, lynisBin, "show", "version")
			return fmt.Sprintf("Lynis ready: v%s (%s). Pure-Go lynis-report.dat parser is also active.", strings.TrimSpace(out), lynisBin), nil
		}
		return "Lynis binary is not installed (install lynis for live system/Dockerfile audits). Pure-Go lynis-report.dat parser is ready for offline hardening review.", nil

	case "parse_report":
		repPath := p.Path
		if strings.TrimSpace(repPath) == "" {
			repPath = "/var/log/lynis-report.dat"
		}
		resolved, err := e.ResolvePath(sessionID, repPath)
		if err != nil {
			return "", err
		}
		return parseLynisReport(resolved, p.Filter, limit)

	case "audit_system":
		if !hasLynis {
			return "", fmt.Errorf("lynis binary is not installed; install lynis or pass action=parse_report with an existing lynis-report.dat")
		}
		tmpRep, err := os.CreateTemp("", "macron-lynis-*.dat")
		if err != nil {
			return "", err
		}
		repFile := tmpRep.Name()
		_ = tmpRep.Close()
		defer os.Remove(repFile)

		stdout, stderr, code, err := e.RunBin(ctx, sessionID, ClampTimeout(p.TimeoutSec, 180), lynisBin,
			"audit", "system", "--quick", "--no-colors", "--quiet", "--report-file", repFile)
		if err != nil {
			return "", fmt.Errorf("lynis audit failed: %w", err)
		}
		if fi, stErr := os.Stat(repFile); stErr == nil && fi.Size() > 0 {
			return parseLynisReport(repFile, p.Filter, limit)
		}
		return CapOutput(fmt.Sprintf("Lynis audit (exit=%d)\n%s\n%s", code, stdout, stderr)), nil

	case "audit_dockerfile":
		if !hasLynis {
			return "", fmt.Errorf("lynis binary is not installed")
		}
		resolved, err := e.ResolvePath(sessionID, p.Path)
		if err != nil {
			return "", err
		}
		stdout, stderr, code, err := e.RunBin(ctx, sessionID, ClampTimeout(p.TimeoutSec, 60), lynisBin,
			"audit", "dockerfile", "--no-colors", resolved)
		if err != nil {
			return "", err
		}
		return CapOutput(fmt.Sprintf("Lynis Dockerfile Audit (exit=%d)\n%s\n%s", code, stdout, stderr)), nil
	}
	return "", fmt.Errorf("unknown lynis action %q (use: audit_system, audit_dockerfile, parse_report, status)", action)
}

func parseLynisReport(path, filter string, limit int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot open Lynis report %s: %w", path, err)
	}
	defer f.Close()

	flt := strings.ToLower(strings.TrimSpace(filter))
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 32<<10), 1<<20)

	meta := map[string]string{}
	var warnings, suggestions, vulnPkgs []string

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "hardening_index", "os", "os_version", "hostname", "lynis_version", "tests_executed":
			meta[k] = v
		case "warning[]":
			if flt == "" || strings.Contains(strings.ToLower(v), flt) {
				warnings = append(warnings, formatLynisItem(v))
			}
		case "suggestion[]":
			if flt == "" || strings.Contains(strings.ToLower(v), flt) {
				suggestions = append(suggestions, formatLynisItem(v))
			}
		case "vulnerable_package[]":
			if flt == "" || strings.Contains(strings.ToLower(v), flt) {
				vulnPkgs = append(vulnPkgs, v)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Lynis Hardening Report (%s)\n", path)
	fmt.Fprintf(&sb, "Hardening Index: %s/100 | Host: %s | OS: %s %s | Tests: %s\n",
		nonEmpty(meta["hardening_index"], "?"),
		nonEmpty(meta["hostname"], "?"),
		nonEmpty(meta["os"], "?"),
		meta["os_version"],
		nonEmpty(meta["tests_executed"], "?"))
	fmt.Fprintf(&sb, "Warnings: %d | Suggestions: %d | Vulnerable Packages: %d\n", len(warnings), len(suggestions), len(vulnPkgs))

	if len(warnings) > 0 {
		sb.WriteString("\nWarnings:\n")
		for i, w := range warnings {
			if i >= limit {
				break
			}
			fmt.Fprintf(&sb, "- [WARN] %s\n", w)
		}
	}
	if len(vulnPkgs) > 0 {
		sb.WriteString("\nVulnerable Packages: " + strings.Join(vulnPkgs[:min(len(vulnPkgs), 15)], ", ") + "\n")
	}
	if len(suggestions) > 0 {
		sb.WriteString("\nTop Suggestions:\n")
		for i, s := range suggestions {
			if i >= limit {
				break
			}
			fmt.Fprintf(&sb, "- %s\n", s)
		}
	}
	return CapOutput(sb.String()), nil
}

func formatLynisItem(raw string) string {
	parts := strings.Split(raw, "|")
	if len(parts) >= 2 {
		id := strings.TrimSpace(parts[0])
		desc := strings.TrimSpace(parts[1])
		extra := ""
		if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" && strings.TrimSpace(parts[2]) != "-" {
			extra = " (" + truncateLine(parts[2], 60) + ")"
		}
		return fmt.Sprintf("[%s] %s%s", id, truncateLine(desc, 110), extra)
	}
	return truncateLine(raw, 130)
}

func nonEmpty(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
