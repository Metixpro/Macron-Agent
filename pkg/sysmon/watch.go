package sysmon

import (
	"sync"
	"time"
)

const (
	AlertCPU  = "cpu"
	AlertRAM  = "ram"
	AlertDisk = "disk"
)

type Limits struct{ CPU, RAM, Disk int }

type Alert struct {
	Kind      string
	Value     float64
	Limit     int
	Recovered bool
	Path      string
	Top       []Proc
}

const (
	cpuWindow  = 5 * time.Minute
	ramWindow  = 2 * time.Minute
	alertAgain = 3 * time.Hour
	recoverGap = 5.0
)

type Watch struct {
	Limits func() Limits
	Notify func(Alert)

	mu    sync.Mutex
	state map[string]*alertState
}

type alertState struct {
	on   bool
	sent time.Time
}

func (w *Watch) st(key string) *alertState {
	if w.state == nil {
		w.state = map[string]*alertState{}
	}
	if w.state[key] == nil {
		w.state[key] = &alertState{}
	}
	return w.state[key]
}

func (w *Watch) fire(key string, over bool, recovered bool, a Alert) {
	w.mu.Lock()
	s := w.st(key)
	send := false
	switch {
	case over && !s.on:
		s.on, s.sent, send = true, time.Now(), true
	case over && time.Since(s.sent) >= alertAgain:
		s.sent, send = time.Now(), true
	case recovered && s.on:
		s.on, send = false, true
		a.Recovered = true
	}
	w.mu.Unlock()
	if send && w.Notify != nil {
		w.Notify(a)
	}
}

func (w *Watch) check(s *Sampler, sample Snapshot) {
	if w.Limits == nil {
		return
	}
	lim := w.Limits()
	if lim.CPU > 0 {
		if avg, ok := s.average(cpuWindow, func(p Point) float64 { return float64(p.CPU) }); ok && len(s.points(cpuWindow)) >= int(cpuWindow/s.opt.Interval)-1 {
			a := Alert{Kind: AlertCPU, Value: avg, Limit: lim.CPU}
			over := avg >= float64(lim.CPU)
			if over || w.active(AlertCPU) {
				a.Top = s.Top(5, "cpu")
			}
			w.fire(AlertCPU, over, avg < float64(lim.CPU)-recoverGap, a)
		}
	}
	if lim.RAM > 0 {
		if avg, ok := s.average(ramWindow, func(p Point) float64 { return float64(p.Mem) }); ok && len(s.points(ramWindow)) >= int(ramWindow/s.opt.Interval)-1 {
			a := Alert{Kind: AlertRAM, Value: avg, Limit: lim.RAM}
			over := avg >= float64(lim.RAM)
			if over || w.active(AlertRAM) {
				a.Top = s.Top(5, "mem")
			}
			w.fire(AlertRAM, over, avg < float64(lim.RAM)-recoverGap, a)
		}
	}
	if lim.Disk > 0 {
		for _, d := range sample.Disks {
			pct := d.UsedPct()
			w.fire(AlertDisk+d.Path, pct >= float64(lim.Disk), pct < float64(lim.Disk)-recoverGap,
				Alert{Kind: AlertDisk, Value: pct, Limit: lim.Disk, Path: d.Path})
		}
	}
}

func (w *Watch) active(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state != nil && w.state[key] != nil && w.state[key].on
}
