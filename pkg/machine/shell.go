package machine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A session's shell: a process that stays up between commands, the way a
// terminal does. Background jobs (a dev server, a database, a watcher) run in
// it, and while it is up the commands that ask for it (RunOptions.InSession)
// run in it too, so they reach those jobs on localhost and share /tmp. The
// first background job starts it; it ends when nothing has run in it for
// shellIdle (a long time while a job still runs). A supervisor inside runs
// the commands; it speaks JSON lines over its stdin and stdout, so nothing in
// the work folder or on the disk carries the bot's orders.

const (
	shellIdle    = time.Hour           // a shell nothing runs in
	shellJobIdle = 30 * 24 * time.Hour // a shell with a job running keeps it for a month of silence
	shellMaxLife = 30 * 24 * time.Hour
	maxShells    = 64
	// MaxJobs is how many background jobs a session runs at once.
	MaxJobs = 50
	// JobsDir holds the background jobs' output, in the workspace.
	JobsDir = ".jobs"
	// jobLogKeep is what a job's log keeps of its end once it grows to 4×.
	jobLogKeep = 2 << 20
)

// Job is a background job of a session's shell.
type Job struct {
	ID      int
	Command string
	PID     int
	Started time.Time
	Ended   time.Time
	Running bool
	Code    int    // the exit code, once it ended
	Log     string // its output, in the workspace
}

type shellMsg struct {
	Op      string `json:"op"`
	ID      int    `json:"id"`
	Err     bool   `json:"err"`
	Data    string `json:"data"`
	Code    int    `json:"code"`
	Timeout bool   `json:"timeout"`
	Job     int    `json:"job"`
	PID     int    `json:"pid"`
	Error   string `json:"error"`
}

type shell struct {
	ws      string
	in      io.WriteCloser
	cancel  context.CancelFunc
	done    chan struct{}
	started time.Time
	why     atomic.Value // why it ended

	wmu sync.Mutex // writes to the supervisor

	mu      sync.Mutex
	calls   map[int]chan shellMsg
	nextID  int
	jobs    map[int]*Job
	nextJob int
	used    time.Time
}

func (sh *shell) alive() bool {
	select {
	case <-sh.done:
		return false
	default:
		return true
	}
}

func (sh *shell) touch() {
	sh.mu.Lock()
	sh.used = time.Now()
	sh.mu.Unlock()
}

func (sh *shell) send(m map[string]any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	sh.wmu.Lock()
	defer sh.wmu.Unlock()
	_, err = sh.in.Write(append(b, '\n'))
	return err
}

// call opens a conversation with the supervisor; drop ends it.
func (sh *shell) call() (int, chan shellMsg) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.nextID++
	ch := make(chan shellMsg, 1024)
	sh.calls[sh.nextID] = ch
	return sh.nextID, ch
}

func (sh *shell) drop(id int) {
	sh.mu.Lock()
	delete(sh.calls, id)
	sh.mu.Unlock()
}

// read hands the supervisor's messages to their callers.
func (sh *shell) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var m shellMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		sh.mu.Lock()
		if m.Op == "jobexit" {
			if j := sh.jobs[m.Job]; j != nil && j.Running {
				j.Running, j.Code, j.Ended = false, m.Code, time.Now()
			}
		} else if ch := sh.calls[m.ID]; ch != nil {
			select {
			case ch <- m:
			default: // a caller that stopped listening
			}
		}
		sh.mu.Unlock()
	}
}

// shellOf is the running shell of a workspace (nil: none).
func (s *Machine) shellOf(ws string) *shell {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh := s.shells[ws]; sh != nil && sh.alive() {
		return sh
	}
	return nil
}

