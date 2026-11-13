package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Metixpro/macron-agent/pkg/config"
)

var httpStatusRe = regexp.MustCompile(`\(HTTP (\d{3})\)`)

func permanentMessengerErr(err error) bool {
	m := httpStatusRe.FindStringSubmatch(err.Error())
	if m == nil {
		return false
	}
	code, _ := strconv.Atoi(m[1])
	return code == 401 || code == 403 || code == 404
}

func waitReachable(ctx context.Context, platform string, check func(context.Context) error) error {
	wait := 5 * time.Second
	for {
		err := check(ctx)
		if err == nil {
			return nil
		}
		if permanentMessengerErr(err) || ctx.Err() != nil {
			return err
		}
		log.Printf("%s is not reachable yet (%v); trying again in %s", platform, err, wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, 5*time.Minute)
	}
}

func sideMessengers() []string {
	chosen := strings.ToLower(strings.TrimSpace(os.Getenv("MACRON_MESSENGER")))
	var out []string
	for _, platform := range []string{"bale", "rubika"} {
		upper := strings.ToUpper(platform)
		token, owners := strings.TrimSpace(os.Getenv(upper+"_BOT_TOKEN")), strings.TrimSpace(os.Getenv("MACRON_"+upper+"_OWNERS"))
		switch {
		case token != "" && owners != "":
			out = append(out, platform)
		case chosen == platform:
			log.Printf("%s is selected (MACRON_MESSENGER), but %s_BOT_TOKEN or MACRON_%s_OWNERS is missing; it is not started", platform, upper, upper)
		}
	}
	return out
}

type supervisor struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func superviseMessengers(platforms []string) *supervisor {
	ctx, cancel := context.WithCancel(context.Background())
	s := &supervisor{cancel: cancel}
	bin, err := os.Executable()
	if err != nil {
		bin = os.Args[0]
	}
	for _, platform := range platforms {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			superviseOne(ctx, bin, platform)
		}()
	}
	return s
}

func superviseOne(ctx context.Context, bin, platform string) {
	wait := 5 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		cmd := exec.Command(bin, platform, "run")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			log.Printf("%s could not start: %v", platform, err)
		} else {
			log.Printf("%s is running alongside Telegram (process %d)", platform, cmd.Process.Pid)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if ctx.Err() == nil {
					log.Printf("%s stopped (%v)", platform, exitText(err))
				}
			case <-ctx.Done():
				_ = cmd.Process.Signal(syscall.SIGTERM)
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					_ = cmd.Process.Kill()
					<-done
				}
				return
			}
		}
		if time.Since(started) > 2*time.Minute {
			wait = 5 * time.Second
		}
		log.Printf("%s starts again in %s", platform, wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, 5*time.Minute)
	}
}

func exitText(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Sprintf("exit %d", exit.ExitCode())
	}
	if err == nil {
		return "exit 0"
	}
	return err.Error()
}

func (s *supervisor) stop() {
	if s == nil {
		return
	}
	s.cancel()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(12 * time.Second):
	}
}

func cleanCgroup() {
	if os.Getenv("INVOCATION_ID") == "" {
		return
	}
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return
	}
	dir := ""
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		switch {
		case parts[0] == "0" && parts[1] == "":
			dir = filepath.Join("/sys/fs/cgroup", parts[2])
		case parts[1] == "name=systemd" && dir == "":
			dir = filepath.Join("/sys/fs/cgroup/systemd", parts[2])
		}
	}
	if dir == "" || filepath.Base(dir) != config.ServiceName()+".service" {
		return
	}
	raw, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return
	}
	self, stopped := os.Getpid(), 0
	for _, field := range strings.Fields(string(raw)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 1 || pid == self {
			continue
		}
		if p, err := os.FindProcess(pid); err == nil && p.Kill() == nil {
			stopped++
		}
	}
	if stopped > 0 {
		log.Printf("stopped %d processes an earlier run of the service left behind", stopped)
	}
}
