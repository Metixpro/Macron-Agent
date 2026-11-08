package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOpenJobs(t *testing.T) {
	s := openTest(t, Options{})
	ctx := context.Background()
	const uid = 7

	if j, err := s.UnfinishedJob(ctx, uid); err != nil || j != nil {
		t.Fatalf("a new user has an unfinished job: %+v %v", j, err)
	}
	// A job that got done leaves nothing.
	if err := s.BeginJob(ctx, uid, 70, 1, "یه شعر بنویس", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishJob(ctx, uid, false); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.UnfinishedJob(ctx, uid); j != nil {
		t.Fatalf("a finished job is unfinished: %+v", j)
	}

	// A job that stops keeps its request, steps and why.
	if err := s.BeginJob(ctx, uid, 70, 2, "سایتمو بساز", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.JobProgress(ctx, uid, "Steps taken:\n1. write_file(index.html) → saved"); err != nil {
		t.Fatal(err)
	}
	if err := s.InterruptJob(ctx, uid, JobTimeout, ""); err != nil {
		t.Fatal(err)
	}
	j, err := s.UnfinishedJob(ctx, uid)
	if err != nil || j == nil || j.Request != "سایتمو بساز" || !strings.Contains(j.Notes, "index.html") || j.Reason != JobTimeout || j.MsgID != 2 || j.ChatID != 70 {
		t.Fatalf("unfinished job: %+v %v", j, err)
	}

	// Other requests done meanwhile count against it, and it is forgotten after a few.
	for i := 1; i <= JobForgetAfter; i++ {
		if err := s.BeginJob(ctx, uid, 70, int64(10+i), "یه سوال دیگه", ""); err != nil {
			t.Fatal(err)
		}
		if j, _ := s.UnfinishedJob(ctx, uid); j == nil || j.Later != i-1 {
			t.Fatalf("a new job must not touch the unfinished one: %+v", j)
		}
		if err := s.FinishJob(ctx, uid, false); err != nil {
			t.Fatal(err)
		}
	}
	if j, _ := s.UnfinishedJob(ctx, uid); j != nil {
		t.Fatalf("an unfinished job is kept after %d other requests: %+v", JobForgetAfter, j)
	}

	// Going on with it and finishing ends both.
	_ = s.BeginJob(ctx, uid, 70, 20, "ویدیو بساز", "")
	_ = s.InterruptJob(ctx, uid, JobStopped, "Steps taken:\n1. a")
	_ = s.BeginJob(ctx, uid, 70, 21, "ویدیو بساز", "Steps taken:\n1. a")
	if err := s.FinishJob(ctx, uid, true); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.UnfinishedJob(ctx, uid); j != nil {
		t.Fatalf("the continued job is done, its unfinished one too: %+v", j)
	}

	// A refusal leaves nothing to go on with, and does not count.
	_ = s.BeginJob(ctx, uid, 70, 30, "الف", "")
	_ = s.InterruptJob(ctx, uid, JobFailed, "")
	_ = s.BeginJob(ctx, uid, 70, 31, "ب", "")
	if err := s.DropJob(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.UnfinishedJob(ctx, uid); j == nil || j.Request != "الف" || j.Later != 0 {
		t.Fatalf("a dropped job changed the unfinished one: %+v", j)
	}

	// Notes are bounded (their end is kept).
	_ = s.BeginJob(ctx, uid, 70, 40, "ج", strings.Repeat("ق", maxJobNotes)+"END")
	_ = s.InterruptJob(ctx, uid, JobFailed, "")
	if j, _ := s.UnfinishedJob(ctx, uid); j == nil || len(j.Notes) > maxJobNotes+len("…") || !strings.HasSuffix(j.Notes, "END") {
		t.Fatalf("notes not clipped: %d bytes", len(j.Notes))
	}

	// Clearing the memory forgets it.
	if err := s.ClearMemory(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.UnfinishedJob(ctx, uid); j != nil {
		t.Fatalf("a cleared memory keeps the unfinished job: %+v", j)
	}
}

func TestOrphanedJobsAfterARestart(t *testing.T) {
	s := openTest(t, Options{})
	ctx := context.Background()
	_ = s.BeginJob(ctx, 1, 10, 100, "کار اول", "Steps taken:\n1. x")
	_ = s.BeginJob(ctx, 2, 20, 200, "کار دوم", "")
	_ = s.BeginJob(ctx, 3, 30, 300, "تموم شد", "")
	_ = s.FinishJob(ctx, 3, false)
	// The first one last ran two hours ago.
	ran := s.Now().Add(-2 * time.Hour)
	if _, err := s.db.ExecContext(ctx, `UPDATE open_jobs SET updated_at = ? WHERE user_id = 1`, ran); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.OrphanedJobs(ctx)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("orphaned: %+v %v", jobs, err)
	}
	j, _ := s.UnfinishedJob(ctx, 1)
	if j == nil || j.Reason != JobRestarted || j.Request != "کار اول" || j.MsgID != 100 || time.Since(j.UpdatedAt) < 90*time.Minute {
		t.Fatalf("after the restart: %+v", j)
	}
	if again, _ := s.OrphanedJobs(ctx); len(again) != 0 {
		t.Fatalf("found twice: %+v", again)
	}
}
