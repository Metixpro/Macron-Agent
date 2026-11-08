package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// User is a Telegram user known to the bot.
type User struct {
	ID               int64
	Username         string
	FirstName        string
	LastName         string
	LanguageCode     string
	IsPremium        bool
	Bio              string
	Birthdate        string
	ProfileCheckedAt *time.Time
	BlockedBot       bool
	MessagesTotal    int
	CreatedAt        time.Time
	LastSeenAt       time.Time
}

// DisplayName is the best human-readable name.
func (u *User) DisplayName() string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" && u.Username != "" {
		return "@" + u.Username
	}
	if name == "" {
		return strconv.FormatInt(u.ID, 10)
	}
	return name
}

// UserInfo is what Telegram tells us about a user on every update.
type UserInfo struct {
	ID           int64
	Username     string
	FirstName    string
	LastName     string
	LanguageCode string
	IsPremium    bool
}

const userColumns = `id, username, first_name, last_name, language_code, is_premium, bio, birthdate, profile_checked_at,
	blocked_bot, messages_total, created_at, last_seen_at`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var (
		u        User
		username sql.NullString
		bio      sql.NullString
		checked  sql.NullTime
	)
	if err := sc.Scan(&u.ID, &username, &u.FirstName, &u.LastName, &u.LanguageCode, &u.IsPremium, &bio, &u.Birthdate,
		&checked, &u.BlockedBot, &u.MessagesTotal, &u.CreatedAt, &u.LastSeenAt); err != nil {
		return nil, err
	}
	u.Username, u.Bio, u.ProfileCheckedAt = username.String, bio.String, nullTime(checked)
	return &u, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TouchUser creates or refreshes a user from an update. isNew reports a first contact.
// A user that had blocked the bot and writes again is marked as reachable.
func (s *Store) TouchUser(ctx context.Context, in UserInfo) (u *User, isNew bool, err error) {
	now := s.Now()
	var seen int
	existed := s.db.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, in.ID).Scan(&seen) == nil
	_, err = s.db.ExecContext(ctx, `INSERT INTO users (id, username, first_name, last_name, language_code, is_premium, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO UPDATE SET username = excluded.username, first_name = excluded.first_name, last_name = excluded.last_name,
			language_code = excluded.language_code, is_premium = excluded.is_premium, last_seen_at = excluded.last_seen_at, blocked_bot = 0`,
		in.ID, nullString(truncate(in.Username, 64)), truncate(in.FirstName, 255), truncate(in.LastName, 255),
		truncate(in.LanguageCode, 16), in.IsPremium, now, now)
	if err != nil {
		return nil, false, err
	}
	u, err = s.User(ctx, in.ID)
	return u, !existed, err
}

