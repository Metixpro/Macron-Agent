package tools

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

const (
	browserDaemonIdle     = 15 * time.Minute
	browserDaemonReapTick = 2 * time.Minute
	// Cold start spawns Python, imports Playwright and launches Chromium;
	// on loaded or low-power machines that can take well over 35s, and the
	// wait only caps failure detection — a healthy daemon signals ready as
	// soon as it is up, so a generous cap costs nothing on the happy path.
	browserDaemonBootWait = 60 * time.Second
)

var errDaemonUnsupported = errors.New("persistent browser daemon unavailable")

type BrowserTab struct {
	Index  int    `json:"index"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Active bool   `json:"active"`
}

type BrowserViewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type BrowserLiveState struct {
	Available      bool             `json:"available"`
	Open           bool             `json:"open"`
	Live           bool             `json:"live"`
	AgentActive    bool             `json:"agentActive"`
	Takeover       bool             `json:"takeover"`
	TakeoverReason string           `json:"takeoverReason,omitempty"`
	URL            string           `json:"url,omitempty"`
	Title          string           `json:"title,omitempty"`
	Updated        int64            `json:"updated,omitempty"`
	Tabs           []BrowserTab     `json:"tabs,omitempty"`
	Viewport       *BrowserViewport `json:"viewport,omitempty"`
}

type BrowserInputEvent struct {
	Type       string  `json:"type"`
	X          float64 `json:"x,omitempty"`
	Y          float64 `json:"y,omitempty"`
	DeltaX     float64 `json:"deltaX,omitempty"`
	DeltaY     float64 `json:"deltaY,omitempty"`
	Button     string  `json:"button,omitempty"`
	ClickCount int     `json:"clickCount,omitempty"`
	Key        string  `json:"key,omitempty"`
	Text       string  `json:"text,omitempty"`
	Modifiers  int     `json:"modifiers,omitempty"`
}

type takeoverSlot struct {
	active bool
	reason string
	waitCh chan struct{}
}

type browserPool struct {
	reg    *Registry
	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	daemons   map[string]*browserDaemon
	starting  map[string]chan struct{}
	takeover  map[string]*takeoverSlot
	observers map[string]func(BrowserLiveState)
}

func (r *Registry) daemonPool() *browserPool {
	r.poolOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		r.browserPool = &browserPool{
			reg:       r,
			ctx:       ctx,
			cancel:    cancel,
			daemons:   map[string]*browserDaemon{},
			starting:  map[string]chan struct{}{},
			takeover:  map[string]*takeoverSlot{},
			observers: map[string]func(BrowserLiveState){},
		}
		go r.browserPool.reapLoop()
	})
	return r.browserPool
}

func browserSessionKey(sessionID string) string {
	return machine.SafeSessionID(machine.ParentSession(sessionID))
}

func (p *browserPool) reapLoop() {
	t := time.NewTicker(browserDaemonReapTick)
	defer t.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-t.C:
			p.reapIdle()
		}
	}
}

func (p *browserPool) reapIdle() {
	now := time.Now()
	var stale []*browserDaemon
	p.mu.Lock()
	for _, d := range p.daemons {
		if d != nil && d.idleExceeded(now, browserDaemonIdle) {
			stale = append(stale, d)
		}
	}
	p.mu.Unlock()
	for _, d := range stale {
		d.close()
	}
}

func (p *browserPool) notifyObserver(key string) {
	p.mu.Lock()
	fn := p.observers[key]
	st := p.stateLocked(key)
	p.mu.Unlock()
	if fn != nil {
		fn(st)
	}
}

func (p *browserPool) stateLocked(key string) BrowserLiveState {
	st := BrowserLiveState{Available: p.reg.HasBrowser()}
	if slot := p.takeover[key]; slot != nil {
		st.Takeover = slot.active
		st.TakeoverReason = slot.reason
	}
	if d := p.daemons[key]; d != nil && !d.isClosed() {
		ds := d.getState()
		ds.Available = st.Available
		ds.Live = true
		ds.Takeover = st.Takeover
		ds.TakeoverReason = st.TakeoverReason
		return ds
	}
	return st
}

func (p *browserPool) isTakeover(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	slot := p.takeover[key]
	return slot != nil && slot.active
}

func (p *browserPool) setTakeover(key string, active bool, reason string) *browserDaemon {
	p.mu.Lock()
	slot := p.takeover[key]
	if slot == nil {
		slot = &takeoverSlot{}
		p.takeover[key] = slot
	}
	if active {
		if !slot.active {
			slot.active = true
			slot.waitCh = make(chan struct{})
		}
		slot.reason = strings.TrimSpace(reason)
		if slot.reason == "" {
			slot.reason = "user"
		}
	} else {
		slot.active = false
		slot.reason = ""
		if slot.waitCh != nil {
			close(slot.waitCh)
			slot.waitCh = nil
		}
	}
	d := p.daemons[key]
	p.mu.Unlock()
	return d
}

func (p *browserPool) waitIfTakeover(ctx context.Context, key string) error {
	for {
		p.mu.Lock()
		slot := p.takeover[key]
		if slot == nil || !slot.active || slot.waitCh == nil {
			p.mu.Unlock()
			return nil
		}
		ch := slot.waitCh
		p.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		case <-p.ctx.Done():
			return errors.New("browser runtime stopped")
		}
	}
}

func (p *browserPool) getDaemon(key string) *browserDaemon {
	p.mu.Lock()
	defer p.mu.Unlock()
	d := p.daemons[key]
	if d != nil && !d.isClosed() {
		d.touch()
		return d
	}
	return nil
}

func (p *browserPool) ensureDaemon(ctx context.Context, sessionID string, initCfg map[string]any) (*browserDaemon, error) {
	if !p.reg.HasBrowser() {
		return nil, errors.New("browser runtime is not available")
	}
	key := browserSessionKey(sessionID)
	for {
		p.mu.Lock()
		if p.ctx.Err() != nil {
			p.mu.Unlock()
			return nil, errors.New("browser runtime stopped")
		}
		if d := p.daemons[key]; d != nil && !d.isClosed() {
			d.touch()
			p.mu.Unlock()
			return d, nil
		}
		if waitCh := p.starting[key]; waitCh != nil {
			p.mu.Unlock()
			select {
			case <-waitCh:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-p.ctx.Done():
				return nil, errors.New("browser runtime stopped")
			}
		}
		startCh := make(chan struct{})
		p.starting[key] = startCh
		p.mu.Unlock()

		d, err := p.startDaemon(ctx, sessionID, key, initCfg)

		p.mu.Lock()
		if err == nil && d != nil {
			p.daemons[key] = d
		}
		delete(p.starting, key)
		close(startCh)
		takeover := p.takeover[key] != nil && p.takeover[key].active
		p.mu.Unlock()

		if err != nil {
			return nil, err
		}
		if takeover {
			pctx, pcancel := context.WithTimeout(p.ctx, 5*time.Second)
			_, _ = d.call(pctx, map[string]any{"op": "pause", "paused": true})
			pcancel()
		}
		p.notifyObserver(key)
		return d, nil
	}
}

func (p *browserPool) startDaemon(ctx context.Context, sessionID, key string, initCfg map[string]any) (*browserDaemon, error) {
	b := p.reg.browser
	if b == nil || b.chrome == "" {
		return nil, errDaemonUnsupported
	}
	common, err := scriptFS.ReadFile("scripts/browser_common.py")
	if err != nil {
		return nil, err
	}
	code, err := scriptFS.ReadFile("scripts/browser_daemon.py")
	if err != nil {
		return nil, err
	}
	scriptDir, err := os.MkdirTemp("", "macron-bd-")
	if err != nil {
		return nil, err
	}
	scriptPath := filepath.Join(scriptDir, "daemon.py")
	payloadScript := make([]byte, 0, len(common)+1+len(code))
	payloadScript = append(payloadScript, common...)
	payloadScript = append(payloadScript, '\n')
	payloadScript = append(payloadScript, code...)
	if err := os.WriteFile(scriptPath, payloadScript, 0o600); err != nil {
		_ = os.RemoveAll(scriptDir)
		return nil, err
	}
	cfg := map[string]any{
		"chrome":  b.chrome,
		"libs":    b.libs,
		"offline": false,
		"mobile":  false,
		"locale":  "en-US",
	}
	for k, v := range initCfg {
		if v != nil {
			cfg[k] = v
		}
	}
	py := b.python
	useIsolated := b.isolated
	if py == "" {
		py = "python3"
		useIsolated = true
	}
	argv := []string{py, scriptPath}
	if useIsolated {
		argv = []string{py, "-I", scriptPath}
	}
	proc, err := p.reg.Machine.StartHostProcess(p.ctx, sessionID, argv, map[string]string{
		"MACRON_BROWSER_DAEMON": "1",
	})
	if err != nil {
		_ = os.RemoveAll(scriptDir)
		return nil, err
	}
	initBytes, err := json.Marshal(cfg)
	if err != nil {
		_ = proc.Close()
		_ = os.RemoveAll(scriptDir)
		return nil, err
	}
	if _, err := proc.Stdin.Write(append(initBytes, '\n')); err != nil {
		_ = proc.Close()
		_ = os.RemoveAll(scriptDir)
		return nil, err
	}

	d := &browserDaemon{
		pool:       p,
		key:        key,
		proc:       proc,
		scriptDir:  scriptDir,
		pending:    map[int64]chan daemonReply{},
		subs:       map[uint64]chan []byte{},
		readyCh:    make(chan struct{}),
		done:       make(chan struct{}),
		stderrDone: make(chan struct{}),
		lastUsed:   time.Now(),
		state:      BrowserLiveState{Available: true, Live: true},
	}
	go d.readStderr()
	go d.readStdout()

	bootTimer := time.NewTimer(browserDaemonBootWait)
	defer bootTimer.Stop()
	select {
	case <-d.readyCh:
		return d, nil
	case <-d.done:
		detail := d.stderrTail()
		if strings.Contains(detail, "No module named") || strings.Contains(detail, "async_api") {
			return nil, fmt.Errorf("%w: %s", errDaemonUnsupported, detail)
		}
		return nil, fmt.Errorf("browser daemon exited on startup: %s", detail)
	case <-ctx.Done():
		d.close()
		return nil, ctx.Err()
	case <-bootTimer.C:
		d.close()
		return nil, fmt.Errorf("timed out starting browser daemon: %s", d.stderrTail())
	}
}

type daemonReply struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error"`
	Result json.RawMessage `json:"result"`
	State  *daemonStateMsg `json:"state"`
	Closed bool            `json:"closed"`
}

