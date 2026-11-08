package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/search"
	"github.com/Metixpro/macron-agent/pkg/skills"
)

// Every tool's definition goes with every request that offers the tools, so
// the definitions are kept short: what the tool is for and what each
// parameter takes, no essays.
func TestToolDefinitionsStayShort(t *testing.T) {
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatalf("machine: %v", err)
	}
	st, _ := skills.New(t.TempDir())
	r := NewRegistry(sb, search.NewEngine(guardOff()), nil, Options{HTTP: guardOff(), Skills: st,
		SkillWrite: func(string) bool { return true }, ImageEditAPI: "https://edit.example/v2"})
	total, per := 0, []string{}
	for _, tool := range r.GetTools() {
		raw, _ := json.Marshal(tool)
		total += len(raw)
		per = append(per, fmt.Sprintf("%5d %s", len(raw), tool.Function.Name))
		if limit := map[string]int{ToolMusic: 2400}[tool.Function.Name]; len(raw) > max(limit, 1500) {
			t.Errorf("%s: %d bytes", tool.Function.Name, len(raw))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(per)))
	t.Logf("%d tools, %d bytes:\n%s", len(per), total, strings.Join(per, "\n"))
	// 28 KB before they were shortened; +1 KB for query_data and create_spreadsheet, +1 KB for prices and the media and PDF actions,
	// +2.9 KB for the browser and motion graphics tools, offered where Chromium is installed (a production server);
	// +0.7 KB for the chart's series, types and units and Markdown PDFs, +0.1 KB for web_search's news and time;
	// +1.1 KB for apply_patch, shell_jobs, bash_execute's background jobs and install_package's system packages (1.44);
	// +0.1 KB for read_file's files and search_code's count (2.4: a job over many files in one call);
	// +0.1 KB for the browser's example of an action (2.6: a weaker model wrote its actions in three wrong shapes in a row).
	// +0.15 KB for generate_image's style, count and wider shapes (2.7: 76 styles, two options, 21:9).
	if total > 25950 {
		t.Errorf("the definitions take %d bytes", total)
	}
	if dir := os.Getenv("WAZE_TOOLS_DUMP"); dir != "" {
		raw, _ := json.MarshalIndent(r.GetTools(), "", " ")
		_ = os.WriteFile(dir+"/tools.json", raw, 0o644)
	}
}
