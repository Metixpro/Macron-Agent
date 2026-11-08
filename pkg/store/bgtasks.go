package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Background tasks: long jobs (a build, a render, a big download) the agent
// starts on their own while the user goes on chatting; the result arrives as
// a message of its own. Kept here so /tasks lists them and a restart of the
// bot never loses one without telling its user.
const (
	BgQueued      = "queued"
	BgRunning     = "running"
	BgDone        = "done"
	BgFailed      = "failed"
	BgStopped     = "stopped"
	BgInterrupted = "interrupted" // the bot restarted while it ran
)

// BgTask is one background task.
type BgTask struct {
	ID, UserID, ChatID, ThreadID, MsgID int64
	ChatType                            string
	Title, Goal                         string
	Status                              string
	Note                                string // how it ended, in a line
	CreatedAt                           time.Time
	StartedAt, FinishedAt               *time.Time
}

// Active reports a task that has not ended.
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

// AddBgTask records a task that waits for its turn and returns its id.
func (s *Store) AddBgTask(ctx context.Context, t BgTask) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO bg_tasks (user_id, chat_id, chat_type, thread_id, msg_id, title, goal, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, t.UserID, t.ChatID, t.ChatType, t.ThreadID, t.MsgID, truncate(t.Title, 200), t.Goal, BgQueued, s.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// StartBgTask marks a task running.
func (s *Store) StartBgTask(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE bg_tasks SET status = ?, started_at = ? WHERE id = ? AND status = ?`, BgRunning, s.Now(), id, BgQueued)
	return err
}

// FinishBgTask records how a task ended.
func (s *Store) FinishBgTask(ctx context.Context, id int64, status, note string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE bg_tasks SET status = ?, note = ?, finished_at = ? WHERE id = ?`, status, truncate(note, 500), s.Now(), id)
	return err
}

// BgTaskByID is one task (nil: none).
func (s *Store) BgTaskByID(ctx context.Context, id int64) (*BgTask, error) {
	t, err := scanBgTask(s.db.QueryRowContext(ctx, `SELECT `+bgColumns+` FROM bg_tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// UserBgTasks are a user's latest tasks, newest first.
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

// ActiveBgTasks counts a user's tasks that have not ended.
func (s *Store) ActiveBgTasks(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bg_tasks WHERE user_id = ? AND status IN (?, ?)`, userID, BgQueued, BgRunning).Scan(&n)
	return n, err
}

// OrphanedBgTasks marks every task the last run of the bot left unfinished
// as interrupted and returns them (nothing runs yet when it is called).
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
