package security

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/netclient"
)

// WazuhParams configures a Wazuh SIEM/XDR query or local alerts.json analysis.
type WazuhParams struct {
	Action     string // status, agents, alerts, syscheck, sca, vulnerabilities, parse_alerts
	AgentID    string
	Path       string // local alerts.json path (for parse_alerts / offline alerts)
	Query      string // search keyword or rule filter
	MinLevel   int    // minimum Wazuh rule level (0..15)
	Limit      int
	TimeoutSec int
}

// RunWazuh queries the Wazuh Manager REST API or analyzes local Wazuh alerts.json.
func (e Env) RunWazuh(ctx context.Context, sessionID string, p WazuhParams) (string, error) {
	action := strings.ToLower(strings.TrimSpace(p.Action))
	if action == "" {
		if strings.TrimSpace(p.Path) != "" {
			action = "parse_alerts"
		} else {
			action = "status"
		}
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 15
	}
	if limit > 50 {
		limit = 50
	}

	if action == "parse_alerts" || (action == "alerts" && strings.TrimSpace(p.Path) != "") {
		logPath := p.Path
		if strings.TrimSpace(logPath) == "" {
			logPath = "/var/ossec/logs/alerts/alerts.json"
		}
		resolved, err := e.ResolvePath(sessionID, logPath)
		if err != nil {
			return "", err
		}
		return parseWazuhAlertsFile(resolved, p.MinLevel, p.Query, limit)
	}

	apiURL := strings.TrimRight(strings.TrimSpace(config.ToolEnvValue("WAZUH_API_URL")), "/")
	apiUser := strings.TrimSpace(config.ToolEnvValue("WAZUH_API_USER"))
	apiPass := strings.TrimSpace(config.ToolEnvValue("WAZUH_API_PASS"))

	if apiURL == "" || apiUser == "" || apiPass == "" {
		// Check if local Wazuh alerts exist before returning setup guidance.
		if _, err := os.Stat("/var/ossec/logs/alerts/alerts.json"); err == nil && (action == "alerts" || action == "status") {
			return parseWazuhAlertsFile("/var/ossec/logs/alerts/alerts.json", p.MinLevel, p.Query, limit)
		}
		return fmt.Sprintf(
			"Wazuh Manager API is not configured yet.\n" +
				"Set WAZUH_API_URL (e.g. https://127.0.0.1:55000), WAZUH_API_USER, and WAZUH_API_PASS in config.env (optional: WAZUH_INSECURE=true for self-signed certs).\n" +
				"Offline mode works immediately without API credentials: call wazuh with action=parse_alerts and path=<alerts.json>.",
		), nil
	}

	u, err := netclient.ValidateURL(apiURL)
	if err != nil {
		return "", fmt.Errorf("invalid WAZUH_API_URL: %w", err)
	}

	client := e.httpClient()
	if strings.EqualFold(strings.TrimSpace(config.ToolEnvValue("WAZUH_INSECURE")), "true") {
		tr := &http.Transport{
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // explicit opt-in for self-signed Wazuh manager certs
			ResponseHeaderTimeout: 20 * time.Second,
		}
		client = &http.Client{Timeout: ClampTimeout(p.TimeoutSec, 30), Transport: tr}
	}

	reqCtx, cancel := context.WithTimeout(ctx, ClampTimeout(p.TimeoutSec, 30))
	defer cancel()

	token, err := wazuhAuthenticate(reqCtx, client, u.String(), apiUser, apiPass)
	if err != nil {
		return "", err
	}

	var endpoint string
	switch action {
	case "status":
		endpoint = "/manager/status"
	case "agents":
		q := url.Values{"limit": {fmt.Sprint(limit)}}
		if strings.TrimSpace(p.Query) != "" {
			q.Set("search", strings.TrimSpace(p.Query))
		}
		endpoint = "/agents?" + q.Encode()
	case "syscheck":
		id := strings.TrimSpace(p.AgentID)
		if id == "" {
			id = "000"
		}
		endpoint = fmt.Sprintf("/syscheck/%s?limit=%d", url.PathEscape(id), limit)
	case "sca":
		id := strings.TrimSpace(p.AgentID)
		if id == "" {
			id = "000"
		}
		endpoint = fmt.Sprintf("/sca/%s?limit=%d", url.PathEscape(id), limit)
	case "vulnerabilities":
		id := strings.TrimSpace(p.AgentID)
		if id == "" {
			id = "000"
		}
		endpoint = fmt.Sprintf("/vulnerability/%s?limit=%d", url.PathEscape(id), limit)
	case "alerts":
		// Wazuh Manager API exposes cluster/manager logs; full alert index lives in alerts.json or Wazuh Indexer.
		endpoint = fmt.Sprintf("/manager/logs?limit=%d", limit)
	default:
		return "", fmt.Errorf("unknown wazuh action %q (use: status, agents, alerts, syscheck, sca, vulnerabilities, parse_alerts)", action)
	}

	data, err := wazuhGet(reqCtx, client, u.String()+endpoint, token)
	if err != nil {
		return "", err
	}
	return summarizeWazuhAPI(action, data, limit), nil
}

func wazuhAuthenticate(ctx context.Context, client *http.Client, baseURL, user, pass string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/security/user/authenticate?raw=true", nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(user, pass)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("wazuh auth failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("wazuh auth refused (HTTP %d): %s", resp.StatusCode, truncateLine(string(body), 160))
	}
	tok := strings.TrimSpace(string(body))
	if strings.HasPrefix(tok, "{") {
		var j struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &j) == nil && j.Data.Token != "" {
			tok = j.Data.Token
		}
	}
	if tok == "" {
		return "", fmt.Errorf("wazuh auth returned empty JWT token")
	}
	return tok, nil
}

