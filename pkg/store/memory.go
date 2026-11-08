package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ContextMessage is one stored turn of a user's conversation.
type ContextMessage struct {
	ID        int64
	Role      string // user | assistant
	Content   string
	Tokens    int
	CreatedAt time.Time
}

// Memory is the long-term state of a conversation.
type Memory struct {
	Summary       string
	SummaryTokens int
	Compressions  int
	Messages      []ContextMessage
}

// Tokens is the estimated size of everything that is sent to the model.
func (m *Memory) Tokens() int {
	n := m.SummaryTokens
	for _, x := range m.Messages {
		n += x.Tokens
	}
	return n
}

// AppendContext stores turns of a conversation in order.
func (s *Store) AppendContext(ctx context.Context, userID int64, msgs ...ContextMessage) error {
	if len(msgs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.Now()
	for _, m := range msgs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO context_messages (user_id, role, content, tokens, created_at) VALUES (?, ?, ?, ?, ?)`,
			userID, m.Role, m.Content, m.Tokens, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// maxLoadedTurns bounds what one load reads: the memory is compacted long
// before it, so only a memory that grew unchecked loses its oldest turns
// (compaction then removes them with the rest).
const maxLoadedTurns = 1000

// LoadMemory returns the summary and the stored turns (the newest
// maxLoadedTurns), oldest first.
func (s *Store) LoadMemory(ctx context.Context, userID int64) (*Memory, error) {
	m := &Memory{}
	err := s.db.QueryRowContext(ctx, `SELECT summary, summary_tokens, compressions FROM context_memory WHERE user_id = ?`, userID).
		Scan(&m.Summary, &m.SummaryTokens, &m.Compressions)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, role, content, tokens, created_at FROM (
		SELECT id, role, content, tokens, created_at FROM context_messages WHERE user_id = ? ORDER BY id DESC LIMIT ?) newest ORDER BY id`,
		userID, maxLoadedTurns)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c ContextMessage
		if err := rows.Scan(&c.ID, &c.Role, &c.Content, &c.Tokens, &c.CreatedAt); err != nil {
			return nil, err
		}
		m.Messages = append(m.Messages, c)
	}
	return m, rows.Err()
}

// Compact replaces all turns with id <= upToID by a new summary, atomically.
func (s *Store) Compact(ctx context.Context, userID, upToID int64, summary string, summaryTokens int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM context_messages WHERE user_id = ? AND id <= ?`, userID, upToID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO context_memory (user_id, summary, summary_tokens, compressions, updated_at) VALUES (?, ?, ?, 1, ?)
		ON CONFLICT DO UPDATE SET summary = excluded.summary, summary_tokens = excluded.summary_tokens, compressions = compressions + 1, updated_at = excluded.updated_at`,
		userID, summary, summaryTokens, s.Now()); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearMemory forgets the whole conversation of a user.
func (s *Store) ClearMemory(ctx context.Context, userID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM context_messages WHERE user_id = ?`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM context_memory WHERE user_id = ?`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM open_jobs WHERE user_id = ? AND kind = ?`, userID, JobUnfinished); err != nil {
		return err // a forgotten conversation has no job to go on with
	}
	return tx.Commit()
}
