package desk

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/cmdgate"
	"github.com/Metixpro/macron-agent/pkg/tui"
)

const (
	keepBlocks   = 2000
	outputLimit  = 24000
	renderEvery  = 90 * time.Millisecond
	previewRunes = 90
)

type Block struct {
	Kind    string `json:"kind"`
	Text    string `json:"text,omitempty"`
	HTML    string `json:"html,omitempty"`
	Name    string `json:"name,omitempty"`
	Label   string `json:"label,omitempty"`
	Arg     string `json:"arg,omitempty"`
	Args    string `json:"args,omitempty"`
	Summary string `json:"summary,omitempty"`
	Output  string `json:"output,omitempty"`
	Failed  bool   `json:"failed,omitempty"`
	At      int64  `json:"at"`
}

type liveTool struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Label    string `json:"label"`
	Arg      string `json:"arg"`
	Progress string `json:"progress,omitempty"`
	Started  int64  `json:"started"`
	args     string
}

type liveSub struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Step   string `json:"step,omitempty"`
	Steps  int    `json:"steps"`
	Done   bool   `json:"done"`
	Failed bool   `json:"failed"`
}

type askView struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Command  string   `json:"command"`
	Findings []string `json:"findings,omitempty"`
	Guardian string   `json:"guardian,omitempty"`
	reply    chan cmdgate.Choice
}

type Live struct {
	Running bool             `json:"running"`
	Started int64            `json:"started,omitempty"`
	HTML    string           `json:"html,omitempty"`
	Thought string           `json:"thought,omitempty"`
	Plan    []agent.PlanItem `json:"plan,omitempty"`
	Subs    []*liveSub       `json:"subs,omitempty"`
	Tools   []*liveTool      `json:"tools,omitempty"`
	Queued  []string         `json:"queued,omitempty"`
	Ask     *askView         `json:"ask,omitempty"`
	Context map[string]int   `json:"context,omitempty"`
	Usage   map[string]int   `json:"usage,omitempty"`
}

type Summary struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Preview string `json:"preview"`
	Updated int64  `json:"updated"`
	Running bool   `json:"running"`
	Asking  bool   `json:"asking"`
}

type project struct {
	srv  *Server
	id   string
	path string

	mu       sync.Mutex
	sess     Session
	blocks   []Block
	loaded   bool
	live     Live
	cancel   context.CancelFunc
	queue    []string
	yield    chan struct{}
	stream   string
	renderAt time.Time
	toolSeq  int
	updated  time.Time
	mode     string
	usageIn  int
	usageOut int
}

func projectID(path string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(path))))
	return hex.EncodeToString(sum[:6])
}

func (p *project) file() string {
	return filepath.Join(p.srv.host.DataDir(), "desktop", p.id+".jsonl")
}

func (p *project) loadLocked() {
	if p.loaded {
		return
	}
	p.loaded = true
	f, err := os.Open(p.file())
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var b Block
		if json.Unmarshal(sc.Bytes(), &b) == nil {
			p.blocks = append(p.blocks, b)
		}
	}
	if len(p.blocks) > keepBlocks {
		p.blocks = p.blocks[len(p.blocks)-keepBlocks:]
	}
	if n := len(p.blocks); n > 0 {
		p.updated = time.UnixMilli(p.blocks[n-1].At)
	}
}

