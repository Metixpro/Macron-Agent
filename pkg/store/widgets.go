package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Widget struct {
	ID         int64
	UserID     int64
	ChatID     int64
	MessageID  int64
	SharedID   string
	Title      string
	File       string
	Handler    string
	Session    string
	PublicData string
	ChatData   string
	CreatedAt  time.Time
	UsedAt     *time.Time
}

const widgetColumns = `id, user_id, chat_id, message_id, shared_id, title, file, handler, session, public_data, chat_data, created_at, used_at`

func scanWidget(row interface{ Scan(...any) error }) (*Widget, error) {
	var w Widget
	var used sql.NullTime
	err := row.Scan(&w.ID, &w.UserID, &w.ChatID, &w.MessageID, &w.SharedID, &w.Title, &w.File, &w.Handler, &w.Session,
		&w.PublicData, &w.ChatData, &w.CreatedAt, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	w.UsedAt = nullTime(used)
	return &w, err
}

func (s *Store) AddWidget(ctx context.Context, w Widget) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO widgets (user_id, chat_id, message_id, shared_id, title, file, handler, session,
		public_data, chat_data, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, w.UserID, w.ChatID, w.MessageID, w.SharedID,
		truncate(w.Title, 80), w.File, w.Handler, w.Session, w.PublicData, w.ChatData, s.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) WidgetByID(ctx context.Context, userID, id int64) (*Widget, error) {
	return scanWidget(s.db.QueryRowContext(ctx, `SELECT `+widgetColumns+` FROM widgets WHERE id = ? AND user_id = ?`, id, userID))
}

func (s *Store) WidgetByMessage(ctx context.Context, chatID, messageID int64) (*Widget, error) {
	return scanWidget(s.db.QueryRowContext(ctx, `SELECT `+widgetColumns+` FROM widgets WHERE chat_id = ? AND message_id = ?
		ORDER BY id DESC LIMIT 1`, chatID, messageID))
}

func (s *Store) WidgetByShared(ctx context.Context, sharedID string) (*Widget, error) {
	return scanWidget(s.db.QueryRowContext(ctx, `SELECT `+widgetColumns+` FROM widgets WHERE shared_id = ? AND shared_id <> ''
		ORDER BY id LIMIT 1`, sharedID))
}

func (s *Store) SetWidgetData(ctx context.Context, id int64, publicData, chatData *string) error {
	if publicData != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE widgets SET public_data = ? WHERE id = ?`, *publicData, id); err != nil {
			return err
		}
	}
	if chatData != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE widgets SET chat_data = ? WHERE id = ?`, *chatData, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) TouchWidget(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE widgets SET used_at = ? WHERE id = ?`, s.Now(), id)
	return err
}