// startShell returns the shell of a session, starting it if needed.
func (s *Machine) startShell(sessionID, ws string) (*shell, error) {
	s.shellStart.Lock()
	defer s.shellStart.Unlock()
	if sh := s.shellOf(ws); sh != nil {
		return sh, nil
	}
	s.mu.Lock()
	n := 0
	for _, sh := range s.shells {
		if sh.alive() {
			n++
		}
	}
	s.mu.Unlock()
	if n >= maxShells {
		return nil, errors.New("the server runs as many background shells as it can; try again in a few minutes")
	}

	opts := RunOptions{InSession: true, Env: map[string]string{"WAZE_SHELL": "1"}}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "python3", "-I", "-c", shellSupervisor, strconv.Itoa(jobLogKeep))
	cmd.Dir = ws
	cmd.Env = flattenEnv(s.baseEnv(opts, ws))
	cmd.WaitDelay = 3 * time.Second
	stderr := &capWriter{max: 4096}
	cmd.Stderr = stderr
	in, err1 := cmd.StdinPipe()
	out, err2 := cmd.StdoutPipe()
	if err := errors.Join(err1, err2); err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	sh := &shell{ws: ws, in: in, cancel: cancel, done: make(chan struct{}), started: time.Now(), used: time.Now(),
		calls: map[int]chan shellMsg{}, jobs: map[int]*Job{}}
	top := s.topWorkspace(sessionID)
	s.begin(top) // the janitor leaves a workspace with a running shell alone
	go sh.read(out)
	go func() {
		err := cmd.Wait()
		cancel()
		s.end(top)
		why := "it ended"
		if err != nil && stderr.String() != "" {
			why = strings.TrimSpace(stderr.String())
		}
		sh.why.Store(why)
		sh.mu.Lock()
		for _, j := range sh.jobs {
			if j.Running {
				j.Running, j.Code, j.Ended = false, -1, time.Now()
			}
		}
		sh.mu.Unlock()
		close(sh.done)
	}()
	s.mu.Lock()
	if s.shells == nil {
		s.shells = map[string]*shell{}
	}
	s.shells[ws] = sh
	s.mu.Unlock()
	s.reaper.Do(func() { go s.reapShells() })
	return sh, nil
}

// stop ends a shell and everything running in it.
func (sh *shell) stop(why string) {
	sh.why.Store(why)
	_ = sh.in.Close() // the supervisor stops its processes and leaves
	go func() {
		select {
		case <-sh.done:
		case <-time.After(5 * time.Second):
			sh.cancel()
		}
	}()
}

// reapShells ends the shells nobody uses any more.
func (s *Machine) reapShells() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		for ws, sh := range s.shells {
			if !sh.alive() {
				delete(s.shells, ws)
				continue
			}
			sh.mu.Lock()
			idle, running := time.Since(sh.used), 0
			for _, j := range sh.jobs {
				if j.Running {
					running++
				}
			}
			sh.mu.Unlock()
			switch {
			case time.Since(sh.started) > shellMaxLife:
				sh.stop(fmt.Sprintf("the shell's %s were over", shellMaxLife))
			case running == 0 && idle > shellIdle, idle > shellJobIdle:
				sh.stop(fmt.Sprintf("nothing used it for %s", idle.Round(time.Minute)))
			}
		}
		s.mu.Unlock()
	}
}

