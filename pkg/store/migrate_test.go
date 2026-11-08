package store

import (
	"path/filepath"
	"testing"
)

func TestUpgradeFromVersion1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := openDB(DBPath(path))
	if err != nil {
		t.Fatal(err)
	}
	stmts := append([]string{`CREATE TABLE schema_migrations (version INTEGER NOT NULL PRIMARY KEY, applied_at DATETIME NOT NULL)`}, migrations[0]...)
	stmts = append(stmts,

		`CREATE TABLE snap_log (id INTEGER PRIMARY KEY AUTOINCREMENT, at DATETIME NOT NULL, kind TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX idx_snap_log_at ON snap_log (at)`,
		`INSERT INTO snap_log (at, kind) VALUES ('2026-01-01 00:00:00', 'chat')`,
		`INSERT INTO schema_migrations VALUES (1, '2026-01-01 00:00:00')`,
		`INSERT INTO settings VALUES ('snap_on', 'true', '2026-01-01 00:00:00')`,
		`INSERT INTO settings VALUES ('snap_key', 'sealed-secret', '2026-01-01 00:00:00')`,
		`INSERT INTO settings VALUES ('snap_trim', 'auto', '2026-01-01 00:00:00')`,
		`INSERT INTO settings VALUES ('snap_chat', 'true', '2026-01-01 00:00:00')`,
		`INSERT INTO settings VALUES ('reactions', 'false', '2026-01-01 00:00:00')`,
		`INSERT INTO users (id, first_name, created_at, last_seen_at) VALUES (42, 'Owner', '2026-01-01 00:00:00', '2026-01-01 00:00:00')`,
	)
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	db.Close()

	s, err := Open(bg, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%snap%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the old log is still there: %d %v", n, err)
	}
	for k, want := range map[string]string{"triage": "local", "triage_trim": "auto", "triage_chat": "true", "reactions": "false"} {
		if got, _ := s.Setting(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	for _, k := range []string{"snap_on", "snap_key", "snap_trim", "snap_chat"} {
		if _, ok := s.Setting(k); ok {
			t.Errorf("%s is left", k)
		}
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = 42`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the user is lost: %d %v", n, err)
	}
	var v int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("version %d", v)
	}
}
