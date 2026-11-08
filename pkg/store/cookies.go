package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

type DLCookie struct {
	ID        int64
	Label     string
	Domains   []string
	Data      string
	SourceURL string
	FetchedAt *time.Time
	RestUntil *time.Time
	LastUsed  *time.Time
	Uses      int
	Blocks    int
	LastError string
	CreatedAt time.Time
}

func (c DLCookie) Resting(now time.Time) bool { return c.RestUntil != nil && c.RestUntil.After(now) }

func (s *Store) AddCookie(ctx context.Context, label string, domains []string, data string) (int64, error) {
	return s.AddCookieFrom(ctx, label, domains, data, "")
}

func (s *Store) AddCookieFrom(ctx context.Context, label string, domains []string, data, sourceURL string) (int64, error) {
	var fetched any
	if sourceURL != "" {
		fetched = s.Now()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO dl_cookies (label, domains, data, source_url, fetched_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		truncate(label, 64), truncate(strings.Join(domains, ","), 500), s.seal(data), truncate(sourceURL, 500), fetched, s.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) RefreshCookie(ctx context.Context, id int64, domains []string, data string, changed bool) error {
	if !changed {
		_, err := s.db.ExecContext(ctx, `UPDATE dl_cookies SET fetched_at = ? WHERE id = ?`, s.Now(), id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE dl_cookies SET data = ?, domains = ?, fetched_at = ?, rest_until = NULL, last_error = '' WHERE id = ?`,
		s.seal(data), truncate(strings.Join(domains, ","), 500), s.Now(), id)
	return err
}

func (s *Store) Cookies(ctx context.Context) ([]DLCookie, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, label, domains, data, source_url, fetched_at, rest_until, last_used, uses, blocks, last_error, created_at
		FROM dl_cookies ORDER BY COALESCE(last_used, '1970-01-01'), id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DLCookie
	for rows.Next() {
		var (
			c       DLCookie
			domains string
		)
		if err := rows.Scan(&c.ID, &c.Label, &domains, &c.Data, &c.SourceURL, &c.FetchedAt, &c.RestUntil, &c.LastUsed, &c.Uses, &c.Blocks, &c.LastError, &c.CreatedAt); err != nil {
			return nil, err
		}
		if c.Data, err = s.open(c.Data); err != nil {
			continue
		}
		if domains != "" {
			c.Domains = strings.Split(domains, ",")
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Cookie(ctx context.Context, id int64) (DLCookie, error) {
	var (
		c       DLCookie
		domains string
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, label, domains, data, source_url, fetched_at, rest_until, last_used, uses, blocks, last_error, created_at
		FROM dl_cookies WHERE id = ?`, id).Scan(&c.ID, &c.Label, &domains, &c.Data, &c.SourceURL, &c.FetchedAt, &c.RestUntil, &c.LastUsed,
		&c.Uses, &c.Blocks, &c.LastError, &c.CreatedAt)
	if err != nil {
		return DLCookie{}, err
	}
	if c.Data, err = s.open(c.Data); err != nil {
		return DLCookie{}, err
	}
	if domains != "" {
		c.Domains = strings.Split(domains, ",")
	}
	return c, nil
}

func (s *Store) DeleteCookie(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM dl_cookies WHERE id = ?`, id)
	return err
}

func (s *Store) RestCookie(ctx context.Context, id int64, until time.Time, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dl_cookies SET rest_until = ?, blocks = blocks + 1, last_error = ? WHERE id = ?`,
		until.UTC(), truncate(reason, 300), id)
	return err
}

func (s *Store) CookieUsed(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dl_cookies SET last_used = ?, uses = uses + 1 WHERE id = ?`, s.Now(), id)
	return err
}

const MaxUserCookies = 5

func (s *Store) AddUserCookie(ctx context.Context, userID int64, label string, domains []string, data string, replace []int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range replace {
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_cookies WHERE id = ? AND user_id = ?`, id, userID); err != nil {
			return 0, err
		}
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_cookies WHERE user_id = ?`, userID).Scan(&n); err != nil {
		return 0, err
	}
	if n >= MaxUserCookies {
		return 0, ErrTooManyCookies
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO user_cookies (user_id, label, domains, data, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, truncate(label, 64), truncate(strings.Join(domains, ","), 500), s.seal(data), s.Now())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

var ErrTooManyCookies = errors.New("too many cookies")

func (s *Store) UserCookies(ctx context.Context, userID int64) ([]DLCookie, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, label, domains, data, created_at FROM user_cookies WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DLCookie
	for rows.Next() {
		var (
			c       DLCookie
			domains string
		)
		if err := rows.Scan(&c.ID, &c.Label, &domains, &c.Data, &c.CreatedAt); err != nil {
			return nil, err
		}
		if c.Data, err = s.open(c.Data); err != nil {
			continue
		}
		if domains != "" {
			c.Domains = strings.Split(domains, ",")
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteUserCookie(ctx context.Context, userID, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM user_cookies WHERE id = ? AND user_id = ?`, id, userID)
	return err
}
