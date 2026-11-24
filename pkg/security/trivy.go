package security

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TrivyParams configures Trivy vulnerability, misconfiguration, and secret scanning.
type TrivyParams struct {
	Action     string // fs, image, config, repo, parse_report, status
	Target     string // workspace path, image name, repo URL, or JSON report path
	Severity   string // comma-separated: CRITICAL,HIGH,MEDIUM,LOW (default: CRITICAL,HIGH)
	Scanners   string // vuln,misconfig,secret (default: vuln,misconfig,secret)
	Limit      int
	TimeoutSec int
}

// RunTrivy executes Trivy scans or summarizes Trivy JSON reports into a compact,
// deduplicated CVE/misconfig/secret table for low token usage.
func (e Env) RunTrivy(ctx context.Context, sessionID string, p TrivyParams) (string, error) {
	action := strings.ToLower(strings.TrimSpace(p.Action))
	if action == "" {
		if strings.HasSuffix(strings.ToLower(p.Target), ".json") {
			action = "parse_report"
		} else {
			action = "fs"
		}
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 80 {
		limit = 80
	}
	sev := strings.ToUpper(strings.TrimSpace(p.Severity))
	if sev == "" {
		sev = "CRITICAL,HIGH"
	}

	trivyBin, hasTrivy := FindBinary("trivy", "trivy.exe")
	if action == "status" {
		if hasTrivy {
			out, _, _, _ := e.RunBin(ctx, sessionID, 10*time.Second, trivyBin, "--version")
			first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
			return fmt.Sprintf("Trivy ready: %s (%s). Pure-Go Trivy JSON report summarizer is also active.", first, trivyBin), nil
		}
		return "Trivy binary is not installed (install trivy for live fs/image/IaC/secret scans). Pure-Go Trivy JSON report parser is ready via action=parse_report.", nil
	}

	if action == "parse_report" {
		resolved, err := e.ResolvePath(sessionID, p.Target)
		if err != nil {
			return "", err
		}
		data, err := ReadFileCapped(resolved, 25<<20)
		if err != nil {
			return "", fmt.Errorf("cannot read Trivy report %s: %w", resolved, err)
		}
		return summarizeTrivyJSON(resolved, data, sev, limit)
	}

	if !hasTrivy {
		return "", fmt.Errorf("trivy binary is not installed; install trivy (https://trivy.dev) or analyze an existing JSON report with action=parse_report")
	}

	target := strings.TrimSpace(p.Target)
	switch action {
	case "fs", "config":
		if target == "" {
			target = e.WorkspaceDir(sessionID)
		} else {
			var err error
			if target, err = e.ResolvePath(sessionID, target); err != nil {
				return "", err
			}
		}
	case "image", "repo":
		if target == "" {
			return "", fmt.Errorf("target is required for trivy %s", action)
		}
	default:
		return "", fmt.Errorf("unknown trivy action %q (use: fs, image, config, repo, parse_report, status)", action)
	}

	args := []string{action, "--format", "json", "--quiet", "--severity", sev}
	if action == "fs" || action == "repo" {
		sc := strings.TrimSpace(p.Scanners)
		if sc == "" {
			sc = "vuln,misconfig,secret"
		}
		args = append(args, "--scanners", sc)
	}
	args = append(args, target)

	stdout, stderr, code, err := e.RunBin(ctx, sessionID, ClampTimeout(p.TimeoutSec, 150), trivyBin, args...)
	if err != nil {
		return "", fmt.Errorf("trivy error: %w", err)
	}
	if strings.TrimSpace(stdout) == "" {
		if code != 0 {
			return "", fmt.Errorf("trivy exited %d: %s", code, truncateLine(stderr, 260))
		}
		return fmt.Sprintf("Trivy %s (%s): no findings matching %s.", action, target, sev), nil
	}
	return summarizeTrivyJSON(target, []byte(stdout), sev, limit)
}

func summarizeTrivyJSON(label string, data []byte, severityFilter string, limit int) (string, error) {
	var rep struct {
		ArtifactName string `json:"ArtifactName"`
		Results      []struct {
			Target          string `json:"Target"`
			Type            string `json:"Type"`
			Vulnerabilities []struct {
				VulnerabilityID  string `json:"VulnerabilityID"`
				PkgName          string `json:"PkgName"`
				InstalledVersion string `json:"InstalledVersion"`
				FixedVersion     string `json:"FixedVersion"`
				Severity         string `json:"Severity"`
				Title            string `json:"Title"`
			} `json:"Vulnerabilities"`
			Misconfigurations []struct {
				ID       string `json:"ID"`
				Title    string `json:"Title"`
				Severity string `json:"Severity"`
				Status   string `json:"Status"`
			} `json:"Misconfigurations"`
			Secrets []struct {
				RuleID    string `json:"RuleID"`
				Category  string `json:"Category"`
				Severity  string `json:"Severity"`
				Title     string `json:"Title"`
				StartLine int    `json:"StartLine"`
			} `json:"Secrets"`
		} `json:"Results"`
	}
	if err := json.Unmarshal(data, &rep); err != nil {
		return "", fmt.Errorf("invalid Trivy JSON: %w", err)
	}

	allowed := map[string]bool{}
	for _, s := range strings.Split(strings.ToUpper(severityFilter), ",") {
		if s = strings.TrimSpace(s); s != "" {
			allowed[s] = true
		}
	}
	wantSev := func(s string) bool {
		if len(allowed) == 0 {
			return true
		}
		return allowed[strings.ToUpper(s)]
	}

	sevCounts := map[string]int{}
	var vulns, misconfigs, secrets []string
	seenVuln := map[string]bool{}

	for _, r := range rep.Results {
		for _, v := range r.Vulnerabilities {
			if !wantSev(v.Severity) {
				continue
			}
			key := v.VulnerabilityID + "|" + v.PkgName + "|" + v.InstalledVersion
			if seenVuln[key] {
				continue
			}
			seenVuln[key] = true
			sevCounts[strings.ToUpper(v.Severity)]++
			fix := v.FixedVersion
			if fix == "" {
				fix = "no-fix"
			}
			vulns = append(vulns, fmt.Sprintf("- [%s] %s %s@%s -> fix:%s (%s)",
				v.Severity, v.VulnerabilityID, v.PkgName, v.InstalledVersion, fix, truncateLine(v.Title, 64)))
		}
		for _, m := range r.Misconfigurations {
			if strings.EqualFold(m.Status, "PASS") || !wantSev(m.Severity) {
				continue
			}
			sevCounts[strings.ToUpper(m.Severity)]++
			misconfigs = append(misconfigs, fmt.Sprintf("- [%s] %s (%s): %s", m.Severity, m.ID, r.Target, truncateLine(m.Title, 72)))
		}
		for _, s := range r.Secrets {
			if !wantSev(s.Severity) {
				continue
			}
			sevCounts[strings.ToUpper(s.Severity)]++
			secrets = append(secrets, fmt.Sprintf("- [%s] %s %s:%d (%s)", s.Severity, s.RuleID, r.Target, s.StartLine, truncateLine(s.Title, 64)))
		}
	}

	name := rep.ArtifactName
	if name == "" {
		name = label
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Trivy Summary (%s)\n", name)
	fmt.Fprintf(&sb, "Severity: CRITICAL=%d HIGH=%d MEDIUM=%d LOW=%d | Vulns=%d Misconfigs=%d Secrets=%d\n",
		sevCounts["CRITICAL"], sevCounts["HIGH"], sevCounts["MEDIUM"], sevCounts["LOW"],
		len(vulns), len(misconfigs), len(secrets))

	if len(secrets) > 0 {
		sb.WriteString("\nExposed Secrets:\n" + strings.Join(secrets[:min(len(secrets), limit)], "\n") + "\n")
	}
	if len(vulns) > 0 {
		sb.WriteString("\nVulnerabilities:\n" + strings.Join(vulns[:min(len(vulns), limit)], "\n") + "\n")
	}
	if len(misconfigs) > 0 {
		sb.WriteString("\nMisconfigurations:\n" + strings.Join(misconfigs[:min(len(misconfigs), limit)], "\n") + "\n")
	}
	if len(vulns)+len(misconfigs)+len(secrets) == 0 {
		sb.WriteString("Clean: 0 findings matched the severity filter.\n")
	}
	return CapOutput(sb.String()), nil
}
