package sysmon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const statSample = `cpu  4705 150 3120 87500 600 0 100 0 0 0
cpu0 2400 70 1500 43000 300 0 60 0 0 0
cpu1 2305 80 1620 44500 300 0 40 0 0 0
intr 1 2 3
ctxt 99
`

func TestParseCPU(t *testing.T) {
	all, cores := parseCPU(statSample)
	if len(cores) != 2 {
		t.Fatalf("cores: %v", cores)
	}
	// busy = user+nice+system+irq+softirq+steal; waiting for the disk is idle
	if all.total != 4705+150+3120+87500+600+100 || all.busy != 4705+150+3120+100 {
		t.Errorf("all: %+v", all)
	}
	later := cpuTimes{busy: all.busy + 50, total: all.total + 100}
	if p := later.pct(all); p != 50 {
		t.Errorf("half busy: %v", p)
	}
	if (cpuTimes{}).pct(all) != 0 {
		t.Error("a counter that went back is no load")
	}
}

func TestParseMemory(t *testing.T) {
	const meminfo = `MemTotal:       16000000 kB
MemFree:         2000000 kB
MemAvailable:    9000000 kB
Buffers:          500000 kB
Cached:          6000000 kB
SwapTotal:       2000000 kB
SwapFree:        1500000 kB
`
	total, used, swapT, swapU := parseMem(meminfo)
	if total != 16000000*1024 || used != 7000000*1024 || swapT != 2000000*1024 || swapU != 500000*1024 {
		t.Errorf("%d %d %d %d", total, used, swapT, swapU)
	}
	old := strings.Replace(meminfo, "MemAvailable:    9000000 kB\n", "", 1)
	if _, used, _, _ := parseMem(old); used != (16000000-2000000-500000-6000000)*1024 {
		t.Errorf("without MemAvailable: %d", used)
	}
}

