package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
)

const driverName = "macron-sqlite"

const timeLayout = "2006-01-02 15:04:05.000"

func init() {
	sql.Register(driverName, &tdriver{})
}

type tdriver struct{}

func (d *tdriver) Open(name string) (driver.Conn, error) {
	c, err := (&sqlite.Driver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return &tconn{Conn: c}, nil
}

type tconn struct{ driver.Conn }

func (c *tconn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if e, ok := c.Conn.(driver.QueryerContext); ok {
		rows, err := e.QueryContext(ctx, q, args)
		if err != nil {
			return nil, err
		}
		return &timeRows{Rows: rows}, nil
	}
	return nil, driver.ErrSkip
}

type timeRows struct {
	driver.Rows
	expr []bool
}

func (r *timeRows) Next(dest []driver.Value) error {
	if err := r.Rows.Next(dest); err != nil {
		return err
	}
	if r.expr == nil {
		r.expr = make([]bool, len(dest))
		typed, ok := r.Rows.(driver.RowsColumnTypeDatabaseTypeName)
		for i := range dest {
			r.expr[i] = ok && typed.ColumnTypeDatabaseTypeName(i) == ""
		}
	}
	for i, v := range dest {
		if str, ok := v.(string); ok && r.expr[i] && looksStoredTime(str) {
			layout := "2006-01-02 15:04:05"
			if len(str) > 19 {
				layout = "2006-01-02 15:04:05.000"
			}
			if t, err := time.ParseInLocation(layout, str, time.UTC); err == nil {
				dest[i] = t
			}
		}
	}
	return nil
}

func (r *timeRows) ColumnTypeDatabaseTypeName(i int) string {
	if typed, ok := r.Rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return typed.ColumnTypeDatabaseTypeName(i)
	}
	return ""
}

func looksStoredTime(s string) bool {
	if len(s) != 19 && len(s) != 23 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 4, 7:
			if c != '-' {
				return false
			}
		case 10:
			if c != ' ' {
				return false
			}
		case 13, 16:
			if c != ':' {
				return false
			}
		case 19:
			if c != '.' {
				return false
			}
		default:
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func (c *tconn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	if p, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, q)
	}
	return c.Conn.Prepare(q)
}

func (c *tconn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if e, ok := c.Conn.(driver.ExecerContext); ok {
		return e.ExecContext(ctx, q, args)
	}
	return nil, driver.ErrSkip
}

func (c *tconn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.Conn.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}

	//lint:ignore SA1019 the fallback for a driver without BeginTx
	return c.Conn.Begin()
}

func (c *tconn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *tconn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *tconn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *tconn) CheckNamedValue(nv *driver.NamedValue) error {
	v, err := driver.DefaultParameterConverter.ConvertValue(nv.Value)
	if err != nil {
		return err
	}
	if t, ok := v.(time.Time); ok {
		v = t.UTC().Format(timeLayout)
	}
	nv.Value = v
	return nil
}

func sqliteDSN(path string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(15000)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(OFF)")
	q.Add("_txlock", "immediate")
	u.RawQuery = q.Encode()
	return u.String()
}

func DBPath(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	dsn = strings.TrimPrefix(dsn, "sqlite://")
	dsn = strings.TrimPrefix(dsn, "sqlite:")
	return dsn
}

func openDB(path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("the database path is empty")
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create %s: %w", path, err)
		}
		f.Close()
	}
	db, err := sql.Open(driverName, sqliteDSN(path))
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)
	return db, nil
}

func sqliteCode(err error) int {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code()
	}
	return 0
}

const (
	codeBusy             = 5
	codeLocked           = 6
	codeConstraintUnique = 2067
	codeConstraintPK     = 1555
)

func alreadyApplied(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "duplicate column name") || strings.Contains(m, "already exists")
}

func isDuplicate(err error) bool {
	switch sqliteCode(err) {
	case codeConstraintUnique, codeConstraintPK:
		return true
	}
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func isBusy(err error) bool {
	c := sqliteCode(err) & 0xff
	return c == codeBusy || c == codeLocked
}
