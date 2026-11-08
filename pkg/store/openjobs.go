package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
	"unicode/utf8"
)

// Open jobs: the request each user has running, and the last one that
// stopped before it was done. A running job is written when it starts and
// as it works, so a restart of the bot, an error or a stop never loses it:
// the job moves to "unfinished", and «ادامه بده» (or ▶️) goes on from its
// exact request, plan and steps instead of from an older message.
const (
	JobRunning    = "running"
	JobUnfinished = "unfinished"
)

// Why an unfinished job stopped.
const (
	JobStopped   = "stopped"   // the user stopped it
	JobFailed    = "failed"    // an error (the model, a provider…)
	JobTimeout   = "timeout"   // its time ran out
	JobBudget    = "budget"    // its steps ran out
	JobRestarted = "restarted" // the bot restarted while it ran
)

// maxJobNotes bounds what a job keeps of its steps (the end is kept).
const maxJobNotes = 12000

// JobForgetAfter: an unfinished job is forgotten once this many other
// requests got done after it (the conversation has moved on).
const JobForgetAfter = 3

// OpenJob is a running or unfinished job.
type OpenJob struct {
	UserID, ChatID, MsgID int64
	Kind                  string
	Request               string // what the user asked, as remembered
	Notes                 string // the plan and the steps taken so far
	Reason                string // why an unfinished job stopped
	Later                 int    // requests done since it stopped
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

// BeginJob records the job a user starts (replacing the one that ran before).
func (s *Store) BeginJob(ctx context.Context, userID, chatID, msgID int64, request, notes string) error {
	now := s.Now()
	_, err := s.db.ExecContext(ctx, `REPLACE INTO open_jobs (user_id, kind, chat_id, msg_id, request, notes, reason, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)`, userID, JobRunning, chatID, msgID, request, clipNotes(notes), now, now)
	return err
}

// JobProgress keeps what a running job did so far.
func (s *Store) JobProgress(ctx context.Context, userID int64, notes string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE open_jobs SET notes = ?, updated_at = ? WHERE user_id = ? AND kind = ?`,
		clipNotes(notes), s.Now(), userID, JobRunning)
	return err
}

// FinishJob ends a running job that got done; continued: it was the
// unfinished one, which is done now too. Another request done counts against
// the unfinished job, forgotten after JobForgetAfter of them.
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

// DropJob forgets the running job (it ended with nothing to go on with).
func (s *Store) DropJob(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM open_jobs WHERE user_id = ? AND kind = ?`, userID, JobRunning)
	return err
}

// InterruptJob makes the running job the unfinished one, with why it
// stopped and its last notes ("" keeps the ones it has).
func (s *Store) InterruptJob(ctx context.Context, userID int64, reason, notes string) error {
	return s.interruptJob(ctx, userID, reason, notes, true)
}

// interruptJob: touch dates it now (a job found after a restart keeps the
// time it last ran).
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
		return tx.Commit() // nothing running (a scheduled task, a guest reply)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM open_jobs WHERE user_id = ? AND kind = ?`, userID, JobUnfinished); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE open_jobs SET kind = ? WHERE user_id = ? AND kind = ?`, JobUnfinished, userID, JobRunning); err != nil {
		return err
	}
	return tx.Commit()
}

// UnfinishedJob is the last job of a user that stopped before it was done (nil: none).
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

// OrphanedJobs makes every job still marked running unfinished (the bot
// restarted while they ran; nothing runs yet when it is called) and
// returns them.
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
