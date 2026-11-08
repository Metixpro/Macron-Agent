package memory

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

type fakeLLM struct {
	mu      sync.Mutex
	prompts []string
	fail    bool
}

func (f *fakeLLM) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, req.Messages[0].Content)
	if f.fail {
		return nil, errors.New("down")
	}
	return &llm.Response{Message: llm.Message{Role: "assistant", Content: "- کاربر اسمش علیه و Go دوست داره"}}, nil
}

func TestEstimateTokens(t *testing.T) {
	if EstimateTokens("") != 0 || EstimateTokens("a") != 1 {
		t.Error("tiny inputs")
	}
	en := EstimateTokens(strings.Repeat("word ", 400))
	fa := EstimateTokens(strings.Repeat("سلام ", 400))
	if en < 400 || en > 600 || fa < 800 || fa > 1000 {
		t.Errorf("estimates en=%d fa=%d", en, fa)
	}
}

func TestSaveLoadAndCompress(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	f := &fakeLLM{}
	budget := 1000
	m := New(s, f, func() int { return budget })

	long := strings.Repeat("این یک پیام طولانی است. ", 20)

	for i := 0; i < 2; i++ {
		if c, err := m.Save(ctx, 1, "سوال "+long, "جواب "+long); err != nil || c {
			t.Fatalf("turn %d: compressed=%v err=%v", i, c, err)
		}
	}
	compressed, err := m.Save(ctx, 1, "سوال آخر "+long, "جواب آخر "+long)
	if err != nil || !compressed {
		t.Fatalf("expected a compression: %v %v", compressed, err)
	}
	c, _ := m.Load(ctx, 1)
	if c.Summary == "" || c.Compressions != 1 || c.Tokens > budget || c.Messages < keepMinimum || c.History[0].Role != "user" {
		t.Fatalf("after compression: summary=%q compressions=%d tokens=%d messages=%d", c.Summary, c.Compressions, c.Tokens, c.Messages)
	}
	if !strings.Contains(c.History[len(c.History)-1].Content, "جواب آخر") {
		t.Error("the newest turn must survive")
	}
	if len(f.prompts) != 1 || !strings.Contains(f.prompts[0], "<transcript>") || !strings.Contains(f.prompts[0], "User: سوال") {
		t.Errorf("summarizer prompt: %v", f.prompts)
	}

	for i := 0; i < 3; i++ {
		m.Save(ctx, 1, "دوباره "+long, "باز "+long)
	}
	c, _ = m.Load(ctx, 1)
	if c.Compressions < 2 || !strings.Contains(f.prompts[len(f.prompts)-1], "کاربر اسمش علیه") {
		t.Errorf("rolling summary not used: %+v", c)
	}
}

func TestCompressionFallbackAndReset(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	f := &fakeLLM{fail: true}
	m := New(s, f, func() int { return 2000 })
	long := strings.Repeat("متن ", 200)
	for i := 0; i < 6; i++ {
		m.Save(ctx, 2, long, long)
	}
	c, _ := m.Load(ctx, 2)
	if c.Tokens > 2000 || strings.Count(c.Summary, "dropped") != 1 {
		t.Fatalf("the budget must hold even when the summarizer fails: tokens=%d summary=%q", c.Tokens, c.Summary)
	}

	m.Save(ctx, 4, strings.Repeat("خیلی ", 5000), "ok")
	if c, _ := m.Load(ctx, 4); c.Tokens > 2000/2+10 {
		t.Errorf("a single huge turn must be capped: %d tokens", c.Tokens)
	}

	f.fail = false
	m.budget = func() int { return 1 << 30 }
	for i := 0; i < 4; i++ {
		m.Save(ctx, 3, "a"+long, "b"+long)
	}
	if ok, err := m.Compress(ctx, 3); !ok || err != nil {
		t.Fatalf("forced compression: %v %v", ok, err)
	}
	c, _ = m.Load(ctx, 3)
	if c.Messages > keepRecent || c.Summary == "" {
		t.Errorf("forced: %+v", c)
	}
	if err := m.Reset(ctx, 3); err != nil {
		t.Fatal(err)
	}
	c, _ = m.Load(ctx, 3)
	if c.Messages != 0 || c.Summary != "" || c.Tokens != 0 {
		t.Errorf("reset: %+v", c)
	}
}

func TestManyShortTurnsAreFoldedNowAndThen(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	m := New(s, &fakeLLM{}, func() int { return DefaultBudget })
	start, folds := "", 0
	for i := range maxTurns {
		compressed, err := m.Save(ctx, 9, fmt.Sprintf("سلام %d", i), "سلام!")
		if err != nil {
			t.Fatal(err)
		}
		c, _ := m.Load(ctx, 9)
		if c.Messages > maxTurns {
			t.Fatalf("%d turns kept", c.Messages)
		}
		if compressed {
			folds++
			if c.Messages > keepTurns || c.History[0].Role != "user" {
				t.Fatalf("a fold keeps at most %d turns from a user turn: %d", keepTurns, c.Messages)
			}
		} else if start != "" && c.History[0].Content != start {
			t.Fatalf("turn %d: the start moved without a fold", i)
		}
		start = c.History[0].Content
	}
	if folds < 2 || folds > 6 {
		t.Errorf("%d folds in %d turns", folds, 2*maxTurns)
	}
}

func TestEachUserHasTheirOwnBudget(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	m := New(s, &fakeLLM{}, func() int { return 32000 })

	m.UserBudget = func(_ context.Context, userID int64) int {
		if userID == 2 {
			return 1000
		}
		return 0
	}
	long := strings.Repeat("این یک پیام طولانی است. ", 20)
	for _, uid := range []int64{1, 2} {
		for i := 0; i < 3; i++ {
			_, _ = m.Save(ctx, uid, "سوال "+long, "جواب "+long)
		}
	}
	big, _ := m.Load(ctx, 1)
	small, _ := m.Load(ctx, 2)
	if big.Budget != 32000 || big.Compressions != 0 || small.Budget != 1000 || small.Compressions != 1 {
		t.Errorf("budgets: %d/%d compressions, %d/%d", big.Budget, small.Budget, big.Compressions, small.Compressions)
	}
}

func TestABriefOutageKeepsTheTurns(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	f := &fakeLLM{fail: true}
	m := New(s, f, func() int { return 1000 })
	long := strings.Repeat("این یک پیام طولانی است. ", 20)
	for i := 0; i < 3; i++ {
		m.Save(ctx, 5, "سوال "+long, "جواب "+long)
	}
	c, _ := m.Load(ctx, 5)
	if c.Messages != 6 || strings.Contains(c.Summary, "dropped") {
		t.Fatalf("the turns stay while the summarizer is down: messages=%d summary=%q", c.Messages, c.Summary)
	}
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()
	if compressed, err := m.Save(ctx, 5, "باز "+long, "خب "+long); err != nil || !compressed {
		t.Fatalf("the next save summarizes: %v %v", compressed, err)
	}
	c, _ = m.Load(ctx, 5)
	if c.Summary == "" || strings.Contains(c.Summary, "dropped") || c.Tokens > 1000 {
		t.Errorf("summarized, nothing dropped: %+v", c)
	}
}
