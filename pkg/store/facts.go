package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	MaxFacts     = 60
	MaxFactChars = 300
)

var ErrTooManyFacts = errors.New("memory is full")

type Fact struct {
	ID        int64
	UserID    int64
	Text      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

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

func (s *Store) AddFact(ctx context.Context, userID int64, text string) (id int64, existed bool, err error) {
	text = truncate(strings.TrimSpace(text), MaxFactChars)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	rows, err := tx.QueryContext(ctx, `SELECT id, fact FROM user_facts WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return 0, false, err
	}
	count := 0
	for rows.Next() {
		var fid int64
		var ftext string
		if err = rows.Scan(&fid, &ftext); err != nil {
			rows.Close()
			return 0, false, err
		}
		if strings.EqualFold(ftext, text) {
			rows.Close()
			_ = tx.Rollback()
			return fid, true, nil
		}
		count++
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, false, err
	}
	rows.Close()
	if count >= MaxFacts {
		_ = tx.Rollback()
		return 0, false, ErrTooManyFacts
	}
	now := s.Now()
	res, err := tx.ExecContext(ctx, `INSERT INTO user_facts (user_id, fact, created_at, updated_at) VALUES (?, ?, ?, ?)`, userID, text, now, now)
	if err != nil {
		return 0, false, err
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, false, err
	}
	if err = tx.Commit(); err != nil {
		return 0, false, err
	}
	return id, false, nil
}

func (s *Store) UpdateFact(ctx context.Context, userID, id int64, text string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE user_facts SET fact = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		truncate(strings.TrimSpace(text), MaxFactChars), s.Now(), id, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) DeleteFact(ctx context.Context, userID, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM user_facts WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) ClearFacts(ctx context.Context, userID int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM user_facts WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const PrefFactsOff = "facts_off"

const PrefMode = "mode"

func (s *Store) UserPref(ctx context.Context, userID int64, name string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM user_prefs WHERE user_id = ? AND name = ?`, userID, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetUserPref(ctx context.Context, userID int64, name, value string) error {
	if value == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM user_prefs WHERE user_id = ? AND name = ?`, userID, name)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_prefs (user_id, name, value) VALUES (?, ?, ?)
		ON CONFLICT DO UPDATE SET value = excluded.value`, userID, truncate(name, 32), truncate(value, 255))
	return err
}

const PrefContextMeter = "ctx_meter"
