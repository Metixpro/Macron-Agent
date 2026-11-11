package tools

import (
	"strings"
	"testing"
)

func TestComputeLineDiff(t *testing.T) {
	oldContent := `header
old line
common line
footer`

	newContent := `header
new line
common line
extra line
footer`

	diff := computeLineDiff(oldContent, newContent)
	if !strings.Contains(diff, "- old line") {
		t.Errorf("diff missing removed line: %s", diff)
	}
	if !strings.Contains(diff, "+ new line") || !strings.Contains(diff, "+ extra line") {
		t.Errorf("diff missing added line: %s", diff)
	}
}
