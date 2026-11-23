package security

import (
	"context"
	"database/sql"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/machine"
	_ "modernc.org/sqlite"
)

func newTestEnv(t *testing.T) (Env, string) {
	t.Helper()
	dir := t.TempDir()
	sb, err := machine.New(machine.Config{BaseDir: dir})
	if err != nil {
		t.Fatalf("machine.New: %v", err)
	}
	return Env{Machine: sb}, "sec-test"
}

func TestCapOutputTokenBudget(t *testing.T) {
	long := strings.Repeat("A", 5500)
	capped := CapOutput(long)
	if len(capped) > 3800 {
		t.Fatalf("expected capped output <= 3800 bytes, got %d", len(capped))
	}
	if !strings.Contains(capped, "for token efficiency") {
		t.Fatalf("expected truncation notice in %q", capped[len(capped)-120:])
	}
}

func TestWazuhParseAlertsAndAPI(t *testing.T) {
	env, sid := newTestEnv(t)
	ctx := context.Background()

	ws, err := env.Machine.Workspace(sid)
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	alertsPath := filepath.Join(ws, "alerts.json")
	fixture := strings.Join([]string{
		`{"timestamp":"2026-11-22T12:00:00Z","rule":{"id":"5710","level":10,"description":"sshd: Attempt to login using a non-existent user","groups":["syslog","sshd"]},"agent":{"id":"001","name":"web-prod-01"},"data":{"srcip":"203.0.113.45"}}`,
		`{"timestamp":"2026-11-22T12:01:00Z","rule":{"id":"31101","level":5,"description":"Web server 400 error code","groups":["web"]},"agent":{"id":"002","name":"edge-gw"},"data":{"srcip":"198.51.100.9"}}`,
		`{"timestamp":"2026-11-22T12:02:00Z","rule":{"id":"5712","level":12,"description":"sshd: brute force trying to get access to the system","groups":["sshd","authentication_failures"]},"agent":{"id":"001","name":"web-prod-01"},"data":{"srcip":"203.0.113.45"}}`,
	}, "\n")
	if err := os.WriteFile(alertsPath, []byte(fixture), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	out, err := env.RunWazuh(ctx, sid, WazuhParams{
		Action:   "parse_alerts",
		Path:     "alerts.json",
		MinLevel: 8,
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("RunWazuh parse_alerts: %v", err)
	}
	for _, want := range []string{"5710", "5712", "203.0.113.45", "web-prod-01"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in Wazuh output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "31101") {
		t.Errorf("expected level-5 rule 31101 to be filtered by MinLevel=8:\n%s", out)
	}

	// Test live Wazuh REST API client with JWT authentication
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/security/user/authenticate":
			_, _ = w.Write([]byte(`{"data":{"token":"test-jwt-token"}}`))
		case "/manager/status":
			if r.Header.Get("Authorization") != "Bearer test-jwt-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"data":{"affected_items":[{"wazuh-analysisd":"running","wazuh-remoted":"running"}]}}`))
		case "/agents":
			_, _ = w.Write([]byte(`{"data":{"total_affected_items":2,"affected_items":[{"id":"000","name":"soc-mgr","status":"active","ip":"10.0.0.1","os":{"name":"Debian"}},{"id":"001","name":"web-01","status":"active","ip":"10.0.0.2","os":{"name":"Ubuntu"}}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("WAZUH_API_URL", srv.URL)
	t.Setenv("WAZUH_API_USER", "wazuh-wui")
	t.Setenv("WAZUH_API_PASS", "secret")

	stOut, err := env.RunWazuh(ctx, sid, WazuhParams{Action: "status"})
	if err != nil {
		t.Fatalf("RunWazuh status: %v", err)
	}
	if !strings.Contains(stOut, "wazuh-analysisd") {
		t.Errorf("expected daemon status in status output:\n%s", stOut)
	}

	agOut, err := env.RunWazuh(ctx, sid, WazuhParams{Action: "agents", Limit: 5})
	if err != nil {
		t.Fatalf("RunWazuh agents: %v", err)
	}
	if !strings.Contains(agOut, "web-01") || !strings.Contains(agOut, "10.0.0.2") {
		t.Errorf("expected agent details in agents output:\n%s", agOut)
	}
}

func buildSamplePCAP() []byte {
	var out []byte
	// Classic PCAP global header (24 bytes, little-endian, Ethernet linktype=1)
	gh := make([]byte, 24)
	binary.LittleEndian.PutUint32(gh[0:4], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(gh[4:6], 2)
	binary.LittleEndian.PutUint16(gh[6:8], 4)
	binary.LittleEndian.PutUint32(gh[16:20], 65535)
	binary.LittleEndian.PutUint32(gh[20:24], 1)
	out = append(out, gh...)

	// Ethernet (14B) + IPv4 (20B) + TCP (20B) = 54B packet: 10.1.2.3:51515 -> 93.184.216.34:443 (SYN)
	pkt := make([]byte, 54)
	binary.BigEndian.PutUint16(pkt[12:14], 0x0800) // IPv4
	pkt[14] = 0x45                                 // Version 4, IHL 5
	binary.BigEndian.PutUint16(pkt[16:18], 40)     // Total length
	pkt[22] = 64                                   // TTL
	pkt[23] = 6                                    // Protocol TCP
	copy(pkt[26:30], []byte{10, 1, 2, 3})
	copy(pkt[30:34], []byte{93, 184, 216, 34})
	binary.BigEndian.PutUint16(pkt[34:36], 51515) // src port
	binary.BigEndian.PutUint16(pkt[36:38], 443)   // dst port
	pkt[46] = 0x50                                // data offset 5
	pkt[47] = 0x02                                // SYN

	ph := make([]byte, 16)
	binary.LittleEndian.PutUint32(ph[0:4], 1700000000)
	binary.LittleEndian.PutUint32(ph[8:12], uint32(len(pkt)))
	binary.LittleEndian.PutUint32(ph[12:16], uint32(len(pkt)))
	out = append(out, ph...)
	out = append(out, pkt...)
	return out
}

func TestWiresharkPureGoPCAP(t *testing.T) {
	pcapData := buildSamplePCAP()
	dir := t.TempDir()
	pcapPath := filepath.Join(dir, "sample.pcap")
	if err := os.WriteFile(pcapPath, pcapData, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	out, err := parseClassicPCAP(pcapPath, "summary", 10)
	if err != nil {
		t.Fatalf("parseClassicPCAP summary: %v", err)
	}
	for _, want := range []string{"10.1.2.3", "93.184.216.34", "TCP", "443"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in PCAP summary:\n%s", want, out)
		}
	}

	ipsOut, err := parseClassicPCAP(pcapPath, "extract_ips", 10)
	if err != nil {
		t.Fatalf("parseClassicPCAP extract_ips: %v", err)
	}
	if !strings.Contains(ipsOut, "93.184.216.34") {
		t.Errorf("expected extracted IP in output:\n%s", ipsOut)
	}
}

func TestSuricataParseEveAndFast(t *testing.T) {
	env, sid := newTestEnv(t)
	ctx := context.Background()
	ws, _ := env.Machine.Workspace(sid)

	eve := strings.Join([]string{
		`{"timestamp":"2026-11-22T10:00:01Z","event_type":"alert","src_ip":"192.0.2.50","src_port":44122,"dest_ip":"10.0.0.12","dest_port":80,"proto":"TCP","alert":{"signature_id":2024001,"severity":1,"signature":"ET WEB_SERVER SQL Injection Attempt","category":"Web Application Attack"}}`,
		`{"timestamp":"2026-11-22T10:00:02Z","event_type":"dns","src_ip":"10.0.0.12","dest_ip":"8.8.8.8","proto":"UDP","dns":{"rrname":"c2.bad-actor.example"}}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(ws, "eve.json"), []byte(eve), 0o600); err != nil {
		t.Fatalf("WriteFile eve.json: %v", err)
	}

	out, err := env.RunSuricata(ctx, sid, SuricataParams{
		Action:    "parse_eve",
		Path:      "eve.json",
		EventType: "alert",
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("RunSuricata parse_eve: %v", err)
	}
	if !strings.Contains(out, "ET WEB_SERVER SQL Injection Attempt") || !strings.Contains(out, "192.0.2.50") {
		t.Errorf("unexpected Suricata EVE output:\n%s", out)
	}

	fast := `11/22/2026-10:00:01.100000  [**] [1:2024001:1] ET EXPLOIT Remote Code Execution [**] [Classification: Attempted Administrator Privilege Gain] [Priority: 1] {TCP} 192.0.2.99:55111 -> 10.0.0.15:443`
	if err := os.WriteFile(filepath.Join(ws, "fast.log"), []byte(fast), 0o600); err != nil {
		t.Fatalf("WriteFile fast.log: %v", err)
	}
	fastOut, err := env.RunSuricata(ctx, sid, SuricataParams{
		Action: "parse_fast",
		Path:   "fast.log",
	})
	if err != nil {
		t.Fatalf("RunSuricata parse_fast: %v", err)
	}
	if !strings.Contains(fastOut, "ET EXPLOIT Remote Code Execution") {
		t.Errorf("unexpected Suricata fast.log output:\n%s", fastOut)
	}
}

func TestZeekParseLogAndSummaryDir(t *testing.T) {
	env, sid := newTestEnv(t)
	ctx := context.Background()
	ws, _ := env.Machine.Workspace(sid)

	connLog := strings.Join([]string{
		"#separator \\x09",
		"#fields\tts\tuid\tid.orig_h\tid.orig_p\tid.resp_h\tid.resp_p\tproto\tservice\tconn_state",
		"1700000001.10\tC1a2b3\t10.0.0.5\t51234\t198.51.100.77\t443\ttcp\tssl\tSF",
		"1700000002.20\tC4d5e6\t10.0.0.5\t51235\t198.51.100.88\t22\ttcp\tssh\tS0",
		"#close\t2026-11-22",
	}, "\n")
	if err := os.WriteFile(filepath.Join(ws, "conn.log"), []byte(connLog), 0o600); err != nil {
		t.Fatalf("WriteFile conn.log: %v", err)
	}

	out, err := env.RunZeek(ctx, sid, ZeekParams{
		Action: "parse_log",
		Path:   "conn.log",
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("RunZeek parse_log: %v", err)
	}
	for _, want := range []string{"198.51.100.77:443", "198.51.100.88:22", "state=SF"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in Zeek output:\n%s", want, out)
		}
	}

	dirOut, err := env.RunZeek(ctx, sid, ZeekParams{
		Action: "summary_dir",
		Path:   ".",
	})
	if err != nil {
		t.Fatalf("RunZeek summary_dir: %v", err)
	}
	if !strings.Contains(dirOut, "conn.log") {
		t.Errorf("expected conn.log in summary_dir:\n%s", dirOut)
	}
}

func TestLynisParseReport(t *testing.T) {
	env, sid := newTestEnv(t)
	ctx := context.Background()
	ws, _ := env.Machine.Workspace(sid)

	report := strings.Join([]string{
		"lynis_version=3.1.1",
		"os_fullname=Debian GNU/Linux 12",
		"hardening_index=74",
		"tests_executed=245",
		"firewall_installed=1",
		"warning[]=SSH-7408|PermitRootLogin is enabled in sshd_config|PermitRootLogin|-|",
		"suggestion[]=KRNL-5820|Disable core dumps in /etc/security/limits.conf|-|-|",
		"vulnerable_package[]=openssl-1.1.1",
	}, "\n")
	if err := os.WriteFile(filepath.Join(ws, "lynis-report.dat"), []byte(report), 0o600); err != nil {
		t.Fatalf("WriteFile lynis-report.dat: %v", err)
	}

	out, err := env.RunLynis(ctx, sid, LynisParams{
		Action: "parse_report",
		Path:   "lynis-report.dat",
	})
	if err != nil {
		t.Fatalf("RunLynis parse_report: %v", err)
	}
	for _, want := range []string{"74/100", "SSH-7408", "PermitRootLogin", "KRNL-5820", "openssl-1.1.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in Lynis report:\n%s", want, out)
		}
	}
}

func TestTrivyParseReport(t *testing.T) {
	env, sid := newTestEnv(t)
	ctx := context.Background()
	ws, _ := env.Machine.Workspace(sid)

	report := `{
	  "ArtifactName": "spectordev/app:latest",
	  "ArtifactType": "container_image",
	  "Results": [
	    {
	      "Target": "alpine:3.19",
	      "Vulnerabilities": [
	        {"VulnerabilityID": "CVE-2026-1111", "PkgName": "libcrypto3", "InstalledVersion": "3.1.0", "FixedVersion": "3.1.4", "Severity": "CRITICAL", "Title": "Remote buffer overflow"},
	        {"VulnerabilityID": "CVE-2026-2222", "PkgName": "curl", "InstalledVersion": "8.1.0", "FixedVersion": "8.5.0", "Severity": "LOW", "Title": "Minor header leak"}
	      ],
	      "Misconfigurations": [
	        {"ID": "DS002", "Title": "Image user should not be root", "Severity": "HIGH", "Message": "Specify USER in Dockerfile"}
	      ],
	      "Secrets": [
	        {"RuleID": "aws-access-key-id", "Category": "AWS", "Severity": "CRITICAL", "Title": "AWS Access Key", "StartLine": 42}
	      ]
	    }
	  ]
	}`
	if err := os.WriteFile(filepath.Join(ws, "trivy.json"), []byte(report), 0o600); err != nil {
		t.Fatalf("WriteFile trivy.json: %v", err)
	}

	out, err := env.RunTrivy(ctx, sid, TrivyParams{
		Action:   "parse_report",
		Target:   "trivy.json",
		Severity: "CRITICAL,HIGH",
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("RunTrivy parse_report: %v", err)
	}
	for _, want := range []string{"CVE-2026-1111", "libcrypto3", "DS002", "aws-access-key-id"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in Trivy summary:\n%s", want, out)
		}
	}
	if strings.Contains(out, "CVE-2026-2222") {
		t.Errorf("expected LOW severity CVE-2026-2222 to be filtered:\n%s", out)
	}
}

func TestSemgrepParseReportAndSARIF(t *testing.T) {
	env, sid := newTestEnv(t)
	ctx := context.Background()
	ws, _ := env.Machine.Workspace(sid)

	semgrepJSON := `{
	  "results": [
	    {
	      "check_id": "go.lang.security.audit.sqli.math-sqli",
	      "path": "pkg/api/query.go",
	      "start": {"line": 88, "col": 12},
	      "extra": {
	        "message": "Formatted SQL string detected; use parameterized query.",
	        "severity": "ERROR",
	        "metadata": {"cwe": ["CWE-89: SQL Injection"], "owasp": ["A03:2021 - Injection"]}
	      }
	    }
	  ]
	}`
	if err := os.WriteFile(filepath.Join(ws, "semgrep.json"), []byte(semgrepJSON), 0o600); err != nil {
		t.Fatalf("WriteFile semgrep.json: %v", err)
	}

	out, err := env.RunSemgrep(ctx, sid, SemgrepParams{
		Action: "parse_report",
		Path:   "semgrep.json",
	})
	if err != nil {
		t.Fatalf("RunSemgrep parse_report: %v", err)
	}
	for _, want := range []string{"pkg/api/query.go:88", "math-sqli", "Formatted SQL string"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in Semgrep output:\n%s", want, out)
		}
	}
}

func TestAutopsySQLiteForensics(t *testing.T) {
	env, sid := newTestEnv(t)
	ctx := context.Background()
	ws, _ := env.Machine.Workspace(sid)

	dbPath := filepath.Join(ws, "autopsy.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	stmts := []string{
		`CREATE TABLE tsk_db_info (schema_ver INTEGER, schema_minor_ver INTEGER);`,
		`INSERT INTO tsk_db_info VALUES (9, 3);`,
		`CREATE TABLE tsk_files (obj_id INTEGER PRIMARY KEY, parent_path TEXT, name TEXT, size INTEGER, mtime INTEGER, crtime INTEGER, dir_flags INTEGER, meta_flags INTEGER, md5 TEXT);`,
		`INSERT INTO tsk_files VALUES (1, '/Users/alice/Downloads/', 'payload.exe', 184320, 1700000100, 1700000050, 1, 1, 'deadbeefcafe1234');`,
		`INSERT INTO tsk_files VALUES (2, '/Users/alice/Temp/', 'wiped_key.pem', 4096, 1700000200, 1700000150, 2, 2, 'cafebabe99887766');`,
		`CREATE TABLE blackboard_artifact_types (artifact_type_id INTEGER PRIMARY KEY, display_name TEXT);`,
		`INSERT INTO blackboard_artifact_types VALUES (10, 'Web Downloads');`,
		`CREATE TABLE blackboard_attribute_types (attribute_type_id INTEGER PRIMARY KEY, display_name TEXT);`,
		`INSERT INTO blackboard_attribute_types VALUES (20, 'Source URL');`,
		`CREATE TABLE blackboard_artifacts (artifact_id INTEGER PRIMARY KEY, obj_id INTEGER, artifact_type_id INTEGER);`,
		`INSERT INTO blackboard_artifacts VALUES (100, 1, 10);`,
		`CREATE TABLE blackboard_attributes (artifact_id INTEGER, attribute_type_id INTEGER, value_text TEXT);`,
		`INSERT INTO blackboard_attributes VALUES (100, 20, 'https://malicious.example/payload.exe');`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("Exec %q: %v", s, err)
		}
	}
	_ = db.Close()

	sumOut, err := env.RunAutopsy(ctx, sid, AutopsyParams{Action: "case_summary", Path: "autopsy.db"})
	if err != nil {
		t.Fatalf("RunAutopsy case_summary: %v", err)
	}
	for _, want := range []string{"TSK v9.3", "Indexed Files: 2", "Deleted Entries: 1", "Web Downloads: 1"} {
		if !strings.Contains(sumOut, want) {
			t.Errorf("expected %q in Autopsy case_summary:\n%s", want, sumOut)
		}
	}

	delOut, err := env.RunAutopsy(ctx, sid, AutopsyParams{Action: "deleted", Path: "autopsy.db"})
	if err != nil {
		t.Fatalf("RunAutopsy deleted: %v", err)
	}
	if !strings.Contains(delOut, "wiped_key.pem") || strings.Contains(delOut, "payload.exe") {
		t.Errorf("unexpected Autopsy deleted output:\n%s", delOut)
	}

	artOut, err := env.RunAutopsy(ctx, sid, AutopsyParams{Action: "artifacts", Path: "autopsy.db", Query: "malicious"})
	if err != nil {
		t.Fatalf("RunAutopsy artifacts: %v", err)
	}
	if !strings.Contains(artOut, "https://malicious.example/payload.exe") {
		t.Errorf("expected URL artifact in output:\n%s", artOut)
	}
}
