// Package guard provides small abuse-protection primitives shared by the
// Telegram bot and the REST server.
package guard

import (
	"context"
	"sync"
	"time"
)

// RateLimiter is a sliding-window limiter: at most Limit events per Window per key.
type RateLimiter struct {
	Limit  int
	Window time.Duration

	mu   sync.Mutex
	hits map[string][]time.Time
	last time.Time
	now  func() time.Time
}

// NewRateLimiter creates a limiter. A Limit <= 0 allows everything.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{Limit: limit, Window: window, hits: make(map[string][]time.Time), now: time.Now}
}

// Allow records an event for key. When refused it returns how long to wait.
func (r *RateLimiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	if r == nil || r.Limit <= 0 {
		return true, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if now.Sub(r.last) > r.Window {
		r.sweep(now)
		r.last = now
	}
	cutoff := now.Add(-r.Window)
	h := r.hits[key]
	i := 0
	for i < len(h) && !h[i].After(cutoff) {
		i++
	}
	h = h[i:]
	if len(h) >= r.Limit {
		r.hits[key] = h
		return false, h[0].Add(r.Window).Sub(now)
	}
	r.hits[key] = append(h, now)
	return true, 0
}

func (r *RateLimiter) sweep(now time.Time) {
	cutoff := now.Add(-r.Window)
	for k, h := range r.hits {
		if len(h) == 0 || !h[len(h)-1].After(cutoff) {
			delete(r.hits, k)
		}
	}
}

// Gate allows one holder per key at a time and caps total holders. With a
// cap, some slots can be kept for high-priority holders, and Acquire waits
// for a slot in a queue where high priority goes first.
type Gate struct {
	mu       sync.Mutex
	busy     map[string]struct{}
	total    int
	max      int
	reserved int // slots only High may take
	queue    []*waiter
	seq      uint64
}

// Priority orders the holders waiting for a slot.
type Priority int

const (
	Normal Priority = iota
	High
)

type waiter struct {
	key     string
	prio    Priority
	seq     uint64
	ready   chan struct{}
	granted bool
}

// NewGate creates a Gate; max <= 0 means no global cap.
func NewGate(max int) *Gate {
	return &Gate{busy: make(map[string]struct{}), max: max}
}

// Max is the cap (0: none).
func (g *Gate) Max() int { return g.max }

// SetReserved keeps n of the slots for High (never all of them).
func (g *Gate) SetReserved(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reserved = min(max(n, 0), max(g.max-1, 0))
}

// Reason explains why TryAcquire failed.
type Reason int

const (
	Acquired   Reason = iota
	KeyBusy           // this key already has a running job
	AtCapacity        // too many jobs overall
)

// canTake reports a free slot for prio (with g.mu held).
func (g *Gate) canTake(prio Priority) bool {
	if g.max <= 0 {
		return true
	}
	if prio == High {
		return g.total < g.max
	}
	return g.total < g.max-g.reserved
}

// ahead counts the waiters served before one of prio (with g.mu held).
func (g *Gate) ahead(prio Priority) int {
	n := 0
	for _, w := range g.queue {
		if w.prio >= prio {
			n++
		}
	}
	return n
}

// TryAcquire reserves key at normal priority without waiting. The returned
// release must be called exactly once.
func (g *Gate) TryAcquire(key string) (release func(), why Reason) {
	return g.Acquire(context.Background(), key, Normal, 0, nil)
}

// Acquire reserves key. When every slot it may take is held, it waits up
// to wait in the queue (High before Normal, then first come first served);
// queued hears how many are ahead when it starts waiting. The returned
// release must be called exactly once.
func (g *Gate) Acquire(ctx context.Context, key string, prio Priority, wait time.Duration, queued func(ahead int)) (release func(), why Reason) {
	g.mu.Lock()
	if _, ok := g.busy[key]; ok {
		g.mu.Unlock()
		return nil, KeyBusy
	}
	if g.ahead(prio) == 0 && g.canTake(prio) {
		g.busy[key] = struct{}{}
		g.total++
		g.mu.Unlock()
		return g.releaser(key), Acquired
	}
	if wait <= 0 {
		g.mu.Unlock()
		return nil, AtCapacity
	}
	g.seq++
	w := &waiter{key: key, prio: prio, seq: g.seq, ready: make(chan struct{})}
	n := g.ahead(prio)
	g.queue = append(g.queue, w)
	g.busy[key] = struct{}{} // the same key can't queue twice
	g.mu.Unlock()
	if queued != nil {
		queued(n)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-w.ready:
		return g.releaser(key), Acquired
	case <-ctx.Done():
	case <-timer.C:
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if w.granted { // the slot came just as the wait ended
		return g.releaser(key), Acquired
	}
	g.drop(w)
	delete(g.busy, key)
	g.wakeLocked() // a Normal waiter may be eligible now that this one left
	return nil, AtCapacity
}

func (g *Gate) drop(w *waiter) {
	for i, x := range g.queue {
		if x == w {
			g.queue = append(g.queue[:i], g.queue[i+1:]...)
			return
		}
	}
}

// wakeLocked hands free slots to the best waiters (with g.mu held).
func (g *Gate) wakeLocked() {
	for {
		var best *waiter
		for _, w := range g.queue {
			if !g.canTake(w.prio) {
				continue
			}
			if best == nil || w.prio > best.prio || (w.prio == best.prio && w.seq < best.seq) {
				best = w
			}
		}
		if best == nil {
			return
		}
		g.drop(best)
		g.total++
		best.granted = true
		close(best.ready)
	}
}

func (g *Gate) releaser(key string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			delete(g.busy, key)
			g.total--
			g.wakeLocked()
			g.mu.Unlock()
		})
	}
}

// Waiting counts the holders in the queue.
func (g *Gate) Waiting() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.queue)
}

// Busy reports whether key currently holds the gate.
func (g *Gate) Busy(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.busy[key]
	return ok
}
