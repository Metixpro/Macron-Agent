package tools

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
)

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

	r.add(ToolDatabaseQuery,
		"Safely run read-only SELECT queries against external PostgreSQL, MySQL, or SQLite databases.",
		map[string]any{
			"engine": enumProp("postgres", "postgres", "mysql", "sqlite"),
			"dsn":    prop("string", "Database connection string or SQLite path"),
			"query":  prop("string", "SELECT query to execute"),
			"limit":  prop("integer", "Maximum rows to return (default 50, max 200)"),
		}, []string{"dsn", "query"}, func(ctx context.Context, _ string, args map[string]any) (string, error) {
			engine := strArg(args, "engine")
			dsn := strArg(args, "dsn")
			q := strArg(args, "query")
			limit := intArg(args, "limit", 50, 1, 200)
			res, err := r.ExecuteReadOnlyQuery(ctx, engine, dsn, q, limit)
			if err != nil {
				return "db error: " + err.Error(), nil
			}
			return res.FormatMarkdown(), nil
		})

	r.add(ToolStatsForecast,
		"Calculate descriptive statistics and time-series linear trend forecasts for numeric data.",
		map[string]any{
			"action": enumProp("descriptive", "descriptive", "forecast"),
			"data":   arrayProp("number", "Array of numeric values to analyze"),
			"steps":  prop("integer", "Forecast periods forward (for forecast, default 3)"),
		}, []string{"data"}, func(ctx context.Context, _ string, args map[string]any) (string, error) {
			action := strArg(args, "action")
			if action == "" {
				action = "descriptive"
			}
			var nums []float64
			if rawList, ok := args["data"].([]any); ok {
				for _, v := range rawList {
					switch n := v.(type) {
					case float64:
						nums = append(nums, n)
					case int:
						nums = append(nums, float64(n))
					}
				}
			}
			if len(nums) == 0 {
				for _, s := range anyList(args["data"]) {
					if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
						nums = append(nums, f)
					}
				}
			}
			if len(nums) == 0 {
				return "error: numeric data array is required", nil
			}
			if action == "forecast" {
				steps := intArg(args, "steps", 3, 1, 20)
				fc := ComputeLinearForecast(nums, steps)
				return fc.Format(), nil
			}
			ds := ComputeDescriptiveStats(nums)
			return ds.Format(), nil
		})

	r.add(ToolCreateDashboard,
		"Generate a responsive, standalone HTML dashboard with KPI metric cards, interactive charts, and tables.",
		map[string]any{
			"title":       prop("string", "Dashboard title"),
			"output_file": prop("string", "Destination HTML filename (default 'dashboard.html')"),
		}, []string{"title"}, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			title := strArg(args, "title")
			outFile := strArg(args, "output_file")
			if outFile == "" {
				outFile = "dashboard.html"
			}
			html := GenerateHTMLDashboard(title, nil, nil, nil)
			if err := r.Machine.WriteFile(sessionID, outFile, []byte(html)); err != nil {
				return "error saving dashboard: " + err.Error(), nil
			}
			return fmt.Sprintf("✅ Interactive HTML dashboard generated at `%s`.", outFile), nil
		})

	r.add(ToolOpenAPICall,
		"Invoke REST API operations defined in an OpenAPI / Swagger specification.",
		map[string]any{
			"spec_path":    prop("string", "Path to OpenAPI spec file in workspace"),
			"operation_id": prop("string", "Operation ID (e.g. getUserById)"),
			"endpoint":     prop("string", "Endpoint path (e.g. /api/v1/users)"),
			"method":       enumProp("GET", "GET", "POST", "PUT", "DELETE"),
			"base_url":     prop("string", "Server URL override"),
		}, []string{"spec_path"}, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			spec := strArg(args, "spec_path")
			opID := strArg(args, "operation_id")
			endpoint := strArg(args, "endpoint")
			method := strArg(args, "method")
			baseURL := strArg(args, "base_url")
			return r.CallOpenAPIEndpoint(ctx, sessionID, spec, opID, endpoint, method, baseURL, nil, nil)
		})

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

func sheetCSV(rows [][]string) []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.WriteAll(rows)
	return buf.Bytes()
}

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
