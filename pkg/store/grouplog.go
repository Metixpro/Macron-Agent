package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	ChatPrefLogHours  = "log_hours"
	chatPrefOptOutPfx = "optout:"
	MaxGroupLogHours  = 7 * 24
	maxGroupText      = 2000
	maxGroupMessages  = 20000
	groupLogBatch     = 5000
)

type GroupMessage struct {
	ChatID    int64
	MessageID int64
	UserID    int64
	Name      string
	Username  string
	ReplyTo   int64
	Text      string
	CreatedAt time.Time
}

func (s *Store) ChatPref(ctx context.Context, chatID int64, name string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM chat_prefs WHERE chat_id = ? AND name = ?`, chatID, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetChatPref(ctx context.Context, chatID int64, name, value string) error {
	if value == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM chat_prefs WHERE chat_id = ? AND name = ?`, chatID, name)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO chat_prefs (chat_id, name, value) VALUES (?, ?, ?) ON CONFLICT DO UPDATE SET value = excluded.value`,
		chatID, truncate(name, 32), truncate(value, 255))
	return err
}

func (s *Store) GroupLogSettings(ctx context.Context, chatID int64) (hours int, optedOut map[int64]bool, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, value FROM chat_prefs WHERE chat_id = ? AND (name = ? OR name LIKE ? ESCAPE '\')`,
		chatID, ChatPrefLogHours, chatPrefOptOutPfx+"%")
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	optedOut = map[int64]bool{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return 0, nil, err
		}
		if name == ChatPrefLogHours {
			hours, _ = strconv.Atoi(value)
		} else if id, err := strconv.ParseInt(strings.TrimPrefix(name, chatPrefOptOutPfx), 10, 64); err == nil {
			optedOut[id] = true
		}
	}
	return min(max(hours, 0), MaxGroupLogHours), optedOut, rows.Err()
}

func (s *Store) SetGroupOptOut(ctx context.Context, chatID, userID int64, out bool) error {
	name := chatPrefOptOutPfx + strconv.FormatInt(userID, 10)
	if !out {
		return s.SetChatPref(ctx, chatID, name, "")
	}
	if err := s.SetChatPref(ctx, chatID, name, "1"); err != nil {
		return err
	}
	_, err := s.DeleteGroupMessages(ctx, chatID, userID)
	return err
}

func (s *Store) SaveGroupMessage(ctx context.Context, m GroupMessage, hours int) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = s.Now()
	}
	m.CreatedAt = m.CreatedAt.UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO group_messages (chat_id, message_id, user_id, name, username, reply_to, text, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO UPDATE SET text = excluded.text`,
		m.ChatID, m.MessageID, m.UserID, truncate(m.Name, 128), truncate(m.Username, 64), m.ReplyTo, truncate(m.Text, maxGroupText),
		m.CreatedAt, m.CreatedAt.Add(time.Duration(hours)*time.Hour))
	return err
}

func (s *Store) UpdateGroupMessage(ctx context.Context, chatID, messageID int64, text string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE group_messages SET text = ? WHERE chat_id = ? AND message_id = ?`,
		truncate(text, maxGroupText), chatID, messageID)
	return err
}

