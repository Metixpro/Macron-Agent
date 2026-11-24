package desk

import (
	"context"
	"net/http"
	"runtime"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/security"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

type securityToolDiag struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Mode      string `json:"mode"`
}

type diagnosticsBundle struct {
	GeneratedAt           string             `json:"generatedAt"`
	Version               string             `json:"version"`
	Creator               string             `json:"creator"`
	CreatorURL            string             `json:"creatorUrl"`
	License               string             `json:"license"`
	OS                    string             `json:"os"`
	Arch                  string             `json:"arch"`
	GoVersion             string             `json:"goVersion"`
	ActiveModel           string             `json:"activeModel"`
	ActiveProvider        string             `json:"activeProvider"`
	ExecutionMode         string             `json:"executionMode"`
	Approvals             string             `json:"approvals"`
	ProjectsCount         int                `json:"projectsCount"`
	ConfiguredConnections []string           `json:"configuredConnections"`
	SecurityToolkit       []securityToolDiag `json:"securityToolkit"`
	LocalModelServers     []llm.LocalServer  `json:"localModelServers"`
}

// localProvidersAPI probes loopback inference servers (Ollama, LM Studio,
// llama.cpp, vLLM) concurrently and returns reachable endpoints and models.
func (s *Server) localProvidersAPI(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 1200*time.Millisecond)
	defer cancel()
	servers := llm.DetectLocalServers(ctx)
	if servers == nil {
		servers = []llm.LocalServer{}
	}
	writeJSON(w, map[string]any{"servers": servers})
}

// diagnosticsAPI exports a privacy-safe diagnostic snapshot (zero API keys,
// tokens, or chat contents) for troubleshooting and support.
func (s *Server) diagnosticsAPI(w http.ResponseWriter, r *http.Request) {
	info := s.host.Info()
	s.mu.Lock()
	projCount := len(s.projects)
	s.mu.Unlock()

	configured := []string{}
	for _, c := range s.host.Connections() {
		if c.Ready {
			configured = append(configured, c.ID)
		}
	}

	secBins := []struct {
		tool string
		bins []string
		note string
	}{
		{tools.ToolWazuh, []string{"wazuh-control"}, "API + offline alerts.json"},
		{tools.ToolWireshark, []string{"tshark"}, "tshark + pure-Go PCAP"},
		{tools.ToolSuricata, []string{"suricata"}, "CLI + pure-Go EVE/fast.log"},
		{tools.ToolZeek, []string{"zeek", "bro"}, "CLI + pure-Go TSV/JSON logs"},
		{tools.ToolLynis, []string{"lynis"}, "CLI + pure-Go lynis-report.dat"},
		{tools.ToolTrivy, []string{"trivy"}, "CLI + pure-Go JSON report"},
		{tools.ToolSemgrep, []string{"semgrep"}, "CLI + pure-Go JSON/SARIF"},
		{tools.ToolAutopsy, []string{"mmls", "fls"}, "pure-Go autopsy.db + Sleuth Kit"},
	}
	secDiag := make([]securityToolDiag, 0, len(secBins))
	for _, sb := range secBins {
		_, found := security.FindBinary(sb.bins...)
		secDiag = append(secDiag, securityToolDiag{
			Name:      sb.tool,
			Installed: found,
			Mode:      sb.note,
		})
	}

	ctx, cancel := context.WithTimeout(r.Context(), 1200*time.Millisecond)
	defer cancel()
	localSrvs := llm.DetectLocalServers(ctx)
	if localSrvs == nil {
		localSrvs = []llm.LocalServer{}
	}

	bundle := diagnosticsBundle{
		GeneratedAt:           time.Now().UTC().Format(time.RFC3339),
		Version:               info.Version,
		Creator:               info.Creator,
		CreatorURL:            info.CreatorURL,
		License:               info.License,
		OS:                    runtime.GOOS,
		Arch:                  runtime.GOARCH,
		GoVersion:             runtime.Version(),
		ActiveModel:           info.Model,
		ActiveProvider:        info.Provider,
		ExecutionMode:         info.ExecutionMode,
		Approvals:             info.Approvals,
		ProjectsCount:         projCount,
		ConfiguredConnections: configured,
		SecurityToolkit:       secDiag,
		LocalModelServers:     localSrvs,
	}
	writeJSON(w, bundle)
}
