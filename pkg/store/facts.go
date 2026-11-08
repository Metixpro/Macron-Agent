package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ---------------------------------------------------------------- long-term memory

// MaxFacts is how many facts are kept per user; MaxFactChars is the size of one.
const (
	MaxFacts     = 60
	MaxFactChars = 300
)

// ErrTooManyFacts is returned when a user's memory is full.
var ErrTooManyFacts = errors.New("memory is full")

// Fact is something lasting the assistant remembers about a user.
type Fact struct {
	ID        int64
	UserID    int64
	Text      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Facts returns a user's facts, oldest first.
func (s *Store) Facts(ctx context.Context, userID int64) ([]Fact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, fact, created_at, updated_at FROM user_facts WHERE user_id = ? ORDER BY id LIMIT ?`,
		userID, MaxFacts*2)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		var f Fact
		if err := rows.Scan(&f.ID, &f.UserID, &f.Text, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// AddFact saves a fact; an identical one is not saved twice (its id is returned).
func (s *Store) AddFact(ctx context.Context, userID int64, text string) (id int64, existed bool, err error) {
	text = truncate(strings.TrimSpace(text), MaxFactChars)
	facts, err := s.Facts(ctx, userID)
	if err != nil {
		return 0, false, err
	}
	for _, f := range facts {
		if strings.EqualFold(f.Text, text) {
			return f.ID, true, nil
		}
	}
	if len(facts) >= MaxFacts {
		return 0, false, ErrTooManyFacts
	}
	now := s.Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO user_facts (user_id, fact, created_at, updated_at) VALUES (?, ?, ?, ?)`, userID, text, now, now)
	if err != nil {
		return 0, false, err
	}
	id, err = res.LastInsertId()
	return id, false, err
}

// UpdateFact replaces the text of one of the user's facts.
func (s *Store) UpdateFact(ctx context.Context, userID, id int64, text string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE user_facts SET fact = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		truncate(strings.TrimSpace(text), MaxFactChars), s.Now(), id, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteFact removes one of the user's facts.
func (s *Store) DeleteFact(ctx context.Context, userID, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM user_facts WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ClearFacts removes everything remembered about a user.
func (s *Store) ClearFacts(ctx context.Context, userID int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM user_facts WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------------- per-user preferences

// PrefFactsOff turns the long-term memory off for a user.
const PrefFactsOff = "facts_off"

// PrefMode is the user's answer mode: "fast", or unset for deep mode.
const PrefMode = "mode"

// UserPref returns a user's preference ("" when unset).
func (s *Store) UserPref(ctx context.Context, userID int64, name string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM user_prefs WHERE user_id = ? AND name = ?`, userID, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetUserPref stores a preference; "" deletes it.
func (s *Store) SetUserPref(ctx context.Context, userID int64, name, value string) error {
	if value == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM user_prefs WHERE user_id = ? AND name = ?`, userID, name)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_prefs (user_id, name, value) VALUES (?, ?, ?)
		ON CONFLICT DO UPDATE SET value = excluded.value`, userID, truncate(name, 32), truncate(value, 255))
	return err
}

// PrefContextMeter is where the user's context meter stood after their last
// request, and how often their conversation had been compacted then
// ("tokens/compressions").
const PrefContextMeter = "ctx_meter"
