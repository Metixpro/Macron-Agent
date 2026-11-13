package machine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	shellIdle    = time.Hour
	shellJobIdle = 30 * 24 * time.Hour
	shellMaxLife = 30 * 24 * time.Hour
	maxShells    = 64

	MaxJobs = 50

	JobsDir = ".jobs"

	jobLogKeep = 2 << 20
)

type Job struct {
	ID      int
	Command string
	PID     int
	Started time.Time
	Ended   time.Time
	Running bool
	Code    int
	Log     string
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
	why     atomic.Value

	wmu sync.Mutex

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
			default:
			}
		}
		sh.mu.Unlock()
	}
}

func (s *Machine) shellOf(ws string) *shell {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh := s.shells[ws]; sh != nil && sh.alive() {
		return sh
	}
	return nil
}

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

	opts := RunOptions{InSession: true, Env: map[string]string{"MACRON_SHELL": "1"}}
	ctx, cancel := context.WithCancel(context.Background())
	var (
		in   io.WriteCloser
		out  io.Reader
		wait func() error
	)
	stderr := &capWriter{max: 4096}
	if useGoShell {
		_, w, r, ended := newGoShell(ws, flattenEnv(s.baseEnv(opts, ws)), jobLogKeep, shellArgv)
		in, out = w, r
		go func() {
			<-ctx.Done()
			_ = w.Close()
		}()
		wait = func() error {
			<-ended
			return nil
		}
	} else {
		argv := s.sandboxed(ws, []string{"python3", "-I", "-c", shellSupervisor, strconv.Itoa(jobLogKeep)})
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = ws
		cmd.Env = flattenEnv(s.baseEnv(opts, ws))
		cmd.WaitDelay = 3 * time.Second
		cmd.Stderr = stderr
		w, err1 := cmd.StdinPipe()
		r, err2 := cmd.StdoutPipe()
		if err := errors.Join(err1, err2); err != nil {
			cancel()
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			cancel()
			return nil, err
		}
		in, out, wait = w, r, cmd.Wait
	}
	sh := &shell{ws: ws, in: in, cancel: cancel, done: make(chan struct{}), started: time.Now(), used: time.Now(),
		calls: map[int]chan shellMsg{}, jobs: map[int]*Job{}}
	top := s.topWorkspace(sessionID)
	s.begin(top)
	go sh.read(out)
	go func() {
		err := wait()
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

func (sh *shell) stop(why string) {
	sh.why.Store(why)
	_ = sh.in.Close()
	go func() {
		select {
		case <-sh.done:
		case <-time.After(5 * time.Second):
			sh.cancel()
		}
	}()
}

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

func (s *Machine) runInShell(ctx context.Context, sessionID string, sh *shell, opts RunOptions) *ExecutionResult {
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
		script = argsScript(opts.Args)
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
	deadline := time.NewTimer(timeout + 15*time.Second)
	defer deadline.Stop()
	stopped := false
	stopRun := func() {
		if !stopped {
			stopped = true
			_ = sh.send(map[string]any{"op": "stop", "id": id})
		}
	}
	yield := opts.Yield
	var grace <-chan time.Time
	detachJob := 0
	undetach := func() {
		if detachJob != 0 {
			sh.mu.Lock()
			delete(sh.jobs, detachJob)
			sh.mu.Unlock()
			detachJob = 0
		}
	}
wait:
	for {
		select {
		case <-yield:
			yield = nil
			grace = time.After(yieldGrace)
		case <-grace:
			grace = nil
			if !stopped {
				detachJob = s.detach(sessionID, sh, id, script, start)
			}
		case m := <-ch:
			switch m.Op {
			case "detached":
				res.Job, res.PID = m.Job, m.PID
				sh.mu.Lock()
				if j := sh.jobs[m.Job]; j != nil {
					j.PID = m.PID
				}
				sh.mu.Unlock()
				break wait
			case "nodetach":
				undetach()
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
				undetach()
				switch {
				case m.Error != "":
					res.ExitCode, res.Error = -1, m.Error
				case m.Timeout:
					res.ExitCode, res.Error = 124, fmt.Sprintf("execution timed out after %v", timeout)
				case m.Code < 0:
					res.ExitCode = 128 - m.Code
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

const yieldGrace = 2 * time.Second

func (s *Machine) detach(sessionID string, sh *shell, id int, command string, start time.Time) int {
	sh.mu.Lock()
	running := 0
	for _, j := range sh.jobs {
		if j.Running {
			running++
		}
	}
	if running >= MaxJobs {
		sh.mu.Unlock()
		return 0
	}
	sh.nextJob++
	n := sh.nextJob
	log := fmt.Sprintf("%s/%d.log", JobsDir, n)
	sh.jobs[n] = &Job{ID: n, Command: command, Started: start, Running: true, Log: log}
	sh.mu.Unlock()
	drop := func() int {
		sh.mu.Lock()
		delete(sh.jobs, n)
		sh.mu.Unlock()
		return 0
	}
	if err := s.WriteFile(sessionID, log, nil); err != nil {
		return drop()
	}
	if err := sh.send(map[string]any{"op": "detach", "id": id, "job": n, "log": filepath.Join(sh.ws, filepath.FromSlash(log))}); err != nil {
		return drop()
	}
	return n
}

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
	if running >= MaxJobs {
		sh.mu.Unlock()
		return Job{}, fmt.Errorf("%d background jobs are running already; stop one first", running)
	}
	sh.nextJob++
	n := sh.nextJob
	log := fmt.Sprintf("%s/%d.log", JobsDir, n)
	j := &Job{ID: n, Command: command, Started: time.Now(), Running: true, Log: log}
	sh.jobs[n] = j
	sh.mu.Unlock()
	fail := func(err error) (Job, error) {
		sh.mu.Lock()
		delete(sh.jobs, n)
		sh.mu.Unlock()
		return Job{}, err
	}
	if err := s.WriteFile(sessionID, log, nil); err != nil {
		return fail(err)
	}
	id, ch := sh.call()
	defer sh.drop(id)
	sh.touch()
	if err := sh.send(map[string]any{"op": "bg", "id": id, "job": n, "script": s.hostPath(ws, command), "log": filepath.Join(ws, filepath.FromSlash(log))}); err != nil {
		return fail(fmt.Errorf("the background shell is gone: %w", err))
	}
	select {
	case m := <-ch:
		if m.Op != "started" {
			return fail(fmt.Errorf("could not start it: %s", m.Error))
		}
		sh.mu.Lock()
		j.PID = m.PID
		out := *j
		sh.mu.Unlock()
		return out, nil
	case <-sh.done:
		why, _ := sh.why.Load().(string)
		return fail(errors.New("the background shell ended: " + why))
	case <-ctx.Done():
		return Job{}, ctx.Err()
	case <-time.After(20 * time.Second):
		return Job{}, errors.New("the background shell did not answer")
	}
}

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

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?&|;<>(){}[]#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

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

def pump(rid, f, err, r):
    dec = codecs.getincrementaldecoder("utf-8")("replace")
    while True:
        try:
            d = os.read(f.fileno(), 65536)
        except OSError:
            break
        if not d:
            break
        with r["lk"]:
            if r["log"] is not None:
                try:
                    r["log"].write(d)
                except (OSError, ValueError):
                    pass
                continue
            t = dec.decode(d)
            if t:
                send({"op": "out", "id": rid, "err": err, "data": t})

def detach(m):
    r = runs.get(m.get("id"))
    if r is None:
        return
    with r["lk"]:
        if r["ended"] or r["log"] is not None:
            return
        try:
            r["log"] = open(m["log"], "ab", buffering=0)
        except OSError as e:
            send({"op": "nodetach", "id": m["id"], "error": str(e)})
            return
        r["job"] = m["job"]
        jobs[m["job"]] = {"p": r["p"], "log": m["log"]}
        send({"op": "detached", "id": m["id"], "job": m["job"], "pid": r["p"].pid})

def run(m):
    rid, stdin = m["id"], m.get("stdin") or ""
    try:
        p = start(m, subprocess.PIPE, subprocess.STDOUT if m.get("merge") else subprocess.PIPE,
                  subprocess.PIPE if stdin else subprocess.DEVNULL)
    except OSError as e:
        send({"op": "exit", "id": rid, "code": -1, "error": str(e)})
        return
    r = {"p": p, "lk": threading.Lock(), "log": None, "job": None, "ended": False}
    runs[rid] = r
    if stdin:
        def feed():
            try:
                p.stdin.write(stdin.encode())
                p.stdin.close()
            except OSError:
                pass
        threading.Thread(target=feed, daemon=True).start()
    ts = [threading.Thread(target=pump, args=(rid, p.stdout, False, r), daemon=True)]
    if p.stderr is not None:
        ts.append(threading.Thread(target=pump, args=(rid, p.stderr, True, r), daemon=True))
    for t in ts:
        t.start()
    timed_out, code = False, None
    deadline = time.time() + (m.get("timeout") or 60)
    while r["log"] is None:
        try:
            code = p.wait(timeout=max(min(0.25, deadline - time.time()), 0.01))
            break
        except subprocess.TimeoutExpired:
            if time.time() >= deadline:
                timed_out = True
                break
    with r["lk"]:
        moved = r["log"] is not None
        r["ended"] = not moved
    if moved:
        runs.pop(rid, None)
        code = p.wait()
        for t in ts:
            t.join(timeout=2)
        with r["lk"]:
            try:
                r["log"].close()
            except OSError:
                pass
        send({"op": "jobexit", "job": r["job"], "code": code})
        return
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
        threading.Thread(target=kill, args=(runs[m["id"]]["p"],), daemon=True).start()
    elif op == "detach":
        detach(m)
    elif op == "bg":
        bg(m)
    elif op == "kill":
        threading.Thread(target=killjob, args=(m,), daemon=True).start()
for p in [r["p"] for r in list(runs.values())] + [j["p"] for j in list(jobs.values())]:
    try:
        os.killpg(p.pid, signal.SIGKILL)
    except OSError:
        pass
`

type Load struct {
	Running  int
	Slots    int
	SlotsMax int
	Heavy    int
	HeavyMax int
	Shells   int
	Jobs     int
}

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
