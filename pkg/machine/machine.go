package machine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Config struct {
	BaseDir       string
	HomeDir       string
	MaxConcurrent int

	Sandbox      string
	SandboxNoNet bool
	ReadOnly     []string
	Writable     []string
	Hide         []string
}

type Machine struct {
	BaseDir string
	cfg     Config
	sb      *sandbox
	sem     chan struct{}
	heavy   chan struct{}

	mu      sync.Mutex
	touched map[string]time.Time
	active  map[string]int
	shells  map[string]*shell

	shellStart sync.Mutex
	reaper     sync.Once

	bindMu sync.RWMutex
	bound  map[string]string
}

type ExecutionResult struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Duration  string `json:"duration"`
	Truncated bool   `json:"output_truncated,omitempty"`
	Error     string `json:"error,omitempty"`
	Job       int    `json:"job,omitempty"`
	PID       int    `json:"pid,omitempty"`
}

type RunOptions struct {
	Args           []string
	HostExecutable bool
	Shell          string
	Stdin          []byte

	InSession bool
	Timeout   time.Duration
	Env       map[string]string

	MergeOutput bool

	Tee io.Writer

	Heavy bool

	Yield <-chan struct{}
}

const (
	MountPoint = "/workspace"
)

const maxCapturedOutput = 64 * 1024

var safeIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func SafeSessionID(id string) string {
	if id == "" {
		return "default"
	}
	if safeIDRe.MatchString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "h." + hex.EncodeToString(sum[:10])
}

func New(cfg Config) (*Machine, error) {
	if cfg.BaseDir == "" {
		cfg.BaseDir = "./workspaces"
	}
	abs, err := filepath.Abs(cfg.BaseDir)
	if err != nil {
		return nil, fmt.Errorf("resolve work dir: %w", err)
	}
	cfg.BaseDir = abs
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = max(8, 2*runtime.NumCPU())
	}
	if err := os.MkdirAll(cfg.BaseDir, 0o700); err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}
	sb, note := newSandbox(cfg)
	if note != "" {
		log.Printf("sandbox: %s", note)
	} else if sb != nil {
		netNote := "with network access"
		if sb.noNet {
			netNote = "network disabled"
		}
		log.Printf("🔒 commands run in a bubblewrap sandbox (%s, workspace %s)", netNote, cfg.BaseDir)
	}
	log.Printf("🖥  running on this machine with the rights of %s", currentUser())
	return &Machine{
		BaseDir: cfg.BaseDir,
		cfg:     cfg,
		sb:      sb,
		sem:     make(chan struct{}, cfg.MaxConcurrent),
		heavy:   make(chan struct{}, max(2, runtime.NumCPU())),
		touched: make(map[string]time.Time),
		active:  make(map[string]int),
	}, nil
}

func (s *Machine) Workspace(sessionID string) (string, error) {
	top := s.topWorkspace(sessionID)
	ws := top
	if sub := SubPath(sessionID); sub != "" {
		ws = filepath.Join(top, filepath.FromSlash(sub))
	}
	if err := os.MkdirAll(ws, 0o700); err != nil {
		return "", fmt.Errorf("create workspace: %w", err)
	}
	s.touch(top)
	return ws, nil
}

const (
	subSep = "::"
	SubDir = ".agents"
)

func SubSession(parent, name string) string {
	top := ParentSession(parent)
	if strings.Contains(parent, subSep) {
		_, sub, _ := strings.Cut(parent, subSep)
		return top + subSep + sub + "_" + name
	}
	return top + subSep + name
}

func ParentSession(id string) string {
	p, _, _ := strings.Cut(id, subSep)
	return p
}

func SubPath(id string) string {
	_, n, ok := strings.Cut(id, subSep)
	if !ok {
		return ""
	}
	return SubDir + "/" + SafeSessionID(n)
}

func (s *Machine) topWorkspace(sessionID string) string {
	key := SafeSessionID(ParentSession(sessionID))
	s.bindMu.RLock()
	dir, ok := s.bound[key]
	s.bindMu.RUnlock()
	if ok {
		return dir
	}
	return filepath.Join(s.BaseDir, key)
}

func (s *Machine) Bind(sessionID, dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a folder", abs)
	}
	s.bindMu.Lock()
	if s.bound == nil {
		s.bound = map[string]string{}
	}
	s.bound[SafeSessionID(ParentSession(sessionID))] = abs
	s.bindMu.Unlock()
	return abs, nil
}

func (s *Machine) touch(ws string) {
	now := time.Now()
	s.mu.Lock()
	last := s.touched[ws]
	if now.Sub(last) < 30*time.Second {
		s.mu.Unlock()
		return
	}
	s.touched[ws] = now
	s.mu.Unlock()
	_ = os.Chtimes(ws, now, now)
}

