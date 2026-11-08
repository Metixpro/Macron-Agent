package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	BgQueued      = "queued"
	BgRunning     = "running"
	BgDone        = "done"
	BgFailed      = "failed"
	BgStopped     = "stopped"
	BgInterrupted = "interrupted"
)

type BgTask struct {
	ID, UserID, ChatID, ThreadID, MsgID int64
	ChatType                            string
	Title, Goal                         string
	Status                              string
	Note                                string
	CreatedAt                           time.Time
	StartedAt, FinishedAt               *time.Time
}

func (t *BgTask) Active() bool { return t.Status == BgQueued || t.Status == BgRunning }

const bgColumns = `id, user_id, chat_id, chat_type, thread_id, msg_id, title, goal, status, note, created_at, started_at, finished_at`

func scanBgTask(row interface{ Scan(...any) error }) (*BgTask, error) {
	var t BgTask
	var started, finished sql.NullTime
	if err := row.Scan(&t.ID, &t.UserID, &t.ChatID, &t.ChatType, &t.ThreadID, &t.MsgID, &t.Title, &t.Goal, &t.Status, &t.Note,
		&t.CreatedAt, &started, &finished); err != nil {
		return nil, err
	}
	if started.Valid {
		t.StartedAt = &started.Time
	}
	if finished.Valid {
		t.FinishedAt = &finished.Time
	}
	return &t, nil
}

func (s *Store) AddBgTask(ctx context.Context, t BgTask) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO bg_tasks (user_id, chat_id, chat_type, thread_id, msg_id, title, goal, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, t.UserID, t.ChatID, t.ChatType, t.ThreadID, t.MsgID, truncate(t.Title, 200), t.Goal, BgQueued, s.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) StartBgTask(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE bg_tasks SET status = ?, started_at = ? WHERE id = ? AND status = ?`, BgRunning, s.Now(), id, BgQueued)
	return err
}

func (s *Store) FinishBgTask(ctx context.Context, id int64, status, note string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE bg_tasks SET status = ?, note = ?, finished_at = ? WHERE id = ?`, status, truncate(note, 500), s.Now(), id)
	return err
}

func (s *Store) BgTaskByID(ctx context.Context, id int64) (*BgTask, error) {
	t, err := scanBgTask(s.db.QueryRowContext(ctx, `SELECT `+bgColumns+` FROM bg_tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func (s *Store) UserBgTasks(ctx context.Context, userID int64, limit int) ([]BgTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+bgColumns+` FROM bg_tasks WHERE user_id = ? ORDER BY id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BgTask
	for rows.Next() {
		t, err := scanBgTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *Store) ActiveBgTasks(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bg_tasks WHERE user_id = ? AND status IN (?, ?)`, userID, BgQueued, BgRunning).Scan(&n)
	return n, err
}

func (s *Store) OrphanedBgTasks(ctx context.Context) ([]BgTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+bgColumns+` FROM bg_tasks WHERE status IN (?, ?)`, BgQueued, BgRunning)
	if err != nil {
		return nil, err
	}
	var out []BgTask
	for rows.Next() {
		t, err := scanBgTask(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, t := range out {
		if err := s.FinishBgTask(ctx, t.ID, BgInterrupted, "the bot restarted while it ran"); err != nil {
			return out, err
		}
	}
	return out, nil
}
