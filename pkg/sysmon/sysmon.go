// Package sysmon watches the server the bot runs on: CPU, memory, disk and
// network from /proc, the bot's own service (its cgroup, so the programs it started
// it starts count), the busiest processes, a day of history and alerts
// when a limit is crossed. Linux only: elsewhere every reading is empty.
package sysmon

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// DefaultInterval is how often the server is read.
	DefaultInterval = 15 * time.Second
	// Keep is how much history is kept.
	Keep = 24 * time.Hour
)

// Disk is one filesystem.
type Disk struct {
	Path        string
	Total, Free uint64
}

// UsedPct is how full the disk is.
func (d Disk) UsedPct() float64 {
	if d.Total == 0 {
		return 0
	}
	return 100 * float64(d.Total-d.Free) / float64(d.Total)
}

// Service is the bot's own service.
type Service struct {
	Cores      float64 // CPU in use, in cores (all its processes)
	Mem        uint64  // memory in use (bytes)
	MemHigh    uint64  // the service's soft memory limit (0: none)
	MemMax     uint64  // its hard limit (0: none)
	Threads    int     // threads of the bot process
	FDs        int     // open files of the bot process
	ProcessRSS uint64  // the bot process alone
	Cgroup     bool    // Mem and Cores are of its cgroup (else of its process tree)
}

// Snapshot is the server at one moment.
type Snapshot struct {
	At        time.Time
	CPU       float64   // % of the whole machine since the last reading
	Cores     []float64 // % of each core
	CoreCount int
	Load      [3]float64
	MemTotal  uint64
	MemUsed   uint64 // total less what can be given back (available)
	SwapTotal uint64
	SwapUsed  uint64
	NetRx     float64 // bytes per second, all real interfaces
	NetTx     float64
	Uptime    time.Duration
	Disks     []Disk
	Service   Service
	Ready     bool // two readings were made: the rates are real
}

// MemPct is the memory in use, in percent.
func (s Snapshot) MemPct() float64 {
	if s.MemTotal == 0 {
		return 0
	}
	return 100 * float64(s.MemUsed) / float64(s.MemTotal)
}

// Point is one reading kept in the history.
type Point struct {
	At      time.Time
	CPU     float32
	Mem     float32 // %
	Load1   float32
	NetRx   float32
	NetTx   float32
	DiskPct float32 // the fullest watched disk
}

// Bucket is a stretch of the history: averages and peaks.
type Bucket struct {
	At           time.Time
	CPU, CPUMax  float64
	Mem, MemMax  float64
	Load1        float64
	NetRx, NetTx float64
	Disk         float64
	Samples      int
}

// Proc is a process.
type Proc struct {
	PID     int
	Name    string
	Cmd     string
	CPU     float64 // % of one core over the last interval
	RSS     uint64
	Threads int
}

// Options configure a Sampler.
type Options struct {
	Interval time.Duration
	// Disks are the paths whose filesystems are watched.
	Disks []string
	// ProcRoot and CgroupRoot are the places /proc and /sys/fs/cgroup are
	// read from (tests).
	ProcRoot, CgroupRoot string
	// Alerts, when set, are checked after every reading.
	Alerts *Watch
}

// Sampler reads the server and keeps its history. Safe for concurrent use.
type Sampler struct {
	opt Options

	mu     sync.RWMutex
	last   Snapshot
	ring   []Point
	head   int
	n      int
	prev   raw
	procs  map[int]procTick
	procs2 map[int]procTick // the readings before procs, for rates
	tops   []Proc
	clk    float64 // clock ticks a second
	now    func() time.Time
}

// New returns a sampler; Run it, or call Sample yourself.
func New(opt Options) *Sampler {
	if opt.Interval <= 0 {
		opt.Interval = DefaultInterval
	}
	if opt.ProcRoot == "" {
		opt.ProcRoot = "/proc"
	}
	if opt.CgroupRoot == "" {
		opt.CgroupRoot = "/sys/fs/cgroup"
	}
	size := int(Keep/opt.Interval) + 1
	return &Sampler{opt: opt, ring: make([]Point, size), clk: 100, now: time.Now}
}

// Run samples until ctx ends.
func (s *Sampler) Run(ctx context.Context) {
	s.Sample()
	t := time.NewTicker(s.opt.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sample()
		}
	}
}

// Latest is the last reading (zero before the first).
func (s *Sampler) Latest() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.last
}

func (s *Sampler) read(name string) string {
	b, err := os.ReadFile(filepath.Join(s.opt.ProcRoot, name))
	if err != nil {
		return ""
	}
	return string(b)
}

// raw is what the rates are computed from.
type raw struct {
	at      time.Time
	cpu     cpuTimes
	cores   []cpuTimes
	rx, tx  uint64
	svcUsec uint64 // the cgroup's cpu usage, microseconds
	svcTick uint64 // the process tree's, in clock ticks
	ok      bool
}

