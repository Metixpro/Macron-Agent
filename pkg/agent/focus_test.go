package agent

import (
	"context"
	"slices"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

func TestToolGroupsNameEveryFocusGroup(t *testing.T) {
	if len(toolGroupInfo) != len(focusGroups) {
		t.Fatalf("%d names for %d groups", len(toolGroupInfo), len(focusGroups))
	}
	seen := map[string]bool{}
	for _, g := range ToolGroups() {
		if g.Key == "" || g.About == "" || len(g.Tools) == 0 || seen[g.Key] {
			t.Errorf("group %+v", g)
		}
		seen[g.Key] = true
	}
}

func TestFocusPickChoosesTheTools(t *testing.T) {
	a, script := setup(t, llm.Message{Role: "assistant", Content: "ok"})
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "یه نمودار بکش"},
		FocusTools: true, FocusPick: []string{"generate_image"}})
	if err != nil {
		t.Fatal(err)
	}
	got := toolNames(script.requests[0])
	if !slices.Contains(got, "generate_image") || !slices.Contains(got, "web_search") || !slices.Contains(got, ToolMoreTools) ||
		!slices.Contains(got, "generate_chart") || slices.Contains(got, "download_media") {
		t.Fatalf("picked: %v", got)
	}
	if res.ToolsUnsent <= 0 {
		t.Fatalf("unsent: %d", res.ToolsUnsent)
	}
}
