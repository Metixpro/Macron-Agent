package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNoToolSchemaHasNullRequired(t *testing.T) {
	h := newHarness(t)
	h.send(private(90, "یه یادآور می‌خوام"))
	req := h.llm.last()
	raw, _ := json.Marshal(req["tools"])
	if !strings.Contains(string(raw), `"name":"`+toolListSchedules+`"`) {
		t.Fatal("the schedule tools are offered")
	}
	for _, tl := range req["tools"].([]any) {
		fn := tl.(map[string]any)["function"].(map[string]any)
		params, _ := fn["parameters"].(map[string]any)
		if v, ok := params["required"]; ok && v == nil {
			t.Errorf("%s: \"required\": null", fn["name"])
		}
	}
}
