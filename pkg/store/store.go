// Package store persists everything the agent needs in one SQLite file:
// users, conversation memory, LLM providers, settings, schedules, the
// searchable conversation archive and usage statistics.
package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Options configures a Store.
type Options struct {
	// Location defines where a "day" starts for daily limits and statistics.
	Location *time.Location
	// SecretKey, when set, encrypts provider API keys at rest (AES-256-GCM).
	SecretKey string
	// Now is replaced in tests.
	Now func() time.Time
}

// Store is safe for concurrent use.
type Store struct {
	db   *sql.DB
	loc  *time.Location
	now  func() time.Time
	aead cipher.AEAD

	mu       sync.RWMutex
	settings map[string]string
	admins   map[int64]bool
}

// Open opens (creating it if needed) the SQLite database at path, applies the
// migrations and loads the cached settings. path may be "sqlite:/file.db".
func Open(ctx context.Context, path string, opts Options) (*Store, error) {
	db, err := openDB(DBPath(path))
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot open the database: %w", err)
	}

	s := &Store{db: db, loc: opts.Location, now: opts.Now, settings: map[string]string{}, admins: map[int64]bool{}}
	if s.loc == nil {
		s.loc = time.UTC
	}
	if s.now == nil {
		s.now = time.Now
	}
	if opts.SecretKey != "" {
		key := sha256.Sum256([]byte(opts.SecretKey))
		block, _ := aes.NewCipher(key[:])
		s.aead, _ = cipher.NewGCM(block)
	}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.reloadCaches(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// DB exposes the pool (for health checks).
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }

// Location is the time zone used for days.
func (s *Store) Location() *time.Location { return s.loc }

// Now returns the current time in UTC.
func (s *Store) Now() time.Time { return s.now().UTC() }

// Today returns the current local day as YYYY-MM-DD.
func (s *Store) Today() string { return s.now().In(s.loc).Format(time.DateOnly) }

// DayStart returns the UTC instant at which the local day containing t begins.
func (s *Store) DayStart(t time.Time) time.Time {
	l := t.In(s.loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, s.loc).UTC()
}

// ---------------------------------------------------------------- migrations

// migrate brings the database up to the last version in migrations (schema.go).
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER NOT NULL PRIMARY KEY, applied_at DATETIME NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for i := current; i < len(migrations); i++ {
		for _, stmt := range migrations[i] {
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				if !alreadyApplied(err) {
					return fmt.Errorf("migration %d: %w", i+1, err)
				}
				log.Printf("store: migration %d was applied already (%v)", i+1, err)
			}
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, i+1, s.Now()); err != nil {
			return fmt.Errorf("record migration %d: %w", i+1, err)
		}
	}
	return nil
}

func (s *Store) reloadCaches(ctx context.Context) error {
	settings := map[string]string{}
	rows, err := s.db.QueryContext(ctx, `SELECT k, v FROM settings`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return err
		}
		settings[k] = v
	}
	rows.Close()

	admins := map[int64]bool{}
	rows, err = s.db.QueryContext(ctx, `SELECT user_id FROM admins`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		admins[id] = true
	}
	rows.Close()

	s.mu.Lock()
	s.settings, s.admins = settings, admins
	s.mu.Unlock()
	return rows.Err()
}

// ---------------------------------------------------------------- settings

