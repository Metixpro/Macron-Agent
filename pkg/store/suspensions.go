package store

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Store) SaveSuspension(ctx context.Context, pollID string, data []byte) error {
	_, err := s.db.ExecContext(ctx, `REPLACE INTO suspensions (poll_id, data, created_at) VALUES (?, ?, ?)`,
		truncate(pollID, 64), data, s.Now())
	return err
}

func (s *Store) Suspension(ctx context.Context, pollID string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM suspensions WHERE poll_id = ?`, pollID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return data, err
}

func (s *Store) DropSuspension(ctx context.Context, pollID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM suspensions WHERE poll_id = ?`, pollID)
	return err
}