// User loads one user.
func (s *Store) User(ctx context.Context, id int64) (*User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// FindUsers looks a user up by numeric id, @username or part of the name.
func (s *Store) FindUsers(ctx context.Context, query string, limit int) ([]User, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	if id, err := strconv.ParseInt(q, 10, 64); err == nil {
		u, err := s.User(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return []User{*u}, nil
	}
	if strings.HasPrefix(q, "@") {
		return s.queryUsers(ctx, `SELECT `+userColumns+` FROM users WHERE username = ? LIMIT ?`, strings.TrimPrefix(q, "@"), limit)
	}
	esc := strings.NewReplacer("%", `\%`, "_", `\_`).Replace(q)
	// A username that starts so comes from its index; names are looked for
	// among the users seen most recently (an admin looks for an active user),
	// so the search never reads the whole table.
	found, err := s.queryUsers(ctx, `SELECT `+userColumns+` FROM users WHERE username LIKE ? ESCAPE '\' ORDER BY last_seen_at DESC LIMIT ?`, esc+"%", limit)
	if err != nil || len(found) >= limit {
		return found, err
	}
	like := "%" + esc + "%"
	more, err := s.queryUsers(ctx, `SELECT `+userColumns+` FROM (SELECT `+userColumns+` FROM users ORDER BY last_seen_at DESC LIMIT ?) recent
		WHERE first_name LIKE ? ESCAPE '\' OR last_name LIKE ? ESCAPE '\' OR username LIKE ? ESCAPE '\' ORDER BY last_seen_at DESC LIMIT ?`, userSearchWindow, like, like, like, limit)
	if err != nil {
		return found, err
	}
	seen := map[int64]bool{}
	for _, u := range found {
		seen[u.ID] = true
	}
	for _, u := range more {
		if !seen[u.ID] && len(found) < limit {
			found = append(found, u)
		}
	}
	return found, nil
}

// userSearchWindow is how many of the most recently seen users a name search reads.
const userSearchWindow = 50000

func (s *Store) queryUsers(ctx context.Context, query string, args ...any) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// RecentUsers lists users by last activity, newest first.
func (s *Store) RecentUsers(ctx context.Context, offset, limit int) ([]User, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&total); err != nil {
		return nil, 0, err
	}
	users, err := s.queryUsers(ctx, `SELECT `+userColumns+` FROM users ORDER BY last_seen_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	return users, total, err
}

func (s *Store) execOne(ctx context.Context, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 0 rows is a missing user, or a value that did not change; tell them apart.
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, args[len(args)-1]).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
	}
	return nil
}

// SetProfileDetails caches what getChat reveals (bio, birthday).
func (s *Store) SetProfileDetails(ctx context.Context, id int64, bio, birthdate string) error {
	return s.execOne(ctx, `UPDATE users SET bio = ?, birthdate = ?, profile_checked_at = ? WHERE id = ?`,
		nullString(truncate(bio, 500)), truncate(birthdate, 32), s.Now(), id)
}

func (s *Store) SetBlockedBot(ctx context.Context, id int64, blocked bool) error {
	return s.execOne(ctx, `UPDATE users SET blocked_bot = ? WHERE id = ?`, blocked, id)
}

// ---------------------------------------------------------------- daily usage
//
// A day counts the requests (for the statistics) and the tokens they used
// (the quota). The tokens are charged when a request is done: what is left
// of the day's allowance first, the rest from the user's extra tokens.

// CountRequest counts one request in today's statistics.
func (s *Store) CountRequest(ctx context.Context, userID int64) error {
	err := retryDeadlock(func() error {
		_, err := s.db.ExecContext(ctx, `INSERT INTO daily_usage (user_id, day, requests) VALUES (?, ?, 1)
			ON CONFLICT DO UPDATE SET requests = requests + 1`, userID, s.Today())
		return err
	})
	if err == nil {
		_, err = s.db.ExecContext(ctx, `UPDATE users SET messages_total = messages_total + 1 WHERE id = ?`, userID)
	}
	return err
}

// TokensToday is how many tokens a user's requests used today.
func (s *Store) TokensToday(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT tokens FROM daily_usage WHERE user_id = ? AND day = ?`, userID, s.Today()).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// retryDeadlock retries a locked database (another writer held the file past
// the wait) a few times.
func retryDeadlock(fn func() error) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if !isBusy(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 15 * time.Millisecond)
	}
	return err
}

// AddTokens adds the tokens a finished request used to the day's figures.
func (s *Store) AddTokens(ctx context.Context, userID int64, n int) error {
	if n <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO daily_usage (user_id, day, tokens) VALUES (?, ?, ?)
		ON CONFLICT DO UPDATE SET tokens = tokens + excluded.tokens`, userID, s.Today(), n)
	return err
}

// Refund takes back one request from today's statistics (the bot itself failed).
func (s *Store) Refund(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE daily_usage SET requests = MAX(requests - 1, 0) WHERE user_id = ? AND day = ?`, userID, s.Today())
	if err == nil {
		_, err = s.db.ExecContext(ctx, `UPDATE users SET messages_total = MAX(messages_total - 1, 0) WHERE id = ?`, userID)
	}
	return err
}

// UsedToday returns today's request count (the statistics).
func (s *Store) UsedToday(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT requests FROM daily_usage WHERE user_id = ? AND day = ?`, userID, s.Today()).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// ResetUsageToday sets today's count back to zero.
func (s *Store) ResetUsageToday(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM daily_usage WHERE user_id = ? AND day = ?`, userID, s.Today())
	return err
}