// Setting keys used by the bot.
const (
	SettingHistoryTokens = "history_tokens" // the latest turns a request carries word for word (older ones are summarized)
	// The server monitor's alerts (on by default) and the percentages that raise them.
	SettingSysAlerts      = "sys_alerts"
	SettingSysCPU         = "sys_cpu_limit"
	SettingSysRAM         = "sys_ram_limit"
	SettingSysDisk        = "sys_disk_limit"
	SettingPremiumEmoji   = "premium_emoji"
	SettingReactions      = "reactions"
	SettingActiveProvider = "active_provider"
	SettingFailover       = "provider_failover"
	SettingLiveTrace      = "live_trace"     // show the live terminal while working
	SettingTraceStyle     = "trace_style"    // how the terminal looks: the console (default) or "code"
	SettingWorkLog        = "work_log"       // a folded work log above answers that used tools (default on)
	SettingLiveMode       = "live_mode"      // how private chats see the work by default (see PrefLive)
	SettingAgentSteps     = "agent_steps"    // tool rounds per request (0 = the server's default)
	SettingAgentMinutes   = "agent_minutes"  // minutes per request (0 = the server's default)
	SettingAutoContinue   = "auto_continue"  // extra rounds a job gets by itself when its budget runs out
	SettingLogChat        = "log_chat"       // where group join/leave notices go (empty = owners)
	SettingGroupApproval  = "group_approval" // new groups wait for an admin's approval
)

// Setting returns a cached setting.
func (s *Store) Setting(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.settings[key]
	return v, ok
}

// SettingInt returns an integer setting or def.
func (s *Store) SettingInt(key string, def int) int {
	if v, ok := s.Setting(key); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

// SettingBool returns a boolean setting or def.
func (s *Store) SettingBool(key string, def bool) bool {
	if v, ok := s.Setting(key); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return b
		}
	}
	return def
}

// SetSetting stores a setting and updates the cache.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (k, v, updated_at) VALUES (?, ?, ?)
		ON CONFLICT DO UPDATE SET v = excluded.v, updated_at = excluded.updated_at`, key, value, s.Now())
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.settings[key] = value
	s.mu.Unlock()
	return nil
}

// DeleteSetting removes a setting.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE k = ?`, key); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.settings, key)
	s.mu.Unlock()
	return nil
}

// ---------------------------------------------------------------- admins

// IsAdmin reports whether the user was added as an admin in the database.
func (s *Store) IsAdmin(id int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.admins[id]
}

func (s *Store) AddAdmin(ctx context.Context, id, addedBy int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO admins (user_id, added_by, created_at) VALUES (?, ?, ?)
		ON CONFLICT DO NOTHING`, id, addedBy, s.Now())
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.admins[id] = true
	s.mu.Unlock()
	return nil
}

func (s *Store) RemoveAdmin(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM admins WHERE user_id = ?`, id); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.admins, id)
	s.mu.Unlock()
	return nil
}

// Admins lists database admins (owners from the environment are not included).
func (s *Store) Admins(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM admins ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- secrets

const encPrefix = "enc:v1:"

// Sealing reports whether secrets (keys, cookies) are stored encrypted.
func (s *Store) Sealing() bool { return s.aead != nil }

func (s *Store) seal(plain string) string {
	if s.aead == nil || plain == "" {
		return plain
	}
	nonce := make([]byte, s.aead.NonceSize())
	_, _ = rand.Read(nonce)
	return encPrefix + base64.StdEncoding.EncodeToString(s.aead.Seal(nonce, nonce, []byte(plain), nil))
}

func (s *Store) open(stored string) (string, error) {
	if !strings.HasPrefix(stored, encPrefix) {
		return stored, nil
	}
	if s.aead == nil {
		return "", errors.New("provider key is encrypted but WAZE_SECRET_KEY is not set")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, encPrefix))
	if err != nil || len(raw) < s.aead.NonceSize() {
		return "", errors.New("corrupt encrypted provider key")
	}
	plain, err := s.aead.Open(nil, raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("cannot decrypt provider key (wrong WAZE_SECRET_KEY?)")
	}
	return string(plain), nil
}

// helpers

func nullTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// SetSecretSetting stores a setting encrypted with WAZE_SECRET_KEY (when set).
func (s *Store) SetSecretSetting(ctx context.Context, key, value string) error {
	if value == "" {
		return s.DeleteSetting(ctx, key)
	}
	return s.SetSetting(ctx, key, s.seal(value))
}

// SecretSetting reads a setting stored with SetSecretSetting.
func (s *Store) SecretSetting(key string) (string, error) {
	v, ok := s.Setting(key)
	if !ok {
		return "", nil
	}
	return s.open(v)
}
