//go:build windows

package tui

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/muesli/cancelreader"
	"golang.org/x/sys/windows"
)

func TestConsoleCloseCancelsConcurrentAndFutureReads(t *testing.T) {
	input, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(input)
	cancel, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &console{in: input, cancel: cancel, done: make(chan struct{})}
	defer c.close()

	var group sync.WaitGroup
	group.Add(8)
	for range 8 {
		go func() {
			defer group.Done()
			if _, err := c.Read(make([]byte, 1)); !errors.Is(err, cancelreader.ErrCanceled) {
				t.Errorf("Read error = %v, want canceled", err)
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		c.close()
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("console did not finish canceling its readers")
	}
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, cancelreader.ErrCanceled) {
		t.Fatalf("read after close returned %v, want canceled", err)
	}
}