type daemonStateMsg struct {
	Open        bool             `json:"open"`
	URL         string           `json:"url"`
	Title       string           `json:"title"`
	Updated     int64            `json:"updated"`
	Paused      bool             `json:"paused"`
	AgentActive bool             `json:"agent_active"`
	Tabs        []BrowserTab     `json:"tabs"`
	Viewport    *BrowserViewport `json:"viewport"`
}

type daemonWireMsg struct {
	ID     int64           `json:"id"`
	Event  string          `json:"event"`
	OK     bool            `json:"ok"`
	Error  string          `json:"error"`
	Result json.RawMessage `json:"result"`
	State  *daemonStateMsg `json:"state"`
	Closed bool            `json:"closed"`
	JPEG   string          `json:"jpeg"`
}

type browserDaemon struct {
	pool       *browserPool
	key        string
	proc       *machine.HostProcess
	scriptDir  string
	wmu        sync.Mutex
	mu         sync.Mutex
	pending    map[int64]chan daemonReply
	nextID     int64
	state      BrowserLiveState
	lastFrame  []byte
	subs       map[uint64]chan []byte
	nextSub    uint64
	lastUsed   time.Time
	closed     bool
	readyOnce  sync.Once
	readyCh    chan struct{}
	done       chan struct{}
	closeOnce  sync.Once
	stderrMu   sync.Mutex
	stderrBuf  []byte
	stderrDone chan struct{}
}