func (s *Machine) begin(ws string) {
	s.mu.Lock()
	s.active[ws]++
	s.mu.Unlock()
}

func (s *Machine) end(ws string) {
	s.mu.Lock()
	if s.active[ws]--; s.active[ws] <= 0 {
		delete(s.active, ws)
	}
	s.mu.Unlock()
}

func (s *Machine) StartJanitor(ctx context.Context, ttl, every time.Duration) {
	if ttl <= 0 {
		return
	}
	if every <= 0 {
		every = 10 * time.Minute
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if files, dirs := s.sweep(ttl); files+dirs > 0 {
					log.Printf("machine: expired %d file(s), removed %d empty work folder(s)", files, dirs)
				}
			}
		}
	}()
}

func (s *Machine) sweep(ttl time.Duration) (files, workspaces int) {
	entries, err := os.ReadDir(s.BaseDir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		cutoff := time.Now().Add(-ttl)
		ws := filepath.Join(s.BaseDir, e.Name())
		s.mu.Lock()
		busy := s.active[ws] > 0
		s.mu.Unlock()
		if busy {
			continue
		}
		var dirs []string
		_ = filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != ws {
					dirs = append(dirs, p)
				}
				return nil
			}

			if info, err := d.Info(); err == nil && info.ModTime().Before(cutoff) {
				if os.Remove(p) == nil {
					files++
				}
			}
			return nil
		})
		for i := len(dirs) - 1; i >= 0; i-- {
			_ = os.Remove(dirs[i])
		}
		if left, err := os.ReadDir(ws); err == nil && len(left) == 0 {
			if os.Remove(ws) == nil {
				workspaces++
				s.mu.Lock()
				delete(s.touched, ws)
				s.mu.Unlock()
			}
		}
	}
	return files, workspaces
}

func (s *Machine) Execute(ctx context.Context, sessionID, command string, timeout time.Duration) *ExecutionResult {
	return s.Run(ctx, sessionID, RunOptions{Shell: command, Timeout: timeout})
}

func (s *Machine) Run(ctx context.Context, sessionID string, opts RunOptions) *ExecutionResult {
	ws, err := s.Workspace(sessionID)
	if err != nil {
		return &ExecutionResult{ExitCode: -1, Error: err.Error()}
	}
	if opts.InSession {
		sh := s.shellOf(ws)
		if sh == nil && opts.Yield != nil && opts.Shell != "" {
			sh, _ = s.startShell(sessionID, ws)
		}
		if sh != nil {
			return s.runInShell(ctx, sessionID, sh, opts)
		}
	}
	release, err := s.acquire(ctx, opts.Heavy)
	if err != nil {
		return &ExecutionResult{ExitCode: -1, Error: err.Error()}
	}
	defer release()
	top := s.topWorkspace(sessionID)
	s.begin(top)
	defer s.end(top)
	return s.runIn(ctx, ws, opts)
}

func (s *Machine) acquire(ctx context.Context, heavy bool) (func(), error) {
	errBusy := errors.New("cancelled while waiting for a free slot")
	if heavy {
		select {
		case s.heavy <- struct{}{}:
		case <-ctx.Done():
			return nil, errBusy
		}
	}
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		if heavy {
			<-s.heavy
		}
		return nil, errBusy
	}
	return func() {
		<-s.sem
		if heavy {
			<-s.heavy
		}
	}, nil
}

