package machine

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Process struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser

	cmd    *exec.Cmd
	stderr *syncWriter
	done   chan struct{}
	once   sync.Once
	err    error
}

func (s *Machine) Start(ctx context.Context, sessionID string, opts RunOptions) (*Process, error) {
	ws, err := s.Workspace(sessionID)
	if err != nil {
		return nil, err
	}
	argv := opts.Args
	if opts.Shell != "" {
		argv = []string{"bash", "-c", opts.Shell}
	}
	if len(argv) == 0 {
		return nil, errors.New("empty command")
	}
	hostExecutable := argv[0]
	argv = s.hostArgv(ws, argv)
	if opts.HostExecutable {
		argv[0] = hostExecutable
	} else {
		argv = s.sandboxed(ws, argv)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = ws
	cmd.Env = flattenEnv(s.baseEnv(opts, ws))
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	p := &Process{Stdin: stdin, Stdout: stdout, cmd: cmd, stderr: &syncWriter{w: &capWriter{max: 16 << 10}}, done: make(chan struct{})}
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	top := s.topWorkspace(sessionID)
	s.begin(top)
	go func() {
		p.err = cmd.Wait()
		s.end(top)
		close(p.done)
	}()
	return p, nil
}

func (p *Process) Done() <-chan struct{} { return p.done }

func (p *Process) Stderr() string {
	return strings.TrimSpace(strings.ToValidUTF8(p.stderr.String(), ""))
}

type syncWriter struct {
	mu sync.Mutex
	w  *capWriter
}

func (s *syncWriter) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(b)
}

func (s *syncWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.String()
}

func (p *Process) Close() {
	p.once.Do(func() {
		_ = p.Stdin.Close()
		select {
		case <-p.done:
			return
		case <-time.After(2 * time.Second):
		}
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		<-p.done
	})
}
