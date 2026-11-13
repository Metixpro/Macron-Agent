//go:build unix

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func matchingProcesses(binary string, uid int) []int {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("ps", "-axo", "pid=,uid=,comm=").Output()
		if err != nil {
			return nil
		}
		var pids []int
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			pid, e := strconv.Atoi(fields[0])
			owner, e2 := strconv.Atoi(fields[1])
			if e != nil || e2 != nil || pid == os.Getpid() || owner != uid {
				continue
			}
			rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))
			rest = strings.TrimSpace(strings.TrimPrefix(rest, fields[1]))
			if matchesInstallBinary(rest, binary) {
				pids = append(pids, pid)
			}
		}
		return pids
	}
	if runtime.GOOS != "linux" {
		return nil
	}
	entries, _ := os.ReadDir("/proc")
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		root := filepath.Join("/proc", entry.Name())
		st, err := os.Stat(root)
		if err != nil {
			continue
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok || int(sys.Uid) != uid {
			continue
		}
		path, err := os.Readlink(filepath.Join(root, "exe"))
		if err == nil && matchesInstallBinary(path, binary) {
			pids = append(pids, pid)
		}
	}
	return pids
}

func stopInstallationProcesses(binary string) error {
	pids := matchingProcesses(binary, os.Geteuid())
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		left := matchingProcesses(binary, os.Geteuid())
		if len(left) == 0 {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, pid := range matchingProcesses(binary, os.Geteuid()) {
		if slices.Contains(pids, pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	for i := 0; i < 20; i++ {
		if len(matchingProcesses(binary, os.Geteuid())) == 0 {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("some Macron processes are still running; data and binary were kept")
}
