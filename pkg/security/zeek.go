package security

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ZeekParams configures Zeek network security log analysis or PCAP processing.
type ZeekParams struct {
	Action     string // parse_log, scan_pcap, summary_dir, status
	Path       string // Zeek .log file, log directory, or .pcap file
	Filter     string // IP, domain, host, or keyword filter
	Limit      int
	TimeoutSec int
}

// RunZeek analyzes Zeek TSV/JSON logs (conn, dns, http, ssl, notice, weird, files)
// in pure Go, or runs zeek -r <pcap> when the zeek binary is installed.
func (e Env) RunZeek(ctx context.Context, sessionID string, p ZeekParams) (string, error) {
	action := strings.ToLower(strings.TrimSpace(p.Action))
	if action == "" {
		lower := strings.ToLower(p.Path)
		switch {
		case strings.HasSuffix(lower, ".pcap") || strings.HasSuffix(lower, ".pcapng"):
			action = "scan_pcap"
		case strings.TrimSpace(p.Path) != "":
			action = "parse_log"
		default:
			action = "status"
		}
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 80 {
		limit = 80
	}

	zeekBin, hasZeek := FindBinary("zeek", "bro")
	switch action {
	case "status":
		if hasZeek {
			out, _, _, _ := e.RunBin(ctx, sessionID, 10*time.Second, zeekBin, "--version")
			return fmt.Sprintf("Zeek ready: %s (%s). Pure-Go TSV/JSON Zeek log parsers are active.", strings.TrimSpace(out), zeekBin), nil
		}
		return "Zeek binary is not installed (install zeek for live PCAP log generation). Pure-Go Zeek TSV/JSON analyzers (conn/dns/http/ssl/notice/weird) are active.", nil

	case "parse_log":
		resolved, err := e.ResolvePath(sessionID, p.Path)
		if err != nil {
			return "", err
		}
		if fi, err := os.Stat(resolved); err == nil && fi.IsDir() {
			return summarizeZeekDir(resolved, p.Filter, limit)
		}
		return parseZeekLog(resolved, p.Filter, limit)

	case "summary_dir":
		dirPath := p.Path
		if strings.TrimSpace(dirPath) == "" {
			dirPath = "/usr/local/zeek/logs/current"
		}
		resolved, err := e.ResolvePath(sessionID, dirPath)
		if err != nil {
			return "", err
		}
		return summarizeZeekDir(resolved, p.Filter, limit)

	case "scan_pcap":
		if !hasZeek {
			return "", fmt.Errorf("zeek binary is not installed; install zeek to generate Zeek logs from a PCAP")
		}
		resolved, err := e.ResolvePath(sessionID, p.Path)
		if err != nil {
			return "", err
		}
		outDir, err := os.MkdirTemp("", "macron-zeek-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(outDir)
		// Run zeek inside outDir so generated *.log stay isolated.
		cmd := []string{"-r", resolved, "Log::default_logdir=" + outDir}
		_, stderr, code, err := e.RunBin(ctx, sessionID, ClampTimeout(p.TimeoutSec, 90), zeekBin, cmd...)
		if err != nil {
			return "", fmt.Errorf("zeek scan failed: %w", err)
		}
		if code != 0 {
			return "", fmt.Errorf("zeek exited %d: %s", code, truncateLine(stderr, 260))
		}
		return summarizeZeekDir(outDir, p.Filter, limit)
	}
	return "", fmt.Errorf("unknown zeek action %q (use: parse_log, summary_dir, scan_pcap, status)", action)
}

func summarizeZeekDir(dir, filter string, limit int) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("cannot read Zeek log directory %s: %w", dir, err)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Zeek Directory Summary (%s)\n", dir)
	found := 0
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".log") {
			continue
		}
		found++
		full := filepath.Join(dir, ent.Name())
		summary, err := parseZeekLog(full, filter, min(limit, 5))
		if err != nil {
			continue
		}
		fmt.Fprintf(&sb, "\n--- %s ---\n%s\n", ent.Name(), truncateLine(summary, 520))
	}
	if found == 0 {
		return fmt.Sprintf("No .log files found in %s", dir), nil
	}
	return CapOutput(sb.String()), nil
}