// Sample reads the server now.
func (s *Sampler) Sample() Snapshot {
	now := s.now()
	sample := Snapshot{At: now}
	cur := raw{at: now, ok: true}
	cur.cpu, cur.cores = parseCPU(s.read("stat"))
	sample.CoreCount = len(cur.cores)
	sample.Load = parseLoad(s.read("loadavg"))
	sample.MemTotal, sample.MemUsed, sample.SwapTotal, sample.SwapUsed = parseMem(s.read("meminfo"))
	sample.Uptime = parseUptime(s.read("uptime"))
	cur.rx, cur.tx = parseNet(s.read("net/dev"))
	for _, p := range s.opt.Disks {
		var st unix.Statfs_t
		if unix.Statfs(p, &st) == nil {
			sample.Disks = append(sample.Disks, Disk{Path: p, Total: st.Blocks * uint64(st.Bsize), Free: st.Bavail * uint64(st.Bsize)})
		}
	}
	svc, usec := s.service()
	sample.Service = svc
	cur.svcUsec = usec

	s.mu.Lock()
	if s.prev.ok {
		dt := now.Sub(s.prev.at).Seconds()
		if dt > 0 {
			sample.CPU = cur.cpu.pct(s.prev.cpu)
			sample.Cores = make([]float64, len(cur.cores))
			for i := range cur.cores {
				if i < len(s.prev.cores) {
					sample.Cores[i] = cur.cores[i].pct(s.prev.cores[i])
				}
			}
			sample.NetRx, sample.NetTx = rate(cur.rx, s.prev.rx, dt), rate(cur.tx, s.prev.tx, dt)
			if svc.Cgroup && cur.svcUsec >= s.prev.svcUsec {
				sample.Service.Cores = float64(cur.svcUsec-s.prev.svcUsec) / 1e6 / dt
			}
			sample.Ready = true
		}
	}
	s.prev = cur
	s.scanProcs(now)
	if sample.Ready && !svc.Cgroup {
		sample.Service.Cores = s.treeCores()
	}
	s.last = sample

	pt := Point{At: now, CPU: float32(sample.CPU), Mem: float32(sample.MemPct()), Load1: float32(sample.Load[0]),
		NetRx: float32(sample.NetRx), NetTx: float32(sample.NetTx)}
	for _, d := range sample.Disks {
		pt.DiskPct = max(pt.DiskPct, float32(d.UsedPct()))
	}
	if sample.Ready {
		s.ring[s.head] = pt
		s.head = (s.head + 1) % len(s.ring)
		s.n = min(s.n+1, len(s.ring))
	}
	s.mu.Unlock()
	if w := s.opt.Alerts; w != nil && sample.Ready { // outside the lock: it asks the sampler for the history
		w.check(s, sample)
	}
	return sample
}

func rate(cur, prev uint64, dt float64) float64 {
	if cur < prev || dt <= 0 {
		return 0
	}
	return float64(cur-prev) / dt
}

// points returns the readings of the last d, oldest first.
func (s *Sampler) points(d time.Duration) []Point {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Point, 0, s.n)
	cut := s.now().Add(-d)
	for i := 0; i < s.n; i++ {
		p := s.ring[(s.head-s.n+i+len(s.ring))%len(s.ring)]
		if !p.At.Before(cut) {
			out = append(out, p)
		}
	}
	return out
}

// History is the last d in n buckets, oldest first; a bucket with no
// readings is left out.
func (s *Sampler) History(d time.Duration, n int) []Bucket {
	pts := s.points(d)
	if len(pts) == 0 || n <= 0 {
		return nil
	}
	start := s.now().Add(-d)
	width := d / time.Duration(n)
	out := make([]Bucket, n)
	for i := range out {
		out[i].At = start.Add(time.Duration(i)*width + width/2)
	}
	for _, p := range pts {
		i := min(int(p.At.Sub(start)/width), n-1)
		b := &out[i]
		b.Samples++
		b.CPU += float64(p.CPU)
		b.Mem += float64(p.Mem)
		b.Load1 += float64(p.Load1)
		b.NetRx += float64(p.NetRx)
		b.NetTx += float64(p.NetTx)
		b.Disk = max(b.Disk, float64(p.DiskPct))
		b.CPUMax, b.MemMax = max(b.CPUMax, float64(p.CPU)), max(b.MemMax, float64(p.Mem))
	}
	kept := out[:0]
	for _, b := range out {
		if b.Samples == 0 {
			continue
		}
		f := float64(b.Samples)
		b.CPU, b.Mem, b.Load1, b.NetRx, b.NetTx = b.CPU/f, b.Mem/f, b.Load1/f, b.NetRx/f, b.NetTx/f
		kept = append(kept, b)
	}
	return kept
}

// Average is the mean of what f reads from the readings of the last d
// (ok false: none yet).
func (s *Sampler) average(d time.Duration, f func(Point) float64) (float64, bool) {
	pts := s.points(d)
	if len(pts) == 0 {
		return 0, false
	}
	var sum float64
	for _, p := range pts {
		sum += f(p)
	}
	return sum / float64(len(pts)), true
}

