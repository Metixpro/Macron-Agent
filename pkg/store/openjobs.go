package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
	"unicode/utf8"
)

const (
	JobRunning    = "running"
	JobUnfinished = "unfinished"
)

const (
	JobStopped   = "stopped"
	JobFailed    = "failed"
	JobTimeout   = "timeout"
	JobBudget    = "budget"
	JobRestarted = "restarted"
)

const maxJobNotes = 12000

const JobForgetAfter = 3

type OpenJob struct {
	UserID, ChatID, MsgID int64
	Kind                  string
	Request               string
	Notes                 string
	Reason                string
	Later                 int
	StartedAt, UpdatedAt  time.Time
}

func clipNotes(s string) string {
	if len(s) <= maxJobNotes {
		return s
	}
	s = s[len(s)-maxJobNotes:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return "…" + s
}

func (s *Store) BeginJob(ctx context.Context, userID, chatID, msgID int64, request, notes string) error {
	now := s.Now()
	_, err := s.db.ExecContext(ctx, `REPLACE INTO open_jobs (user_id, kind, chat_id, msg_id, request, notes, reason, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)`, userID, JobRunning, chatID, msgID, request, clipNotes(notes), now, now)
	return err
}

func (s *Store) JobProgress(ctx context.Context, userID int64, notes string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE open_jobs SET notes = ?, updated_at = ? WHERE user_id = ? AND kind = ?`,
		clipNotes(notes), s.Now(), userID, JobRunning)
	return err
}

func (s *Store) FinishJob(ctx context.Context, userID int64, continued bool) error {
	if continued {
		_, err := s.db.ExecContext(ctx, `DELETE FROM open_jobs WHERE user_id = ?`, userID)
		return err
	}
	if err := s.DropJob(ctx, userID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE open_jobs SET later = later + 1 WHERE user_id = ? AND kind = ?`, userID, JobUnfinished); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM open_jobs WHERE user_id = ? AND kind = ? AND later >= ?`, userID, JobUnfinished, JobForgetAfter)
	return err
}

func (s *Store) DropJob(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM open_jobs WHERE user_id = ? AND kind = ?`, userID, JobRunning)
	return err
}

func (s *Store) InterruptJob(ctx context.Context, userID int64, reason, notes string) error {
	return s.interruptJob(ctx, userID, reason, notes, true)
}

func (s *Store) interruptJob(ctx context.Context, userID int64, reason, notes string, touch bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if notes != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE open_jobs SET notes = ? WHERE user_id = ? AND kind = ?`, clipNotes(notes), userID, JobRunning); err != nil {
			return err
		}
	}
	q, args := `UPDATE open_jobs SET reason = ?, later = 0 WHERE user_id = ? AND kind = ?`, []any{reason, userID, JobRunning}
	if touch {
		q, args = `UPDATE open_jobs SET reason = ?, later = 0, updated_at = ? WHERE user_id = ? AND kind = ?`, []any{reason, s.Now(), userID, JobRunning}
	}
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM open_jobs WHERE user_id = ? AND kind = ?`, userID, JobUnfinished); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE open_jobs SET kind = ? WHERE user_id = ? AND kind = ?`, JobUnfinished, userID, JobRunning); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UnfinishedJob(ctx context.Context, userID int64) (*OpenJob, error) {
	j := OpenJob{UserID: userID, Kind: JobUnfinished}
	err := s.db.QueryRowContext(ctx, `SELECT chat_id, msg_id, request, notes, reason, later, started_at, updated_at FROM open_jobs
		WHERE user_id = ? AND kind = ?`, userID, JobUnfinished).Scan(&j.ChatID, &j.MsgID, &j.Request, &j.Notes, &j.Reason, &j.Later, &j.StartedAt, &j.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *Store) OrphanedJobs(ctx context.Context) ([]OpenJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, chat_id, msg_id, request, notes, started_at, updated_at FROM open_jobs WHERE kind = ?`, JobRunning)
	if err != nil {
		return nil, err
	}
	var out []OpenJob
	for rows.Next() {
		j := OpenJob{Kind: JobUnfinished, Reason: JobRestarted}
		if err := rows.Scan(&j.UserID, &j.ChatID, &j.MsgID, &j.Request, &j.Notes, &j.StartedAt, &j.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, j := range out {
		if err := s.interruptJob(ctx, j.UserID, JobRestarted, "", false); err != nil {
			return out, err
		}
	}
	return out, nil
}