func parseZeekLog(path, filter string, limit int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot open Zeek log %s: %w", path, err)
	}
	defer f.Close()

	flt := strings.ToLower(strings.TrimSpace(filter))
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)

	var fields []string
	sep := "\t"
	logName := filepath.Base(path)
	total, matched := 0, 0
	srcIPs := map[string]int{}
	dstPorts := map[string]int{}
	domains := map[string]int{}
	notices := map[string]int{}
	var samples []string

	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if strings.HasPrefix(line, "#fields") {
				parts := strings.Split(line, sep)
				if len(parts) > 1 {
					fields = parts[1:]
				}
			} else if strings.HasPrefix(line, "#path") {
				parts := strings.Split(line, sep)
				if len(parts) > 1 {
					logName = parts[1]
				}
			}
			continue
		}

		row := map[string]string{}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "{") {
			var raw map[string]any
			if json.Unmarshal([]byte(trimmed), &raw) != nil {
				continue
			}
			for k, v := range raw {
				row[k] = fmt.Sprint(v)
			}
		} else if len(fields) > 0 {
			cols := strings.Split(line, sep)
			for i := 0; i < len(fields) && i < len(cols); i++ {
				if cols[i] != "-" && cols[i] != "(empty)" {
					row[fields[i]] = cols[i]
				}
			}
		} else {
			continue
		}

		total++
		if flt != "" && !strings.Contains(strings.ToLower(line), flt) {
			continue
		}
		matched++

		if v := row["id.orig_h"]; v != "" {
			srcIPs[v]++
		}
		if v := row["id.resp_p"]; v != "" {
			dstPorts[v]++
		}
		if v := row["query"]; v != "" {
			domains[v]++
		} else if v := row["host"]; v != "" {
			domains[v]++
		} else if v := row["server_name"]; v != "" {
			domains[v]++
		}
		if v := row["note"]; v != "" {
			notices[v]++
		} else if v := row["name"]; v != "" && strings.Contains(logName, "weird") {
			notices[v]++
		}

		if len(samples) < limit {
			samples = append(samples, formatZeekRow(logName, row))
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Zeek %s (%s)\nRecords: %d | Matched: %d\n", logName, path, total, matched)
	if top := TopCounts(notices, 8); len(top) > 0 {
		sb.WriteString("Notices/Weirds: ")
		sb.WriteString(joinCounts(top) + "\n")
	}
	if top := TopCounts(domains, 8); len(top) > 0 {
		sb.WriteString("Top Domains/Hosts: ")
		sb.WriteString(joinCounts(top) + "\n")
	}
	if top := TopCounts(srcIPs, 6); len(top) > 0 {
		sb.WriteString("Top Orig IPs: ")
		sb.WriteString(joinCounts(top) + "\n")
	}
	if top := TopCounts(dstPorts, 6); len(top) > 0 {
		sb.WriteString("Top Resp Ports: ")
		sb.WriteString(joinCounts(top) + "\n")
	}
	if len(samples) > 0 {
		sb.WriteString("\nRecords:\n" + strings.Join(samples, "\n") + "\n")
	}
	return CapOutput(sb.String()), nil
}

func formatZeekRow(logName string, r map[string]string) string {
	orig, resp, port := r["id.orig_h"], r["id.resp_h"], r["id.resp_p"]
	switch {
	case r["query"] != "":
		return fmt.Sprintf("- DNS %s -> %s q=%s rcode=%s ans=%s", orig, resp, r["query"], r["rcode_name"], truncateLine(r["answers"], 60))
	case r["uri"] != "" || r["method"] != "":
		return fmt.Sprintf("- HTTP %s -> %s%s %s (%s)", orig, r["host"], truncateLine(r["uri"], 50), r["method"], r["status_code"])
	case r["server_name"] != "":
		return fmt.Sprintf("- SSL %s -> %s:%s sni=%s ver=%s", orig, resp, port, r["server_name"], r["version"])
	case r["note"] != "":
		return fmt.Sprintf("- NOTICE %s: %s (%s->%s)", r["note"], truncateLine(r["msg"], 90), orig, resp)
	default:
		return fmt.Sprintf("- %s %s -> %s:%s proto=%s state=%s bytes=%s/%s",
			logName, orig, resp, port, r["proto"], r["conn_state"], r["orig_bytes"], r["resp_bytes"])
	}
}

func joinCounts(items []KVCount) string {
	parts := make([]string, 0, len(items))
	for _, kv := range items {
		parts = append(parts, fmt.Sprintf("%s(%d)", kv.Key, kv.Count))
	}
	return strings.Join(parts, ", ")
}
