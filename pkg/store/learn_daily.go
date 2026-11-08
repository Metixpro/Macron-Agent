package store

import (
	"context"
	"strings"
	"time"
)

// Daily counts of how the bot did, for the weekly report: jobs, stopped
// jobs, reactions and complaints (by the tools of the answer), and each
// tool's calls and failures with its last error. Nothing about the users.

// Kinds of daily counts.
const (
	DailyJob       = "job"       // a request answered
	DailyPartial   = "partial"   // one that stopped before the end
	DailyUp        = "up"        // 👍 and other good reactions
	DailyDown      = "down"      // 👎 and other bad ones
	DailyComplaint = "complaint" // «that's wrong», «it didn't work»
	DailyRepeat    = "repeat"    // the same request again soon after
	DailyCall      = "call"      // a tool call
	DailyFail      = "fail"      // a failed tool call (note: its last error)
)

// DailyCount is one count to add.
type DailyCount struct {
	Kind, Tool string
	N          int
	Note       string
}

// AddDaily adds counts to a day (the day of now in the store's clock).
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

// DailySum is a count over some days.
type DailySum struct {
	Kind, Tool string
	N          int
	Note       string // the latest note
}

// DailySums adds up the counts of the days from since to now.
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

// PruneDaily drops the counts older than keep.
func (s *Store) PruneDaily(ctx context.Context, keep time.Duration) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM learn_daily WHERE day < ?`, s.Now().Add(-keep).Format("2006-01-02"))
	return err
}
