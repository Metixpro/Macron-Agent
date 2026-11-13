package machine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type supMsg struct {
	Op      string            `json:"op"`
	ID      int               `json:"id"`
	Script  string            `json:"script"`
	Timeout float64           `json:"timeout"`
	Env     map[string]string `json:"env"`
	Merge   bool              `json:"merge"`
	Stdin   string            `json:"stdin"`
	Job     int               `json:"job"`
	Log     string            `json:"log"`
}

type goRun struct {
	cmd   *exec.Cmd
	tree  *tree
	lk    sync.Mutex
	log   *os.File
	job   int
	ended bool
	moved chan struct{}
}

type goJob struct {
	tree *tree
	log  string
}

type goShell struct {
	dir  string
	env  []string
	keep int64
	argv func(script string) ([]string, func(), error)

	out *io.PipeWriter
	wmu sync.Mutex

	mu   sync.Mutex
	runs map[int]*goRun
	jobs map[int]*goJob
	work sync.WaitGroup
}

func newGoShell(dir string, env []string, keep int64, argv func(string) ([]string, func(), error)) (*goShell, io.WriteCloser, io.Reader, <-chan struct{}) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	g := &goShell{dir: dir, env: env, keep: keep, argv: argv, out: outW, runs: map[int]*goRun{}, jobs: map[int]*goJob{}}
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go g.trim(ctx)
	go func() {
		g.serve(inR)
		cancel()
		g.killAll()
		g.work.Wait()
		_ = outW.Close()
		close(done)
	}()
	return g, inW, outR, done
}

func (g *goShell) send(m map[string]any) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	g.wmu.Lock()
	defer g.wmu.Unlock()
	_, _ = g.out.Write(append(b, '\n'))
}

func (g *goShell) serve(in io.Reader) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		var m supMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Op {
		case "run":
			g.spawn(func() { g.run(m) })
		case "stop":
			g.mu.Lock()
			r := g.runs[m.ID]
			g.mu.Unlock()
			if r != nil {
				go r.tree.kill()
			}
		case "detach":
			g.detach(m)
		case "bg":
			g.bg(m)
		case "kill":
			g.spawn(func() { g.killJob(m) })
		}
	}
}

func (g *goShell) spawn(fn func()) {
	g.work.Add(1)
	go func() {
		defer g.work.Done()
		fn()
	}()
}

func (g *goShell) killAll() {
	g.mu.Lock()
	var trees []*tree
	for _, r := range g.runs {
		trees = append(trees, r.tree)
	}
	for _, j := range g.jobs {
		trees = append(trees, j.tree)
	}
	g.mu.Unlock()
	for _, t := range trees {
		t.kill()
	}
}

func (g *goShell) command(script string, extra map[string]string) (*exec.Cmd, func(), error) {
	argv, cleanup, err := g.argv(script)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = g.dir
	cmd.Env = mergeEnv(g.env, extra)
	groupAttr(cmd)
	return cmd, cleanup, nil
}