// runInShell runs one command of the user's code inside the session's shell.
func (s *Machine) runInShell(ctx context.Context, sh *shell, opts RunOptions) *ExecutionResult {
	release, err := s.acquire(ctx, opts.Heavy)
	if err != nil {
		return &ExecutionResult{ExitCode: -1, Error: err.Error()}
	}
	defer release()
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	script := opts.Shell
	if script == "" {
		if len(opts.Args) == 0 {
			return &ExecutionResult{ExitCode: -1, Error: "empty command"}
		}
		quoted := make([]string, len(opts.Args))
		for i, a := range opts.Args {
			quoted[i] = shellQuote(a)
		}
		script = "exec " + strings.Join(quoted, " ")
	}
	id, ch := sh.call()
	defer sh.drop(id)
	sh.touch()
	defer sh.touch()
	msg := map[string]any{"op": "run", "id": id, "script": s.hostPath(sh.ws, script), "timeout": timeout.Seconds(), "env": opts.Env,
		"merge": opts.MergeOutput, "stdin": string(opts.Stdin)}
	if err := sh.send(msg); err != nil {
		return &ExecutionResult{ExitCode: -1, Error: "the background shell is gone: " + err.Error()}
	}

	stdout, stderr := &capWriter{max: maxCapturedOutput}, &capWriter{max: maxCapturedOutput}
	start := time.Now()
	res := &ExecutionResult{}
	deadline := time.NewTimer(timeout + 15*time.Second) // the supervisor's own timer stops it first
	defer deadline.Stop()
	stopped := false
	stopRun := func() {
		if !stopped {
			stopped = true
			_ = sh.send(map[string]any{"op": "stop", "id": id})
		}
	}
wait:
	for {
		select {
		case m := <-ch:
			switch m.Op {
			case "out":
				w := io.Writer(stdout)
				if m.Err && !opts.MergeOutput {
					w = stderr
				}
				_, _ = w.Write([]byte(m.Data))
				if opts.Tee != nil {
					_, _ = opts.Tee.Write([]byte(m.Data))
				}
			case "exit":
				switch {
				case m.Error != "":
					res.ExitCode, res.Error = -1, m.Error
				case m.Timeout:
					res.ExitCode, res.Error = 124, fmt.Sprintf("execution timed out after %v", timeout)
				case m.Code < 0:
					res.ExitCode = 128 - m.Code // a signal, as a shell reports it
				default:
					res.ExitCode = m.Code
				}
				break wait
			}
		case <-ctx.Done():
			stopRun()
			res.ExitCode, res.Error = -1, "cancelled"
			select {
			case <-time.After(3 * time.Second):
				break wait
			case <-sh.done:
				break wait
			case m := <-ch:
				if m.Op == "exit" {
					break wait
				}
			}
		case <-deadline.C:
			stopRun()
			res.ExitCode, res.Error = 124, fmt.Sprintf("execution timed out after %v", timeout)
			break wait
		case <-sh.done:
			why, _ := sh.why.Load().(string)
			res.ExitCode, res.Error = -1, "the background shell ended: "+why
			break wait
		}
	}
	res.Stdout = strings.ToValidUTF8(stdout.String(), "")
	if !opts.MergeOutput {
		res.Stderr = strings.ToValidUTF8(stderr.String(), "")
	}
	res.Truncated = stdout.truncated || stderr.truncated
	res.Duration = time.Since(start).Round(time.Millisecond).String()
	return res
}

// StartJob runs a command in the background in the session's shell (started
// if needed); its output goes to JobsDir/<id>.log in the workspace.
func (s *Machine) StartJob(ctx context.Context, sessionID, command string) (Job, error) {
	ws, err := s.Workspace(sessionID)
	if err != nil {
		return Job{}, err
	}
	sh, err := s.startShell(sessionID, ws)
	if err != nil {
		return Job{}, err
	}
	sh.mu.Lock()
	running := 0
	for _, j := range sh.jobs {
		if j.Running {
			running++
		}
	}
	sh.nextJob++
	n := sh.nextJob
	sh.mu.Unlock()
	if running >= MaxJobs {
		return Job{}, fmt.Errorf("%d background jobs are running already; stop one first", running)
	}
	log := fmt.Sprintf("%s/%d.log", JobsDir, n)
	if err := s.WriteFile(sessionID, log, nil); err != nil {
		return Job{}, err
	}
	id, ch := sh.call()
	defer sh.drop(id)
	sh.touch()
	if err := sh.send(map[string]any{"op": "bg", "id": id, "job": n, "script": s.hostPath(ws, command), "log": ws + "/" + log}); err != nil {
		return Job{}, fmt.Errorf("the background shell is gone: %w", err)
	}
	select {
	case m := <-ch:
		if m.Op != "started" {
			return Job{}, fmt.Errorf("could not start it: %s", m.Error)
		}
		j := &Job{ID: n, Command: command, PID: m.PID, Started: time.Now(), Running: true, Log: log}
		sh.mu.Lock()
		sh.jobs[n] = j
		sh.mu.Unlock()
		return *j, nil
	case <-sh.done:
		why, _ := sh.why.Load().(string)
		return Job{}, errors.New("the background shell ended: " + why)
	case <-ctx.Done():
		return Job{}, ctx.Err()
	case <-time.After(20 * time.Second):
		return Job{}, errors.New("the background shell did not answer")
	}
}

