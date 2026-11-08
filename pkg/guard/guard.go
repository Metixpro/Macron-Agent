package guard

import (
	"context"
	"sync"
	"time"
)

type RateLimiter struct {
	Limit  int
	Window time.Duration

	mu   sync.Mutex
	hits map[string][]time.Time
	last time.Time
	now  func() time.Time
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{Limit: limit, Window: window, hits: make(map[string][]time.Time), now: time.Now}
}

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

type Gate struct {
	mu       sync.Mutex
	busy     map[string]struct{}
	total    int
	max      int
	reserved int
	queue    []*waiter
	seq      uint64
}

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

func NewGate(max int) *Gate {
	return &Gate{busy: make(map[string]struct{}), max: max}
}

func (g *Gate) Max() int { return g.max }

func (g *Gate) SetReserved(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reserved = min(max(n, 0), max(g.max-1, 0))
}

type Reason int

const (
	Acquired Reason = iota
	KeyBusy
	AtCapacity
)

func (g *Gate) canTake(prio Priority) bool {
	if g.max <= 0 {
		return true
	}
	if prio == High {
		return g.total < g.max
	}
	return g.total < g.max-g.reserved
}

func (g *Gate) ahead(prio Priority) int {
	n := 0
	for _, w := range g.queue {
		if w.prio >= prio {
			n++
		}
	}
	return n
}

func (g *Gate) TryAcquire(key string) (release func(), why Reason) {
	return g.Acquire(context.Background(), key, Normal, 0, nil)
}

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
	g.busy[key] = struct{}{}
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
	if w.granted {
		return g.releaser(key), Acquired
	}
	g.drop(w)
	delete(g.busy, key)
	g.wakeLocked()
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

func (g *Gate) Waiting() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.queue)
}

func (g *Gate) Busy(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.busy[key]
	return ok
}
