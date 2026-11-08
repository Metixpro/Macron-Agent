package store

import (
	"context"
)

// ---------------------------------------------------------------- premium emoji

// SetPremiumEmoji maps a Unicode emoji to a custom (premium) emoji id.
func (s *Store) SetPremiumEmoji(ctx context.Context, emoji, customID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO premium_emoji (emoji, custom_emoji_id, created_at) VALUES (?, ?, ?)
		ON CONFLICT DO UPDATE SET custom_emoji_id = excluded.custom_emoji_id`, emoji, customID, s.Now())
	return err
}

func (s *Store) DeletePremiumEmoji(ctx context.Context, emoji string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM premium_emoji WHERE emoji = ?`, emoji)
	return err
}

func (s *Store) ClearPremiumEmoji(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM premium_emoji`)
	return err
}

// PremiumEmojiInOrder returns the mappings as [emoji, id] pairs, oldest first.
func (s *Store) PremiumEmojiInOrder(ctx context.Context) ([][2]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT emoji, custom_emoji_id FROM premium_emoji ORDER BY created_at, emoji`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var p [2]string
		if err := rows.Scan(&p[0], &p[1]); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PremiumEmoji returns all mappings.
func (s *Store) PremiumEmoji(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT emoji, custom_emoji_id FROM premium_emoji`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var e, id string
		if err := rows.Scan(&e, &id); err != nil {
			return nil, err
		}
		out[e] = id
	}
	return out, rows.Err()
}
