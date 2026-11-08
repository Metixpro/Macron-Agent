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

var ErrNotFound = errors.New("not found")

type Options struct {
	Location *time.Location

	SecretKey string

	Now func() time.Time
}

type Store struct {
	db   *sql.DB
	loc  *time.Location
	now  func() time.Time
	aead cipher.AEAD

	mu       sync.RWMutex
	settings map[string]string
	admins   map[int64]bool
}

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

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Location() *time.Location { return s.loc }

func (s *Store) Now() time.Time { return s.now().UTC() }

func (s *Store) Today() string { return s.now().In(s.loc).Format(time.DateOnly) }

func (s *Store) DayStart(t time.Time) time.Time {
	l := t.In(s.loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, s.loc).UTC()
}

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

const (
	SettingHistoryTokens = "history_tokens"

	SettingSysAlerts      = "sys_alerts"
	SettingSysCPU         = "sys_cpu_limit"
	SettingSysRAM         = "sys_ram_limit"
	SettingSysDisk        = "sys_disk_limit"
	SettingPremiumEmoji   = "premium_emoji"
	SettingReactions      = "reactions"
	SettingReactionEmoji  = "reaction_emoji"
	SettingActiveProvider = "active_provider"
	SettingFailover       = "provider_failover"
	SettingLiveTrace      = "live_trace"
	SettingAgentSteps     = "agent_steps"
	SettingAgentMinutes   = "agent_minutes"
	SettingAutoContinue   = "auto_continue"
	SettingLogChat        = "log_chat"
	SettingGroupApproval  = "group_approval"
)

func (s *Store) Setting(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.settings[key]
	return v, ok
}

func (s *Store) SettingInt(key string, def int) int {
	if v, ok := s.Setting(key); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func (s *Store) SettingBool(key string, def bool) bool {
	if v, ok := s.Setting(key); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return b
		}
	}
	return def
}

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

func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE k = ?`, key); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.settings, key)
	s.mu.Unlock()
	return nil
}

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

const encPrefix = "enc:v1:"

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

func (s *Store) SetSecretSetting(ctx context.Context, key, value string) error {
	if value == "" {
		return s.DeleteSetting(ctx, key)
	}
	return s.SetSetting(ctx, key, s.seal(value))
}

func (s *Store) SecretSetting(key string) (string, error) {
	v, ok := s.Setting(key)
	if !ok {
		return "", nil
	}
	return s.open(v)
}

func (s *Store) EnsureSecretSetting(ctx context.Context, key, candidate string) (string, error) {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (k,v,updated_at) VALUES (?,?,?)
 ON CONFLICT(k) DO UPDATE SET v=excluded.v, updated_at=excluded.updated_at WHERE settings.v=''`, key, s.seal(candidate), s.Now())
	if err != nil {
		return "", err
	}
	var stored string
	if err = s.db.QueryRowContext(ctx, `SELECT v FROM settings WHERE k=?`, key).Scan(&stored); err != nil {
		return "", err
	}
	value, err := s.open(stored)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.settings[key] = stored
	s.mu.Unlock()
	return value, nil
}