func (p *project) appendLocked(b Block) Block {
	b.At = time.Now().UnixMilli()
	if b.Kind == "assistant" && b.HTML == "" {
		b.HTML = renderMarkdown(b.Text)
	}
	p.blocks = append(p.blocks, b)
	if len(p.blocks) > keepBlocks {
		p.blocks = p.blocks[len(p.blocks)-keepBlocks:]
	}
	p.updated = time.Now()
	stored := b
	stored.HTML = ""
	if raw, err := json.Marshal(stored); err == nil {
		_ = os.MkdirAll(filepath.Dir(p.file()), 0o700)
		if f, err := os.OpenFile(p.file(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			_, _ = f.Write(append(raw, '\n'))
			f.Close()
		}
	}
	return b
}

func (p *project) summary() Summary {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loadLocked()
	s := Summary{ID: p.id, Name: filepath.Base(p.path), Path: p.path, Running: p.live.Running, Asking: p.live.Ask != nil}
	if s.Name == "" || s.Name == "." || s.Name == string(filepath.Separator) {
		s.Name = p.path
	}
	if !p.updated.IsZero() {
		s.Updated = p.updated.UnixMilli()
	}
	for i := len(p.blocks) - 1; i >= 0; i-- {
		if b := p.blocks[i]; b.Kind == "user" || b.Kind == "assistant" {
			s.Preview = clipRunes(plainText(b.Text), previewRunes)
			break
		}
	}
	return s
}

var markdownMarks = strings.NewReplacer("**", "", "__", "", "`", "", "~~", "", "|", " ", "#", "", "> ", "", "- [ ] ", "", "- [x] ", "")

func plainText(s string) string {
	var keep []string
	fence := false
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			fence = !fence
			continue
		}
		if fence || strings.Trim(t, "-|: ") == "" {
			continue
		}
		keep = append(keep, t)
	}
	return strings.Join(strings.Fields(markdownMarks.Replace(strings.Join(keep, " "))), " ")
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func (p *project) snapshot() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loadLocked()
	blocks := make([]Block, len(p.blocks))
	for i, b := range p.blocks {
		if b.Kind == "assistant" && b.HTML == "" {
			p.blocks[i].HTML = renderMarkdown(b.Text)
			b = p.blocks[i]
		}
		blocks[i] = b
	}
	live := p.live
	live.Queued = append([]string(nil), p.queue...)
	return map[string]any{"id": p.id, "path": p.path, "name": filepath.Base(p.path), "blocks": blocks, "live": live}
}

func (p *project) emit(kind string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["type"], data["project"] = kind, p.id
	p.srv.hub.publish(data)
}

func (p *project) session() (Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sess != nil {
		return p.sess, nil
	}
	s, err := p.srv.host.Open(p.path)
	if err != nil {
		return nil, err
	}
	if p.mode != "" {
		_ = s.SetMode(p.mode)
	}
	p.sess = s
	return s, nil
}

func (p *project) send(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("empty message")
	}
	p.mu.Lock()
	p.loadLocked()
	if p.live.Running {
		p.queue = append(p.queue, text)
		if p.yield != nil {
			close(p.yield)
			p.yield = nil
		}
		q := append([]string(nil), p.queue...)
		p.mu.Unlock()
		p.emit("queued", map[string]any{"queued": q})
		return nil
	}
	p.mu.Unlock()
	sess, err := p.session()
	if err != nil {
		return err
	}
	p.mu.Lock()
	user := p.appendLocked(Block{Kind: "user", Text: text})
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.live = Live{Running: true, Started: time.Now().UnixMilli()}
	p.stream, p.toolSeq = "", 0
	p.mu.Unlock()
	p.emit("user", map[string]any{"block": user})
	p.emit("state", map[string]any{"running": true, "started": time.Now().UnixMilli()})
	go p.run(ctx, cancel, sess, text)
	return nil
}

func (p *project) stop() {
	p.mu.Lock()
	cancel, ask := p.cancel, p.live.Ask
	p.live.Ask = nil
	p.mu.Unlock()
	if ask != nil {
		select {
		case ask.reply <- cmdgate.Deny:
		default:
		}
	}
	if cancel != nil {
		cancel()
	}
}

func (p *project) answer(id, choice string) error {
	p.mu.Lock()
	ask := p.live.Ask
	if ask == nil || ask.ID != id {
		p.mu.Unlock()
		return errors.New("that question is no longer open")
	}
	p.live.Ask = nil
	p.mu.Unlock()
	c := cmdgate.Deny
	switch choice {
	case "once":
		c = cmdgate.Once
	case "session":
		c = cmdgate.Session
	case "always":
		c = cmdgate.Always
	}
	select {
	case ask.reply <- c:
	default:
	}
	p.emit("ask_done", map[string]any{"id": id, "choice": choice})
	return nil
}

func (p *project) inbox() []string {
	p.mu.Lock()
	items := p.queue
	p.queue = nil
	var flushed *Block
	var blocks []Block
	if len(items) > 0 {
		flushed = p.flushStreamLocked()
	}
	for _, t := range items {
		blocks = append(blocks, p.appendLocked(Block{Kind: "user", Text: t}))
	}
	p.mu.Unlock()
	if flushed != nil {
		p.emit("block", map[string]any{"block": *flushed})
	}
	for _, b := range blocks {
		p.emit("user", map[string]any{"block": b, "taken": true})
	}
	return items
}

