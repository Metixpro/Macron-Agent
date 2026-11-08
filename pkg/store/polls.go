package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type Poll struct {
	PollID     string
	UserID     int64
	ChatID     int64
	ChatType   string
	MessageID  int64
	Question   string
	Options    []string
	Multiple   bool
	CreatedAt  time.Time
	AnsweredAt *time.Time
}

func (s *Store) SavePoll(ctx context.Context, p Poll) error {
	opts, _ := json.Marshal(p.Options)
	_, err := s.db.ExecContext(ctx, `INSERT INTO polls (poll_id, user_id, chat_id, chat_type, message_id, question, options, multiple, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		truncate(p.PollID, 64), p.UserID, p.ChatID, truncate(p.ChatType, 16), p.MessageID, truncate(p.Question, 400), string(opts), p.Multiple, s.Now())
	return err
}

func (s *Store) Poll(ctx context.Context, pollID string) (*Poll, error) {
	var (
		p    Poll
		opts string
	)
	err := s.db.QueryRowContext(ctx, `SELECT poll_id, user_id, chat_id, chat_type, message_id, question, options, multiple, created_at, answered_at
		FROM polls WHERE poll_id = ?`, pollID).Scan(&p.PollID, &p.UserID, &p.ChatID, &p.ChatType, &p.MessageID, &p.Question, &opts,
		&p.Multiple, &p.CreatedAt, &p.AnsweredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(opts), &p.Options)
	return &p, nil
}

func (s *Store) MarkPollAnswered(ctx context.Context, pollID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE polls SET answered_at = ? WHERE poll_id = ? AND answered_at IS NULL`, s.Now(), pollID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) DeleteOldPolls(ctx context.Context, age time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM polls WHERE created_at < ?`, s.Now().Add(-age))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
