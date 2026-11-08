package store

import (
	"context"
	"time"
)

// The command gate's allowlist and audit log (see pkg/cmdgate).

// CmdAudit is one decision of the gate.
type CmdAudit struct {
	ID       int64
	At       time.Time
	Session  string
	Origin   string
	Command  string
	Decision string
	Decider  string
	Rule     string
}

// CmdAllowlist is what the owner allowed for good, oldest first.
func (s *Store) CmdAllowlist(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pattern FROM cmd_allow ORDER BY created_at, pattern`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AddCmdAllow allows a command for good.
func (s *Store) AddCmdAllow(ctx context.Context, pattern string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO cmd_allow (pattern, created_at) VALUES (?, ?)`, truncate(pattern, 4000), s.Now())
	return err
}

// RemoveCmdAllow takes an allowance back.
func (s *Store) RemoveCmdAllow(ctx context.Context, pattern string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM cmd_allow WHERE pattern = ?`, pattern)
	return err
}

// AddCmdAudit records a decision.
func (s *Store) AddCmdAudit(ctx context.Context, a CmdAudit) error {
	at := a.At
	if at.IsZero() {
		at = s.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO cmd_audit (at, session, origin, command, decision, decider, rule) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		at.UTC(), truncate(a.Session, 100), truncate(a.Origin, 20), truncate(a.Command, 4000), truncate(a.Decision, 20), truncate(a.Decider, 40), truncate(a.Rule, 400))
	return err
}

// CmdAudits are the latest decisions, newest first.
func (s *Store) CmdAudits(ctx context.Context, limit int) ([]CmdAudit, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, session, origin, command, decision, decider, rule FROM cmd_audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CmdAudit
	for rows.Next() {
		var a CmdAudit
		if err := rows.Scan(&a.ID, &a.At, &a.Session, &a.Origin, &a.Command, &a.Decision, &a.Decider, &a.Rule); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