func (s *Machine) runIn(ctx context.Context, ws string, opts RunOptions) *ExecutionResult {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	argv := opts.Args
	if opts.Shell != "" {
		argv = []string{"bash", "-c", opts.Shell}
	}
	if len(argv) == 0 {
		return &ExecutionResult{ExitCode: -1, Error: "empty command"}
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	hostExecutable := argv[0]
	argv = s.hostArgv(ws, argv)
	if opts.HostExecutable {
		argv[0] = hostExecutable
	} else {
		argv = s.sandboxed(ws, argv)
	}
	cmd := exec.CommandContext(execCtx, argv[0], argv[1:]...)
	cmd.Dir = ws
	cmd.Env = flattenEnv(s.baseEnv(opts, ws))
	cmd.WaitDelay = 2 * time.Second
	if len(opts.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(opts.Stdin)
	}
	stdout := &capWriter{max: maxCapturedOutput}
	stderr := &capWriter{max: maxCapturedOutput}
	if opts.MergeOutput {
		stderr = stdout
	}
	var outW, errW io.Writer = stdout, stderr
	if opts.Tee != nil {
		outW = io.MultiWriter(stdout, opts.Tee)
		errW = io.MultiWriter(stderr, opts.Tee)
		if opts.MergeOutput {
			errW = outW
		}
	}
	cmd.Stdout, cmd.Stderr = outW, errW

	start := time.Now()
	err := cmd.Run()
	res := &ExecutionResult{
		Stdout:    strings.ToValidUTF8(stdout.String(), ""),
		Duration:  time.Since(start).Round(time.Millisecond).String(),
		Truncated: stdout.truncated || stderr.truncated,
	}
	if !opts.MergeOutput {
		res.Stderr = strings.ToValidUTF8(stderr.String(), "")
	}
	if err != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.Is(execCtx.Err(), context.DeadlineExceeded):
			res.ExitCode = 124
			res.Error = fmt.Sprintf("execution timed out after %v", timeout)
		case ctx.Err() != nil:
			res.ExitCode = -1
			res.Error = "cancelled"
		case errors.As(err, &exitErr):
			res.ExitCode = exitErr.ExitCode()
		default:
			res.ExitCode = -1
			res.Error = err.Error()
		}
	}
	return res
}

const defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

func (s *Machine) Available(programs ...string) map[string]bool {
	out := map[string]bool{}
	for _, p := range programs {
		if _, err := exec.LookPath(p); err == nil {
			out[p] = true
		}
	}
	return out
}

func (s *Machine) Check(argv ...string) bool {
	tmp, err := os.MkdirTemp(s.BaseDir, ".probe-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(tmp)
	return s.runIn(context.Background(), tmp, RunOptions{Args: argv, HostExecutable: true, Timeout: 30 * time.Second}).ExitCode == 0
}

func (s *Machine) baseEnv(opts RunOptions, ws string) map[string]string {
	host := hostEnv()
	home, tmp := host["HOME"], "/tmp"
	if t := host["TMPDIR"]; t != "" {
		tmp = t
	}
	env := map[string]string{
		"LC_ALL":                        "C.UTF-8",
		"PYTHONUNBUFFERED":              "1",
		"PYTHONDONTWRITEBYTECODE":       "1",
		"MPLBACKEND":                    "Agg",
		"MPLCONFIGDIR":                  tmp + "/mpl",
		"TERM":                          "dumb",
		"NO_COLOR":                      "1",
		"PIP_DISABLE_PIP_VERSION_CHECK": "1",

		"GIT_AUTHOR_NAME": "Macron", "GIT_AUTHOR_EMAIL": "macron@localhost",
		"GIT_COMMITTER_NAME": "Macron", "GIT_COMMITTER_EMAIL": "macron@localhost",
	}
	for k, v := range host {
		env[k] = v
	}
	if env["HOME"] == "" {
		env["HOME"] = home
	}
	for k, v := range opts.Env {
		env[k] = s.hostPath(ws, v)
	}
	return env
}

func flattenEnv(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

type capWriter struct {
	max       int
	head      []byte
	tail      []byte
	dropped   int64
	truncated bool
}

func (w *capWriter) headMax() int { return w.max / 4 }
func (w *capWriter) tailMax() int { return w.max - w.headMax() }

func (w *capWriter) Write(p []byte) (int, error) {
	n := len(p)
	if room := w.headMax() - len(w.head); room > 0 && len(w.tail) == 0 {
		take := min(room, len(p))
		w.head = append(w.head, p[:take]...)
		p = p[take:]
	}
	if len(p) >= w.tailMax() {
		w.dropped += int64(len(w.tail) + len(p) - w.tailMax())
		w.tail = append(w.tail[:0], p[len(p)-w.tailMax():]...)
		w.truncated = w.dropped > 0
		return n, nil
	}
	w.tail = append(w.tail, p...)
	if len(w.tail) > 2*w.tailMax() {
		w.trim()
	}
	return n, nil
}

func (w *capWriter) trim() {
	if over := len(w.tail) - w.tailMax(); over > 0 {
		w.dropped += int64(over)
		w.tail = append(w.tail[:0:0], w.tail[over:]...)
		w.truncated = true
	}
}

func (w *capWriter) String() string {
	w.trim()
	if !w.truncated {
		return string(w.head) + string(w.tail)
	}
	mark := fmt.Sprintf("\n… [%s of output cut from the middle] …\n", humanBytes(w.dropped))
	tail := w.tail[min(len(mark), len(w.tail)):]
	if i := bytes.IndexByte(tail, '\n'); i >= 0 && i < 400 {
		tail = tail[i+1:]
	}
	return strings.TrimRight(string(w.head), "\n") + mark + string(tail)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}
