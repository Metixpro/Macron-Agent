package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrDuplicate = errors.New("already exists")

type Provider struct {
	ID       int64
	Name     string
	BaseURL  string
	APIKey   string
	Model    string
	Priority int
	Enabled  bool
	Vision   bool
	API      string

	NoStream bool

	ContextWindow int

	Temperature *float64

	Effort       string
	CreatedAt    time.Time
	LastOKAt     *time.Time
	LastError    string
	LastErrorAt  *time.Time
	Requests     int64
	Failures     int64
	AvgLatencyMS int
}

const providerColumns = `id, name, base_url, api_key, model, priority, enabled, vision, api, context_window,
	created_at, last_ok_at, last_error, last_error_at, requests, failures, avg_latency_ms, temperature, effort, no_stream`

func (s *Store) scanProvider(sc interface{ Scan(...any) error }) (*Provider, error) {
	var (
		p           Provider
		ok, errAt   sql.NullTime
		encodedKey  string
		temperature sql.NullFloat64
	)
	if err := sc.Scan(&p.ID, &p.Name, &p.BaseURL, &encodedKey, &p.Model, &p.Priority, &p.Enabled, &p.Vision, &p.API, &p.ContextWindow,
		&p.CreatedAt,
		&ok, &p.LastError, &errAt, &p.Requests, &p.Failures, &p.AvgLatencyMS, &temperature, &p.Effort, &p.NoStream); err != nil {
		return nil, err
	}
	if temperature.Valid {
		t := temperature.Float64
		p.Temperature = &t
	}
	key, err := s.open(encodedKey)
	if err != nil {
		p.LastError = err.Error()
	}
	p.APIKey, p.LastOKAt, p.LastErrorAt = key, nullTime(ok), nullTime(errAt)
	return &p, nil
}

func (s *Store) AddProvider(ctx context.Context, p Provider) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO providers (name, base_url, api_key, model, priority, enabled, vision, api, context_window,
		created_at, temperature, effort, no_stream) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, truncate(p.Name, 64), p.BaseURL, s.seal(p.APIKey),
		p.Model, p.Priority, p.Enabled, p.Vision, p.API, p.ContextWindow, s.Now(), p.Temperature, truncate(p.Effort, 16), p.NoStream)
	if isDuplicate(err) {
		return 0, ErrDuplicate
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateProvider(ctx context.Context, p Provider) error {
	res, err := s.db.ExecContext(ctx, `UPDATE providers SET name = ?, base_url = ?, api_key = ?, model = ?, priority = ?, enabled = ?, vision = ?,
		api = ?, context_window = ?, temperature = ?, effort = ?, no_stream = ? WHERE id = ?`, truncate(p.Name, 64), p.BaseURL, s.seal(p.APIKey),
		p.Model, p.Priority, p.Enabled, p.Vision, p.API, p.ContextWindow, p.Temperature, truncate(p.Effort, 16), p.NoStream, p.ID)
	if isDuplicate(err) {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteProvider(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM providers WHERE id = ?`, id)
	return err
}

func (s *Store) Provider(ctx context.Context, id int64) (*Provider, error) {
	p, err := s.scanProvider(s.db.QueryRowContext(ctx, `SELECT `+providerColumns+` FROM providers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *Store) Providers(ctx context.Context) ([]Provider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+providerColumns+` FROM providers ORDER BY priority, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Provider
	for rows.Next() {
		p, err := s.scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) RecordProviderResult(ctx context.Context, id int64, ok bool, latency time.Duration, errMsg string) error {
	ms := int(latency.Milliseconds())
	if ok {
		_, err := s.db.ExecContext(ctx, `UPDATE providers SET requests = requests + 1, last_ok_at = ?,
			avg_latency_ms = IIF(avg_latency_ms = 0, ?, ROUND(avg_latency_ms * 0.8 + ? * 0.2)) WHERE id = ?`, s.Now(), ms, ms, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE providers SET requests = requests + 1, failures = failures + 1, last_error = ?, last_error_at = ?
		WHERE id = ?`, truncate(errMsg, 500), s.Now(), id)
	return err
}

func (s *Store) RefreshProviderSettings(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT k,v FROM settings WHERE k IN (?,?)`, SettingActiveProvider, SettingFailover)
	if err != nil {
		return err
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		values[k] = v
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, key := range []string{SettingActiveProvider, SettingFailover} {
		if v, ok := values[key]; ok {
			s.settings[key] = v
		} else {
			delete(s.settings, key)
		}
	}
	return nil
}