func (d *browserDaemon) touch() {
	d.mu.Lock()
	d.lastUsed = time.Now()
	d.mu.Unlock()
}

func (d *browserDaemon) isClosed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closed
}

func (d *browserDaemon) getState() BrowserLiveState {
	d.mu.Lock()
	defer d.mu.Unlock()
	cp := d.state
	if len(d.state.Tabs) > 0 {
		cp.Tabs = append([]BrowserTab(nil), d.state.Tabs...)
	}
	if d.state.Viewport != nil {
		vp := *d.state.Viewport
		cp.Viewport = &vp
	}
	return cp
}

func (d *browserDaemon) idleExceeded(now time.Time, ttl time.Duration) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.state.AgentActive || len(d.subs) > 0 {
		return false
	}
	return now.Sub(d.lastUsed) > ttl
}

func (d *browserDaemon) applyState(sm *daemonStateMsg) {
	if sm == nil {
		return
	}
	d.mu.Lock()
	d.state.Open = sm.Open
	d.state.Live = true
	d.state.AgentActive = sm.AgentActive
	d.state.URL = sm.URL
	d.state.Title = sm.Title
	d.state.Updated = sm.Updated
	d.state.Tabs = append([]BrowserTab(nil), sm.Tabs...)
	if sm.Viewport != nil {
		vp := *sm.Viewport
		d.state.Viewport = &vp
	}
	d.lastUsed = time.Now()
	d.mu.Unlock()
}

