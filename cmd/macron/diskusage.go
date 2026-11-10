package main

import (
	"github.com/Metixpro/macron-agent/pkg/guard"
	"io/fs"
	"path/filepath"
	"sync"
	"time"
)

const usageFresh = 10 * time.Minute

type diskUsage struct {
	Size   int64
	Files  int
	At     time.Time
	Took   time.Duration
	Walked bool
}

type usageCache struct {
	mu   sync.Mutex
	last diskUsage
}

func (c *usageCache) get(root string, maxAge time.Duration, measure func(string) (int64, int)) diskUsage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.last.Walked && (c.last.At.IsZero() || time.Since(c.last.At) > maxAge) {
		c.last.Walked = true
		go func() {
			defer guard.Recover("diskusage")
			start := time.Now()
			size, files := measure(root)
			c.mu.Lock()
			c.last = diskUsage{Size: size, Files: files, At: time.Now(), Took: time.Since(start)}
			c.mu.Unlock()
		}()
	}
	return c.last
}

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
