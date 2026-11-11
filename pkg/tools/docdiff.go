package tools

import (
	"context"
	"fmt"
	"path"
	"strings"
)

const ToolDocumentDiff = "document_diff"

const diffMaxLines = 20000

type DiffReport struct {
	FileA, FileB        string
	AddedLines, Deleted int
	Hunks               []string
}

var textExts = map[string]bool{".pdf": true, ".docx": true, ".pptx": true, ".xlsx": true, ".xlsm": true, ".odt": true, ".epub": true, ".rtf": true, ".doc": true}

func (r *Registry) docLines(ctx context.Context, sessionID, name string) ([]string, error) {
	ext := strings.ToLower(path.Ext(name))
	if textExts[ext] {
		d, problem := r.extractDocument(ctx, sessionID, name, ext)
		if problem != "" {
			return nil, fmt.Errorf("%s", strings.TrimPrefix(problem, "error: "))
		}
		return strings.Split(strings.Join(d.Pages, "\n"), "\n"), nil
	}
	data, truncated, err := r.Machine.ReadFile(sessionID, name, 4<<20)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("%s is larger than 4 MB", name)
	}
	return strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n"), nil
}

func lcsDiff(a, b []string) []string {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	ma, mb := a[prefix:len(a)-suffix], b[prefix:len(b)-suffix]
	ops := make([]string, 0, len(ma)+len(mb))
	if len(ma)*len(mb) > 25_000_000 {
		for _, l := range ma {
			ops = append(ops, "-"+l)
		}
		for _, l := range mb {
			ops = append(ops, "+"+l)
		}
	} else {
		n, m := len(ma), len(mb)
		dp := make([][]int32, n+1)
		for i := range dp {
			dp[i] = make([]int32, m+1)
		}
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if ma[i] == mb[j] {
					dp[i][j] = dp[i+1][j+1] + 1
				} else {
					dp[i][j] = max(dp[i+1][j], dp[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < n || j < m {
			switch {
			case i < n && j < m && ma[i] == mb[j]:
				ops = append(ops, " "+ma[i])
				i++
				j++
			case i < n && (j == m || dp[i+1][j] >= dp[i][j+1]):
				ops = append(ops, "-"+ma[i])
				i++
			default:
				ops = append(ops, "+"+mb[j])
				j++
			}
		}
	}
	out := make([]string, 0, prefix+len(ops)+suffix)
	for _, l := range a[:prefix] {
		out = append(out, " "+l)
	}
	out = append(out, ops...)
	for _, l := range a[len(a)-suffix:] {
		out = append(out, " "+l)
	}
	return out
}

func (r *Registry) CompareDocuments(ctx context.Context, sessionID, fileA, fileB, _ string) (*DiffReport, error) {
	a, err := r.docLines(ctx, sessionID, fileA)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", fileA, err)
	}
	b, err := r.docLines(ctx, sessionID, fileB)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", fileB, err)
	}
	if len(a) > diffMaxLines || len(b) > diffMaxLines {
		return nil, fmt.Errorf("more than %d lines: compare them with bash_execute (diff -u)", diffMaxLines)
	}
	ops := lcsDiff(a, b)
	rep := &DiffReport{FileA: fileA, FileB: fileB}
	const ctxLines = 2
	lineA, lineB := 1, 1
	for i := 0; i < len(ops); {
		if ops[i][0] == ' ' {
			i++
			lineA++
			lineB++
			continue
		}
		start := max(i-ctxLines, 0)
		for k := start; k < i; k++ {
			if ops[k][0] == ' ' {
				lineA--
				lineB--
			}
		}
		j := i
		for j < len(ops) {
			if ops[j][0] != ' ' {
				j++
				continue
			}
			run := j
			for run < len(ops) && ops[run][0] == ' ' {
				run++
			}
			if run-j > 2*ctxLines || run == len(ops) {
				break
			}
			j = run
		}
		end := min(j+ctxLines, len(ops))
		var h strings.Builder
		fmt.Fprintf(&h, "@@ -%d +%d @@", lineA, lineB)
		for _, op := range ops[start:end] {
			h.WriteString("\n" + op)
			switch op[0] {
			case '-':
				rep.Deleted++
				lineA++
			case '+':
				rep.AddedLines++
				lineB++
			default:
				lineA++
				lineB++
			}
		}
		rep.Hunks = append(rep.Hunks, h.String())
		i = end
	}
	return rep, nil
}

func (d *DiffReport) Format(format string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s → %s: %d lines added, %d removed, %d changed places.", d.FileA, d.FileB, d.AddedLines, d.Deleted, len(d.Hunks))
	if len(d.Hunks) == 0 {
		sb.WriteString(" The texts are the same.")
		return sb.String()
	}
	if format == "summary" {
		return sb.String()
	}
	limit, size := len(d.Hunks), 0
	for i, h := range d.Hunks {
		if size += len(h); size > 12000 {
			limit = i
			break
		}
	}
	sb.WriteString("\n```diff")
	for _, h := range d.Hunks[:limit] {
		sb.WriteString("\n" + h)
	}
	sb.WriteString("\n```")
	if limit < len(d.Hunks) {
		fmt.Fprintf(&sb, "\n… %d more changed places (narrow the comparison or use diff -u)", len(d.Hunks)-limit)
	}
	return sb.String()
}
