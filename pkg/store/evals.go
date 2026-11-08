package store

import (
	"context"
	"time"
)

type EvalRun struct {
	ID        int64
	StartedAt time.Time
	Took      time.Duration
	Reason    string
	Provider  string
	Model     string
	Passed    int
	Total     int
	Details   string
}

func (r EvalRun) Score() int {
	if r.Total == 0 {
		return 0
	}
	return r.Passed * 100 / r.Total
}

func (s *Store) AddEvalRun(ctx context.Context, r EvalRun) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO eval_runs (started_at, took_ms, reason, provider, model, passed, total, details)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, r.StartedAt.UTC(), r.Took.Milliseconds(), truncate(r.Reason, 32), truncate(r.Provider, 64),
		truncate(r.Model, 120), r.Passed, r.Total, r.Details)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) EvalRuns(ctx context.Context, limit int) ([]EvalRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, started_at, took_ms, reason, provider, model, passed, total, details
		FROM eval_runs ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvalRun
	for rows.Next() {
		var (
			r  EvalRun
			ms int64
		)
		if err := rows.Scan(&r.ID, &r.StartedAt, &ms, &r.Reason, &r.Provider, &r.Model, &r.Passed, &r.Total, &r.Details); err != nil {
			return nil, err
		}
		r.Took = time.Duration(ms) * time.Millisecond
		out = append(out, r)
	}
	return out, rows.Err()
}