func (p *project) signal() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.yield == nil {
		p.yield = make(chan struct{})
	}
	return p.yield
}

func (p *project) flushStreamLocked() *Block {
	text := strings.TrimSpace(p.stream)
	p.stream, p.live.HTML = "", ""
	if text == "" {
		return nil
	}
	b := p.appendLocked(Block{Kind: "assistant", Text: text})
	return &b
}

func (p *project) listener() *agent.EventListener {
	var subs atomic.Int32
	l := &agent.EventListener{
		OnText: func(s string) {
			if i := strings.LastIndex(s, "<work_notes"); i >= 0 {
				s = s[:i]
			}
			p.mu.Lock()
			p.stream = s
			due := time.Since(p.renderAt) >= renderEvery || s == ""
			var html string
			if due {
				p.renderAt = time.Now()
				html = renderMarkdown(s)
				p.live.HTML = html
			}
			p.mu.Unlock()
			if due {
				p.emit("text", map[string]any{"html": html})
			}
		},
		OnThought: func(t string) {
			t = lastLine(t)
			p.mu.Lock()
			p.live.Thought = t
			p.mu.Unlock()
			p.emit("thought", map[string]any{"text": t})
		},
		OnToolStart: func(name, args string) {
			if name == agent.ToolPlan {
				return
			}
			label, arg := tui.ToolHeading(name, args)
			p.mu.Lock()
			flushed := p.flushStreamLocked()
			p.toolSeq++
			t := &liveTool{ID: p.toolSeq, Name: name, Label: label, Arg: arg, Started: time.Now().UnixMilli(), args: args}
			p.live.Tools = append(p.live.Tools, t)
			p.live.Thought = ""
			p.mu.Unlock()
			if flushed != nil {
				p.emit("block", map[string]any{"block": *flushed})
			}
			p.emit("tool_start", map[string]any{"tool": t})
		},
		OnToolProgress: func(name, text string, _ bool) {
			line := lastLine(text)
			if line == "" {
				return
			}
			p.mu.Lock()
			id := 0
			for i := len(p.live.Tools) - 1; i >= 0; i-- {
				if p.live.Tools[i].Name == name {
					p.live.Tools[i].Progress, id = line, p.live.Tools[i].ID
					break
				}
			}
			p.mu.Unlock()
			if id != 0 {
				p.emit("tool_progress", map[string]any{"id": id, "line": line})
			}
		},
		OnToolEnd: func(name, args, result string, took time.Duration) {
			if name == agent.ToolPlan {
				return
			}
			label, arg := tui.ToolHeading(name, args)
			b := Block{Kind: "tool", Name: name, Label: label, Arg: arg, Args: clipRunes(args, outputLimit), Summary: agent.StepLine(name, args, result),
				Output: clipRunes(result, outputLimit), Failed: agent.StepFailed(name, args, result)}
			p.mu.Lock()
			id := 0
			for i, t := range p.live.Tools {
				if t.Name == name && t.args == args {
					id = t.ID
					p.live.Tools = append(p.live.Tools[:i], p.live.Tools[i+1:]...)
					break
				}
			}
			b = p.appendLocked(b)
			p.mu.Unlock()
			p.emit("tool_end", map[string]any{"id": id, "block": b, "took": took.Milliseconds()})
		},
		OnPlan: func(items []agent.PlanItem) {
			items = append([]agent.PlanItem(nil), items...)
			p.mu.Lock()
			p.live.Plan = items
			p.mu.Unlock()
			p.emit("plan", map[string]any{"items": items})
		},
		OnContext: func(used, window int) {
			p.mu.Lock()
			p.live.Context = map[string]int{"used": used, "window": window}
			p.mu.Unlock()
			p.emit("context", map[string]any{"used": used, "window": window})
		},
	}
	l.SubAgent = func(title, _ string) *agent.EventListener {
		id := int(subs.Add(1))
		s := &liveSub{ID: id, Title: title}
		p.mu.Lock()
		p.live.Subs = append(p.live.Subs, s)
		p.mu.Unlock()
		p.emit("sub", map[string]any{"sub": *s})
		return &agent.EventListener{
			OnToolStart: func(name, args string) {
				if name == agent.ToolPlan {
					return
				}
				label, arg := tui.ToolHeading(name, args)
				p.mu.Lock()
				s.Steps++
				s.Step = strings.TrimSpace(label + " " + arg)
				snap := *s
				p.mu.Unlock()
				p.emit("sub", map[string]any{"sub": snap})
			},
			OnDone: func(_ int, failed bool) {
				p.mu.Lock()
				s.Done, s.Failed = true, failed
				snap := *s
				p.mu.Unlock()
				p.emit("sub", map[string]any{"sub": snap})
			},
		}
	}
	return l
}