func TestParseOthers(t *testing.T) {
	if l := parseLoad("0.52 0.58 0.59 1/423 12345\n"); l != [3]float64{0.52, 0.58, 0.59} {
		t.Errorf("load: %v", l)
	}
	if d := parseUptime("350735.47 234388.90\n"); d.Round(time.Second) != 350735*time.Second+time.Second/2*1 && d < 350735*time.Second {
		t.Errorf("uptime: %v", d)
	}
	const net = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000 10 0 0 0 0 0 0 1000 10 0 0 0 0 0 0
  eth0: 5000 50 0 0 0 0 0 0 7000 70 0 0 0 0 0 0
 veth1: 9999 1 0 0 0 0 0 0 9999 1 0 0 0 0 0 0
docker0: 8888 1 0 0 0 0 0 0 8888 1 0 0 0 0 0 0
  ens3: 100 5 0 0 0 0 0 0 200 5 0 0 0 0 0 0
`
	if rx, tx := parseNet(net); rx != 5100 || tx != 7200 {
		t.Errorf("only the real interfaces count: %d %d", rx, tx)
	}
	// A process name may hold spaces and parentheses.
	st, ok := parseProcStat("1234 (Web Content (x)) S 1 1234 1234 0 -1 4194560 100 0 0 0 250 50 0 0 20 0 7 0 100 1000000 2048 18446744073709551615")
	if !ok || st.name != "Web Content (x)" || st.ticks != 300 || st.threads != 7 || st.rssPages != 2048 {
		t.Errorf("proc stat: %+v %v", st, ok)
	}
	if _, ok := parseProcStat("garbage"); ok {
		t.Error("garbage is not a process")
	}
}

// fakeProc is a /proc whose numbers the test moves.
type fakeProc struct {
	t    *testing.T
	root string
}

func newFakeProc(t *testing.T) *fakeProc {
	f := &fakeProc{t: t, root: t.TempDir()}
	for _, d := range []string{"net", "self/fd", "100", "200"} {
		_ = os.MkdirAll(filepath.Join(f.root, d), 0o755)
	}
	_ = os.WriteFile(filepath.Join(f.root, "self", "fd", "0"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(f.root, "self", "fd", "1"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(f.root, "self", "status"), []byte("Name:\tbot\nVmRSS:\t  204800 kB\nThreads:\t42\n"), 0o644)
	_ = os.WriteFile(filepath.Join(f.root, "uptime"), []byte("3600.5 100.0\n"), 0o644)
	_ = os.WriteFile(filepath.Join(f.root, "loadavg"), []byte("1.50 1.00 0.50 2/300 1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(f.root, "100", "cmdline"), []byte("ffmpeg\x00-i\x00in.mp4\x00"), 0o644)
	return f
}

func (f *fakeProc) set(busy, idle, memUsedKB, rx, tx, p100, p200 uint64) {
	w := func(name, body string) {
		if err := os.WriteFile(filepath.Join(f.root, name), []byte(body), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	w("stat", fmt.Sprintf("cpu  %d 0 0 %d 0 0 0 0 0 0\ncpu0 %d 0 0 %d 0 0 0 0 0 0\n", busy, idle, busy, idle))
	w("meminfo", fmt.Sprintf("MemTotal: 1000000 kB\nMemAvailable: %d kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n", 1000000-memUsedKB))
	w("net/dev", fmt.Sprintf("Inter-|\n face |\n  eth0: %d 1 0 0 0 0 0 0 %d 1 0 0 0 0 0 0\n", rx, tx))
	w("100/stat", fmt.Sprintf("100 (ffmpeg) R 1 100 100 0 -1 0 0 0 0 0 %d 0 0 0 20 0 8 0 1 1 5000 0\n", p100))
	w("200/stat", fmt.Sprintf("200 (dbserverd) S 1 200 200 0 -1 0 0 0 0 0 %d 0 0 0 20 0 30 0 1 1 90000 0\n", p200))
}

func TestSamplerRatesTopAndHistory(t *testing.T) {
	f := newFakeProc(t)
	clock := time.Unix(1_000_000, 0)
	s := New(Options{ProcRoot: f.root, CgroupRoot: filepath.Join(f.root, "cg"), Interval: 15 * time.Second, Disks: []string{"/"}})
	s.now = func() time.Time { return clock }

	f.set(1000, 9000, 400000, 1_000_000, 500_000, 0, 0)
	if first := s.Sample(); first.Ready || first.MemTotal != 1000000*1024 || first.CoreCount != 1 || first.Service.Threads != 42 || first.Service.FDs != 2 {
		t.Fatalf("the first reading has no rates yet: %+v", first)
	}
	for i := 1; i <= 8; i++ {
		clock = clock.Add(15 * time.Second)
		busy := uint64(1000 + i*600) // 60% busy
		f.set(busy, uint64(9000+i*400), 400000+uint64(i)*10000, 1_000_000+uint64(i)*150_000, 500_000+uint64(i)*30_000, uint64(i)*1500, uint64(i)*30)
		s.Sample()
	}
	sample := s.Latest()
	if !sample.Ready || int(sample.CPU+0.5) != 60 || len(sample.Cores) != 1 {
		t.Errorf("cpu: %+v", sample)
	}
	if int(sample.NetRx) != 10_000 || int(sample.NetTx) != 2_000 {
		t.Errorf("network, bytes a second: %v %v", sample.NetRx, sample.NetTx)
	}
	if sample.Load[0] != 1.5 || sample.Uptime != 3600*time.Second+500*time.Millisecond || int(sample.MemPct()+0.5) != 48 {
		t.Errorf("load, uptime, memory: %+v %.1f", sample.Load, sample.MemPct())
	}
	top := s.Top(2, "cpu")
	if len(top) != 2 || top[0].Name != "ffmpeg" || top[0].Cmd != "ffmpeg -i in.mp4" || int(top[0].CPU+0.5) != 100 {
		t.Errorf("busiest by cpu (1500 ticks in 15 s at 100 Hz is one whole core): %+v", top)
	}
	if mem := s.Top(1, "mem"); mem[0].Name != "dbserverd" {
		t.Errorf("by memory: %+v", mem)
	}
	h := s.History(2*time.Minute, 4)
	if len(h) != 4 || int(h[len(h)-1].CPU+0.5) != 60 || h[0].Samples == 0 {
		t.Errorf("history: %+v", h)
	}
	if h[len(h)-1].MemMax < h[0].MemMax {
		t.Errorf("memory grew: %+v", h)
	}
	if got := s.History(time.Second, 3); len(got) > 1 {
		t.Errorf("only the readings inside the stretch: %+v", got)
	}
}

func TestServiceCgroup(t *testing.T) {
	f := newFakeProc(t)
	cg := filepath.Join(f.root, "cg", "system.slice", "waze-bot.service")
	_ = os.MkdirAll(cg, 0o755)
	_ = os.WriteFile(filepath.Join(f.root, "self", "cgroup"), []byte("0::/system.slice/waze-bot.service\n"), 0o644)
	_ = os.WriteFile(filepath.Join(cg, "memory.current"), []byte("2147483648\n"), 0o644)
	_ = os.WriteFile(filepath.Join(cg, "memory.max"), []byte("8589934592\n"), 0o644)
	_ = os.WriteFile(filepath.Join(cg, "memory.high"), []byte("max\n"), 0o644)
	clock := time.Unix(2_000_000, 0)
	s := New(Options{ProcRoot: f.root, CgroupRoot: filepath.Join(f.root, "cg")})
	s.now = func() time.Time { return clock }
	f.set(100, 900, 100000, 0, 0, 0, 0)
	_ = os.WriteFile(filepath.Join(cg, "cpu.stat"), []byte("usage_usec 1000000\nuser_usec 800000\n"), 0o644)
	s.Sample()
	clock = clock.Add(10 * time.Second)
	_ = os.WriteFile(filepath.Join(cg, "cpu.stat"), []byte("usage_usec 26000000\n"), 0o644) // 25 s of CPU in 10 s
	f.set(200, 1800, 100000, 0, 0, 0, 0)
	svc := s.Sample().Service
	if !svc.Cgroup || svc.Mem != 2<<30 || svc.MemMax != 8<<30 || svc.MemHigh != 0 || svc.Cores < 2.49 || svc.Cores > 2.51 {
		t.Errorf("the service's cgroup (its programs included): %+v", svc)
	}
	// The root cgroup is the whole machine, not the service.
	_ = os.WriteFile(filepath.Join(f.root, "self", "cgroup"), []byte("0::/\n"), 0o644)
	if svc := s.Sample().Service; svc.Cgroup {
		t.Errorf("the root cgroup is not the service: %+v", svc)
	}
}

func TestAlertsFireOnceRepeatAndRecover(t *testing.T) {
	f := newFakeProc(t)
	clock := time.Unix(3_000_000, 0)
	var got []Alert
	lim := Limits{CPU: 90, RAM: 90}
	w := &Watch{Limits: func() Limits { return lim }, Notify: func(a Alert) { got = append(got, a) }}
	s := New(Options{ProcRoot: f.root, CgroupRoot: filepath.Join(f.root, "cg"), Interval: 15 * time.Second, Alerts: w})
	s.now = func() time.Time { return clock }
	var busy, idle, mem uint64 = 0, 1000, 100000
	step := func(busyPct int, memKB uint64) {
		clock = clock.Add(15 * time.Second)
		busy += uint64(busyPct)
		idle += uint64(100 - busyPct)
		mem = memKB
		f.set(busy, idle, mem, 0, 0, 0, 0)
		s.Sample()
	}
	f.set(busy, idle, mem, 0, 0, 0, 0)
	s.Sample()
	for i := 0; i < 30; i++ { // 7 minutes at 20%: nothing to tell
		step(20, 100000)
	}
	if len(got) != 0 {
		t.Fatalf("calm: %+v", got)
	}
	for i := 0; i < 10; i++ { // two and a half minutes at 99%: the 5-minute mean is not over yet
		step(99, 100000)
	}
	if len(got) != 0 {
		t.Fatalf("a short burst is no alert: %+v", got)
	}
	for i := 0; i < 20; i++ {
		step(99, 100000)
	}
	if len(got) != 1 || got[0].Kind != AlertCPU || got[0].Recovered || got[0].Value < 90 || len(got[0].Top) == 0 {
		t.Fatalf("the CPU alert comes once, with the busiest processes: %+v", got)
	}
	for i := 0; i < 20; i++ {
		step(99, 100000)
	}
	if len(got) != 1 {
		t.Errorf("not again while it stays over: %d", len(got))
	}
	for i := 0; i < 40; i++ {
		step(10, 100000)
	}
	if len(got) != 2 || !got[1].Recovered || got[1].Kind != AlertCPU {
		t.Errorf("and one note when it is over: %+v", got)
	}
	// Memory has its own window; a limit of 0 is off.
	got = nil
	for i := 0; i < 10; i++ {
		step(10, 950000)
	}
	if len(got) != 1 || got[0].Kind != AlertRAM || got[0].Value < 90 {
		t.Errorf("memory: %+v", got)
	}
	lim.RAM = 0
	got = nil
	for i := 0; i < 80; i++ { // the three hours' repeat does not fire on an off limit
		step(10, 950000)
	}
	if len(got) != 0 {
		t.Errorf("an off limit is silent: %+v", got)
	}
}

func TestDiskAlert(t *testing.T) {
	dir := t.TempDir()
	var got []Alert
	w := &Watch{Limits: func() Limits { return Limits{Disk: 1} }, Notify: func(a Alert) { got = append(got, a) }}
	f := newFakeProc(t)
	s := New(Options{ProcRoot: f.root, CgroupRoot: filepath.Join(f.root, "cg"), Disks: []string{dir}, Alerts: w})
	clock := time.Unix(4_000_000, 0)
	s.now = func() time.Time { return clock }
	f.set(1, 9, 1000, 0, 0, 0, 0)
	s.Sample()
	clock = clock.Add(15 * time.Second)
	f.set(2, 18, 1000, 0, 0, 0, 0)
	s.Sample()
	if len(got) != 1 || got[0].Kind != AlertDisk || got[0].Path != dir {
		t.Errorf("a disk over 1%% full: %+v", got)
	}
}

// The real /proc of this machine reads without errors (Linux only).
func TestRealProc(t *testing.T) {
	if _, err := os.Stat("/proc/stat"); err != nil {
		t.Skip("no /proc")
	}
	s := New(Options{Disks: []string{"/"}})
	s.Sample()
	time.Sleep(50 * time.Millisecond)
	sample := s.Sample()
	if sample.MemTotal == 0 || sample.CoreCount == 0 || len(sample.Disks) != 1 || sample.Service.Threads == 0 {
		t.Errorf("%+v", sample)
	}
	if len(s.Top(3, "mem")) == 0 {
		t.Error("processes")
	}
}