func (d *browserDaemon) readStderr() {
	defer close(d.stderrDone)
	buf := make([]byte, 4096)
	for {
		n, err := d.proc.Stderr.Read(buf)
		if n > 0 {
			d.stderrMu.Lock()
			d.stderrBuf = append(d.stderrBuf, buf[:n]...)
			if len(d.stderrBuf) > 4096 {
				d.stderrBuf = append([]byte(nil), d.stderrBuf[len(d.stderrBuf)-4096:]...)
			}
			d.stderrMu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (d *browserDaemon) stderrTail() string {
	d.stderrMu.Lock()
	defer d.stderrMu.Unlock()
	return tail(strings.TrimSpace(string(d.stderrBuf)), 600)
}

func (d *browserDaemon) readStdout() {
	defer func() {
		<-d.stderrDone
		_ = d.proc.Wait()
		if d.scriptDir != "" {
			_ = os.RemoveAll(d.scriptDir)
		}

		d.mu.Lock()
		d.closed = true
		d.state.Live = false
		d.state.AgentActive = false
		for id, ch := range d.pending {
			select {
			case ch <- daemonReply{OK: false, Error: "browser daemon stopped"}:
			default:
			}
			delete(d.pending, id)
		}
		for sid, sch := range d.subs {
			close(sch)
			delete(d.subs, sid)
		}
		d.mu.Unlock()

		d.pool.mu.Lock()
		if d.pool.daemons[d.key] == d {
			delete(d.pool.daemons, d.key)
		}
		d.pool.mu.Unlock()

		d.pool.notifyObserver(d.key)
		close(d.done)
	}()

	reader := bufio.NewReaderSize(d.proc.Stdout, 256*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var msg daemonWireMsg
			if json.Unmarshal(line, &msg) == nil {
				d.handleWireMsg(&msg)
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				d.stderrMu.Lock()
				if len(d.stderrBuf) == 0 {
					d.stderrBuf = []byte(err.Error())
				}
				d.stderrMu.Unlock()
			}
			return
		}
	}
}

func (d *browserDaemon) handleWireMsg(msg *daemonWireMsg) {
	if msg.State != nil {
		d.applyState(msg.State)
	}
	if msg.ID > 0 {
		rep := daemonReply{
			OK:     msg.OK,
			Error:  msg.Error,
			Result: msg.Result,
			State:  msg.State,
			Closed: msg.Closed,
		}
		d.mu.Lock()
		ch := d.pending[msg.ID]
		delete(d.pending, msg.ID)
		d.mu.Unlock()
		if ch != nil {
			select {
			case ch <- rep:
			default:
			}
		}
		if msg.State != nil {
			d.pool.notifyObserver(d.key)
		}
		return
	}

	switch msg.Event {
	case "ready":
		d.readyOnce.Do(func() { close(d.readyCh) })
		if msg.State != nil && msg.State.Open {
			d.pool.notifyObserver(d.key)
		}
	case "state":
		d.pool.notifyObserver(d.key)
	case "frame":
		if msg.JPEG == "" {
			return
		}
		raw, err := base64.StdEncoding.DecodeString(msg.JPEG)
		if err != nil || len(raw) == 0 {
			return
		}
		d.mu.Lock()
		d.lastFrame = raw
		subs := make([]chan []byte, 0, len(d.subs))
		for _, ch := range d.subs {
			subs = append(subs, ch)
		}
		d.mu.Unlock()
		for _, ch := range subs {
			pushLatestFrame(ch, raw)
		}
	}
}

func pushLatestFrame(ch chan []byte, frame []byte) {
	defer func() { _ = recover() }()
	select {
	case ch <- frame:
	default:
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- frame:
		default:
		}
	}
}

func (d *browserDaemon) send(payload map[string]any, expectReply bool) (int64, chan daemonReply, error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return 0, nil, errors.New("browser daemon is closed")
	}
	d.lastUsed = time.Now()
	var id int64
	var ch chan daemonReply
	if expectReply {
		d.nextID++
		id = d.nextID
		ch = make(chan daemonReply, 1)
		d.pending[id] = ch
		payload["id"] = id
	}
	d.mu.Unlock()

	raw, err := json.Marshal(payload)
	if err != nil {
		if expectReply {
			d.dropPending(id)
		}
		return 0, nil, err
	}
	d.wmu.Lock()
	_, err = d.proc.Stdin.Write(append(raw, '\n'))
	d.wmu.Unlock()
	if err != nil {
		if expectReply {
			d.dropPending(id)
		}
		return 0, nil, err
	}
	return id, ch, nil
}

