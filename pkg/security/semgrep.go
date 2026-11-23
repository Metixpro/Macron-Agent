package security

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// SemgrepParams configures Semgrep SAST scanning or JSON/SARIF report triage.
type SemgrepParams struct {
	Action     string // scan, parse_report, status
	Path       string // target dir/file or JSON report path
	Config     string // rule config: auto, p/security-audit, p/owasp-top-ten, or local .yml
	Severity   string // ERROR,WARNING,INFO
	Limit      int
	TimeoutSec int
}

// RunSemgrep executes Semgrep SAST scans when installed or summarizes Semgrep
// JSON/SARIF reports into compact, high-signal findings.
func (e Env) RunSemgrep(ctx context.Context, sessionID string, p SemgrepParams) (string, error) {
	action := strings.ToLower(strings.TrimSpace(p.Action))
	if action == "" {
		lower := strings.ToLower(p.Path)
		if strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".sarif") {
			action = "parse_report"
		} else {
			action = "scan"
		}
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 80 {
		limit = 80
	}

	semgrepBin, hasSemgrep := FindBinary("semgrep", "semgrep.exe")
	if action == "status" {
		if hasSemgrep {
			out, _, _, _ := e.RunBin(ctx, sessionID, 15*time.Second, semgrepBin, "--version")
			return fmt.Sprintf("Semgrep ready: %s (%s). Pure-Go JSON/SARIF report parser is also active.", strings.TrimSpace(out), semgrepBin), nil
		}
		return "Semgrep binary is not installed (install via `pip install semgrep` for live SAST scans). Pure-Go JSON/SARIF report parser is ready via action=parse_report.", nil
	}

	if action == "parse_report" {
		resolved, err := e.ResolvePath(sessionID, p.Path)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return "", fmt.Errorf("cannot read Semgrep report %s: %w", resolved, err)
		}
		return summarizeSemgrepJSON(resolved, data, p.Severity, limit)
	}

	if action != "scan" {
		return "", fmt.Errorf("unknown semgrep action %q (use: scan, parse_report, status)", action)
	}
	if !hasSemgrep {
		return "", fmt.Errorf("semgrep binary is not installed; install semgrep (`pip install semgrep`) or pass action=parse_report with an existing JSON/SARIF report")
	}

	target := strings.TrimSpace(p.Path)
	if target == "" {
		target = e.WorkspaceDir(sessionID)
	} else {
		var err error
		if target, err = e.ResolvePath(sessionID, target); err != nil {
			return "", err
		}
	}
	cfg := strings.TrimSpace(p.Config)
	if cfg == "" {
		cfg = "auto"
	}

	args := []string{"scan", "--json", "--quiet", "--metrics=off", "--config", cfg}
	if sev := strings.ToUpper(strings.TrimSpace(p.Severity)); sev != "" {
		for _, s := range strings.Split(sev, ",") {
			if s = strings.TrimSpace(s); s != "" {
				args = append(args, "--severity", s)
			}
		}
	}
	args = append(args, target)

	stdout, stderr, code, err := e.RunBin(ctx, sessionID, ClampTimeout(p.TimeoutSec, 150), semgrepBin, args...)
	if err != nil {
		return "", fmt.Errorf("semgrep error: %w", err)
	}
	if strings.TrimSpace(stdout) == "" {
		if code != 0 {
			return "", fmt.Errorf("semgrep exited %d: %s", code, truncateLine(stderr, 260))
		}
		return fmt.Sprintf("Semgrep scan (%s, config=%s): 0 findings.", target, cfg), nil
	}
	return summarizeSemgrepJSON(target, []byte(stdout), p.Severity, limit)
}

func summarizeSemgrepJSON(label string, data []byte, severityFilter string, limit int) (string, error) {
	// Support both native Semgrep JSON (`results`) and SARIF (`runs`).
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", fmt.Errorf("invalid Semgrep JSON/SARIF: %w", err)
	}
	if _, isSarif := raw["runs"]; isSarif {
		return summarizeSARIF(label, data, limit)
	}

	var rep struct {
		Results []struct {
			CheckID string `json:"check_id"`
			Path    string `json:"path"`
			Start   struct {
				Line int `json:"line"`
			} `json:"start"`
			Extra struct {
				Message  string `json:"message"`
				Severity string `json:"severity"`
				Metadata struct {
					CWE   any `json:"cwe"`
					OWASP any `json:"owasp"`
				} `json:"metadata"`
			} `json:"extra"`
		} `json:"results"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &rep); err != nil {
		return "", fmt.Errorf("invalid Semgrep JSON: %w", err)
	}

	allowed := map[string]bool{}
	for _, s := range strings.Split(strings.ToUpper(severityFilter), ",") {
		if s = strings.TrimSpace(s); s != "" {
			allowed[s] = true
		}
	}
	sevCounts := map[string]int{}
	ruleCounts := map[string]int{}
	var findings []string

	for _, r := range rep.Results {
		sev := strings.ToUpper(nonEmpty(r.Extra.Severity, "WARNING"))
		if len(allowed) > 0 && !allowed[sev] {
			continue
		}
		sevCounts[sev]++
		ruleCounts[r.CheckID]++
		if len(findings) < limit {
			findings = append(findings, fmt.Sprintf("- [%s] %s:%d (%s) — %s",
				sev, r.Path, r.Start.Line, shortRuleID(r.CheckID), truncateLine(r.Extra.Message, 95)))
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Semgrep SAST Summary (%s)\nFindings: %d (ERROR=%d WARNING=%d INFO=%d) | Errors: %d\n",
		label, len(rep.Results), sevCounts["ERROR"], sevCounts["WARNING"], sevCounts["INFO"], len(rep.Errors))
	if top := TopCounts(ruleCounts, 6); len(top) > 0 {
		sb.WriteString("Top Rules: " + joinCounts(top) + "\n")
	}
	if len(findings) > 0 {
		sb.WriteString("\nFindings:\n" + strings.Join(findings, "\n") + "\n")
	} else {
		sb.WriteString("Clean: no security issues matched.\n")
	}
	return CapOutput(sb.String()), nil
}

func summarizeSARIF(label string, data []byte, limit int) (string, error) {
	var sarif struct {
		Runs []struct {
			Results []struct {
				RuleID  string `json:"ruleId"`
				Level   string `json:"level"`
				Message struct {
					Text string `json:"text"`
				} `json:"message"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
						Region struct {
							StartLine int `json:"startLine"`
						} `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(data, &sarif); err != nil {
		return "", err
	}
	total := 0
	var lines []string
	for _, run := range sarif.Runs {
		for _, r := range run.Results {
			total++
			if len(lines) < limit {
				loc := ""
				if len(r.Locations) > 0 {
					pl := r.Locations[0].PhysicalLocation
					loc = fmt.Sprintf("%s:%d", pl.ArtifactLocation.URI, pl.Region.StartLine)
				}
				lines = append(lines, fmt.Sprintf("- [%s] %s (%s) — %s",
					strings.ToUpper(nonEmpty(r.Level, "warning")), loc, shortRuleID(r.RuleID), truncateLine(r.Message.Text, 90)))
			}
		}
	}
	return CapOutput(fmt.Sprintf("Semgrep SARIF Summary (%s)\nFindings: %d\n\n%s", label, total, strings.Join(lines, "\n"))), nil
}

func shortRuleID(id string) string {
	if i := strings.LastIndex(id, "."); i >= 0 && i+1 < len(id) {
		return id[i+1:]
	}
	return id
}
