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

	if total > 25950 {
		t.Errorf("the definitions take %d bytes", total)
	}
	if dir := os.Getenv("WAZE_TOOLS_DUMP"); dir != "" {
		raw, _ := json.MarshalIndent(r.GetTools(), "", " ")
		_ = os.WriteFile(dir+"/tools.json", raw, 0o644)
	}
}
