package store

import (
	"context"
	"strings"
	"time"
)

const (
	DailyJob       = "job"
	DailyPartial   = "partial"
	DailyUp        = "up"
	DailyDown      = "down"
	DailyComplaint = "complaint"
	DailyRepeat    = "repeat"
	DailyCall      = "call"
	DailyFail      = "fail"
)

type DailyCount struct {
	Kind, Tool string
	N          int
	Note       string
}

func (s *Store) AddDaily(ctx context.Context, counts ...DailyCount) error {
	if len(counts) == 0 {
		return nil
	}
	day := s.Now().Format("2006-01-02")
	var q strings.Builder
	q.WriteString(`INSERT INTO learn_daily (day, kind, tool, n, note) VALUES `)
	args := make([]any, 0, 5*len(counts))
	for i, c := range counts {
		if i > 0 {
			q.WriteString(", ")
		}
		q.WriteString("(?, ?, ?, ?, ?)")
		args = append(args, day, truncate(c.Kind, 16), truncate(c.Tool, 64), c.N, truncate(c.Note, 300))
	}
	q.WriteString(` ON CONFLICT DO UPDATE SET n = n + excluded.n, note = IIF(excluded.note <> '', excluded.note, note)`)
	_, err := s.db.ExecContext(ctx, q.String(), args...)
	return err
}

type DailySum struct {
	Kind, Tool string
	N          int
	Note       string
}

func (s *Store) DailySums(ctx context.Context, since time.Time) ([]DailySum, error) {
	from := since.Format("2006-01-02")
	rows, err := s.db.QueryContext(ctx, `SELECT kind, tool, SUM(n),
		COALESCE((SELECT l.note FROM learn_daily l WHERE l.kind = learn_daily.kind AND l.tool = learn_daily.tool
			AND l.day >= ? AND l.note <> '' ORDER BY l.day DESC LIMIT 1), '')
		FROM learn_daily WHERE day >= ? GROUP BY kind, tool`, from, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DailySum
	for rows.Next() {
		var d DailySum
		if err := rows.Scan(&d.Kind, &d.Tool, &d.N, &d.Note); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) PruneDaily(ctx context.Context, keep time.Duration) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM learn_daily WHERE day < ?`, s.Now().Add(-keep).Format("2006-01-02"))
	return err
}
