package store

import "context"

const groupLogBatch = 5000

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
