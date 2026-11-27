package machine

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"
)

// HostProcess is a long-lived host process tracked inside a session workspace
// via process-tree guards (Windows Job Object / Unix process group).
type HostProcess struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Stderr io.ReadCloser

	machine *Machine
	top     string
	cmd     *exec.Cmd
	tree    *tree
	cancel  context.CancelFunc
	done    chan struct{}

	waitOnce  sync.Once
	closeOnce sync.Once
	waitErr   error
}

// StartHostProcess spawns a long-lived host executable (such as python3 running
// the persistent browser daemon) with stdio pipes and orphan-process protection.
func (s *Machine) StartHostProcess(ctx context.Context, sessionID string, args []string, extraEnv map[string]string) (*HostProcess, error) {
	ws, err := s.Workspace(sessionID)
	if err != nil {
		return nil, err
	}
	argv := resolveArgv(args)
	if len(argv) == 0 {
		return nil, errors.New("empty command")
	}
	hostExecutable := argv[0]
	argv = s.hostArgv(ws, argv)
	argv[0] = hostExecutable

	procCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(procCtx, argv[0], argv[1:]...)
	cmd.Dir = ws
	cmd.Env = flattenEnv(s.baseEnv(RunOptions{HostExecutable: true, Env: extraEnv}, ws))
	cmd.WaitDelay = 2 * time.Second
	prepare(cmd, false)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}

	t, err := startTracked(cmd)
	if err != nil {
		cancel()
		return nil, err
	}
	top := s.topWorkspace(sessionID)
	s.begin(top)

	return &HostProcess{
		Stdin:   stdin,
		Stdout:  stdout,
		Stderr:  stderr,
		machine: s,
		top:     top,
		cmd:     cmd,
		tree:    t,
		cancel:  cancel,
		done:    make(chan struct{}),
	}, nil
}

// Wait waits for the process to exit after stdout/stderr have been drained.
func (p *HostProcess) Wait() error {
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
		p.tree.close()
		p.machine.end(p.top)
		p.cancel()
		close(p.done)
	})
	return p.waitErr
}

// Close terminates the process tree gracefully (closing stdin first) and frees
// associated workspace accounting.
func (p *HostProcess) Close() error {
	p.closeOnce.Do(func() {
		if p.Stdin != nil {
			_ = p.Stdin.Close()
		}
		select {
		case <-p.done:
			return
		case <-time.After(1500 * time.Millisecond):
		}
		p.cancel()
		p.tree.kill()
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
			p.tree.close()
		}
	})
	return nil
}