func (d *browserDaemon) dropPending(id int64) {
	d.mu.Lock()
	delete(d.pending, id)
	d.mu.Unlock()
}

func (d *browserDaemon) call(ctx context.Context, payload map[string]any) (daemonReply, error) {
	id, ch, err := d.send(payload, true)
	if err != nil {
		return daemonReply{}, err
	}
	select {
	case rep := <-ch:
		return rep, nil
	case <-d.done:
		return daemonReply{}, fmt.Errorf("browser closed unexpectedly: %s", d.stderrTail())
	case <-ctx.Done():
		d.dropPending(id)
		return daemonReply{}, ctx.Err()
	}
}

func (d *browserDaemon) subscribe() (initial []byte, ch <-chan []byte, unsub func()) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		closed := make(chan []byte)
		close(closed)
		return nil, closed, func() {}
	}
	d.nextSub++
	sid := d.nextSub
	c := make(chan []byte, 2)
	d.subs[sid] = c
	if len(d.lastFrame) > 0 {
		initial = append([]byte(nil), d.lastFrame...)
	}
	needShot := len(initial) == 0 && d.state.Open
	d.mu.Unlock()

	if needShot {
		go func() { _, _, _ = d.send(map[string]any{"op": "snapshot_frame"}, false) }()
	}
	var once sync.Once
	return initial, c, func() {
		once.Do(func() {
			d.mu.Lock()
			if cur, ok := d.subs[sid]; ok {
				delete(d.subs, sid)
				close(cur)
			}
			d.mu.Unlock()
		})
	}
}

func (d *browserDaemon) close() {
	d.closeOnce.Do(func() {
		_, _, _ = d.send(map[string]any{"op": "nav", "action": "close"}, false)
		_ = d.proc.Close()
		<-d.done
	})
}

// Registry public methods for Desk UI & CLI lifecycle.

func (r *Registry) BrowserState(sessionID string) BrowserLiveState {
	if r == nil || r.browser == nil {
		return BrowserLiveState{Available: false}
	}
	p := r.daemonPool()
	key := browserSessionKey(sessionID)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stateLocked(key)
}

func (r *Registry) SetBrowserObserver(sessionID string, fn func(BrowserLiveState)) {
	if r == nil || r.browser == nil {
		return
	}
	p := r.daemonPool()
	key := browserSessionKey(sessionID)
	p.mu.Lock()
	p.observers[key] = fn
	p.mu.Unlock()
}

func (r *Registry) SubscribeBrowserFrames(sessionID string) (initial []byte, ch <-chan []byte, unsub func()) {
	if r == nil || r.browser == nil {
		closed := make(chan []byte)
		close(closed)
		return nil, closed, func() {}
	}
	d := r.daemonPool().getDaemon(browserSessionKey(sessionID))
	if d == nil {
		closed := make(chan []byte)
		close(closed)
		return nil, closed, func() {}
	}
	return d.subscribe()
}

