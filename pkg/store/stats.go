package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Stat kinds.
const (
	StatTool     = "tool"
	StatProvider = "provider"
	StatRoute    = "route" // smart model routing: "light:KIND", "strong:KIND" or "escalated:KIND"
)

// RecordStat adds one event (and optional token counts) to today's statistics.
func (s *Store) RecordStat(ctx context.Context, kind, name string, failed bool, promptTokens, completionTokens int) error {
	fail := 0
	if failed {
		fail = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO usage_stats (day, kind, name, count, failures, prompt_tokens, completion_tokens)
		VALUES (?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT DO UPDATE SET count = count + 1, failures = failures + excluded.failures,
			prompt_tokens = prompt_tokens + excluded.prompt_tokens, completion_tokens = completion_tokens + excluded.completion_tokens`,
		s.Today(), kind, truncate(name, 64), fail, promptTokens, completionTokens)
	return err
}

// Overview is the headline numbers of the admin dashboard.
type Overview struct {
	Users, NewToday, New7d                   int
	ActiveToday, Active7d, Active30d         int
	RequestsToday, Requests7d, RequestsTotal int
	BlockedBot, Premium                      int
	MemoryUsers, MemoryMessages              int
	TokensTodayPrompt, TokensTodayCompletion int64
	Providers, ProvidersEnabled              int
	PremiumEmoji                             int
}

// Overview computes the dashboard numbers.
func (s *Store) Overview(ctx context.Context) (*Overview, error) {
	now := s.Now()
	today := s.DayStart(now)
	o := &Overview{}
	queries := []struct {
		q    string
		args []any
		dst  []any
	}{
		{`SELECT COUNT(*) AS users, COALESCE(SUM(created_at >= ?), 0) AS new_today, COALESCE(SUM(created_at >= ?), 0) AS new_7d,
			COALESCE(SUM(last_seen_at >= ?), 0) AS active_today, COALESCE(SUM(last_seen_at >= ?), 0) AS active_7d,
			COALESCE(SUM(last_seen_at >= ?), 0) AS active_30d, COALESCE(SUM(blocked_bot), 0) AS blocked,
			COALESCE(SUM(is_premium), 0) AS premium, COALESCE(SUM(messages_total), 0) AS requests_total FROM users`,
			[]any{today, now.Add(-7 * 24 * time.Hour), today, now.Add(-7 * 24 * time.Hour), now.Add(-30 * 24 * time.Hour)},
			[]any{&o.Users, &o.NewToday, &o.New7d, &o.ActiveToday, &o.Active7d, &o.Active30d, &o.BlockedBot, &o.Premium, &o.RequestsTotal}},
		{`SELECT COALESCE(SUM(IIF(day = ?, requests, 0)), 0) AS requests_today, COALESCE(SUM(IIF(day >= ?, requests, 0)), 0) AS requests_7d FROM daily_usage`,
			[]any{s.Today(), now.In(s.loc).AddDate(0, 0, -6).Format(time.DateOnly)}, []any{&o.RequestsToday, &o.Requests7d}},
		{`SELECT COUNT(DISTINCT user_id) AS memory_users, COUNT(*) AS memory_messages FROM context_messages`, nil,
			[]any{&o.MemoryUsers, &o.MemoryMessages}},
		{`SELECT COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens, COALESCE(SUM(completion_tokens), 0) AS completion_tokens FROM usage_stats
			WHERE day = ? AND kind = ?`, []any{s.Today(), StatProvider}, []any{&o.TokensTodayPrompt, &o.TokensTodayCompletion}},
		{`SELECT COUNT(*) AS providers, COALESCE(SUM(enabled), 0) AS providers_enabled FROM providers`, nil, []any{&o.Providers, &o.ProvidersEnabled}},
		{`SELECT COUNT(*) AS premium_emoji FROM premium_emoji`, nil, []any{&o.PremiumEmoji}},
	}
	// One round trip: each part is a derived table of one row.
	var (
		sb   strings.Builder
		args []any
		dst  []any
	)
	sb.WriteString("SELECT * FROM ")
	for i, q := range queries {
		if i > 0 {
			sb.WriteString(" CROSS JOIN ")
		}
		fmt.Fprintf(&sb, "(%s) part%d", q.q, i)
		args, dst = append(args, q.args...), append(dst, q.dst...)
	}
	if err := s.db.QueryRowContext(ctx, sb.String(), args...).Scan(dst...); err != nil {
		return nil, err
	}
	return o, nil
}

// DayPoint is one day of the activity chart.
type DayPoint struct {
	Day      string
	Requests int
	Users    int
	NewUsers int
}

// DailySeries returns the last n days of activity, oldest first (missing days are zero).
func (s *Store) DailySeries(ctx context.Context, n int) ([]DayPoint, error) {
	local := s.now().In(s.loc)
	points := make([]DayPoint, n)
	index := map[string]int{}
	for i := 0; i < n; i++ {
		d := local.AddDate(0, 0, i-n+1).Format(time.DateOnly)
		points[i].Day = d
		index[d] = i
	}
	rows, err := s.db.QueryContext(ctx, `SELECT STRFTIME('%Y-%m-%d', day), SUM(requests), COUNT(DISTINCT user_id) FROM daily_usage
		WHERE day >= ? GROUP BY day`, points[0].Day)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d string
		var req, users int
		if err := rows.Scan(&d, &req, &users); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := index[d]; ok {
			points[i].Requests, points[i].Users = req, users
		}
	}
	rows.Close()

	// New users per local day: shift UTC timestamps by the zone offset (Tehran has no DST).
	_, offset := local.Zone()
	rows, err = s.db.QueryContext(ctx, `SELECT STRFTIME('%Y-%m-%d', created_at, PRINTF('%+d seconds', ?)) d, COUNT(*) FROM users
		WHERE created_at >= ? GROUP BY d`, offset, s.DayStart(local.AddDate(0, 0, -n+1)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		var c int
		if err := rows.Scan(&d, &c); err != nil {
			return nil, err
		}
		if i, ok := index[d]; ok {
			points[i].NewUsers = c
		}
	}
	return points, rows.Err()
}

// Ranked is a name with a count, for "top" lists.
type Ranked struct {
	ID       int64
	Name     string
	Count    int64
	Failures int64
	Tokens   int64
}

// TopUsersToday ranks users by requests today.
func (s *Store) TopUsersToday(ctx context.Context, limit int) ([]Ranked, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.id, TRIM((u.first_name || ' ' || u.last_name)), d.requests FROM daily_usage d
		JOIN users u ON u.id = d.user_id WHERE d.day = ? ORDER BY d.requests DESC LIMIT ?`, s.Today(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ranked
	for rows.Next() {
		var r Ranked
		if err := rows.Scan(&r.ID, &r.Name, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TopStats ranks tools or providers over the last n days.
func (s *Store) TopStats(ctx context.Context, kind string, days, limit int) ([]Ranked, error) {
	since := s.now().In(s.loc).AddDate(0, 0, -days+1).Format(time.DateOnly)
	rows, err := s.db.QueryContext(ctx, `SELECT name, SUM(count), SUM(failures), SUM(prompt_tokens + completion_tokens) FROM usage_stats
		WHERE kind = ? AND day >= ? GROUP BY name ORDER BY SUM(count) DESC LIMIT ?`, kind, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ranked
	for rows.Next() {
		var r Ranked
		if err := rows.Scan(&r.Name, &r.Count, &r.Failures, &r.Tokens); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DatabaseSize is the size of the database file in bytes.
func (s *Store) DatabaseSize(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`).Scan(&n)
	return n, err
}
