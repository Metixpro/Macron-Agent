package security

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// AutopsyParams configures Autopsy forensic case DB (autopsy.db) inspection
// or Sleuth Kit disk image triage (fls/mmls/fsstat).
type AutopsyParams struct {
	Action     string // case_summary, artifacts, files, deleted, timeline, keyword_search, image_info, status
	Path       string // autopsy.db, Autopsy case directory, or disk image (.dd/.raw/.e01)
	Query      string // filename, extension, or artifact filter
	Limit      int
	TimeoutSec int
}

// RunAutopsy inspects Autopsy SQLite forensic case databases in pure Go
// (zero external installation needed) and supports Sleuth Kit CLIs for disk images.
func (e Env) RunAutopsy(ctx context.Context, sessionID string, p AutopsyParams) (string, error) {
	action := strings.ToLower(strings.TrimSpace(p.Action))
	if action == "" {
		if strings.TrimSpace(p.Path) != "" {
			action = "case_summary"
		} else {
			action = "status"
		}
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 80 {
		limit = 80
	}

	flsBin, hasFls := FindBinary("fls")
	mmlsBin, hasMmls := FindBinary("mmls")
	if action == "status" {
		tsk := "not installed"
		if hasFls || hasMmls {
			tsk = fmt.Sprintf("installed (fls=%v, mmls=%v)", hasFls, hasMmls)
		}
		return fmt.Sprintf("Autopsy forensic module ready: pure-Go autopsy.db SQLite analyzer is active; Sleuth Kit CLI (fls/mmls) is %s.", tsk), nil
	}

	resolved, err := e.ResolvePath(sessionID, p.Path)
	if err != nil {
		return "", err
	}

	if action == "image_info" {
		if !hasMmls && !hasFls {
			return "", fmt.Errorf("Sleuth Kit tools (mmls/fls) are not installed; install sleuthkit to inspect raw disk images, or pass an autopsy.db path for case_summary/artifacts/files/deleted/timeline")
		}
		var sb strings.Builder
		if hasMmls {
			out, stderr, _, _ := e.RunBin(ctx, sessionID, ClampTimeout(p.TimeoutSec, 45), mmlsBin, resolved)
			fmt.Fprintf(&sb, "Partition Table (mmls %s):\n%s%s\n", resolved, out, stderr)
		}
		if hasFls {
			out, _, _, _ := e.RunBin(ctx, sessionID, ClampTimeout(p.TimeoutSec, 45), flsBin, "-r", "-p", resolved)
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) > limit {
				lines = lines[:limit]
			}
			fmt.Fprintf(&sb, "\nTop File Entries (fls):\n%s\n", strings.Join(lines, "\n"))
		}
		return CapOutput(sb.String()), nil
	}

	// If user passed an Autopsy case directory, locate autopsy.db inside it.
	dbPath := resolved
	if fi, stErr := os.Stat(resolved); stErr == nil && fi.IsDir() {
		cand := filepath.Join(resolved, "autopsy.db")
		if _, err := os.Stat(cand); err == nil {
			dbPath = cand
		}
	}
	if _, err := os.Stat(dbPath); err != nil {
		return "", fmt.Errorf("Autopsy case database not found: %s", dbPath)
	}

	return queryAutopsyDB(ctx, dbPath, action, p.Query, limit)
}

