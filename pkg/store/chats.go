package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type BotChat struct {
	ChatID      int64
	Type        string
	Title       string
	Username    string
	Status      string
	Members     int
	AddedBy     int64
	AddedByName string
	JoinedAt    *time.Time
	LeftAt      *time.Time
	UpdatedAt   time.Time
}

func (c BotChat) Active() bool { return c.Status != "left" && c.Status != "kicked" }

func (s *Store) SaveChatJoin(ctx context.Context, c BotChat) error {
	now := s.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO bot_chats
		(chat_id, type, title, username, status, members, added_by, added_by_name, joined_at, left_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)
		ON CONFLICT DO UPDATE SET
			added_by_name = IIF(status IN ('left', 'kicked') OR added_by = 0, excluded.added_by_name, added_by_name),
			added_by = IIF(status IN ('left', 'kicked') OR added_by = 0, excluded.added_by, added_by),
			joined_at = IIF(status IN ('left', 'kicked') OR joined_at IS NULL, excluded.joined_at, joined_at),
			members = IIF(excluded.members > 0, excluded.members, members),
			type = excluded.type, title = excluded.title, username = excluded.username, left_at = NULL, updated_at = excluded.updated_at,
			status = excluded.status`,
		c.ChatID, truncate(c.Type, 16), truncate(c.Title, 255), truncate(c.Username, 64), truncate(c.Status, 16), c.Members,
		c.AddedBy, truncate(c.AddedByName, 255), now, now)
	return err
}

func (s *Store) SaveChatLeave(ctx context.Context, chatID int64, status, title string) error {
	now := s.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO bot_chats (chat_id, type, title, status, left_at, updated_at) VALUES (?, '', ?, ?, ?, ?)
		ON CONFLICT DO UPDATE SET status = excluded.status, title = IIF(excluded.title = '', title, excluded.title),
			left_at = excluded.left_at, updated_at = excluded.updated_at`,
		chatID, truncate(title, 255), truncate(status, 16), now, now)
	return err
}

func (s *Store) TouchChat(ctx context.Context, chatID int64, typ, title, username string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO bot_chats (chat_id, type, title, username, status, updated_at) VALUES (?, ?, ?, ?, 'member', ?)
		ON CONFLICT DO UPDATE SET type = excluded.type, title = excluded.title, username = excluded.username,
			status = IIF(status IN ('left', 'kicked'), 'member', status), updated_at = excluded.updated_at`,
		chatID, truncate(typ, 16), truncate(title, 255), truncate(username, 64), s.Now())
	return err
}

func (s *Store) Chats(ctx context.Context, limit int) ([]BotChat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT chat_id, type, title, username, status, members, added_by, added_by_name, joined_at, left_at, updated_at
		FROM bot_chats ORDER BY (status IN ('left', 'kicked')), updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BotChat
	for rows.Next() {
		var c BotChat
		if err := rows.Scan(&c.ChatID, &c.Type, &c.Title, &c.Username, &c.Status, &c.Members, &c.AddedBy, &c.AddedByName,
			&c.JoinedAt, &c.LeftAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Chat(ctx context.Context, chatID int64) (*BotChat, error) {
	var c BotChat
	err := s.db.QueryRowContext(ctx, `SELECT chat_id, type, title, username, status, members, added_by, added_by_name, joined_at, left_at, updated_at
		FROM bot_chats WHERE chat_id = ?`, chatID).Scan(&c.ChatID, &c.Type, &c.Title, &c.Username, &c.Status, &c.Members,
		&c.AddedBy, &c.AddedByName, &c.JoinedAt, &c.LeftAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) ChatCounts(ctx context.Context) (active, left, members int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(status NOT IN ('left', 'kicked')), 0),
		COALESCE(SUM(status IN ('left', 'kicked')), 0),
		COALESCE(SUM(IIF(status NOT IN ('left', 'kicked'), members, 0)), 0)
		FROM bot_chats`).Scan(&active, &left, &members)
	return
}