func (r *Registry) BrowserInput(ctx context.Context, sessionID string, ev BrowserInputEvent) error {
	if !r.HasBrowser() {
		return errors.New("browser runtime is not available")
	}
	p := r.daemonPool()
	key := browserSessionKey(sessionID)
	d := p.getDaemon(key)
	if d == nil {
		if ws, err := r.Machine.Workspace(sessionID); err == nil {
			if raw, err := os.ReadFile(filepath.Join(ws, ".browser", "url")); err == nil && strings.TrimSpace(string(raw)) != "" {
				d, err = p.ensureDaemon(ctx, sessionID, map[string]any{"url": strings.TrimSpace(string(raw))})
				if err != nil {
					return err
				}
			}
		}
	}
	if d == nil {
		return errors.New("no active live browser open")
	}
	payload := map[string]any{
		"op":         "input",
		"type":       ev.Type,
		"x":          ev.X,
		"y":          ev.Y,
		"deltaX":     ev.DeltaX,
		"deltaY":     ev.DeltaY,
		"button":     ev.Button,
		"clickCount": ev.ClickCount,
		"key":        ev.Key,
		"text":       ev.Text,
		"modifiers":  ev.Modifiers,
	}
	rep, err := d.call(ctx, payload)
	if err != nil {
		return err
	}
	if !rep.OK {
		return errors.New(rep.Error)
	}
	return nil
}

func (r *Registry) BrowserNav(ctx context.Context, sessionID, action string, params map[string]any) (BrowserLiveState, error) {
	if !r.HasBrowser() {
		return BrowserLiveState{Available: false}, errors.New("browser runtime is not available")
	}
	p := r.daemonPool()
	key := browserSessionKey(sessionID)
	act := strings.ToLower(strings.TrimSpace(action))

	if rawURL, _ := params["url"].(string); strings.TrimSpace(rawURL) != "" {
		if err := validateBrowserTarget(rawURL); err != nil {
			return r.BrowserState(sessionID), err
		}
	}

	if act == "close" {
		if d := p.getDaemon(key); d != nil {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, _ = d.call(cctx, map[string]any{"op": "nav", "action": "close"})
			cancel()
			d.close()
		}
		r.clearWorkspaceBrowserFiles(sessionID)
		p.setTakeover(key, false, "")
		st := r.BrowserState(sessionID)
		p.notifyObserver(key)
		return st, nil
	}

	d := p.getDaemon(key)
	if d == nil {
		switch act {
		case "close_tab":
			r.clearWorkspaceBrowserFiles(sessionID)
			p.setTakeover(key, false, "")
			return r.BrowserState(sessionID), nil
		case "back", "forward", "switch_tab":
			return r.BrowserState(sessionID), errors.New("no live browser tab is open")
		case "reload":
			// Restore last saved URL if available on disk
			initCfg := map[string]any{}
			if ws, err := r.Machine.Workspace(sessionID); err == nil {
				if raw, err := os.ReadFile(filepath.Join(ws, ".browser", "url")); err == nil && strings.TrimSpace(string(raw)) != "" {
					initCfg["url"] = strings.TrimSpace(string(raw))
				}
			}
			var err error
			d, err = p.ensureDaemon(ctx, sessionID, initCfg)
			if err != nil {
				return r.BrowserState(sessionID), err
			}
			if _, ok := initCfg["url"]; ok {
				return r.BrowserState(sessionID), nil
			}
		default:
			var err error
			d, err = p.ensureDaemon(ctx, sessionID, nil)
			if err != nil {
				return r.BrowserState(sessionID), err
			}
		}
	}

	payload := map[string]any{"op": "nav", "action": act}
	for k, v := range params {
		payload[k] = v
	}
	rep, err := d.call(ctx, payload)
	if err != nil {
		return r.BrowserState(sessionID), err
	}
	if rep.Closed {
		d.close()
	}
	if !rep.OK {
		return r.BrowserState(sessionID), errors.New(rep.Error)
	}
	return r.BrowserState(sessionID), nil
}

