package store

import (
	"context"
	"fmt"
	"time"
)

// How long finished records are kept. The archive of conversations and the
// users themselves are never removed here.
const (
	keepDailyUsage   = 90 * 24 * time.Hour  // per-user daily counters (quotas need today only)
	keepStats        = 400 * 24 * time.Hour // the panel's statistics (a year and a bit)
	keepLeftChats    = 180 * 24 * time.Hour // groups the bot left or was removed from
	keepEndedJobs    = 90 * 24 * time.Hour  // scheduled tasks that ended
	keepOpenJobs     = 3 * 24 * time.Hour   // an unfinished job nobody went on with (its files are gone by then)
	keepBgTasks      = 7 * 24 * time.Hour   // a finished background task, listed in /tasks
	retentionBatch   = 5000
	retentionMaxRuns = 40 // batches per table and pass (the next pass goes on)
)

// Retention removes what no longer serves anyone, a batch at a time: old
// daily counters and statistics, finished scheduled tasks,
// groups the bot left long ago, and the memory, facts and cookies of users
// that no longer exist. It reports how many rows each kind lost.
func (s *Store) Retention(ctx context.Context) (map[string]int64, error) {
	now := s.Now()
	jobs := []struct {
		name, query string
		args        []any
	}{
		{"daily_usage", `DELETE FROM daily_usage WHERE rowid IN (SELECT rowid FROM daily_usage WHERE day < ? LIMIT ?)`, []any{now.Add(-keepDailyUsage).Format("2006-01-02")}},
		{"usage_stats", `DELETE FROM usage_stats WHERE rowid IN (SELECT rowid FROM usage_stats WHERE day < ? LIMIT ?)`, []any{now.Add(-keepStats).Format("2006-01-02")}},
		{"bot_chats", `DELETE FROM bot_chats WHERE rowid IN (SELECT rowid FROM bot_chats WHERE status IN ('left', 'kicked') AND updated_at < ? LIMIT ?)`, []any{now.Add(-keepLeftChats)}},
		{"schedules", `DELETE FROM schedules WHERE rowid IN (SELECT rowid FROM schedules WHERE status IN (?, ?) AND COALESCE(last_run, created_at) < ? LIMIT ?)`,
			[]any{ScheduleDone, ScheduleCancelled, now.Add(-keepEndedJobs)}},
		{"open_jobs", `DELETE FROM open_jobs WHERE rowid IN (SELECT rowid FROM open_jobs WHERE updated_at < ? LIMIT ?)`, []any{now.Add(-keepOpenJobs)}},
		{"bg_tasks", `DELETE FROM bg_tasks WHERE rowid IN (SELECT rowid FROM bg_tasks WHERE created_at < ? AND status NOT IN ('queued', 'running') LIMIT ?)`, []any{now.Add(-keepBgTasks)}},
		{"context_messages", `DELETE FROM context_messages WHERE rowid IN (SELECT rowid FROM context_messages WHERE user_id NOT IN (SELECT id FROM users) LIMIT ?)`, nil},
		{"context_memory", `DELETE FROM context_memory WHERE rowid IN (SELECT rowid FROM context_memory WHERE user_id NOT IN (SELECT id FROM users) LIMIT ?)`, nil},
		{"user_facts", `DELETE FROM user_facts WHERE rowid IN (SELECT rowid FROM user_facts WHERE user_id NOT IN (SELECT id FROM users) LIMIT ?)`, nil},
		{"user_cookies", `DELETE FROM user_cookies WHERE rowid IN (SELECT rowid FROM user_cookies WHERE user_id NOT IN (SELECT id FROM users) LIMIT ?)`, nil},
	}
	out := map[string]int64{}
	for _, j := range jobs {
		for range retentionMaxRuns {
			res, err := s.db.ExecContext(ctx, j.query, append(j.args, retentionBatch)...)
			if err != nil {
				return out, fmt.Errorf("retention of %s: %w", j.name, err)
			}
			n, _ := res.RowsAffected()
			if n > 0 {
				out[j.name] += n
			}
			if n < retentionBatch {
				break
			}
		}
	}
	return out, nil
}
