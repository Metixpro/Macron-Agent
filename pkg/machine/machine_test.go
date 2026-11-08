package machine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func newTestMachine(t *testing.T) *Machine {
	t.Helper()
	s, err := New(Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSafeSessionID(t *testing.T) {
	cases := map[string]string{
		"tg-123":   "tg-123",
		"":         "default",
		"user_1-A": "user_1-A",
	}
	for in, want := range cases {
		if got := SafeSessionID(in); got != want {
			t.Errorf("SafeSessionID(%q) = %q, want %q", in, got, want)
		}
	}
	for _, evil := range []string{".", "..", "../x", "a/b", "tg-1/../tg-2", "x\x00y", strings.Repeat("a", 65), "نام"} {
		got := SafeSessionID(evil)
		if strings.ContainsAny(got, "/\\") || got == "." || got == ".." || !strings.HasPrefix(got, "h.") {
			t.Errorf("SafeSessionID(%q) = %q is not a hashed safe name", evil, got)
		}
	}
	if SafeSessionID("a/b") == SafeSessionID("a_b") {
		t.Error("distinct ids must not collide")
	}
}

func TestListDepthAndLimit(t *testing.T) {
	s := newTestMachine(t)
	_ = s.WriteFile("l", "top.txt", []byte("x"))
	_ = s.WriteFile("l", "d1/mid.txt", []byte("x"))
	_ = s.WriteFile("l", "d1/d2/deep.txt", []byte("x"))
	got, err := s.List("l", 2, 100)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range got {
		names[e.Name] = true
	}
	if !names["top.txt"] || !names["d1/mid.txt"] || names["d1/d2/deep.txt"] {
		t.Fatalf("unexpected listing: %v", names)
	}
	if got, _ := s.List("l", 5, 2); len(got) != 2 {
		t.Fatalf("maxEntries not honoured: %d", len(got))
	}
}

func TestSweepExpiresOldFiles(t *testing.T) {
	s := newTestMachine(t)
	past := time.Now().Add(-25 * time.Hour)
	_ = s.WriteFile("u1", "old.mp3", []byte("x"))
	_ = s.WriteFile("u1", "fresh.txt", []byte("x"))
	_ = s.WriteFile("u1", "dir/old.pdf", []byte("x"))
	_ = s.WriteFile("u2", "old.png", []byte("x"))
	ws1, _ := s.Workspace("u1")
	ws2, _ := s.Workspace("u2")
	for _, p := range []string{filepath.Join(ws1, "old.mp3"), filepath.Join(ws1, "dir", "old.pdf"), filepath.Join(ws2, "old.png")} {
		_ = os.Chtimes(p, past, past)
	}
	// An old symlink is removed, its target outside the workspace is not touched.
	target := filepath.Join(t.TempDir(), "keep")
	_ = os.WriteFile(target, []byte("host"), 0o600)
	_ = os.Symlink(target, filepath.Join(ws1, "link"))
	_ = os.Chtimes(target, past, past)
	tv := []unix.Timeval{unix.NsecToTimeval(past.UnixNano()), unix.NsecToTimeval(past.UnixNano())}
	_ = unix.Lutimes(filepath.Join(ws1, "link"), tv)

	files, dirs := s.sweep(24 * time.Hour)
	if files != 4 || dirs != 1 {
		t.Fatalf("sweep removed %d files and %d workspaces, want 4 and 1", files, dirs)
	}
	if _, err := s.Stat("u1", "fresh.txt"); err != nil {
		t.Error("fresh files must stay")
	}
	if _, err := os.Stat(filepath.Join(ws1, "dir")); !os.IsNotExist(err) {
		t.Error("emptied sub-directories are removed")
	}
	if _, err := os.Stat(ws2); !os.IsNotExist(err) {
		t.Error("an empty workspace is removed")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("the symlink target outside the workspace was deleted")
	}

	// Busy workspaces are left alone.
	_ = s.WriteFile("busy", "old.txt", []byte("x"))
	wsb, _ := s.Workspace("busy")
	_ = os.Chtimes(filepath.Join(wsb, "old.txt"), past, past)
	s.begin(wsb)
	if f, _ := s.sweep(24 * time.Hour); f != 0 {
		t.Error("a running workspace must not be swept")
	}
	s.end(wsb)
	if f, _ := s.sweep(24 * time.Hour); f != 1 {
		t.Error("swept after the command finished")
	}
}

func TestMergedOutputKeepsTerminalOrder(t *testing.T) {
	s := newTestMachine(t)
	res := s.Run(context.Background(), "merge", RunOptions{Shell: "echo a; echo b >&2; echo c", MergeOutput: true, Timeout: 10 * time.Second})
	if res.Stdout != "a\nb\nc\n" || res.Stderr != "" {
		t.Errorf("merged output: %q / %q", res.Stdout, res.Stderr)
	}
}

// A command that prints a lot keeps its start and its end (the error of a
// build, the result line of a script), with a mark where the middle went.
func TestLongOutputKeepsTheEnd(t *testing.T) {
	w := &capWriter{max: 1000}
	w.Write([]byte("START\n"))
	for i := 0; i < 500; i++ {
		w.Write([]byte(strings.Repeat("noise ", 5) + "\n"))
	}
	w.Write([]byte(`{"result":"ok"}` + "\n"))
	out := w.String()
	if !strings.HasPrefix(out, "START") || !strings.HasSuffix(out, `{"result":"ok"}`+"\n") || !strings.Contains(out, "cut from the middle") || !w.truncated {
		t.Fatalf("head and tail: %q", out)
	}
	if len(out) > 1100 {
		t.Errorf("it stays near its size: %d", len(out))
	}
	big := &capWriter{max: 1000}
	big.Write(append([]byte(strings.Repeat("a", 5000)), "END"...))
	if out := big.String(); !strings.HasSuffix(out, "END") || !strings.HasPrefix(out, "aaa") {
		t.Errorf("one huge write: %q", out[len(out)-20:])
	}
	small := &capWriter{max: 1000}
	small.Write([]byte("hello"))
	small.Write([]byte(" world"))
	if small.String() != "hello world" || small.truncated {
		t.Errorf("short output is untouched: %q", small.String())
	}
}

func TestSubSessionsLiveInsideTheirParent(t *testing.T) {
	sb := newTestMachine(t)
	sub := SubSession("tg-1", "d1")
	if ParentSession(sub) != "tg-1" || SubPath(sub) != ".agents/d1" || ParentSession("tg-1") != "tg-1" || SubPath("tg-1") != "" {
		t.Fatal("names")
	}
	parent, _ := sb.Workspace("tg-1")
	ws, _ := sb.Workspace(sub)
	if ws != filepath.Join(parent, ".agents", "d1") {
		t.Fatalf("the sub-workspace is %s", ws)
	}
}

// A command runs on the machine: in the work folder, with the user's own
// environment minus the agent's secrets, with no limits of its own.
func TestCommandsRunOnTheMachine(t *testing.T) {
	s := newTestMachine(t)
	ws, _ := s.Workspace("tg-9")
	res := s.Run(context.Background(), "tg-9", RunOptions{Shell: "echo hi > /workspace/a.txt && cat /workspace/a.txt && pwd", Timeout: 10 * time.Second})
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "hi") || !strings.Contains(res.Stdout, ws) {
		t.Fatalf("run: %+v", res)
	}
	if b, err := os.ReadFile(filepath.Join(ws, "a.txt")); err != nil || strings.TrimSpace(string(b)) != "hi" {
		t.Fatalf("/workspace is the work folder: %q %v", b, err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	res = s.Run(context.Background(), "tg-9", RunOptions{Shell: "echo out > " + outside + " && cat " + outside})
	if !strings.Contains(res.Stdout, "out") {
		t.Fatalf("anywhere on the machine: %+v", res)
	}
	res = s.Run(context.Background(), "tg-9", RunOptions{Shell: "sleep 5", Timeout: 300 * time.Millisecond})
	if res.ExitCode != 124 || !strings.Contains(res.Error, "timed out") {
		t.Fatalf("a timeout is a timeout: %+v", res)
	}
	t.Setenv("MACRON_TELEGRAM_BOT_TOKEN", "must-not-leak")
	t.Setenv("MY_API_KEY", "must-not-leak")
	t.Setenv("HARMLESS_SETTING", "visible")
	res = s.Run(context.Background(), "tg-9", RunOptions{Shell: "env"})
	if strings.Contains(res.Stdout, "must-not-leak") || !strings.Contains(res.Stdout, "HARMLESS_SETTING=visible") {
		t.Fatalf("the user's environment, without the agent's secrets:\n%s", res.Stdout)
	}
}
