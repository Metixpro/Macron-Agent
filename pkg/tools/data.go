package tools

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
)

// query_data answers questions about data files with SQL (SQLite): a
// database is attached read-only, a CSV/TSV or JSON file becomes a table
// named after it (columns typed from their values), and every sheet of an
// Excel file a table named after the sheet. Several files can be joined.
// The query runs here; only reads are allowed.

const (
	ToolQueryData = "query_data"

	maxQueryFiles = 10
	queryTimeout  = 2 * time.Minute
)

func (r *Registry) registerDataTools() {
	r.add(ToolQueryData,
		"Read-only SQLite SQL over .db/.sqlite, .csv/.tsv, .xlsx (a table per sheet) and .json files, each a table named after it; "+
			"joins work. No sql: the tables and columns. output: save the result as .xlsx or .csv.",
		map[string]any{
			"files":    arrayProp("string", ""),
			"sql":      prop("string", ""),
			"max_rows": prop("integer", "Default 50, max 500"),
			"output":   prop("string", ""),
		}, []string{"files"}, r.queryData)
	r.registerSpreadsheetTools()
}

func (r *Registry) queryData(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	files := anyList(args["files"])
	switch {
	case len(files) == 0:
		return "error: files is required (the data files to query)", nil
	case len(files) > maxQueryFiles:
		return fmt.Sprintf("error: at most %d files at once", maxQueryFiles), nil
	}
	scratch := scratchDir + "/data"
	_ = r.Machine.RemoveAll(sessionID, scratch)
	defer r.Machine.RemoveAll(sessionID, scratch)

	used := map[string]bool{}
	var sources []map[string]any
	for _, f := range files {
		f = cleanDocName(f)
		info, err := r.Machine.Stat(sessionID, f)
		switch {
		case err != nil || !info.Mode().IsRegular():
			return fmt.Sprintf("error: %q is not a file in the workspace", f), nil
		case info.Size() > docMaxBytes:
			return fmt.Sprintf("error: %s is larger than 200 MB", f), nil
		}
		ext := strings.ToLower(path.Ext(f))
		stem := strings.TrimSuffix(path.Base(f), path.Ext(f))
		switch {
		case sqliteExts[ext]:
			sources = append(sources, map[string]any{"kind": "sqlite", "path": "./" + f, "table": tableIdent(stem, used), "label": f})
		case ext == ".csv" || ext == ".tsv":
			delim := ","
			if head, _, _ := r.Machine.ReadFile(sessionID, f, 4096); ext == ".tsv" ||
				(!bytes.Contains(firstLineBytes(head), []byte(",")) && bytes.Contains(firstLineBytes(head), []byte("\t"))) {
				delim = "\t"
			}
			sources = append(sources, map[string]any{"kind": "csv", "path": "./" + f, "delim": delim, "table": tableIdent(stem, used), "label": f})
		case ext == ".json":
			sources = append(sources, map[string]any{"kind": "json", "path": "./" + f, "table": tableIdent(stem, used), "label": f})
		case ext == ".xlsx" || ext == ".xlsm":
			fh, info, err := r.Machine.Open(sessionID, f)
			if err != nil {
				return "error: " + friendlyFSError(err), nil
			}
			sheets, err := readXLSX(fh, info.Size())
			fh.Close()
			if err != nil {
				return fmt.Sprintf("error: cannot read %s: %v", f, err), nil
			}
			for _, s := range sheets {
				if len(s.Rows) == 0 {
					continue
				}
				name := s.Name
				if len(files) > 1 {
					name = stem + "_" + s.Name
				}
				csvName := fmt.Sprintf("%s/%d.csv", scratch, len(sources))
				if err := r.Machine.WriteFile(sessionID, csvName, sheetCSV(s.Rows)); err != nil {
					return "error: " + friendlyFSError(err), nil
				}
				sources = append(sources, map[string]any{"kind": "csv", "path": "./" + csvName, "delim": ",",
					"table": tableIdent(name, used), "label": f + " › " + s.Name})
			}
		default:
			return fmt.Sprintf("error: %s: query_data reads .db/.sqlite, .csv, .tsv, .xlsx and .json files", f), nil
		}
	}
	if len(sources) == 0 {
		return "error: the files hold no data", nil
	}

	output := cleanDocName(strArg(args, "output"))
	outCSV := ""
	switch strings.ToLower(path.Ext(output)) {
	case "":
		if output != "" {
			return "error: output must end in .csv or .xlsx", nil
		}
	case ".csv":
		outCSV = "./" + output
	case ".xlsx":
		outCSV = "./" + scratch + "/result.csv"
	default:
		return "error: output must end in .csv or .xlsx", nil
	}
	res, err := r.runScript(ctx, sessionID, "data_query.py", map[string]any{
		"sources": sources, "sql": rawStrArg(args, "sql"), "max_rows": intArg(args, "max_rows", 50, 1, 500), "out_csv": outCSV,
	}, queryTimeout)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	switch {
	case res.ExitCode == 3:
		return "error: " + strings.TrimSpace(res.Stderr) + " (SQLite SQL; call without sql to see the tables and columns)", nil
	case res.ExitCode != 0:
		return "error: " + failure("The query", res), nil
	}
	text := strings.TrimSpace(res.Stdout)
	if output != "" && strings.EqualFold(path.Ext(output), ".xlsx") {
		rows, err := r.tableFile(sessionID, strings.TrimPrefix(outCSV, "./"))
		if err != nil {
			return text + "\nerror: the result could not be saved: " + err.Error(), nil
		}
		var buf bytes.Buffer
		if err := writeXLSX(&buf, []xlsxSheet{{Name: "Result", Rows: rows, Header: true}}); err != nil {
			return text + "\nerror: the result could not be saved: " + err.Error(), nil
		}
		if err := r.Machine.WriteFile(sessionID, output, buf.Bytes()); err != nil {
			return text + "\nerror: the result could not be saved: " + friendlyFSError(err), nil
		}
	}
	if output != "" {
		text += "\nSaved the whole result to " + output + "."
	}
	return text, nil
}

// sheetCSV writes the rows of a sheet as CSV.
func sheetCSV(rows [][]string) []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.WriteAll(rows)
	return buf.Bytes()
}

// tableIdent makes a table name SQL can use without quotes: ASCII
// punctuation and spaces become "_" (Persian letters stay), unique.
func tableIdent(name string, used map[string]bool) string {
	var b strings.Builder
	under := false
	for _, r := range name {
		if r < 0x80 && !unicode.IsLetter(r) && !unicode.IsDigit(r) || unicode.IsSpace(r) {
			if !under && b.Len() > 0 {
				b.WriteByte('_')
			}
			under = true
			continue
		}
		b.WriteRune(r)
		under = false
	}
	s := strings.Trim(b.String(), "_")
	if s == "" {
		s = "data"
	}
	if s[0] >= '0' && s[0] <= '9' {
		s = "t_" + s
	}
	base := s
	for n := 2; used[strings.ToLower(s)]; n++ {
		s = fmt.Sprintf("%s_%d", base, n)
	}
	used[strings.ToLower(s)] = true
	return s
}
