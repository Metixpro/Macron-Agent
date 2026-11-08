package store

import (
	"context"
	"strings"
	"time"
)

// The fast classifier's decisions (cmd/bot/snap.go): what it read in a
// request and what the bot did with it, in figures, for the admin panel.
// Never a message, never who sent it.

// SnapLog is one decision.
type SnapLog struct {
	At          time.Time
	Tier        string
	Kind        string
	KindConf    float64
	NeedsTools  float64
	Work        float64
	Context     string
	Applied     []string // what it changed: route_strong, route_light, effort_low, effort_medium, chat, trim, focus
	LatencyMS   int
	SnapTokens  int
	SavedTokens int // tokens the language model was not sent (an estimate)
	Error       string
}

// AddSnapLog records a decision.
func (s *Store) AddSnapLog(ctx context.Context, l SnapLog) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO snap_log (at, tier, kind, kind_conf, needs_tools, work, context, applied, latency_ms, snap_tokens,
		saved_tokens, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, s.Now(), truncate(l.Tier, 8), truncate(l.Kind, 16), l.KindConf,
		l.NeedsTools, l.Work, truncate(l.Context, 12), truncate(strings.Join(l.Applied, ","), 128), l.LatencyMS, l.SnapTokens, l.SavedTokens,
		truncate(l.Error, 160))
	return err
}

// SnapStats sum the decisions since a time.
type SnapStats struct {
	Calls, Failures          int
	AvgLatencyMS, MaxLatency int
	SnapTokens, SavedTokens  int64
	Applied                  map[string]int // how often each change was made
	Kinds                    map[string]int
}

var snapApplied = []string{"route_strong", "route_light", "effort_low", "effort_medium", "chat", "trim", "focus"}

// SnapSince sums the decisions since a time.
func (s *Store) SnapSince(ctx context.Context, since time.Time) (SnapStats, error) {
	st := SnapStats{Applied: map[string]int{}, Kinds: map[string]int{}}
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(error <> ''), 0), CAST(ROUND(COALESCE(AVG(CASE WHEN error = '' THEN latency_ms END), 0)) AS INTEGER),
		COALESCE(MAX(latency_ms), 0), COALESCE(SUM(snap_tokens), 0), COALESCE(SUM(saved_tokens), 0) FROM snap_log WHERE at >= ?`, since).
		Scan(&st.Calls, &st.Failures, &st.AvgLatencyMS, &st.MaxLatency, &st.SnapTokens, &st.SavedTokens)
	if err != nil {
		return st, err
	}
	for _, a := range snapApplied {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM snap_log WHERE at >= ? AND INSTR(',' || applied || ',', ',' || ? || ',') > 0`, since, a).Scan(&n); err != nil {
			return st, err
		}
		if n > 0 {
			st.Applied[a] = n
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kind, COUNT(*) FROM snap_log WHERE at >= ? AND error = '' AND kind <> '' GROUP BY kind`, since)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return st, err
		}
		st.Kinds[k] = n
	}
	return st, rows.Err()
}
