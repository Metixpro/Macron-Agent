package guard

import (
	"context"
	"log"
	"runtime/debug"
	"time"
)

// Recover, deferred at the top of a goroutine, turns a panic there into a log
// line. A panic in a goroutine that nobody recovers ends the whole process, and
// with it every job that was running.
func Recover(what string) {
	if p := recover(); p != nil {
		log.Printf("panic in %s: %v\n%s", what, p, debug.Stack())
	}
}

// Failed is Recover for work that reports an error: the panic becomes *err.
func Failed(what string, err *error) {
	if p := recover(); p != nil {
		log.Printf("panic in %s: %v\n%s", what, p, debug.Stack())
		*err = &Crash{What: what, Value: p}
	}
}

type Crash struct {
	What  string
	Value any
}

func (c *Crash) Error() string { return c.What + " crashed unexpectedly" }

var restartAfter = 5 * time.Second

// KeepRunning runs one of the long-lived loops (the scheduler, housekeeping) and
// starts it again when it panics; it returns when the loop itself returns.
func KeepRunning(ctx context.Context, what string, loop func(context.Context)) {
	for {
		finished := func() (ok bool) {
			defer Recover(what)
			loop(ctx)
			return true
		}()
		if finished {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(restartAfter):
		}
	}
}