// ---------------------------------------------------------------- processes

type procTick struct {
	ticks   uint64
	name    string
	rss     uint64
	threads int
	at      time.Time
}

// scanProcs reads every process (caller holds mu).
func (s *Sampler) scanProcs(now time.Time) {
	entries, err := os.ReadDir(s.opt.ProcRoot)
	if err != nil {
		return
	}
	cur := make(map[int]procTick, len(entries))
	page := uint64(os.Getpagesize())
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.opt.ProcRoot, e.Name(), "stat"))
		if err != nil {
			continue
		}
		st, ok := parseProcStat(string(b))
		if !ok {
			continue
		}
		cur[pid] = procTick{ticks: st.ticks, name: st.name, rss: st.rssPages * page, threads: st.threads, at: now}
	}
	s.procs2, s.procs = s.procs, cur
	s.tops = s.tops[:0]
	for pid, c := range cur {
		p := Proc{PID: pid, Name: c.name, RSS: c.rss, Threads: c.threads}
		if old, ok := s.procs2[pid]; ok && c.at.After(old.at) && c.ticks >= old.ticks {
			p.CPU = float64(c.ticks-old.ticks) / s.clk / c.at.Sub(old.at).Seconds() * 100
		}
		s.tops = append(s.tops, p)
	}
}

// treeCores is the CPU of the bot process and its children, in cores, from
// the process table (when no cgroup says better).
func (s *Sampler) treeCores() float64 {
	own := os.Getpid()
	var sum float64
	for _, p := range s.tops {
		if p.PID == own {
			sum += p.CPU / 100
		}
	}
	return sum
}

// Top is the n busiest processes by CPU ("cpu") or memory ("mem").
func (s *Sampler) Top(n int, by string) []Proc {
	s.mu.RLock()
	list := append([]Proc(nil), s.tops...)
	s.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool {
		if by == "mem" {
			return list[i].RSS > list[j].RSS
		}
		if list[i].CPU != list[j].CPU {
			return list[i].CPU > list[j].CPU
		}
		return list[i].PID < list[j].PID
	})
	if len(list) > n {
		list = list[:n]
	}
	for i := range list {
		list[i].Cmd = s.cmdline(list[i].PID)
	}
	return list
}

func (s *Sampler) cmdline(pid int) string {
	b, err := os.ReadFile(filepath.Join(s.opt.ProcRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil || len(b) == 0 {
		return ""
	}
	parts := bytes.Split(bytes.TrimRight(b, "\x00"), []byte{0})
	words := make([]string, 0, len(parts))
	for _, p := range parts {
		words = append(words, string(p))
	}
	out := strings.Join(words, " ")
	if len(out) > 80 {
		out = out[:80] + "…"
	}
	return strings.ToValidUTF8(out, "")
}

// ---------------------------------------------------------------- the bot's service

// service reads the bot's own service: its cgroup when there is one (it
// holds the programs the bot started), else its process alone.
func (s *Sampler) service() (Service, uint64) {
	var svc Service
	if b, err := os.ReadFile(filepath.Join(s.opt.ProcRoot, "self", "status")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			switch {
			case strings.HasPrefix(line, "VmRSS:"):
				svc.ProcessRSS = kbField(line) * 1024
			case strings.HasPrefix(line, "Threads:"):
				svc.Threads = int(kbField(line))
			}
		}
	}
	if fds, err := os.ReadDir(filepath.Join(s.opt.ProcRoot, "self", "fd")); err == nil {
		svc.FDs = len(fds)
	}
	svc.Mem = svc.ProcessRSS
	cg, err := os.ReadFile(filepath.Join(s.opt.ProcRoot, "self", "cgroup"))
	if err != nil {
		return svc, 0
	}
	path := ""
	for _, line := range strings.Split(string(cg), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			path = strings.TrimSpace(rest)
		}
	}
	if path == "" || path == "/" {
		return svc, 0 // the root cgroup is the whole machine: not the service's
	}
	dir := filepath.Join(s.opt.CgroupRoot, path)
	cur, ok := readUint(filepath.Join(dir, "memory.current"))
	if !ok {
		return svc, 0
	}
	svc.Cgroup, svc.Mem = true, cur
	svc.MemHigh, _ = readUint(filepath.Join(dir, "memory.high"))
	svc.MemMax, _ = readUint(filepath.Join(dir, "memory.max"))
	var usec uint64
	if b, err := os.ReadFile(filepath.Join(dir, "cpu.stat")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if rest, ok := strings.CutPrefix(line, "usage_usec "); ok {
				usec, _ = strconv.ParseUint(strings.TrimSpace(rest), 10, 64)
			}
		}
	}
	return svc, usec
}

// readUint reads a number from a file ("max" and unreadable: ok false, 0).
func readUint(path string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return n, err == nil
}