func queryAutopsyDB(ctx context.Context, dbPath, action, query string, limit int) (string, error) {
	dsn := "file:" + dbPath + "?mode=ro&_pragma=busy_timeout(3000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", fmt.Errorf("open autopsy.db: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	hasTable := func(name string) bool {
		var found string
		err := db.QueryRowContext(qctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&found)
		return err == nil && found == name
	}

	var sb strings.Builder
	switch action {
	case "case_summary":
		fmt.Fprintf(&sb, "Autopsy Forensic Case Summary (%s)\n", dbPath)
		if hasTable("tsk_db_info") {
			var maj, minv int
			if db.QueryRowContext(qctx, `SELECT schema_ver, schema_minor_ver FROM tsk_db_info LIMIT 1`).Scan(&maj, &minv) == nil {
				fmt.Fprintf(&sb, "Schema: TSK v%d.%d\n", maj, minv)
			}
		}
		if hasTable("tsk_files") {
			var total, deleted int
			_ = db.QueryRowContext(qctx, `SELECT COUNT(*) FROM tsk_files`).Scan(&total)
			_ = db.QueryRowContext(qctx, `SELECT COUNT(*) FROM tsk_files WHERE dir_flags = 2 OR meta_flags = 2`).Scan(&deleted)
			fmt.Fprintf(&sb, "Indexed Files: %d | Deleted Entries: %d\n", total, deleted)
		}
		if hasTable("blackboard_artifacts") && hasTable("blackboard_artifact_types") {
			rows, err := db.QueryContext(qctx, `
				SELECT t.display_name, COUNT(*) AS cnt
				FROM blackboard_artifacts a
				JOIN blackboard_artifact_types t ON a.artifact_type_id = t.artifact_type_id
				GROUP BY t.display_name
				ORDER BY cnt DESC LIMIT 12`)
			if err == nil {
				defer rows.Close()
				sb.WriteString("\nBlackboard Artifacts:\n")
				for rows.Next() {
					var name string
					var cnt int
					if rows.Scan(&name, &cnt) == nil {
						fmt.Fprintf(&sb, "- %s: %d\n", name, cnt)
					}
				}
				if err := rows.Err(); err != nil {
					return "", err
				}
			}
		}
		return CapOutput(sb.String()), nil

	case "artifacts":
		if !hasTable("blackboard_artifacts") {
			return "", fmt.Errorf("blackboard_artifacts table not found in %s", dbPath)
		}
		like := "%" + strings.TrimSpace(query) + "%"
		rows, err := db.QueryContext(qctx, `
			SELECT t.display_name, COALESCE(at.display_name,''), COALESCE(attr.value_text,'')
			FROM blackboard_artifacts a
			JOIN blackboard_artifact_types t ON a.artifact_type_id = t.artifact_type_id
			LEFT JOIN blackboard_attributes attr ON a.artifact_id = attr.artifact_id
			LEFT JOIN blackboard_attribute_types at ON attr.attribute_type_id = at.attribute_type_id
			WHERE (? = '%%' OR t.display_name LIKE ? OR attr.value_text LIKE ?)
			LIMIT ?`, like, like, like, limit)
		if err != nil {
			return "", err
		}
		defer rows.Close()
		fmt.Fprintf(&sb, "Autopsy Blackboard Artifacts (%s)\n", dbPath)
		n := 0
		for rows.Next() {
			var artType, attrName, val string
			if rows.Scan(&artType, &attrName, &val) == nil {
				n++
				fmt.Fprintf(&sb, "- [%s] %s = %s\n", artType, attrName, truncateLine(val, 110))
			}
		}
		if err := rows.Err(); err != nil {
			return "", err
		}
		if n == 0 {
			sb.WriteString("No matching artifacts.\n")
		}
		return CapOutput(sb.String()), nil

	case "files", "deleted", "keyword_search":
		if !hasTable("tsk_files") {
			return "", fmt.Errorf("tsk_files table not found in %s", dbPath)
		}
		where := "1=1"
		var qargs []any
		if action == "deleted" {
			where += " AND (dir_flags = 2 OR meta_flags = 2)"
		}
		if q := strings.TrimSpace(query); q != "" {
			where += " AND (name LIKE ? OR parent_path LIKE ? OR md5 LIKE ?)"
			like := "%" + q + "%"
			qargs = append(qargs, like, like, like)
		}
		qargs = append(qargs, limit)
		rows, err := db.QueryContext(qctx, `
			SELECT COALESCE(parent_path,''), name, COALESCE(size,0), COALESCE(mtime,0), COALESCE(md5,'')
			FROM tsk_files WHERE `+where+` ORDER BY mtime DESC LIMIT ?`, qargs...)
		if err != nil {
			return "", err
		}
		defer rows.Close()
		fmt.Fprintf(&sb, "Autopsy %s (%s)\n", action, dbPath)
		n := 0
		for rows.Next() {
			var parent, name, md5 string
			var size, mtime int64
			if rows.Scan(&parent, &name, &size, &mtime, &md5) == nil {
				n++
				ts := "-"
				if mtime > 0 {
					ts = time.Unix(mtime, 0).UTC().Format("2006-01-02 15:04:05")
				}
				hashNote := ""
				if md5 != "" {
					hashNote = " md5=" + truncateLine(md5, 16)
				}
				fmt.Fprintf(&sb, "- %s%s (%dB, mtime=%s%s)\n", parent, name, size, ts, hashNote)
			}
		}
		if err := rows.Err(); err != nil {
			return "", err
		}
		if n == 0 {
			sb.WriteString("No matching file records.\n")
		}
		return CapOutput(sb.String()), nil

	case "timeline":
		if !hasTable("tsk_files") {
			return "", fmt.Errorf("tsk_files table not found in %s", dbPath)
		}
		rows, err := db.QueryContext(qctx, `
			SELECT COALESCE(parent_path,''), name, mtime, COALESCE(crtime,0), COALESCE(size,0)
			FROM tsk_files WHERE mtime > 0 ORDER BY mtime DESC LIMIT ?`, limit)
		if err != nil {
			return "", err
		}
		defer rows.Close()
		fmt.Fprintf(&sb, "Autopsy Recent File Timeline (%s)\n", dbPath)
		for rows.Next() {
			var parent, name string
			var mtime, crtime, size int64
			if rows.Scan(&parent, &name, &mtime, &crtime, &size) == nil {
				fmt.Fprintf(&sb, "- %s | %s%s (%dB)\n",
					time.Unix(mtime, 0).UTC().Format("2006-01-02 15:04:05"), parent, name, size)
			}
		}
		if err := rows.Err(); err != nil {
			return "", err
		}
		return CapOutput(sb.String()), nil
	}
	return "", fmt.Errorf("unknown autopsy action %q (use: case_summary, artifacts, files, deleted, timeline, keyword_search, image_info, status)", action)
}
