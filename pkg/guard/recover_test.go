package guard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRecoverKeepsTheProcessAlive(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer Recover("a test goroutine")
		panic("boom")
	}()
	wg.Wait()
}

func TestFailedTurnsAPanicIntoAnError(t *testing.T) {
	work := func() (err error) {
		defer Failed("the job", &err)
		panic("boom")
	}
	err := work()
	var crash *Crash
	if !errors.As(err, &crash) || crash.Value != "boom" {
		t.Fatalf("got %v", err)
	}
}

func TestKeepRunningStartsAPanickedLoopAgain(t *testing.T) {
	old := restartAfter
	restartAfter = time.Millisecond
	defer func() { restartAfter = old }()
	runs := 0
	KeepRunning(context.Background(), "a test loop", func(context.Context) {
		if runs++; runs < 3 {
			panic("again")
		}
	})
	if runs != 3 {
		t.Fatalf("the loop ran %d times", runs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	KeepRunning(ctx, "a stopped loop", func(context.Context) { panic("once") })
}
