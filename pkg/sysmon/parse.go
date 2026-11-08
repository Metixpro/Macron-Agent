package sysmon

import (
	"strconv"
	"strings"
	"time"
)

type cpuTimes struct{ busy, total uint64 }

func (c cpuTimes) pct(prev cpuTimes) float64 {
	if c.total <= prev.total || c.busy < prev.busy {
		return 0
	}
	return 100 * float64(c.busy-prev.busy) / float64(c.total-prev.total)
}

func fields(s string) []uint64 {
	var out []uint64
	for _, f := range strings.Fields(s) {
		n, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

func parseCPU(stat string) (all cpuTimes, cores []cpuTimes) {
	for _, line := range strings.Split(stat, "\n") {
		name, rest, ok := strings.Cut(line, " ")
		if !ok || !strings.HasPrefix(name, "cpu") {
			continue
		}
		f := fields(rest)
		if len(f) < 4 {
			continue
		}
		var total uint64
		for i, v := range f {
			if i < 8 {
				total += v
			}
		}
		idle := f[3]
		if len(f) > 4 {
			idle += f[4]
		}
		t := cpuTimes{busy: total - min(idle, total), total: total}
		if name == "cpu" {
			all = t
		} else {
			cores = append(cores, t)
		}
	}
	return all, cores
}

func parseLoad(s string) (l [3]float64) {
	f := strings.Fields(s)
	for i := 0; i < 3 && i < len(f); i++ {
		l[i], _ = strconv.ParseFloat(f[i], 64)
	}
	return l
}

func parseMem(s string) (total, used, swapTotal, swapUsed uint64) {
	var avail, free, swapFree uint64
	var haveAvail bool
	var buffers, cached uint64
	for _, line := range strings.Split(s, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v := kbField(name+":"+rest) * 1024
		switch name {
		case "MemTotal":
			total = v
		case "MemAvailable":
			avail, haveAvail = v, true
		case "MemFree":
			free = v
		case "Buffers":
			buffers = v
		case "Cached":
			cached = v
		case "SwapTotal":
			swapTotal = v
		case "SwapFree":
			swapFree = v
		}
	}
	if !haveAvail {
		avail = free + buffers + cached
	}
	return total, total - min(avail, total), swapTotal, swapTotal - min(swapFree, swapTotal)
}

func kbField(line string) uint64 {
	_, rest, _ := strings.Cut(line, ":")
	f := strings.Fields(rest)
	if len(f) == 0 {
		return 0
	}
	n, _ := strconv.ParseUint(f[0], 10, 64)
	return n
}

func parseUptime(s string) time.Duration {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0
	}
	sec, _ := strconv.ParseFloat(f[0], 64)
	return time.Duration(sec * float64(time.Second))
}

func parseNet(s string) (rx, tx uint64) {
	for _, line := range strings.Split(s, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") ||
			strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "virbr") || strings.HasPrefix(name, "cni") {
			continue
		}
		f := fields(rest)
		if len(f) >= 9 {
			rx += f[0]
			tx += f[8]
		}
	}
	return rx, tx
}

type procStat struct {
	name     string
	ticks    uint64
	threads  int
	rssPages uint64
}

func parseProcStat(s string) (procStat, bool) {
	open, closeP := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || closeP < open {
		return procStat{}, false
	}
	f := strings.Fields(s[closeP+1:])
	if len(f) < 22 {
		return procStat{}, false
	}
	utime, _ := strconv.ParseUint(f[11], 10, 64)
	stime, _ := strconv.ParseUint(f[12], 10, 64)
	threads, _ := strconv.Atoi(f[17])
	rss, _ := strconv.ParseUint(f[21], 10, 64)
	return procStat{name: s[open+1 : closeP], ticks: utime + stime, threads: threads, rssPages: rss}, true
}
