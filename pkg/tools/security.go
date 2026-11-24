package tools

import (
	"context"

	"github.com/Metixpro/macron-agent/pkg/security"
)

// Security tool names. They are deliberately the product names so the agent's
// deferred-tool shelf preloads a tool only when the user actually mentions it
// (zero prompt tokens otherwise).
const (
	ToolWazuh     = "wazuh"
	ToolWireshark = "wireshark"
	ToolSuricata  = "suricata"
	ToolZeek      = "zeek"
	ToolLynis     = "lynis"
	ToolTrivy     = "trivy"
	ToolSemgrep   = "semgrep"
	ToolAutopsy   = "autopsy"
)

// SecurityToolNames lists the security toolkit in display order.
var SecurityToolNames = []string{ToolWazuh, ToolWireshark, ToolSuricata, ToolZeek, ToolLynis, ToolTrivy, ToolSemgrep, ToolAutopsy}

func (r *Registry) securityEnv() security.Env {
	return security.Env{Machine: r.Machine, HTTP: r.httpClient()}
}

func secLimit(args map[string]any) int   { return intArg(args, "limit", 20, 1, 200) }
func secTimeout(args map[string]any) int { return intArg(args, "timeout_seconds", 60, 5, 300) }

// registerSecurityTools wires the pkg/security toolkit into the shared registry,
// so Desktop, TUI and Telegram all get identical tools. Schemas stay compact and
// every result is capped, keeping token use low.
func (r *Registry) registerSecurityTools() {
	limit := prop("integer", "max items (default 20)")
	timeout := prop("integer", "seconds, 5-300 (default 60)")

	r.add(ToolWazuh,
		"Wazuh SIEM/XDR: manager status, agents, alerts, FIM (syscheck), SCA, vulnerabilities via WAZUH_API_URL/USER/PASS; parse_alerts reads a local alerts.json offline.",
		map[string]any{
			"action":          enumProp("", "status", "agents", "alerts", "syscheck", "sca", "vulnerabilities", "parse_alerts"),
			"agent_id":        prop("string", "agent id for syscheck/sca/vulnerabilities (default 000)"),
			"path":            prop("string", "alerts.json path (parse_alerts)"),
			"query":           prop("string", "keyword filter"),
			"min_level":       prop("integer", "minimum rule level 0-15"),
			"limit":           limit,
			"timeout_seconds": timeout,
		}, []string{"action"},
		func(ctx context.Context, sid string, a map[string]any) (string, error) {
			return r.securityEnv().RunWazuh(ctx, sid, security.WazuhParams{
				Action: strArg(a, "action"), AgentID: strArg(a, "agent_id"), Path: strArg(a, "path"),
				Query: strArg(a, "query"), MinLevel: intArg(a, "min_level", 0, 0, 15),
				Limit: secLimit(a), TimeoutSec: secTimeout(a),
			})
		})

	r.add(ToolWireshark,
		"Wireshark/tshark packet-capture analysis of a .pcap/.pcapng (pure-Go fallback for classic pcap): summary, packets, conversations, dns, http, tls, extract_ips.",
		map[string]any{
			"action":          enumProp("", "summary", "packets", "conversations", "dns", "http", "tls", "extract_ips", "status"),
			"path":            prop("string", "capture file"),
			"filter":          prop("string", "display filter, e.g. 'dns || http.request' (tshark only)"),
			"limit":           limit,
			"timeout_seconds": timeout,
		}, []string{"action"},
		func(ctx context.Context, sid string, a map[string]any) (string, error) {
			return r.securityEnv().RunWireshark(ctx, sid, security.WiresharkParams{
				Action: strArg(a, "action"), Path: strArg(a, "path"), Filter: strArg(a, "filter"),
				Limit: secLimit(a), TimeoutSec: secTimeout(a),
			})
		})

	r.add(ToolSuricata,
		"Suricata IDS: summarize eve.json / fast.log alerts, run rules over a pcap, or test a rules file.",
		map[string]any{
			"action":          enumProp("", "parse_eve", "parse_fast", "scan_pcap", "test_rules", "status"),
			"path":            prop("string", "eve.json, fast.log, pcap or rules file"),
			"event_type":      enumProp("default alert", "alert", "dns", "http", "tls", "flow", "all"),
			"max_severity":    prop("integer", "1=high 2=medium 3=low (0=all)"),
			"filter":          prop("string", "keyword or IP"),
			"limit":           limit,
			"timeout_seconds": timeout,
		}, []string{"action"},
		func(ctx context.Context, sid string, a map[string]any) (string, error) {
			return r.securityEnv().RunSuricata(ctx, sid, security.SuricataParams{
				Action: strArg(a, "action"), Path: strArg(a, "path"), EventType: strArg(a, "event_type"),
				MaxSeverity: intArg(a, "max_severity", 0, 0, 3), Filter: strArg(a, "filter"),
				Limit: secLimit(a), TimeoutSec: secTimeout(a),
			})
		})

	r.add(ToolZeek,
		"Zeek network security monitor: parse conn/dns/http/ssl/notice/weird logs (TSV or JSON), summarize a log directory, or run zeek over a pcap.",
		map[string]any{
			"action":          enumProp("", "parse_log", "summary_dir", "scan_pcap", "status"),
			"path":            prop("string", "log file, log directory or pcap"),
			"filter":          prop("string", "IP, domain or keyword"),
			"limit":           limit,
			"timeout_seconds": timeout,
		}, []string{"action"},
		func(ctx context.Context, sid string, a map[string]any) (string, error) {
			return r.securityEnv().RunZeek(ctx, sid, security.ZeekParams{
				Action: strArg(a, "action"), Path: strArg(a, "path"), Filter: strArg(a, "filter"),
				Limit: secLimit(a), TimeoutSec: secTimeout(a),
			})
		})

	r.add(ToolLynis,
		"Lynis host hardening audit: run a system or Dockerfile audit, or parse lynis-report.dat into warnings, suggestions and hardening index.",
		map[string]any{
			"action":          enumProp("", "audit_system", "audit_dockerfile", "parse_report", "status"),
			"path":            prop("string", "lynis-report.dat or Dockerfile"),
			"filter":          prop("string", "keyword, e.g. ssh, kernel, firewall"),
			"limit":           limit,
			"timeout_seconds": prop("integer", "seconds, 5-300 (default 60; system audits may need 240)"),
		}, []string{"action"},
		func(ctx context.Context, sid string, a map[string]any) (string, error) {
			return r.securityEnv().RunLynis(ctx, sid, security.LynisParams{
				Action: strArg(a, "action"), Path: strArg(a, "path"), Filter: strArg(a, "filter"),
				Limit: secLimit(a), TimeoutSec: secTimeout(a),
			})
		})

	r.add(ToolTrivy,
		"Trivy scanner: vulnerabilities, misconfigurations and secrets in a workspace dir, container image or git repo; parse_report summarizes a Trivy JSON report.",
		map[string]any{
			"action":          enumProp("", "fs", "image", "config", "repo", "parse_report", "status"),
			"target":          prop("string", "path, image name, repo URL or report.json"),
			"severity":        prop("string", "default CRITICAL,HIGH"),
			"scanners":        prop("string", "default vuln,misconfig,secret"),
			"limit":           limit,
			"timeout_seconds": timeout,
		}, []string{"action"},
		func(ctx context.Context, sid string, a map[string]any) (string, error) {
			return r.securityEnv().RunTrivy(ctx, sid, security.TrivyParams{
				Action: strArg(a, "action"), Target: strArg(a, "target"), Severity: strArg(a, "severity"),
				Scanners: strArg(a, "scanners"), Limit: secLimit(a), TimeoutSec: secTimeout(a),
			})
		})

	r.add(ToolSemgrep,
		"Semgrep SAST: scan code for security bugs (rulesets auto, p/security-audit, p/owasp-top-ten or a local .yml), or summarize a Semgrep JSON/SARIF report.",
		map[string]any{
			"action":          enumProp("", "scan", "parse_report", "status"),
			"path":            prop("string", "code dir/file or report path"),
			"config":          prop("string", "rule config (default auto)"),
			"severity":        prop("string", "ERROR,WARNING,INFO"),
			"limit":           limit,
			"timeout_seconds": timeout,
		}, []string{"action"},
		func(ctx context.Context, sid string, a map[string]any) (string, error) {
			return r.securityEnv().RunSemgrep(ctx, sid, security.SemgrepParams{
				Action: strArg(a, "action"), Path: strArg(a, "path"), Config: strArg(a, "config"),
				Severity: strArg(a, "severity"), Limit: secLimit(a), TimeoutSec: secTimeout(a),
			})
		})

	r.add(ToolAutopsy,
		"Autopsy / Sleuth Kit digital forensics: read-only analysis of an Autopsy case (autopsy.db): summary, artifacts, files, deleted files, keyword search, timeline; image_info for disk images.",
		map[string]any{
			"action":          enumProp("", "case_summary", "artifacts", "files", "deleted", "keyword_search", "timeline", "image_info", "status"),
			"path":            prop("string", "autopsy.db, case directory or disk image"),
			"query":           prop("string", "filename, extension or artifact filter"),
			"limit":           limit,
			"timeout_seconds": timeout,
		}, []string{"action"},
		func(ctx context.Context, sid string, a map[string]any) (string, error) {
			return r.securityEnv().RunAutopsy(ctx, sid, security.AutopsyParams{
				Action: strArg(a, "action"), Path: strArg(a, "path"), Query: strArg(a, "query"),
				Limit: secLimit(a), TimeoutSec: secTimeout(a),
			})
		})
}
