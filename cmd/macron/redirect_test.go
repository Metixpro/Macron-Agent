package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/store"
)

func TestPrivateCorrectionCancelsOnlyModelAndContinuesSameTurn(t *testing.T) {
	h := newHarness(t)
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			close(started)
			<-release
			return map[string]any{"role": "assistant", "content": "پاسخ قدیمی"}, 0
		}
		return map[string]any{"role": "assistant", "content": "Here is the concise English report."}, 0
	}
	first := private(65, "گزارش را فارسی بنویس")
	wait := h.background(first)
	<-started
	h.bot.handleMessage(followUpMsg(first, 78, "انگلیسی بنویس و کوتاه باشد"))
	wait()
	if h.llm.count() != 2 {
		t.Fatalf("expected redirected request, got %d", h.llm.count())
	}
	req := llmText(h.llm.last())
	if !strings.Contains(req, "گزارش را فارسی بنویس") || !strings.Contains(req, "انگلیسی بنویس") {
		t.Fatal("redirect lost original task or correction")
	}
	if used, _ := h.st.UsedToday(bg, 65); used != 1 {
		t.Fatalf("correction must stay one turn, got %d", used)
	}
	h.wantText("Here is the concise English report.")
	h.noText("پاسخ قدیمی")
	h.noText(textCancelled)
}

func TestSteeringDuringToolKeepsToolAliveAndDelegationQueues(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newRun(1, private(1, "task"), cancel, nil, tierOwner)
	request, done := r.modelRequest(parent)
	if done() {
		t.Fatal("unexpected redirect")
	}
	if request.Err() == nil {
		t.Fatal("finished model request should be cleaned up")
	}
	x := &interjection{text: "correction", msg: llm.Message{Role: "user", Content: "correction"}}
	if !r.steer(x) || parent.Err() != nil {
		t.Fatal("steering cancelled tool context")
	}
	if xs := r.take(); len(xs) != 1 || xs[0] != x {
		t.Fatal("tool boundary lost guidance")
	}
	r.delegation(1)
	if r.steer(x) {
		t.Fatal("delegation must defer new messages")
	}
	if ok, queued := r.push(x); !ok || !queued {
		t.Fatal("delegation fallback did not queue")
	}
	if len(r.take()) != 0 {
		t.Fatal("queued message leaked into delegated task")
	}
	r.delegation(-1)
	r.stop()
	if xs := r.finish(); len(xs) != 1 || xs[0] != x {
		t.Fatal("stop lost deferred message")
	}
}

func TestCorrectionsAtRequestBoundaryAreNotLost(t *testing.T) {
	r := newRun(1, private(1, "task"), func() {}, nil, tierOwner)
	if !r.steer(&interjection{text: "new", msg: llm.Message{Role: "user", Content: "new"}}) {
		t.Fatal("guidance refused")
	}
	ctx, done := r.modelRequest(context.Background())
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("request did not notice pending correction")
	}
	if !done() || len(r.take()) != 1 {
		t.Fatal("boundary correction disappeared")
	}
}

func TestStopDiscardsTaskCorrectionsButKeepsExplicitQueue(t *testing.T) {
	r := newRun(1, private(1, "task"), func() {}, nil, tierOwner)
	r.steer(&interjection{text: "change current task"})
	r.push(&interjection{text: "separate task"})
	r.stop()
	if r.steer(&interjection{text: "late correction"}) {
		t.Fatal("stopped task accepted guidance")
	}
	next := r.finish()
	if len(next) != 1 || next[0].text != "separate task" {
		t.Fatal("stop restarted a correction or lost explicit queue")
	}
}

func TestQueueStopWordsDoNotStopCurrentTask(t *testing.T) {
	h := newHarness(t)
	first := private(ownerID, "task")
	user := &store.User{ID: ownerID}
	var cancelled bool
	r := newRun(ownerID, first, func() { cancelled = true }, nil, tierOwner)
	h.bot.runs.Store(int64(ownerID), r)
	if !h.bot.interjectMode(user, followUpMsg(first, 78, "stop"), "stop", true) {
		t.Fatal("explicit queue refused")
	}
	if cancelled || r.stopped.Load() {
		t.Fatal("explicit queued text was mistaken for active stop")
	}
	if next := r.finish(); len(next) != 1 || next[0].text != "stop" {
		t.Fatal("queued text lost")
	}
}
