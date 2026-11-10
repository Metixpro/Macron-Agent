package guard

import (
	"context"
	"log"
	"runtime/debug"
	"time"
)

func Recover(what string) {
	if p := recover(); p != nil {
		log.Printf("panic in %s: %v\n%s", what, p, debug.Stack())
	}
}

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