func mergeEnv(base []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := lookupFold(extra, k); ok {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

func lookupFold(m map[string]string, key string) (string, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	if !foldEnv {
		return "", false
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func completeUTF8(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if utf8.FullRune(b[i:]) {
				return len(b)
			}
			return i
		}
	}
	return len(b)
}

func (g *goShell) pump(id int, r *goRun, f *os.File, isErr bool, wg *sync.WaitGroup) {
	defer wg.Done()
	defer f.Close()
	buf := make([]byte, 64<<10)
	var rest []byte
	for {
		n, err := f.Read(buf)
		if n > 0 {
			r.lk.Lock()
			if r.log != nil {
				if len(rest) > 0 {
					_, _ = r.log.Write(rest)
					rest = nil
				}
				_, _ = r.log.Write(buf[:n])
			} else {
				data := append(rest, buf[:n]...)
				cut := completeUTF8(data)
				rest = append([]byte(nil), data[cut:]...)
				if cut > 0 {
					g.send(map[string]any{"op": "out", "id": id, "err": isErr, "data": strings.ToValidUTF8(string(data[:cut]), "�")})
				}
			}
			r.lk.Unlock()
		}
		if err != nil {
			break
		}
	}
	if len(rest) > 0 {
		r.lk.Lock()
		if r.log == nil {
			g.send(map[string]any{"op": "out", "id": id, "err": isErr, "data": strings.ToValidUTF8(string(rest), "�")})
		}
		r.lk.Unlock()
	}
}

func joinWithin(wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}

func (g *goShell) run(m supMsg) {
	id := m.ID
	fail := func(err error) {
		g.send(map[string]any{"op": "exit", "id": id, "code": -1, "error": err.Error()})
	}
	cmd, cleanup, err := g.command(m.Script, m.Env)
	if err != nil {
		fail(err)
		return
	}
	defer cleanup()
	outR, outW, err := os.Pipe()
	if err != nil {
		fail(err)
		return
	}
	var errR, errW *os.File
	cmd.Stdout, cmd.Stderr = outW, outW
	if !m.Merge {
		if errR, errW, err = os.Pipe(); err != nil {
			outR.Close()
			outW.Close()
			fail(err)
			return
		}
		cmd.Stderr = errW
	}
	if m.Stdin != "" {
		cmd.Stdin = strings.NewReader(m.Stdin)
	}
	t, err := startGroup(cmd)
	outW.Close()
	if errW != nil {
		errW.Close()
	}
	if err != nil {
		outR.Close()
		if errR != nil {
			errR.Close()
		}
		fail(err)
		return
	}
	r := &goRun{cmd: cmd, tree: t, moved: make(chan struct{})}
	g.mu.Lock()
	g.runs[id] = r
	g.mu.Unlock()
	var pumps sync.WaitGroup
	pumps.Add(1)
	go g.pump(id, r, outR, false, &pumps)
	if errR != nil {
		pumps.Add(1)
		go g.pump(id, r, errR, true, &pumps)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	timeout := time.Duration(m.Timeout * float64(time.Second))
	if timeout <= 0 {
		timeout = time.Minute
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	timedOut := false
	var werr error
	select {
	case werr = <-waited:
	case <-r.moved:
		werr = <-waited
	case <-timer.C:
		timedOut = true
		t.kill()
		werr = <-waited
	}
	r.lk.Lock()
	moved := r.log != nil
	r.ended = !moved
	r.lk.Unlock()
	t.kill()
	joinWithin(&pumps, 2*time.Second)
	t.close()
	g.mu.Lock()
	delete(g.runs, id)
	g.mu.Unlock()
	if moved {
		r.lk.Lock()
		_ = r.log.Close()
		r.lk.Unlock()
		g.send(map[string]any{"op": "jobexit", "job": r.job, "code": exitCode(werr)})
		return
	}
	g.send(map[string]any{"op": "exit", "id": id, "code": exitCode(werr), "timeout": timedOut})
}

func (g *goShell) detach(m supMsg) {
	g.mu.Lock()
	r := g.runs[m.ID]
	g.mu.Unlock()
	if r == nil {
		return
	}
	r.lk.Lock()
	defer r.lk.Unlock()
	if r.ended || r.log != nil {
		return
	}
	f, err := os.OpenFile(m.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		g.send(map[string]any{"op": "nodetach", "id": m.ID, "error": err.Error()})
		return
	}
	r.log, r.job = f, m.Job
	g.mu.Lock()
	g.jobs[m.Job] = &goJob{tree: r.tree, log: m.Log}
	g.mu.Unlock()
	g.send(map[string]any{"op": "detached", "id": m.ID, "job": m.Job, "pid": r.cmd.Process.Pid})
	close(r.moved)
}

func (g *goShell) bg(m supMsg) {
	fail := func(err error) {
		g.send(map[string]any{"op": "error", "id": m.ID, "error": err.Error()})
	}
	log, err := os.OpenFile(m.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fail(err)
		return
	}
	cmd, cleanup, err := g.command(m.Script, nil)
	if err != nil {
		log.Close()
		fail(err)
		return
	}
	cmd.Stdout, cmd.Stderr = log, log
	t, err := startGroup(cmd)
	log.Close()
	if err != nil {
		cleanup()
		fail(err)
		return
	}
	g.mu.Lock()
	g.jobs[m.Job] = &goJob{tree: t, log: m.Log}
	g.mu.Unlock()
	g.send(map[string]any{"op": "started", "id": m.ID, "job": m.Job, "pid": cmd.Process.Pid})
	g.spawn(func() {
		err := cmd.Wait()
		t.kill()
		t.close()
		cleanup()
		g.send(map[string]any{"op": "jobexit", "job": m.Job, "code": exitCode(err)})
	})
}

func (g *goShell) killJob(m supMsg) {
	g.mu.Lock()
	j := g.jobs[m.Job]
	g.mu.Unlock()
	if j != nil {
		j.tree.kill()
	}
	g.send(map[string]any{"op": "killed", "id": m.ID})
}

func (g *goShell) trim(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		g.mu.Lock()
		var logs []string
		for _, j := range g.jobs {
			logs = append(logs, j.log)
		}
		g.mu.Unlock()
		for _, p := range logs {
			trimLog(p, g.keep)
		}
	}
}

func trimLog(p string, keep int64) {
	fi, err := os.Stat(p)
	if err != nil || fi.Size() <= 4*keep {
		return
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer f.Close()
	tail := make([]byte, keep)
	n, err := f.ReadAt(tail, fi.Size()-keep)
	if err != nil && !errors.Is(err, io.EOF) {
		return
	}
	data := append([]byte("[... earlier output was cut ...]\n"), tail[:n]...)
	if _, err := f.WriteAt(data, 0); err == nil {
		_ = f.Truncate(int64(len(data)))
	}
}
