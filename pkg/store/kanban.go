package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	CardTodo    = "todo"
	CardDoing   = "doing"
	CardBlocked = "blocked"
	CardDone    = "done"
)

func ValidCardStatus(s string) bool {
	return s == CardTodo || s == CardDoing || s == CardBlocked || s == CardDone
}

type Card struct {
	ID        int64
	UserID    int64
	Title     string
	Notes     string
	Status    string
	BgTask    int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

const cardColumns = `id, user_id, title, notes, status, bg_task, created_at, updated_at`

func scanCard(row interface{ Scan(...any) error }) (*Card, error) {
	var c Card
	err := row.Scan(&c.ID, &c.UserID, &c.Title, &c.Notes, &c.Status, &c.BgTask, &c.CreatedAt, &c.UpdatedAt)
	return &c, err
}

func (s *Store) AddCard(ctx context.Context, userID int64, title, notes string) (int64, error) {
	now := s.Now()
	r, err := s.db.ExecContext(ctx, `INSERT INTO kanban (user_id, title, notes, status, created_at, updated_at) VALUES (?, ?, ?, 'todo', ?, ?)`,
		userID, truncate(title, 200), truncate(notes, 4000), now, now)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) Card(ctx context.Context, userID, id int64) (*Card, error) {
	c, err := scanCard(s.db.QueryRowContext(ctx, `SELECT `+cardColumns+` FROM kanban WHERE id = ? AND user_id = ?`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (s *Store) Cards(ctx context.Context, userID int64, doneDays int) ([]Card, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cardColumns+` FROM kanban WHERE user_id = ? AND (status <> 'done' OR updated_at >= ?)
		ORDER BY id`, userID, s.Now().AddDate(0, 0, -doneDays))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Card
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) MoveCard(ctx context.Context, userID, id int64, status, note string) error {
	if !ValidCardStatus(status) {
		return errors.New("unknown status " + status)
	}
	r, err := s.db.ExecContext(ctx, `UPDATE kanban SET status = ?, notes = TRIM(notes || CASE WHEN ? = '' THEN '' ELSE char(10) || ? END),
		updated_at = ? WHERE id = ? AND user_id = ?`, status, note, truncate(note, 2000), s.Now(), id, userID)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetCardTask(ctx context.Context, userID, id, task int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE kanban SET bg_task = ?, status = 'doing', updated_at = ? WHERE id = ? AND user_id = ?`, task, s.Now(), id, userID)
	return err
}

func (s *Store) DeleteCard(ctx context.Context, userID, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM kanban WHERE id = ? AND user_id = ?`, id, userID)
	return err
}
