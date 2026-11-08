package guard

import (
	"context"
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	r := NewRateLimiter(3, time.Minute)
	base := time.Unix(1000, 0)
	r.now = func() time.Time { return base }
	for i := 0; i < 3; i++ {
		if ok, _ := r.Allow("u"); !ok {
			t.Fatalf("event %d should pass", i)
		}
	}
	ok, wait := r.Allow("u")
	if ok || wait <= 0 || wait > time.Minute {
		t.Fatalf("4th event must be refused with a wait, got ok=%v wait=%v", ok, wait)
	}
	if ok, _ := r.Allow("other"); !ok {
		t.Fatal("other keys are independent")
	}
	r.now = func() time.Time { return base.Add(61 * time.Second) }
	if ok, _ := r.Allow("u"); !ok {
		t.Fatal("window should have expired")
	}
	if ok, _ := NewRateLimiter(0, time.Minute).Allow("x"); !ok {
		t.Fatal("limit 0 means unlimited")
	}
}

func TestGate(t *testing.T) {
	g := NewGate(2)
	rel1, why := g.TryAcquire("a")
	if why != Acquired {
		t.Fatal("first acquire")
	}
	if _, why := g.TryAcquire("a"); why != KeyBusy {
		t.Fatal("same key must be busy")
	}
	rel2, _ := g.TryAcquire("b")
	if _, why := g.TryAcquire("c"); why != AtCapacity {
		t.Fatal("capacity should be reached")
	}
	rel1()
	rel1()
	if _, why := g.TryAcquire("c"); why != Acquired {
		t.Fatal("slot should be free after release")
	}
	rel2()
	if g.Busy("b") {
		t.Fatal("b released")
	}
}

func TestGateQueueAndReservedSlots(t *testing.T) {
	g := NewGate(3)
	g.SetReserved(1)
	ctx := context.Background()
	r1, why := g.Acquire(ctx, "a", Normal, 0, nil)
	r2, _ := g.Acquire(ctx, "b", Normal, 0, nil)
	if why != Acquired || r2 == nil {
		t.Fatal("two normal slots")
	}
	if _, why := g.Acquire(ctx, "c", Normal, 0, nil); why != AtCapacity {
		t.Fatal("the last slot is kept for High")
	}
	r3, why := g.Acquire(ctx, "vip1", High, 0, nil)
	if why != Acquired {
		t.Fatal("High takes the reserved slot")
	}

	order := make(chan string, 2)
	wait := func(key string, prio Priority) {
		r, why := g.Acquire(ctx, key, prio, 5*time.Second, nil)
		if why != Acquired {
			t.Errorf("%s: %v", key, why)
			return
		}
		order <- key
		r()
	}
	go wait("normal", Normal)
	for g.Waiting() < 1 {
		time.Sleep(time.Millisecond)
	}
	ahead := make(chan int, 1)
	go func() {
		r, why := g.Acquire(ctx, "vip2", High, 5*time.Second, func(n int) { ahead <- n })
		if why == Acquired {
			order <- "vip2"
			r()
		}
	}()
	for g.Waiting() < 2 {
		time.Sleep(time.Millisecond)
	}
	if n := <-ahead; n != 0 {
		t.Errorf("nobody is ahead of the High: %d", n)
	}
	if _, why := g.Acquire(ctx, "normal", Normal, 0, nil); why != KeyBusy {
		t.Error("a waiting key is busy")
	}
	r1()
	if first := <-order; first != "vip2" {
		t.Errorf("High first, got %s", first)
	}
	r2()
	if second := <-order; second != "normal" {
		t.Errorf("then Normal, got %s", second)
	}
	r3()

	h1, _ := g.Acquire(ctx, "x", High, 0, nil)
	h2, _ := g.Acquire(ctx, "y", High, 0, nil)
	h3, _ := g.Acquire(ctx, "z", High, 0, nil)
	if _, why := g.Acquire(ctx, "late", High, 20*time.Millisecond, nil); why != AtCapacity || g.Waiting() != 0 {
		t.Errorf("timed out: %v, %d waiting", why, g.Waiting())
	}
	h1()
	h2()
	h3()
	if r, why := g.TryAcquire("late"); why != Acquired {
		t.Error("the key is free again")
	} else {
		r()
	}
}