func (s *Store) GroupMessages(ctx context.Context, chatID int64, since time.Time, afterID int64, limit int) ([]GroupMessage, error) {
	limit = min(max(limit, 1), maxGroupMessages)
	rows, err := s.db.QueryContext(ctx, `SELECT chat_id, message_id, user_id, name, username, reply_to, text, created_at FROM (
		SELECT * FROM group_messages WHERE chat_id = ? AND created_at >= ? AND message_id > ? AND expires_at > ?
		ORDER BY message_id DESC LIMIT ?) recent ORDER BY message_id`, chatID, since.UTC(), afterID, s.Now(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupMessage
	for rows.Next() {
		var m GroupMessage
		if err := rows.Scan(&m.ChatID, &m.MessageID, &m.UserID, &m.Name, &m.Username, &m.ReplyTo, &m.Text, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) LastGroupMessageBy(ctx context.Context, chatID, userID, beforeID int64) (*GroupMessage, error) {
	var m GroupMessage
	err := s.db.QueryRowContext(ctx, `SELECT chat_id, message_id, user_id, name, username, reply_to, text, created_at FROM group_messages
		WHERE chat_id = ? AND user_id = ? AND message_id < ? AND expires_at > ? ORDER BY message_id DESC LIMIT 1`,
		chatID, userID, beforeID, s.Now()).Scan(&m.ChatID, &m.MessageID, &m.UserID, &m.Name, &m.Username, &m.ReplyTo, &m.Text, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Store) GroupLogStats(ctx context.Context, chatID int64) (count int, oldest *time.Time, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(created_at) FROM group_messages WHERE chat_id = ? AND expires_at > ?`,
		chatID, s.Now()).Scan(&count, &oldest)
	return
}

func (s *Store) DeleteGroupMessages(ctx context.Context, chatID, userID int64) (int64, error) {
	q, args := `DELETE FROM group_messages WHERE chat_id = ?`, []any{chatID}
	if userID != 0 {
		q, args = q+` AND user_id = ?`, append(args, userID)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) ForgetChat(ctx context.Context, chatID int64) error {
	if _, err := s.DeleteGroupMessages(ctx, chatID, 0); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM chat_prefs WHERE chat_id = ?`, chatID)
	return err
}

func (s *Store) PurgeGroupMessages(ctx context.Context) (int64, error) {
	var total int64
	for {
		res, err := s.db.ExecContext(ctx, `DELETE FROM group_messages WHERE rowid IN (SELECT rowid FROM group_messages WHERE expires_at <= ? LIMIT ?)`, s.Now(), groupLogBatch)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if n < groupLogBatch {
			return total, nil
		}
	}
}

func (s *Store) GroupMessageAuthors(ctx context.Context, chatID int64, ids []int64) (map[int64]GroupMessage, error) {
	out := map[int64]GroupMessage{}
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > 1000 {
		ids = ids[:1000]
	}
	args := []any{chatID}
	marks := make([]string, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT message_id, user_id, name FROM group_messages WHERE chat_id = ? AND message_id IN (`+
		strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m GroupMessage
		if err := rows.Scan(&m.MessageID, &m.UserID, &m.Name); err != nil {
			return nil, err
		}
		out[m.MessageID] = m
	}
	return out, rows.Err()
}

const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalRejected = "rejected"
	chatPrefApproval = "approval"
)

func (s *Store) GroupApproval(ctx context.Context, chatID int64) (state string, since time.Time, err error) {
	v, err := s.ChatPref(ctx, chatID, chatPrefApproval)
	if err != nil {
		return "", time.Time{}, err
	}
	if rest, ok := strings.CutPrefix(v, ApprovalPending+":"); ok {
		unix, _ := strconv.ParseInt(rest, 10, 64)
		return ApprovalPending, time.Unix(unix, 0), nil
	}
	if v == "" {
		return ApprovalApproved, time.Time{}, nil
	}
	return v, time.Time{}, nil
}

func (s *Store) SetGroupApproval(ctx context.Context, chatID int64, state string) error {
	if state == ApprovalPending {
		state += ":" + strconv.FormatInt(s.Now().Unix(), 10)
	}
	return s.SetChatPref(ctx, chatID, chatPrefApproval, state)
}

func (s *Store) DecideGroup(ctx context.Context, chatID int64, state string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE chat_prefs SET value = ? WHERE chat_id = ? AND name = ? AND value LIKE ? ESCAPE '\'`,
		state, chatID, chatPrefApproval, ApprovalPending+":%")
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) PendingGroupsBefore(ctx context.Context, t time.Time) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT chat_id, value FROM chat_prefs WHERE name = ? AND value LIKE ? ESCAPE '\'`, chatPrefApproval, ApprovalPending+":%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		unix, _ := strconv.ParseInt(strings.TrimPrefix(v, ApprovalPending+":"), 10, 64)
		if time.Unix(unix, 0).Before(t) {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

func (s *Store) GroupMemberByUsername(ctx context.Context, chatID int64, username string) (int64, string, error) {
	var id int64
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT user_id, name FROM group_messages WHERE chat_id = ? AND username = ? ORDER BY message_id DESC LIMIT 1`,
		chatID, username).Scan(&id, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return id, name, err
}

func (s *Store) GroupMessageIDsBy(ctx context.Context, chatID, userID int64, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT message_id FROM group_messages WHERE chat_id = ? AND user_id = ? ORDER BY message_id DESC LIMIT ?`,
		chatID, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
