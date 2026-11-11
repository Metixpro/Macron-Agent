package tools

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/Metixpro/macron-agent/pkg/config"
)

const ToolDatabaseQuery = "database_query"

var (
	writeQueryRe = regexp.MustCompile(`(?i)\b(insert|update|delete|drop|alter|truncate|create|replace|grant|revoke|exec|execute|call|vacuum|copy|attach|detach|merge|lock|reindex|pragma)\b`)
	readQueryRe  = regexp.MustCompile(`(?i)^(?:select|with|explain|show|describe|desc|values|table)\b`)
)

type DBQueryResult struct {
	Columns  []string
	Rows     [][]string
	More     bool
	Duration time.Duration
}

func (r *Registry) openDatabase(sessionID, engine, target string) (*sql.DB, string, error) {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "postgres", "postgresql":
		db, err := sql.Open("pgx", target)
		return db, "postgres", err
	case "mysql", "mariadb":
		db, err := sql.Open("mysql", target)
		return db, "mysql", err
	case "sqlite", "sqlite3", "":
		path := strings.TrimPrefix(target, "file:")
		if i := strings.IndexByte(path, '?'); i >= 0 {
			path = path[:i]
		}
		abs, _, err := r.Machine.HostPath(sessionID, path)
		if err != nil {
			return nil, "", err
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		for _, own := range append(config.SecretFiles(), config.HomePath(config.FileDB)) {
			if filepath.Clean(own) == abs {
				return nil, "", fmt.Errorf("%s is the agent's own database or settings; it is not opened here", path)
			}
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, "", fmt.Errorf("no database file %s", path)
		}
		db, err := sql.Open("sqlite", "file:"+(&url.URL{Path: abs}).EscapedPath()+"?mode=ro&_pragma=query_only(1)")
		return db, "sqlite", err
	}
	return nil, "", fmt.Errorf("unknown engine %q: postgres, mysql or sqlite", engine)
}

func (r *Registry) ExecuteReadOnlyQuery(ctx context.Context, sessionID, engine, target, query string, limit int) (*DBQueryResult, error) {
	limit = min(max(limit, 1), 500)
	q := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(query), ";"))
	switch {
	case q == "":
		return nil, fmt.Errorf("query is required")
	case strings.Contains(q, ";"):
		return nil, fmt.Errorf("one statement at a time")
	case !readQueryRe.MatchString(q) || writeQueryRe.MatchString(q):
		return nil, fmt.Errorf("this tool only reads (SELECT, WITH, EXPLAIN, SHOW, DESCRIBE); run changes with bash_execute and the database's own client, where they are checked first")
	}
	db, kind, err := r.openDatabase(sessionID, engine, target)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := db.BeginTx(qctx, &sql.TxOptions{ReadOnly: kind != "sqlite"})
	if err != nil {
		return nil, fmt.Errorf("connecting to the %s database: %w", kind, err)
	}
	defer func() { _ = tx.Rollback() }()
	start := time.Now()
	rows, err := tx.QueryContext(qctx, q)
	if err != nil {
		return nil, fmt.Errorf("the query failed: %w", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	res := &DBQueryResult{Columns: cols}
	for rows.Next() {
		if len(res.Rows) == limit {
			res.More = true
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			switch x := v.(type) {
			case nil:
				row[i] = "NULL"
			case []byte:
				row[i] = string(x)
			default:
				row[i] = fmt.Sprint(x)
			}
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res.Duration = time.Since(start)
	return res, nil
}

func (res *DBQueryResult) FormatMarkdown() string {
	if res == nil || len(res.Columns) == 0 {
		return "(no columns)"
	}
	var sb strings.Builder
	more := ""
	if res.More {
		more = ", more rows exist (raise limit or narrow the query)"
	}
	fmt.Fprintf(&sb, "(%d rows in %s%s)\n\n", len(res.Rows), res.Duration.Round(time.Millisecond), more)
	sb.WriteString("| " + strings.Join(res.Columns, " | ") + " |\n")
	sb.WriteString("|" + strings.Repeat(" --- |", len(res.Columns)) + "\n")
	for _, row := range res.Rows {
		for i, cell := range row {
			row[i] = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(cell), "|", "¦"), "\n", " ")
		}
		sb.WriteString("| " + strings.Join(row, " | ") + " |\n")
	}
	return strings.TrimSpace(sb.String())
}
