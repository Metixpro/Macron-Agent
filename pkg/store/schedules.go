package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	ScheduleActive    = "active"
	ScheduleDone      = "done"
	ScheduleCancelled = "cancelled"
	SchedulePaused    = "paused"
)

const (
	ScheduleReminder = "reminder"
	ScheduleTask     = "task"
)

type Schedule struct {
	ID        int64
	UserID    int64
	ChatID    int64
	ChatType  string
	ThreadID  int64
	Kind      string
	Title     string
	Body      string
	Rule      string
	NextRun   time.Time
	LastRun   *time.Time
	Runs      int
	Failures  int
	Status    string
	LastError string
	CreatedAt time.Time
}

const scheduleColumns = `id, user_id, chat_id, chat_type, kind, title, body, rule, next_run, last_run, runs, failures, status, last_error, created_at, thread_id`

func scanSchedule(row interface{ Scan(...any) error }) (*Schedule, error) {
	var s Schedule
	err := row.Scan(&s.ID, &s.UserID, &s.ChatID, &s.ChatType, &s.Kind, &s.Title, &s.Body, &s.Rule, &s.NextRun, &s.LastRun,
		&s.Runs, &s.Failures, &s.Status, &s.LastError, &s.CreatedAt, &s.ThreadID)
	return &s, err
}

func (s *Store) AddSchedule(ctx context.Context, x Schedule) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO schedules (user_id, chat_id, chat_type, kind, title, body, rule, next_run, status, created_at, thread_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
		x.UserID, x.ChatID, truncate(x.ChatType, 16), truncate(x.Kind, 16), truncate(x.Title, 200), x.Body, truncate(x.Rule, 64),
		x.NextRun.UTC(), s.Now(), x.ThreadID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) Schedule(ctx context.Context, id int64) (*Schedule, error) {
	x, err := scanSchedule(s.db.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return x, err
}

func (s *Store) querySchedules(ctx context.Context, where string, args ...any) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		x, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

func (s *Store) UserSchedules(ctx context.Context, userID int64) ([]Schedule, error) {
	return s.querySchedules(ctx, `user_id = ? AND status IN ('active', 'paused') ORDER BY status = 'paused', next_run LIMIT 50`, userID)
}

func (s *Store) CountActiveSchedules(ctx context.Context, userID, chatID int64) (byUser, byChat int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(user_id = ?), 0), COALESCE(SUM(chat_id = ?), 0)
		FROM schedules WHERE status = 'active' AND (user_id = ? OR chat_id = ?)`, userID, chatID, userID, chatID).Scan(&byUser, &byChat)
	return
}

func (s *Store) DueSchedules(ctx context.Context, now time.Time, limit int) ([]Schedule, error) {
	return s.querySchedules(ctx, `status = 'active' AND next_run <= ? ORDER BY next_run LIMIT ?`, now.UTC(), limit)
}

func (s *Store) ClaimSchedule(ctx context.Context, x Schedule, next time.Time) (bool, error) {
	status, nextRun := ScheduleActive, next.UTC()
	if next.IsZero() {
		status, nextRun = ScheduleDone, x.NextRun.UTC()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE schedules SET next_run = ?, status = ?, last_run = ?, runs = runs + 1
		WHERE id = ? AND status = 'active' AND next_run = ?`, nextRun, status, s.Now(), x.ID, x.NextRun.UTC())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) RecordScheduleResult(ctx context.Context, id int64, errMsg string, maxFailures int) (paused bool, err error) {
	if errMsg == "" {
		_, err := s.db.ExecContext(ctx, `UPDATE schedules SET failures = 0, last_error = '' WHERE id = ?`, id)
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE schedules SET failures = failures + 1, last_error = ?,
		status = IIF(failures + 1 >= ? AND status = 'active', 'paused', status) WHERE id = ?`, truncate(errMsg, 300), maxFailures, id)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	var status string
	var failures int
	if err := s.db.QueryRowContext(ctx, `SELECT status, failures FROM schedules WHERE id = ?`, id).Scan(&status, &failures); err != nil {
		return false, err
	}
	return status == SchedulePaused && failures == maxFailures, nil
}

func (s *Store) RetrySchedule(ctx context.Context, id int64, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE schedules SET status = 'active', next_run = ? WHERE id = ? AND status = 'done'`, at.UTC(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) ResumeSchedule(ctx context.Context, id, userID int64, at time.Time) (bool, error) {
	q, args := `UPDATE schedules SET status = 'active', failures = 0, last_error = '', next_run = MAX(next_run, ?)
		WHERE id = ? AND status = 'paused'`, []any{at.UTC(), id}
	if userID != 0 {
		q += ` AND user_id = ?`
		args = append(args, userID)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) CancelSchedule(ctx context.Context, id, userID int64) (bool, error) {
	q, args := `UPDATE schedules SET status = 'cancelled' WHERE id = ? AND status IN ('active', 'paused', 'done')`, []any{id}
	if userID != 0 {
		q = `UPDATE schedules SET status = 'cancelled' WHERE id = ? AND status IN ('active', 'paused') AND user_id = ?`
		args = append(args, userID)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) ScheduleCounts(ctx context.Context) (reminders, tasks int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(kind = 'reminder'), 0), COALESCE(SUM(kind = 'task'), 0)
		FROM schedules WHERE status = 'active'`).Scan(&reminders, &tasks)
	return
}
