//go:build windows

package tui

import (
	"encoding/binary"
	"os"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/cancelreader"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

var procReadConsoleInput = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReadConsoleInputW")

type inputRecord struct {
	eventType uint16
	_         uint16
	event     [16]byte
}

type console struct {
	in     windows.Handle
	cancel windows.Handle
	orig   uint32
	split  vtSplitter
	queue  []vtPart
	high   uint16

	mu     sync.Mutex
	p      *tea.Program
	paused bool
	reset  bool
	done   chan struct{}
	closed bool
	wg     sync.WaitGroup
	readWG sync.WaitGroup
	readMu sync.Mutex
}

func newConsole() *console {
	if os.Getenv("MACRON_TUI_INPUT") == "console" {
		return nil
	}
	h, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return nil
	}
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return nil
	}
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil
	}
	c := &console{in: h, cancel: ev, orig: mode, done: make(chan struct{})}
	if c.raw() != nil {
		_ = windows.CloseHandle(ev)
		return nil
	}
	return c
}

func (c *console) raw() error {
	return windows.SetConsoleMode(c.in, windows.ENABLE_VIRTUAL_TERMINAL_INPUT|windows.ENABLE_EXTENDED_FLAGS)
}

func (c *console) options() []tea.ProgramOption {
	if c == nil {
		return nil
	}
	return []tea.ProgramOption{tea.WithInput(c)}
}

func (c *console) attach(p *tea.Program) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed || c.p != nil {
		c.mu.Unlock()
		return
	}
	c.p = p
	c.wg.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.wg.Done()
		c.watchSize()
	}()
}

func (c *console) watchSize() {
	fd := int(os.Stdout.Fd())
	w0, h0, _ := term.GetSize(fd)
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
		}
		w, h, err := term.GetSize(fd)
		if err != nil || (w == w0 && h == h0) {
			continue
		}
		w0, h0 = w, h
		c.mu.Lock()
		p := c.p
		c.mu.Unlock()
		if p != nil {
			p.Send(tea.WindowSizeMsg{Width: w, Height: h})
		}
	}
}

func (c *console) pause() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.paused = true
	_ = windows.SetEvent(c.cancel)
	_ = windows.SetConsoleMode(c.in, c.orig)
}

func (c *console) resume() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paused && !c.closed {
		c.paused, c.reset = false, true
	}
}

func (c *console) close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	close(c.done)
	_ = windows.SetEvent(c.cancel)
	c.mu.Unlock()
	c.wg.Wait()
	c.readWG.Wait()
	if c.cancel != 0 && c.cancel != windows.InvalidHandle {
		_ = windows.CloseHandle(c.cancel)
	}
	_ = windows.SetConsoleMode(c.in, c.orig)
}

func (c *console) Read(b []byte) (int, error) {
	// A restored Bubble Tea program can start its next reader while the
	// previous reader is unwinding from cancellation.
	c.readMu.Lock()
	defer c.readMu.Unlock()

	c.mu.Lock()
	if c.closed || c.paused {
		c.mu.Unlock()
		return 0, cancelreader.ErrCanceled
	}
	c.readWG.Add(1)
	if c.reset {
		c.reset = false
		_ = windows.ResetEvent(c.cancel)
		_ = c.raw()
		_ = windows.FlushConsoleInputBuffer(c.in)
		c.queue, c.split, c.high = nil, vtSplitter{}, 0
	}
	p := c.p
	c.mu.Unlock()
	defer c.readWG.Done()
	for {
		c.mu.Lock()
		canceled := c.closed || c.paused
		c.mu.Unlock()
		if canceled {
			return 0, cancelreader.ErrCanceled
		}
		if len(c.queue) > 0 {
			part := c.queue[0]
			if part.msg != nil {
				c.queue = c.queue[1:]
				if p != nil {
					p.Send(part.msg)
				}
				continue
			}
			n := copy(b, part.bytes)
			if n < len(part.bytes) {
				c.queue[0].bytes = part.bytes[n:]
			} else {
				c.queue = c.queue[1:]
			}
			return n, nil
		}
		ev, err := windows.WaitForMultipleObjects([]windows.Handle{c.cancel, c.in}, false, windows.INFINITE)
		if err != nil {
			return 0, err
		}
		if ev == windows.WAIT_OBJECT_0 {
			return 0, cancelreader.ErrCanceled
		}
		rs, err := c.records()
		if err != nil {
			return 0, err
		}
		if len(rs) > 0 {
			c.queue = append(c.queue, c.split.feed(rs)...)
		}
	}
}

func (c *console) records() ([]rune, error) {
	var n uint32
	if err := windows.GetNumberOfConsoleInputEvents(c.in, &n); err != nil {
		return nil, err
	}
	if n == 0 {
		time.Sleep(10 * time.Millisecond)
		return nil, nil
	}
	recs := make([]inputRecord, min(n, 256))
	var got uint32
	r, _, err := procReadConsoleInput.Call(uintptr(c.in), uintptr(unsafe.Pointer(&recs[0])), uintptr(len(recs)), uintptr(unsafe.Pointer(&got)))
	if r == 0 {
		return nil, err
	}
	var units []uint16
	if c.high != 0 {
		units = append(units, c.high)
		c.high = 0
	}
	for _, rec := range recs[:got] {
		if rec.eventType != windows.KEY_EVENT {
			continue
		}
		down := binary.LittleEndian.Uint32(rec.event[0:4]) != 0
		repeat := int(binary.LittleEndian.Uint16(rec.event[4:6]))
		ch := binary.LittleEndian.Uint16(rec.event[10:12])
		if !down || ch == 0 {
			continue
		}
		for i := 0; i < max(repeat, 1); i++ {
			units = append(units, ch)
		}
	}
	if k := len(units); k > 0 && utf16.IsSurrogate(rune(units[k-1])) && units[k-1] < 0xdc00 {
		c.high = units[k-1]
		units = units[:k-1]
	}
	return utf16.Decode(units), nil
}
