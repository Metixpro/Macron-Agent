package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(context.Background(), path, Options{SecretKey: "test-secret-key"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestConcurrentSetSettingMatchesDB(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if err := s.SetSetting(ctx, "race_key", fmt.Sprintf("val-%d", n)); err != nil {
				t.Errorf("SetSetting(%d): %v", n, err)
			}
		}(i)
	}
	wg.Wait()

	cached, ok := s.Setting("race_key")
	if !ok {
		t.Fatal("expected race_key in cache")
	}
	var fromDB string
	if err := s.DB().QueryRowContext(ctx, `SELECT v FROM settings WHERE k = ?`, "race_key").Scan(&fromDB); err != nil {
		t.Fatalf("select from DB: %v", err)
	}
	if cached != fromDB {
		t.Fatalf("cache/DB mismatch: cache=%q db=%q", cached, fromDB)
	}
}

func TestAddFactAtomicDedupAndLimit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const uid = int64(42)

	// Concurrent duplicate inserts must create only a single row.
	var wg sync.WaitGroup
	var createdCount int
	var mu sync.Mutex
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, existed, err := s.AddFact(ctx, uid, "Prefers Persian UI")
			if err != nil {
				t.Errorf("AddFact dup: %v", err)
				return
			}
			if !existed {
				mu.Lock()
				createdCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if createdCount != 1 {
		t.Fatalf("expected exactly 1 creation for concurrent duplicate fact, got %d", createdCount)
	}

	// Fill up to MaxFacts and ensure concurrent inserts cannot exceed MaxFacts.
	for i := 0; i < MaxFacts+10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _, _ = s.AddFact(ctx, uid, fmt.Sprintf("fact-%d", n))
		}(i)
	}
	wg.Wait()

	facts, err := s.Facts(ctx, uid)
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	if len(facts) != MaxFacts {
		t.Fatalf("expected exact MaxFacts=%d, got %d", MaxFacts, len(facts))
	}
}

func TestCountRequestAndRefundAtomic(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, _, err := s.TouchUser(ctx, UserInfo{ID: 7, Username: "alice", FirstName: "Alice"}); err != nil {
		t.Fatalf("TouchUser: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.CountRequest(ctx, 7); err != nil {
				t.Errorf("CountRequest: %v", err)
			}
		}()
	}
	wg.Wait()

	used, err := s.UsedToday(ctx, 7)
	if err != nil {
		t.Fatalf("UsedToday: %v", err)
	}
	if used != 10 {
		t.Fatalf("expected 10 requests today, got %d", used)
	}

	if err := s.Refund(ctx, 7); err != nil {
		t.Fatalf("Refund: %v", err)
	}
	used, _ = s.UsedToday(ctx, 7)
	u, err := s.User(ctx, 7)
	if err != nil {
		t.Fatalf("User: %v", err)
	}
	if used != 9 || u.MessagesTotal != 9 {
		t.Fatalf("expected both daily_usage and messages_total to be 9 after refund, got daily=%d total=%d", used, u.MessagesTotal)
	}
}

func TestSecretSettingRoundTripAndReopenIdempotentMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reopen.db")
	ctx := context.Background()
	s1, err := Open(ctx, path, Options{SecretKey: "my-secret"})
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	if err := s1.SetSecretSetting(ctx, "api_token", "sk-live-12345"); err != nil {
		t.Fatalf("SetSecretSetting: %v", err)
	}
	_ = s1.Close()

	s2, err := Open(ctx, path, Options{SecretKey: "my-secret"})
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	defer s2.Close()

	got, err := s2.SecretSetting("api_token")
	if err != nil || got != "sk-live-12345" {
		t.Fatalf("SecretSetting after reopen: got=%q err=%v", got, err)
	}
}