func wazuhGet(ctx context.Context, client *http.Client, fullURL, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wazuh request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("wazuh API HTTP %d: %s", resp.StatusCode, truncateLine(string(body), 200))
	}
	return body, nil
}

func summarizeWazuhAPI(action string, raw []byte, limit int) string {
	var env struct {
		Data struct {
			AffectedItems []map[string]any `json:"affected_items"`
			TotalAffected int              `json:"total_affected_items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return CapOutput(string(raw))
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Wazuh %s (%d items shown / %d total)\n", action, len(env.Data.AffectedItems), env.Data.TotalAffected)
	for i, item := range env.Data.AffectedItems {
		if i >= limit {
			break
		}
		switch action {
		case "agents":
			fmt.Fprintf(&sb, "- [%v] %v (%v) status=%v ip=%v os=%v\n",
				item["id"], item["name"], item["version"], item["status"], item["ip"], nestedStr(item, "os", "name"))
		case "status":
			keys := make([]string, 0, len(item))
			for k := range item {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(&sb, "- %s: %v\n", k, item[k])
			}
		case "sca":
			fmt.Fprintf(&sb, "- %v: score=%v%% pass=%v fail=%v\n",
				item["name"], item["score"], item["pass"], item["fail"])
		case "vulnerabilities":
			fmt.Fprintf(&sb, "- [%v] %v pkg=%v (%v)\n",
				item["severity"], item["cve"], item["name"], item["version"])
		case "syscheck":
			fmt.Fprintf(&sb, "- %v perm=%v sha256=%v mtime=%v\n",
				item["file"], item["perm"], truncateLine(fmt.Sprint(item["sha256"]), 16), item["mtime"])
		default:
			b, _ := json.Marshal(item)
			fmt.Fprintf(&sb, "- %s\n", truncateLine(string(b), 220))
		}
	}
	return CapOutput(sb.String())
}

func nestedStr(m map[string]any, keys ...string) string {
	cur := any(m)
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	if cur == nil {
		return ""
	}
	return fmt.Sprint(cur)
}

func parseWazuhAlertsFile(path string, minLevel int, query string, limit int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot open Wazuh alerts file %s: %w", path, err)
	}
	defer f.Close()

	type alertEntry struct {
		Timestamp string `json:"timestamp"`
		Agent     struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			IP   string `json:"ip"`
		} `json:"agent"`
		Rule struct {
			ID          string   `json:"id"`
			Level       int      `json:"level"`
			Description string   `json:"description"`
			Groups      []string `json:"groups"`
			MITRE       struct {
				ID []string `json:"id"`
			} `json:"mitre"`
		} `json:"rule"`
		Data struct {
			SrcIP string `json:"srcip"`
		} `json:"data"`
	}

	qLower := strings.ToLower(strings.TrimSpace(query))
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)

	total, matched := 0, 0
	byRule := map[string]int{}
	byAgent := map[string]int{}
	byMitre := map[string]int{}
	var samples []alertEntry

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var a alertEntry
		if json.Unmarshal([]byte(line), &a) != nil {
			continue
		}
		total++
		if a.Rule.Level < minLevel {
			continue
		}
		if qLower != "" {
			hay := strings.ToLower(a.Rule.Description + " " + a.Rule.ID + " " + a.Agent.Name + " " + a.Data.SrcIP + " " + strings.Join(a.Rule.Groups, " "))
			if !strings.Contains(hay, qLower) {
				continue
			}
		}
		matched++
		rk := fmt.Sprintf("L%02d #%s %s", a.Rule.Level, a.Rule.ID, truncateLine(a.Rule.Description, 64))
		byRule[rk]++
		if a.Agent.Name != "" {
			byAgent[a.Agent.Name]++
		}
		for _, mid := range a.Rule.MITRE.ID {
			byMitre[mid]++
		}
		if len(samples) < limit {
			samples = append(samples, a)
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Wazuh Alerts Summary (%s)\nTotal parsed: %d | Matched (level>=%d): %d\n", path, total, minLevel, matched)
	if top := TopCounts(byRule, 8); len(top) > 0 {
		sb.WriteString("\nTop Rules:\n")
		for _, kv := range top {
			fmt.Fprintf(&sb, "- %dx %s\n", kv.Count, kv.Key)
		}
	}
	if top := TopCounts(byAgent, 5); len(top) > 0 {
		sb.WriteString("\nTop Agents: ")
		parts := make([]string, 0, len(top))
		for _, kv := range top {
			parts = append(parts, fmt.Sprintf("%s(%d)", kv.Key, kv.Count))
		}
		sb.WriteString(strings.Join(parts, ", ") + "\n")
	}
	if top := TopCounts(byMitre, 8); len(top) > 0 {
		sb.WriteString("MITRE ATT&CK: ")
		parts := make([]string, 0, len(top))
		for _, kv := range top {
			parts = append(parts, fmt.Sprintf("%s(%d)", kv.Key, kv.Count))
		}
		sb.WriteString(strings.Join(parts, ", ") + "\n")
	}
	if len(samples) > 0 {
		sb.WriteString("\nSample Alerts:\n")
		for _, a := range samples {
			src := ""
			if a.Data.SrcIP != "" {
				src = " src=" + a.Data.SrcIP
			}
			fmt.Fprintf(&sb, "- [%s] L%d #%s agent=%s%s: %s\n",
				truncateLine(a.Timestamp, 19), a.Rule.Level, a.Rule.ID, a.Agent.Name, src, truncateLine(a.Rule.Description, 90))
		}
	}
	return CapOutput(sb.String()), nil
}
