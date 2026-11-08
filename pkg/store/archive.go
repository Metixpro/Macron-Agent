package store

import (
	"context"
	"strings"
	"time"
	"unicode"
)

const MaxArchiveChars = 8000

type ArchiveTurn struct {
	ID        int64
	UserID    int64
	Role      string
	Content   string
	CreatedAt time.Time
}

func (s *Store) Archive(ctx context.Context, userID int64, turns ...ArchiveTurn) error {
	now := s.Now()
	for _, t := range turns {
		text := strings.TrimSpace(t.Content)
		if text == "" || (t.Role != "user" && t.Role != "assistant") {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO chat_archive (user_id, role, content, created_at) VALUES (?, ?, ?, ?)`,
			userID, t.Role, truncate(text, MaxArchiveChars), now); err != nil {
			return err
		}
	}
	return nil
}

type ArchiveHit struct {
	ID        int64
	Role      string
	Snippet   string
	CreatedAt time.Time
}

func searchTerms(query string) []string {
	var terms []string
	for _, w := range strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) >= 2 || unicode.IsDigit([]rune(w)[0]) {
			terms = append(terms, w)
		}
		if len(terms) == 12 {
			break
		}
	}
	return terms
}

func (s *Store) SearchArchive(ctx context.Context, userID int64, query string, since time.Time, limit int) ([]ArchiveHit, error) {
	terms := searchTerms(query)
	if len(terms) == 0 {
		return nil, nil
	}
	limit = max(1, min(limit, 20))
	for _, joiner := range []string{" ", " OR "} {
		if joiner == " OR " && len(terms) < 2 {
			break
		}
		quoted := make([]string, len(terms))
		for i, t := range terms {
			quoted[i] = `"` + strings.ReplaceAll(t, `"`, "") + `"*`
		}
		args := []any{strings.Join(quoted, joiner), userID}
		q := `SELECT a.id, a.role, a.created_at, snippet(chat_archive_fts, 0, '«', '»', '…', 30)
			FROM chat_archive_fts JOIN chat_archive a ON a.id = chat_archive_fts.rowid
			WHERE chat_archive_fts MATCH ? AND a.user_id = ?`
		if !since.IsZero() {
			q += ` AND a.created_at >= ?`
			args = append(args, since)
		}
		q += ` ORDER BY bm25(chat_archive_fts) LIMIT ?`
		args = append(args, limit)
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		var out []ArchiveHit
		for rows.Next() {
			var h ArchiveHit
			if err := rows.Scan(&h.ID, &h.Role, &h.CreatedAt, &h.Snippet); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	return nil, nil
}

func (s *Store) ArchiveAround(ctx context.Context, userID, id int64, before, after int) ([]ArchiveTurn, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, role, content, created_at FROM (
			SELECT id, user_id, role, content, created_at FROM chat_archive WHERE user_id = ? AND id <= ? ORDER BY id DESC LIMIT ?
		) UNION SELECT id, user_id, role, content, created_at FROM (
			SELECT id, user_id, role, content, created_at FROM chat_archive WHERE user_id = ? AND id > ? ORDER BY id LIMIT ?
		) ORDER BY id`, userID, id, before+1, userID, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArchiveTurn
	for rows.Next() {
		var t ArchiveTurn
		if err := rows.Scan(&t.ID, &t.UserID, &t.Role, &t.Content, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) ArchiveCount(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_archive WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

func (s *Store) DeleteArchive(ctx context.Context, userID int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM chat_archive WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
