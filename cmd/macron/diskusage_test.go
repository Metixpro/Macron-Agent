package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The panel never waits for the walk of the workspaces: it shows the last
// measure, and a new one runs in the background (one at a time).
func TestUsageIsMeasuredInTheBackground(t *testing.T) {
	var c usageCache
	var runs atomic.Int32
	release := make(chan struct{})
	measure := func(string) (int64, int) {
		runs.Add(1)
		<-release
		return 1234, 7
	}
	start := time.Now()
	first := c.get("/x", time.Minute, measure)
	if !first.At.IsZero() || time.Since(start) > time.Second {
		t.Fatalf("the first call answers at once with nothing measured yet: %+v", first)
	}
	if again := c.get("/x", time.Minute, measure); !again.Walked {
		t.Errorf("while it walks, callers see that: %+v", again)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	var got diskUsage
	for time.Now().Before(deadline) {
		if got = c.get("/x", time.Minute, measure); !got.At.IsZero() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.Size != 1234 || got.Files != 7 || runs.Load() != 1 {
		t.Errorf("measured once: %+v (%d walks)", got, runs.Load())
	}
	if c.get("/x", time.Minute, measure); runs.Load() != 1 {
		t.Error("a fresh measure is kept")
	}
	c.mu.Lock()
	c.last.At = time.Now().Add(-time.Hour)
	c.mu.Unlock()
	if old := c.get("/x", time.Minute, measure); old.Size != 1234 {
		t.Errorf("a stale measure is shown while the new one runs: %+v", old)
	}
	for i := 0; i < 100 && runs.Load() < 2; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if runs.Load() != 2 {
		t.Errorf("and a stale one is measured again: %d", runs.Load())
	}
}

func TestDirUsageCountsFiles(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "a", "b", "f"), make([]byte, 100), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "g"), make([]byte, 50), 0o644)
	_ = os.Symlink("/etc/passwd", filepath.Join(dir, "link"))
	if size, files := dirUsage(dir); size != 150 || files != 2 {
		t.Errorf("%d bytes, %d files (links are not followed)", size, files)
	}
}

// The stats button answers even while the workspaces are still being measured.
func TestStatsButtonDoesNotWaitForTheDisk(t *testing.T) {
	h := newHarness(t)
	h.send(private(55, "سلام"))
	h.bot.usage.mu.Lock()
	h.bot.usage.last = diskUsage{Walked: true} // a walk that never ends
	h.bot.usage.mu.Unlock()
	h.tg.reset()
	start := time.Now()
	h.click(ownerID, ownerID, 9, "a:stats")
	if time.Since(start) > 10*time.Second {
		t.Fatalf("the stats took %s", time.Since(start))
	}
	v := lastOf(h, "editMessageText")
	if txt := v.body["text"].(string); !strings.Contains(txt, "آمار کامل") || !strings.Contains(txt, "دارم اندازه می‌گیرم") {
		t.Errorf("the panel shows the figures and says the disk is being measured: %v", txt)
	}
}
