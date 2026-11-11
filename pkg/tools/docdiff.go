package tools

import (
	"context"
	"fmt"
	"strings"
)

const ToolDocumentDiff = "document_diff"

type DiffReport struct {
	FileA        string   `json:"file_a"`
	FileB        string   `json:"file_b"`
	AddedLines   int      `json:"added_lines"`
	DeletedLines int      `json:"deleted_lines"`
	Summary      string   `json:"summary"`
	Hunks        []string `json:"hunks"`
}

// CompareDocuments reads two files and computes their line-level and structural diff.
func (r *Registry) CompareDocuments(ctx context.Context, sessionID, fileA, fileB, format string) (*DiffReport, error) {
	dataA, _, err := r.Machine.ReadFile(sessionID, fileA, 256<<10)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", fileA, err)
	}

	dataB, _, err := r.Machine.ReadFile(sessionID, fileB, 256<<10)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", fileB, err)
	}

	linesA := strings.Split(string(dataA), "\n")
	linesB := strings.Split(string(dataB), "\n")

	var hunks []string
	added, deleted := 0, 0

	maxL := max(len(linesA), len(linesB))
	for i := 0; i < maxL; i++ {
		var a, b string
		hasA, hasB := i < len(linesA), i < len(linesB)
		if hasA {
			a = linesA[i]
		}
		if hasB {
			b = linesB[i]
		}

		if hasA && hasB {
			if a != b {
				hunks = append(hunks, fmt.Sprintf("@@ line %d @@\n- %s\n+ %s", i+1, a, b))
				deleted++
				added++
			}
		} else if hasA && !hasB {
			hunks = append(hunks, fmt.Sprintf("@@ line %d @@\n- %s", i+1, a))
			deleted++
		} else if !hasA && hasB {
			hunks = append(hunks, fmt.Sprintf("@@ line %d @@\n+ %s", i+1, b))
			added++
		}
	}

	summary := fmt.Sprintf("Files %s and %s: %d additions, %d deletions across %d hunks.",
		fileA, fileB, added, deleted, len(hunks))

	return &DiffReport{
		FileA:        fileA,
		FileB:        fileB,
		AddedLines:   added,
		DeletedLines: deleted,
		Summary:      summary,
		Hunks:        hunks,
	}, nil
}

func (d *DiffReport) Format(format string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("### 📑 Document Diff: `%s` ⟷ `%s`\n", d.FileA, d.FileB))
	sb.WriteString(fmt.Sprintf("**خلاصه:** %d خط اضافه شده (+)، %d خط حذف شده (-)\n\n", d.AddedLines, d.DeletedLines))

	if len(d.Hunks) == 0 {
		sb.WriteString("فایل‌ها کاملاً یکسان هستند (No differences found).\n")
		return sb.String()
	}

	limit := min(len(d.Hunks), 25)
	sb.WriteString("```diff\n")
	for i := 0; i < limit; i++ {
		sb.WriteString(d.Hunks[i] + "\n\n")
	}
	if len(d.Hunks) > limit {
		sb.WriteString(fmt.Sprintf("... و %d تغییر دیگر\n", len(d.Hunks)-limit))
	}
	sb.WriteString("```\n")
	return strings.TrimSpace(sb.String())
}