// Jobs lists a session's background jobs, the newest last (nil: no shell).
func (s *Machine) Jobs(sessionID string) []Job {
	ws, err := s.Workspace(sessionID)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	sh := s.shells[ws]
	s.mu.Unlock()
	if sh == nil {
		return nil
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	out := make([]Job, 0, len(sh.jobs))
	for _, j := range sh.jobs {
		out = append(out, *j)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

// ShellEnded says why a session's shell is gone ("" while it runs or if it never did).
func (s *Machine) ShellEnded(sessionID string) string {
	ws, err := s.Workspace(sessionID)
	if err != nil {
		return ""
	}
	s.mu.Lock()
	sh := s.shells[ws]
	s.mu.Unlock()
	if sh == nil || sh.alive() {
		return ""
	}
	why, _ := sh.why.Load().(string)
	return why
}

// KillJob stops a background job and what it started.
func (s *Machine) KillJob(ctx context.Context, sessionID string, n int) error {
	ws, err := s.Workspace(sessionID)
	if err != nil {
		return err
	}
	sh := s.shellOf(ws)
	if sh == nil {
		return errors.New("no background job is running")
	}
	sh.mu.Lock()
	j := sh.jobs[n]
	sh.mu.Unlock()
	if j == nil {
		return fmt.Errorf("there is no job %d", n)
	}
	id, ch := sh.call()
	defer sh.drop(id)
	sh.touch()
	if err := sh.send(map[string]any{"op": "kill", "id": id, "job": n}); err != nil {
		return err
	}
	select {
	case <-ch:
	case <-sh.done:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(10 * time.Second):
		return errors.New("the background shell did not answer")
	}
	return nil
}

// StopShell ends a session's shell and its jobs (false: it had none).
func (s *Machine) StopShell(sessionID string) bool {
	ws, err := s.Workspace(sessionID)
	if err != nil {
		return false
	}
	sh := s.shellOf(ws)
	if sh == nil {
		return false
	}
	sh.stop("it was stopped")
	<-sh.done
	return true
}

// shellQuote quotes a word for bash.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?&|;<>(){}[]#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellSupervisor runs inside a session's shell: commands come as JSON lines
// on its stdin and their output and ends go back on its stdout. Each command
// runs in a process group of its own; what a command leaves behind ends
// with it, while a background job keeps what it starts until it is stopped.
const shellSupervisor = `import codecs, json, os, signal, subprocess, sys, threading, time
KEEP = int(sys.argv[1])
out = sys.stdout.buffer
lock = threading.Lock()
runs, jobs = {}, {}

def send(m):
    b = (json.dumps(m) + "\n").encode()
    with lock:
        try:
            out.write(b)
            out.flush()
        except OSError:
            pass

def start(m, so, se, si, nice=False):
    env = dict(os.environ)
    env.update(m.get("env") or {})
    argv = ["bash", "-c", m["script"]]
    if nice:
        argv = ["nice", "-n", "10"] + argv
    return subprocess.Popen(argv, stdin=si, stdout=so, stderr=se, env=env, cwd=os.getcwd(), start_new_session=True)

def kill(p):
    try:
        os.killpg(p.pid, signal.SIGTERM)
    except OSError:
        return
    try:
        p.wait(timeout=2)
    except subprocess.TimeoutExpired:
        pass
    time.sleep(0.2)
    try:
        os.killpg(p.pid, signal.SIGKILL)
    except OSError:
        pass

def pump(rid, f, err):
    dec = codecs.getincrementaldecoder("utf-8")("replace")
    while True:
        try:
            d = os.read(f.fileno(), 65536)
        except OSError:
            break
        if not d:
            break
        t = dec.decode(d)
        if t:
            send({"op": "out", "id": rid, "err": err, "data": t})

def run(m):
    rid, stdin = m["id"], m.get("stdin") or ""
    try:
        p = start(m, subprocess.PIPE, subprocess.STDOUT if m.get("merge") else subprocess.PIPE,
                  subprocess.PIPE if stdin else subprocess.DEVNULL)
    except OSError as e:
        send({"op": "exit", "id": rid, "code": -1, "error": str(e)})
        return
    runs[rid] = p
    if stdin:
        def feed():
            try:
                p.stdin.write(stdin.encode())
                p.stdin.close()
            except OSError:
                pass
        threading.Thread(target=feed, daemon=True).start()
    ts = [threading.Thread(target=pump, args=(rid, p.stdout, False), daemon=True)]
    if p.stderr is not None:
        ts.append(threading.Thread(target=pump, args=(rid, p.stderr, True), daemon=True))
    for t in ts:
        t.start()
    timed_out = False
    try:
        code = p.wait(timeout=m.get("timeout") or 60)
    except subprocess.TimeoutExpired:
        timed_out, code = True, None
    kill(p)
    if code is None:
        code = p.wait()
    for t in ts:
        t.join(timeout=2)
    runs.pop(rid, None)
    send({"op": "exit", "id": rid, "code": code, "timeout": timed_out})

def bg(m):
    n = m["job"]
    try:
        with open(m["log"], "ab", buffering=0) as log:
            p = start(m, log, subprocess.STDOUT, subprocess.DEVNULL, nice=True)
    except OSError as e:
        send({"op": "error", "id": m["id"], "error": str(e)})
        return
    jobs[n] = {"p": p, "log": m["log"]}
    send({"op": "started", "id": m["id"], "job": n, "pid": p.pid})
    def wait():
        send({"op": "jobexit", "job": n, "code": p.wait()})
    threading.Thread(target=wait, daemon=True).start()

def killjob(m):
    j = jobs.get(m.get("job"))
    if j:
        kill(j["p"])
    send({"op": "killed", "id": m["id"]})

def trim():
    while True:
        time.sleep(5)
        for j in list(jobs.values()):
            try:
                if os.path.getsize(j["log"]) > 4 * KEEP:
                    with open(j["log"], "r+b") as f:
                        f.seek(-KEEP, 2)
                        tail = f.read()
                        f.seek(0)
                        f.write(b"[... earlier output was cut ...]\n" + tail)
                        f.truncate()
            except OSError:
                pass

threading.Thread(target=trim, daemon=True).start()
for line in sys.stdin.buffer:
    try:
        m = json.loads(line)
    except ValueError:
        continue
    op = m.get("op")
    if op == "run":
        threading.Thread(target=run, args=(m,), daemon=True).start()
    elif op == "stop" and m.get("id") in runs:
        threading.Thread(target=kill, args=(runs[m["id"]],), daemon=True).start()
    elif op == "bg":
        bg(m)
    elif op == "kill":
        threading.Thread(target=killjob, args=(m,), daemon=True).start()
for p in list(runs.values()) + [j["p"] for j in jobs.values()]:
    try:
        os.killpg(p.pid, signal.SIGKILL)
    except OSError:
        pass
`

// Load is what the machine is doing now.
type Load struct {
	Running  int // commands running (and shells counted as one each)
	Slots    int // command slots taken
	SlotsMax int
	Heavy    int // slots heavy commands (downloads, encodes) take
	HeavyMax int
	Shells   int // persistent shells up
	Jobs     int // background jobs running in them
}

// Load reports the machine's activity.
func (s *Machine) Load() Load {
	l := Load{Slots: len(s.sem), SlotsMax: cap(s.sem), Heavy: len(s.heavy), HeavyMax: cap(s.heavy)}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.active {
		l.Running += n
	}
	for _, sh := range s.shells {
		if !sh.alive() {
			continue
		}
		l.Shells++
		sh.mu.Lock()
		for _, j := range sh.jobs {
			if j.Running {
				l.Jobs++
			}
		}
		sh.mu.Unlock()
	}
	return l
}