func (p *project) asker() cmdgate.Asker {
	return func(ctx context.Context, q cmdgate.Question) cmdgate.Choice {
		id := fmt.Sprintf("%d", time.Now().UnixNano())
		title := "Run this command?"
		if strings.HasPrefix(strings.TrimSpace(q.Command), "[") {
			title = "Allow this action?"
		}
		a := &askView{ID: id, Title: title, Command: q.Command, Guardian: q.Guardian, reply: make(chan cmdgate.Choice, 1)}
		for _, f := range q.Findings {
			a.Findings = append(a.Findings, f.Reason)
		}
		p.mu.Lock()
		p.live.Ask = a
		p.mu.Unlock()
		p.emit("ask", map[string]any{"ask": a})
		p.srv.notify("Macron needs your approval", filepath.Base(p.path))
		select {
		case c := <-a.reply:
			return c
		case <-ctx.Done():
			p.mu.Lock()
			if p.live.Ask == a {
				p.live.Ask = nil
			}
			p.mu.Unlock()
			p.emit("ask_done", map[string]any{"id": id, "choice": "deny"})
			return cmdgate.Deny
		}
	}
}

func (p *project) run(ctx context.Context, cancel context.CancelFunc, sess Session, text string) {
	defer cancel()
	started := time.Now()
	var res *agent.Result
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("internal error: %v", r)
			}
		}()
		res, err = sess.Turn(cmdgate.WithAsker(ctx, p.asker()), text, p.listener(), p.inbox, p.signal)
	}()
	stopped := errors.Is(err, context.Canceled) || ctx.Err() != nil
	p.mu.Lock()
	var out []Block
	partial := strings.TrimSpace(p.stream)
	p.stream, p.live.HTML = "", ""
	answer := ""
	if res != nil {
		answer = strings.TrimSpace(res.Answer)
		p.usageIn += res.Usage.PromptTokens
		p.usageOut += res.Usage.CompletionTokens
	}
	switch {
	case stopped:
		if partial != "" {
			out = append(out, p.appendLocked(Block{Kind: "assistant", Text: partial}))
		}
		out = append(out, p.appendLocked(Block{Kind: "note", Text: "Stopped. Tell Macron what to do instead, or say «continue»."}))
	case err != nil:
		if answer != "" {
			out = append(out, p.appendLocked(Block{Kind: "assistant", Text: answer}))
		}
		title, hint := explain(err)
		out = append(out, p.appendLocked(Block{Kind: "error", Text: title, Summary: hint, Output: err.Error()}))
	case answer != "":
		out = append(out, p.appendLocked(Block{Kind: "assistant", Text: answer}))
	}
	if plan := p.live.Plan; len(plan) > 0 {
		raw, _ := json.Marshal(plan)
		out = append(out, p.appendLocked(Block{Kind: "plan", Text: string(raw)}))
	}
	queued := p.queue
	p.queue = nil
	p.live = Live{Usage: map[string]int{"in": p.usageIn, "out": p.usageOut}}
	p.cancel = nil
	p.mu.Unlock()
	took := time.Since(started)
	done := map[string]any{"blocks": out, "took": took.Milliseconds(), "stopped": stopped, "usage": map[string]int{"in": p.usageIn, "out": p.usageOut}}
	if stopped && len(queued) > 0 {
		done["requeue"] = queued
	}
	p.emit("done", done)
	if took > 30*time.Second && !stopped {
		p.srv.notify("Macron finished", filepath.Base(p.path))
	}
	if len(queued) > 0 && !stopped {
		_ = p.send(strings.Join(queued, "\n\n"))
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return clipRunes(t, 220)
		}
	}
	return ""
}
