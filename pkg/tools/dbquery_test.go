package tools

import (
	"context"
	"testing"
)

func TestReadOnlyQueryRejection(t *testing.T) {
	r := &Registry{}
	ctx := context.Background()

	// Modifying queries must be rejected
	badQueries := []string{
		"DROP TABLE users;",
		"INSERT INTO accounts (name) VALUES ('hacker');",
		"UPDATE settings SET key = 'val';",
		"DELETE FROM logs;",
		"TRUNCATE TABLE audit;",
		"ALTER TABLE users ADD COLUMN pass TEXT;",
	}

	for _, q := range badQueries {
		_, err := r.ExecuteReadOnlyQuery(ctx, "postgres", "postgres://localhost/test", q, 10)
		if err == nil {
			t.Errorf("expected query %q to be rejected as read-only violation", q)
		}
	}
}