func (r *Registry) SetBrowserTakeover(ctx context.Context, sessionID string, active bool, reason string) (BrowserLiveState, error) {
	if !r.HasBrowser() {
		return BrowserLiveState{Available: false}, errors.New("browser runtime is not available")
	}
	p := r.daemonPool()
	key := browserSessionKey(sessionID)
	d := p.setTakeover(key, active, reason)
	if d != nil && !d.isClosed() {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		rep, err := d.call(pctx, map[string]any{"op": "pause", "paused": active})
		cancel()
		if err != nil {
			return r.BrowserState(sessionID), err
		}
		if !rep.OK {
			return r.BrowserState(sessionID), errors.New(rep.Error)
		}
	}
	st := r.BrowserState(sessionID)
	p.notifyObserver(key)
	return st, nil
}

func (r *Registry) CloseBrowser(sessionID string) {
	if r == nil || r.browser == nil {
		return
	}
	p := r.daemonPool()
	key := browserSessionKey(sessionID)
	if d := p.getDaemon(key); d != nil {
		d.close()
	}
	p.setTakeover(key, false, "")
}

func (r *Registry) CloseAllBrowsers() {
	if r == nil || r.browserPool == nil {
		return
	}
	p := r.browserPool
	p.mu.Lock()
	var list []*browserDaemon
	for _, d := range p.daemons {
		if d != nil {
			list = append(list, d)
		}
	}
	p.mu.Unlock()
	for _, d := range list {
		d.close()
	}
	p.cancel()
}

func (r *Registry) clearWorkspaceBrowserFiles(sessionID string) {
	if r == nil || r.Machine == nil {
		return
	}
	ws, err := r.Machine.Workspace(sessionID)
	if err != nil {
		return
	}
	for _, name := range []string{"preview.png", "meta.json", "url"} {
		_ = os.Remove(filepath.Join(ws, ".browser", name))
	}
}

func (r *Registry) browseViaDaemon(ctx context.Context, sessionID string, cfg map[string]any, runTimeout time.Duration) (browserToolOutput, error) {
	var out browserToolOutput
	p := r.daemonPool()
	key := browserSessionKey(sessionID)

	acts, _ := cfg["actions"].([]any)
	closingOnly := len(acts) > 0
	for _, a := range acts {
		m, ok := a.(map[string]any)
		if !ok || !strings.EqualFold(fmt.Sprint(m["action"]), "close") {
			closingOnly = false
			break
		}
	}
	if closingOnly && p.getDaemon(key) == nil {
		r.clearWorkspaceBrowserFiles(sessionID)
		p.setTakeover(key, false, "")
		out.Log = append(out.Log, struct {
			Step   int    `json:"step"`
			What   string `json:"what"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		}{Step: 1, What: "close", OK: true, Detail: "closed"})
		p.notifyObserver(key)
		return out, nil
	}

	if err := p.waitIfTakeover(ctx, key); err != nil {
		return out, err
	}

	initCfg := map[string]any{
		"mobile":         cfg["mobile"],
		"locale":         cfg["locale"],
		"timeout_ms":     cfg["timeout_ms"],
		"nav_timeout_ms": cfg["nav_timeout_ms"],
	}
	d, err := p.ensureDaemon(ctx, sessionID, initCfg)
	if err != nil {
		return out, err
	}

	id, ch, err := d.send(map[string]any{"op": "tool", "cfg": cfg}, true)
	if err != nil {
		return out, err
	}

	// Pause-aware timeout loop: budget only counts down while user is NOT in Takeover.
	remaining := runTimeout
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case rep := <-ch:
			if rep.Closed {
				go d.close()
			}
			if !rep.OK {
				return out, errors.New(rep.Error)
			}
			if len(rep.Result) > 0 {
				if err := json.Unmarshal(rep.Result, &out); err != nil {
					return out, fmt.Errorf("invalid browser response: %w", err)
				}
			}
			return out, nil
		case <-d.done:
			return out, fmt.Errorf("browser stopped unexpectedly: %s", d.stderrTail())
		case <-ctx.Done():
			d.dropPending(id)
			_, _, _ = d.send(map[string]any{"op": "cancel_tool"}, false)
			return out, ctx.Err()
		case <-tick.C:
			if !p.isTakeover(key) {
				remaining -= time.Second
				if remaining <= 0 {
					d.dropPending(id)
					_, _, _ = d.send(map[string]any{"op": "cancel_tool"}, false)
					return out, fmt.Errorf("browser action timed out after %v", runTimeout)
				}
			}
		}
	}
}
