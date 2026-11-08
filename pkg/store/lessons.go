package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Lessons are what the bot drew from its own jobs (a step failed, another
// way worked): general rules, without anything about the user. They wait
// for an admin; only approved ones reach the agent, as skills. The same
// lesson again (the same key) counts up instead of making another.

// Lesson statuses.
const (
	LessonPending  = "pending"
	LessonApproved = "approved"
	LessonRejected = "rejected"
)

// Lesson is one lesson.
type Lesson struct {
	ID        int64
	Key       string // category/short-problem-name: the same lesson has the same key
	Category  string
	Title     string
	Rule      string
	Tools     string // the tools it is about, comma separated
	Status    string
	Seen      int // how many jobs taught it
	CreatedAt time.Time
	UpdatedAt time.Time
	DecidedAt *time.Time
}

const lessonColumns = `id, lesson_key, category, title, rule, tools, status, seen, created_at, updated_at, decided_at`

func scanLesson(row interface{ Scan(...any) error }) (*Lesson, error) {
	var (
		x       Lesson
		decided sql.NullTime
	)
	if err := row.Scan(&x.ID, &x.Key, &x.Category, &x.Title, &x.Rule, &x.Tools, &x.Status, &x.Seen, &x.CreatedAt, &x.UpdatedAt, &decided); err != nil {
		return nil, err
	}
	x.DecidedAt = nullTime(decided)
	return &x, nil
}

// AddLesson records a lesson. One with the same key counts up (and keeps
// its status: a rejected one stays rejected, so it is never asked again).
// fresh: it is new; status: where it stands now.
func (s *Store) AddLesson(ctx context.Context, x Lesson) (id int64, fresh bool, status string, err error) {
	now := s.Now()
	var seen int64
	existed := s.db.QueryRowContext(ctx, `SELECT id FROM lessons WHERE lesson_key = ?`, truncate(x.Key, 120)).Scan(&seen) == nil
	_, err = s.db.ExecContext(ctx, `INSERT INTO lessons (lesson_key, category, title, rule, tools, status, seen, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'pending', 1, ?, ?)
		ON CONFLICT DO UPDATE SET seen = seen + 1, updated_at = excluded.updated_at,
			rule = IIF(status = 'pending', excluded.rule, rule), title = IIF(status = 'pending', excluded.title, title)`,
		truncate(x.Key, 120), truncate(x.Category, 32), truncate(x.Title, 200), truncate(x.Rule, 4000), truncate(x.Tools, 300), now, now)
	if err != nil {
		return 0, false, "", err
	}
	err = s.db.QueryRowContext(ctx, `SELECT id, status FROM lessons WHERE lesson_key = ?`, truncate(x.Key, 120)).Scan(&id, &status)
	return id, !existed, status, err
}

// Lesson returns one lesson.
func (s *Store) Lesson(ctx context.Context, id int64) (*Lesson, error) {
	x, err := scanLesson(s.db.QueryRowContext(ctx, `SELECT `+lessonColumns+` FROM lessons WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return x, err
}

// Lessons lists the lessons in a status, the most taught first ("" = all).
func (s *Store) Lessons(ctx context.Context, status, category string, limit int) ([]Lesson, error) {
	q, args := `SELECT `+lessonColumns+` FROM lessons WHERE 1 = 1`, []any{}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	if category != "" {
		q += ` AND category = ?`
		args = append(args, category)
	}
	q += ` ORDER BY seen DESC, updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Lesson
	for rows.Next() {
		x, err := scanLesson(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// LessonKeys are the keys of the lessons (of a category; "" = all) that
// are not rejected, so the same lesson is told under the same key.
func (s *Store) LessonKeys(ctx context.Context, category string, limit int) (map[string]string, error) {
	q, args := `SELECT lesson_key, title FROM lessons WHERE status <> 'rejected'`, []any{}
	if category != "" {
		q += ` AND category = ?`
		args = append(args, category)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY seen DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, t string
		if err := rows.Scan(&k, &t); err != nil {
			return nil, err
		}
		out[k] = t
	}
	return out, rows.Err()
}

// DecideLesson approves or rejects a lesson (or puts it back to pending).
func (s *Store) DecideLesson(ctx context.Context, id int64, status string) error {
	var decided any
	if status != LessonPending {
		decided = s.Now()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE lessons SET status = ?, decided_at = ? WHERE id = ?`, status, decided, id)
	return err
}

// EditLesson replaces a lesson's text (an admin's edit).
func (s *Store) EditLesson(ctx context.Context, id int64, title, rule string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lessons SET title = ?, rule = ?, updated_at = ? WHERE id = ?`,
		truncate(title, 200), truncate(rule, 4000), s.Now(), id)
	return err
}

// LessonCounts counts the lessons by status.
func (s *Store) LessonCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM lessons GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// MoveLesson puts a lesson in another category; its key follows.
func (s *Store) MoveLesson(ctx context.Context, id int64, category string) error {
	x, err := s.Lesson(ctx, id)
	if err != nil {
		return err
	}
	key := category + "/" + strings.TrimPrefix(x.Key, x.Category+"/")
	_, err = s.db.ExecContext(ctx, `UPDATE lessons SET category = ?, lesson_key = ?, updated_at = ? WHERE id = ?`,
		truncate(category, 32), truncate(key, 120), s.Now(), id)
	return err
}

// LessonLog actions.
const (
	LessonAdopted  = "adopt"  // a lesson went into its skill
	LessonReverted = "undo"   // it was taken back, the skill as it was before
	LessonReviewed = "review" // the background review merged or flagged
)

// LessonLog is one thing learning did to the skills. Before and After are
// the skill as it was and as it became (JSON of its description and body;
// "" when there was none), so taking a lesson back restores it exactly.
type LessonLog struct {
	ID        int64
	LessonID  int64
	Category  string
	Action    string
	Title     string
	Before    string
	After     string
	Note      string
	CreatedAt time.Time
}

const lessonLogColumns = `id, lesson_id, category, action, title, before, after, note, created_at`

func scanLessonLog(row interface{ Scan(...any) error }) (*LessonLog, error) {
	var x LessonLog
	err := row.Scan(&x.ID, &x.LessonID, &x.Category, &x.Action, &x.Title, &x.Before, &x.After, &x.Note, &x.CreatedAt)
	return &x, err
}

// AddLessonLog records what learning did.
func (s *Store) AddLessonLog(ctx context.Context, x LessonLog) (int64, error) {
	r, err := s.db.ExecContext(ctx, `INSERT INTO lesson_log (lesson_id, category, action, title, before, after, note, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, x.LessonID, truncate(x.Category, 32), x.Action, truncate(x.Title, 200), x.Before, x.After,
		truncate(x.Note, 2000), s.Now())
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// LessonLogs are the latest things learning did, newest first ("" = every action).
func (s *Store) LessonLogs(ctx context.Context, action string, limit int) ([]LessonLog, error) {
	q, args := `SELECT `+lessonLogColumns+` FROM lesson_log`, []any{}
	if action != "" {
		q += ` WHERE action = ?`
		args = append(args, action)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LessonLog
	for rows.Next() {
		x, err := scanLessonLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// LastAdoption is the latest time a lesson went into its skill (ErrNotFound: never).
func (s *Store) LastAdoption(ctx context.Context, lessonID int64) (*LessonLog, error) {
	x, err := scanLessonLog(s.db.QueryRowContext(ctx, `SELECT `+lessonLogColumns+` FROM lesson_log WHERE lesson_id = ? AND action = ?
		ORDER BY id DESC LIMIT 1`, lessonID, LessonAdopted))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return x, err
}

// MergeLesson folds a duplicate into the lesson kept: its count joins the
// kept one, and it is rejected so it is never proposed again.
func (s *Store) MergeLesson(ctx context.Context, keep, drop int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE lessons SET seen = seen + (SELECT seen FROM lessons WHERE id = ?), updated_at = ? WHERE id = ?`,
		drop, s.Now(), keep); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE lessons SET status = 'rejected', decided_at = ? WHERE id = ?`, s.Now(), drop); err != nil {
		return err
	}
	return tx.Commit()
}
