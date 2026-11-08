package main

import (
	"io/fs"
	"path/filepath"
	"sync"
	"time"
)

// What the users' workspaces take is found by walking every file, which on
// a busy server (thousands of workspaces, build caches, installed
// packages) takes minutes: the walk runs in the background and the panels
// show its last result at once.

const usageFresh = 10 * time.Minute

// diskUsage is the last measure of a folder.
type diskUsage struct {
	Size   int64
	Files  int
	At     time.Time     // when it finished (zero: never)
	Took   time.Duration // how long it took
	Walked bool          // a measure is running now
}

type usageCache struct {
	mu   sync.Mutex
	last diskUsage
}

// get returns the last measure and starts a new one in the background when
// it is older than maxAge and none is running.
func (c *usageCache) get(root string, maxAge time.Duration, measure func(string) (int64, int)) diskUsage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.last.Walked && (c.last.At.IsZero() || time.Since(c.last.At) > maxAge) {
		c.last.Walked = true
		go func() {
			start := time.Now()
			size, files := measure(root)
			c.mu.Lock()
			c.last = diskUsage{Size: size, Files: files, At: time.Now(), Took: time.Since(start)}
			c.mu.Unlock()
		}()
	}
	return c.last
}

// dirUsage walks a folder, counting its regular files and their bytes.
func dirUsage(root string) (size int64, files int) {
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			size += info.Size()
			files++
		}
		return nil
	})
	return size, files
}
