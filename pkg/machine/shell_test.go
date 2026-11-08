package machine

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A server started in the background keeps running, and the user's next
// commands reach it on localhost: the session's shell.
func TestBackgroundJobsShareTheShell(t *testing.T) {
	s := newTestMachine(t)
	ctx := context.Background()
	_ = s.WriteFile("dev", "site/hello.txt", []byte("hello from the server"))
	job, err := s.StartJob(ctx, "dev", "cd site && exec python3 -m http.server 8765 --bind 127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != 1 || !job.Running || job.Log != JobsDir+"/1.log" {
		t.Fatalf("job: %+v", job)
	}
	res := s.Run(ctx, "dev", RunOptions{Shell: `test -n "$WAZE_SHELL" || exit 9; for i in $(seq 1 50); do curl -s localhost:8765/hello.txt && exit 0; sleep 0.1; done; exit 7`,
		InSession: true, Timeout: 20 * time.Second})
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "hello from the server") {
		t.Fatalf("a later command reaches the server on localhost: %+v", res)
	}
	// run_code's way: argv, stdin and stderr apart.
	res = s.Run(ctx, "dev", RunOptions{Args: []string{"python3", "-c", "import sys; print(sys.stdin.read().upper()); print('oops', file=sys.stderr)"},
		Stdin: []byte("abc"), InSession: true, Timeout: 10 * time.Second})
	if res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "ABC" || strings.TrimSpace(res.Stderr) != "oops" {
		t.Errorf("argv, stdin and stderr: %+v", res)
	}
	// What a command leaves behind ends with it; a timeout stops it.
	_ = s.Run(ctx, "dev", RunOptions{Shell: "sleep 301 & echo left", InSession: true, Timeout: 10 * time.Second})
	if res := s.Run(ctx, "dev", RunOptions{Shell: "pgrep -f '[s]leep 301' || echo none", InSession: true, Timeout: 10 * time.Second}); strings.TrimSpace(res.Stdout) != "none" {
		t.Errorf("a command's leftovers end with it: %q", res.Stdout)
	}
	if res := s.Run(ctx, "dev", RunOptions{Shell: "sleep 30", InSession: true, Timeout: time.Second}); res.ExitCode != 124 {
		t.Errorf("timeout: %+v", res)
	}
	// The log, the list, and stopping a job.
	log, _, err := s.ReadTail("dev", job.Log, 4096)
	if err != nil || !strings.Contains(string(log), "GET /hello.txt") {
		t.Errorf("the job's output: %q %v", log, err)
	}
	if err := s.KillJob(ctx, "dev", 1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { j := s.Jobs("dev"); return len(j) == 1 && !j[0].Running })
	if res := s.Run(ctx, "dev", RunOptions{Shell: "curl -s -m 2 localhost:8765/hello.txt || echo down", InSession: true, Timeout: 10 * time.Second}); !strings.Contains(res.Stdout, "down") {
		t.Errorf("a stopped server is gone: %q", res.Stdout)
	}
	// A server tool never runs in the user's shell.
	if res := s.Run(ctx, "dev", RunOptions{Shell: "test -n \"$WAZE_SHELL\" && echo shell || echo own", Timeout: 10 * time.Second}); strings.TrimSpace(res.Stdout) != "own" {
		t.Errorf("a server tool runs on its own: %q", res.Stdout)
	}
	if !s.StopShell("dev") || s.Jobs("dev")[0].Running {
		t.Error("the shell stops")
	}
	if s.ShellEnded("dev") == "" {
		t.Error("and says why")
	}
	if res := s.Run(ctx, "dev", RunOptions{Shell: "echo again", InSession: true, Timeout: 10 * time.Second}); strings.TrimSpace(res.Stdout) != "again" {
		t.Errorf("commands run on their own again: %+v", res)
	}
}

func TestBackgroundJobLimits(t *testing.T) {
	s := newTestMachine(t)
	ctx := context.Background()
	for i := 0; i < MaxJobs; i++ {
		if _, err := s.StartJob(ctx, "many", "sleep 60"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.StartJob(ctx, "many", "sleep 60"); err == nil {
		t.Error("at most MaxJobs at once")
	}
	if err := s.KillJob(ctx, "many", 99); err == nil {
		t.Error("an unknown job")
	}
	s.StopShell("many")
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting")
}
