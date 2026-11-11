package tools

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const ToolDatabaseQuery = "database_query"

var writeQueryRe = regexp.MustCompile(`(?i)\b(insert|update|delete|drop|alter|truncate|create|replace|grant|revoke|exec|execute|call|vacuum|copy|attach|detach)\b`)

type DBQueryResult struct {
	Columns  []string   `json:"columns"`
	Rows     [][]string `json:"rows"`
	RowCount int        `json:"row_count"`
	Duration time.Duration `json:"duration"`
}

// ExecuteReadOnlyQuery safely executes a read-only SQL query against a database.
func (r *Registry) ExecuteReadOnlyQuery(ctx context.Context, engine, dsn, query string, limit int) (*DBQueryResult, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)

	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	// 1. Guard against non-read-only queries
	cleanQ := strings.TrimSpace(strings.TrimRight(query, ";"))
	if writeQueryRe.MatchString(cleanQ) {
		return nil, fmt.Errorf("read-only violation: modifying statements (INSERT, UPDATE, DELETE, DROP, ALTER, etc.) are strictly prohibited")
	}
	if !strings.HasPrefix(strings.ToLower(cleanQ), "select") && !strings.HasPrefix(strings.ToLower(cleanQ), "explain") && !strings.HasPrefix(strings.ToLower(cleanQ), "with") && !strings.HasPrefix(strings.ToLower(cleanQ), "show") && !strings.HasPrefix(strings.ToLower(cleanQ), "describe") {
		return nil, fmt.Errorf("only SELECT, SHOW, DESCRIBE, and EXPLAIN queries are permitted")
	}

	driver := "sqlite"
	engine = strings.ToLower(strings.TrimSpace(engine))
	switch engine {
	case "postgres", "postgresql":
		driver = "postgres"
	case "mysql":
		driver = "mysql"
	case "sqlite", "sqlite3", "":
		driver = "sqlite"
	}

	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// For SQLite, or external drivers when registered
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("database connection error: %w", err)
	}
	defer db.Close()

	start := time.Now()
	rows, err := db.QueryContext(qctx, cleanQ)
	if err != nil {
		return nil, fmt.Errorf("query execution failed: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var resultRows [][]string
	for rows.Next() && len(resultRows) < limit {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		rowStr := make([]string, len(cols))
		for i, v := range vals {
			if v == nil {
				rowStr[i] = "NULL"
			} else {
				rowStr[i] = fmt.Sprint(v)
			}
		}
		resultRows = append(resultRows, rowStr)
	}

	return &DBQueryResult{
		Columns:  cols,
		Rows:     resultRows,
		RowCount: len(resultRows),
		Duration: time.Since(start),
	}, nil
}

func (res *DBQueryResult) FormatMarkdown() string {
	if res == nil || len(res.Columns) == 0 {
		return "(Empty query result)"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 Query returned %d rows in %v:\n\n", res.RowCount, res.Duration.Round(time.Millisecond)))

	// Header
	sb.WriteString("| " + strings.Join(res.Columns, " | ") + " |\n")
	sep := make([]string, len(res.Columns))
	for i := range sep {
		sep[i] = "---"
	}
	sb.WriteString("| " + strings.Join(sep, " | ") + " |\n")

	// Rows
	for _, row := range res.Rows {
		cleanRow := make([]string, len(row))
		for i, cell := range row {
			cleanRow[i] = strings.ReplaceAll(strings.TrimSpace(cell), "|", "¦")
		}
		sb.WriteString("| " + strings.Join(cleanRow, " | ") + " |\n")
	}
	return strings.TrimSpace(sb.String())
}
