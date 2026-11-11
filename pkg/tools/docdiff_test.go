package tools

import (
	"strings"
	"testing"
)

func TestDiffReportFormat(t *testing.T) {
	rep := &DiffReport{
		FileA:        "original.md",
		FileB:        "modified.md",
		AddedLines:   2,
		DeletedLines: 1,
		Hunks: []string{
			"@@ line 3 @@\n- old text\n+ new improved text",
			"@@ line 10 @@\n+ additional note",
		},
	}

	formatted := rep.Format("unified")
	if !strings.Contains(formatted, "Document Diff") {
		t.Errorf("missing title: %s", formatted)
	}
	if !strings.Contains(formatted, "- old text") || !strings.Contains(formatted, "+ new improved text") {
		t.Errorf("missing hunks: %s", formatted)
	}
}
