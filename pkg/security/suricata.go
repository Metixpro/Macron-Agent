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

// SuricataParams configures Suricata IDS log analysis or offline PCAP rule scanning.
type SuricataParams struct {
	Action      string // parse_eve, parse_fast, scan_pcap, test_rules, status
	Path        string // eve.json, fast.log, pcap file, or rules file
	EventType   string // alert, dns, http, tls, flow, all (default: alert)
	MaxSeverity int    // Suricata severity 1=high, 2=medium, 3=low (0=all)
	Filter      string // keyword or IP filter
	Limit       int
	TimeoutSec  int
}

// RunSuricata parses Suricata eve.json/fast.log in pure Go or runs suricata
// against a PCAP/rules file when the suricata binary is available.
func (e Env) RunSuricata(ctx context.Context, sessionID string, p SuricataParams) (string, error) {
	action := strings.ToLower(strings.TrimSpace(p.Action))
	if action == "" {
		lower := strings.ToLower(p.Path)
		switch {
		case strings.HasSuffix(lower, ".pcap") || strings.HasSuffix(lower, ".pcapng"):
			action = "scan_pcap"
		case strings.HasSuffix(lower, "fast.log"):
			action = "parse_fast"
		case strings.TrimSpace(p.Path) != "":
			action = "parse_eve"
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

	suriBin, hasSuri := FindBinary("suricata")
	switch action {
	case "status":
		if hasSuri {
			out, _, _, _ := e.RunBin(ctx, sessionID, 10*time.Second, suriBin, "-V")
			return fmt.Sprintf("Suricata ready: %s (%s). Pure-Go eve.json & fast.log parsers are also active.", strings.TrimSpace(out), suriBin), nil
		}
		return "Suricata binary is not installed (install suricata for live PCAP rule scanning). Pure-Go eve.json & fast.log analyzers are ready for immediate offline log triage.", nil

	case "parse_eve":
		logPath := p.Path
		if strings.TrimSpace(logPath) == "" {
			logPath = "/var/log/suricata/eve.json"
		}
		resolved, err := e.ResolvePath(sessionID, logPath)
		if err != nil {
			return "", err
		}
		return parseSuricataEve(resolved, p.EventType, p.MaxSeverity, p.Filter, limit)

	case "parse_fast":
		logPath := p.Path
		if strings.TrimSpace(logPath) == "" {
			logPath = "/var/log/suricata/fast.log"
		}
		resolved, err := e.ResolvePath(sessionID, logPath)
		if err != nil {
			return "", err
		}
		return parseSuricataFast(resolved, p.Filter, limit)

	case "scan_pcap":
		if !hasSuri {
			return "", fmt.Errorf("suricata binary is not installed; install suricata to scan PCAP files against IDS signatures")
		}
		resolved, err := e.ResolvePath(sessionID, p.Path)
		if err != nil {
			return "", err
		}
		outDir, err := os.MkdirTemp("", "macron-suri-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(outDir)
		_, stderr, code, err := e.RunBin(ctx, sessionID, ClampTimeout(p.TimeoutSec, 90), suriBin, "-r", resolved, "-l", outDir)
		if err != nil {
			return "", fmt.Errorf("suricata scan failed: %w", err)
		}
		evePath := filepath.Join(outDir, "eve.json")
		if _, statErr := os.Stat(evePath); statErr == nil {
			return parseSuricataEve(evePath, p.EventType, p.MaxSeverity, p.Filter, limit)
		}
		if code != 0 {
			return "", fmt.Errorf("suricata exited %d: %s", code, truncateLine(stderr, 260))
		}
		return "Suricata scan finished with no eve.json alerts.", nil

	case "test_rules":
		if !hasSuri {
			return "", fmt.Errorf("suricata binary is not installed")
		}
		args := []string{"-T"}
		if strings.TrimSpace(p.Path) != "" {
			resolved, err := e.ResolvePath(sessionID, p.Path)
			if err != nil {
				return "", err
			}
			args = append(args, "-S", resolved)
		}
		stdout, stderr, code, err := e.RunBin(ctx, sessionID, ClampTimeout(p.TimeoutSec, 45), suriBin, args...)
		if err != nil {
			return "", err
		}
		combined := strings.TrimSpace(stdout + "\n" + stderr)
		return CapOutput(fmt.Sprintf("Suricata rule test (exit=%d)\n%s", code, combined)), nil
	}
	return "", fmt.Errorf("unknown suricata action %q (use: parse_eve, parse_fast, scan_pcap, test_rules, status)", action)
}

func parseSuricataEve(path, eventType string, maxSev int, filter string, limit int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot open Suricata eve.json %s: %w", path, err)
	}
	defer f.Close()

	evFilter := strings.ToLower(strings.TrimSpace(eventType))
	if evFilter == "" {
		evFilter = "alert"
	}
	flt := strings.ToLower(strings.TrimSpace(filter))

	type eveRecord struct {
		Timestamp string `json:"timestamp"`
		EventType string `json:"event_type"`
		SrcIP     string `json:"src_ip"`
		SrcPort   int    `json:"src_port"`
		DestIP    string `json:"dest_ip"`
		DestPort  int    `json:"dest_port"`
		Proto     string `json:"proto"`
		Alert     *struct {
			SignatureID int    `json:"signature_id"`
			Signature   string `json:"signature"`
			Category    string `json:"category"`
			Severity    int    `json:"severity"`
			Action      string `json:"action"`
		} `json:"alert"`
		DNS *struct {
			RRName string `json:"rrname"`
			Type   string `json:"type"`
		} `json:"dns"`
		HTTP *struct {
			Hostname string `json:"hostname"`
			URL      string `json:"url"`
			Method   string `json:"http_method"`
			Status   int    `json:"status"`
		} `json:"http"`
		TLS *struct {
			SNI string `json:"sni"`
		} `json:"tls"`
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)

	total, matched := 0, 0
	types := map[string]int{}
	sigs := map[string]int{}
	sevCounts := map[int]int{}
	srcIPs := map[string]int{}
	var samples []string

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r eveRecord
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		total++
		types[r.EventType]++
		if evFilter != "all" && !strings.EqualFold(r.EventType, evFilter) {
			continue
		}
		if r.Alert != nil && maxSev > 0 && r.Alert.Severity > maxSev {
			continue
		}
		if flt != "" && !strings.Contains(strings.ToLower(line), flt) {
			continue
		}
		matched++
		if r.SrcIP != "" {
			srcIPs[r.SrcIP]++
		}
		if r.Alert != nil {
			sevCounts[r.Alert.Severity]++
			sigs[fmt.Sprintf("[S%d SID:%d] %s", r.Alert.Severity, r.Alert.SignatureID, truncateLine(r.Alert.Signature, 72))]++
			if len(samples) < limit {
				samples = append(samples, fmt.Sprintf("- [%s] S%d %s:%d->%s:%d %s (%s)",
					truncateLine(r.Timestamp, 19), r.Alert.Severity, r.SrcIP, r.SrcPort, r.DestIP, r.DestPort,
					truncateLine(r.Alert.Signature, 75), r.Alert.Category))
			}
		} else if len(samples) < limit {
			detail := r.EventType
			switch {
			case r.DNS != nil:
				detail = "DNS " + r.DNS.Type + " " + r.DNS.RRName
			case r.HTTP != nil:
				detail = fmt.Sprintf("HTTP %s %s%s (%d)", r.HTTP.Method, r.HTTP.Hostname, truncateLine(r.HTTP.URL, 50), r.HTTP.Status)
			case r.TLS != nil:
				detail = "TLS SNI=" + r.TLS.SNI
			}
			samples = append(samples, fmt.Sprintf("- [%s] %s->%s %s", truncateLine(r.Timestamp, 19), r.SrcIP, r.DestIP, detail))
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Suricata EVE Summary (%s)\nEvents parsed: %d | Matched (%s): %d\n", path, total, evFilter, matched)
	if len(sevCounts) > 0 {
		fmt.Fprintf(&sb, "Alert Severity: High(1)=%d, Medium(2)=%d, Low(3)=%d\n", sevCounts[1], sevCounts[2], sevCounts[3])
	}
	if top := TopCounts(sigs, 8); len(top) > 0 {
		sb.WriteString("\nTop Signatures:\n")
		for _, kv := range top {
			fmt.Fprintf(&sb, "- %dx %s\n", kv.Count, kv.Key)
		}
	}
	if top := TopCounts(srcIPs, 6); len(top) > 0 {
		parts := make([]string, 0, len(top))
		for _, kv := range top {
			parts = append(parts, fmt.Sprintf("%s(%d)", kv.Key, kv.Count))
		}
		sb.WriteString("Top Sources: " + strings.Join(parts, ", ") + "\n")
	}
	if len(samples) > 0 {
		sb.WriteString("\nEvents:\n" + strings.Join(samples, "\n") + "\n")
	}
	return CapOutput(sb.String()), nil
}

func parseSuricataFast(path, filter string, limit int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot open Suricata fast.log %s: %w", path, err)
	}
	defer f.Close()
	flt := strings.ToLower(strings.TrimSpace(filter))
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 32<<10), 1<<20)
	total, matched := 0, 0
	var lines []string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		total++
		if flt != "" && !strings.Contains(strings.ToLower(line), flt) {
			continue
		}
		matched++
		if len(lines) < limit {
			lines = append(lines, "- "+truncateLine(line, 180))
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return CapOutput(fmt.Sprintf("Suricata fast.log (%s)\nTotal: %d | Matched: %d\n\n%s", path, total, matched, strings.Join(lines, "\n"))), nil
}
