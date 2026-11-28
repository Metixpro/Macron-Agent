package desk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/cmdgate"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/tui"
)

const (
	keepBlocks   = 2000
	outputLimit  = 24000
	renderEvery  = 90 * time.Millisecond
	previewRunes = 90
)

type Block struct {
	Kind        string `json:"kind"`
	Text        string `json:"text,omitempty"`
	HTML        string `json:"html,omitempty"`
	Name        string `json:"name,omitempty"`
	Label       string `json:"label,omitempty"`
	Arg         string `json:"arg,omitempty"`
	Args        string `json:"args,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Output      string `json:"output,omitempty"`
	Failed      bool   `json:"failed,omitempty"`
	File        *File  `json:"file,omitempty"`
	Attachments []File `json:"attachments,omitempty"`
	At          int64  `json:"at"`
}

type File struct {
	Name    string `json:"name"`
	Base    string `json:"base"`
	Size    int64  `json:"size"`
	Type    string `json:"type,omitempty"`
	Caption string `json:"caption,omitempty"`
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

type liveDraft struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Arg   string `json:"arg,omitempty"`
	Size  int    `json:"size"`
}

var draftArgRe = regexp.MustCompile(`"(?:filename|path|command|url|query|code)"\s*:\s*"((?:[^"\\]|\\.){1,200})`)

func draftArg(head string) string {
	m := draftArgRe.FindStringSubmatch(head)
	if m == nil {
		return ""
	}
	var v string
	if json.Unmarshal([]byte(`"`+strings.TrimSuffix(m[1], `\`)+`"`), &v) != nil {
		v = m[1]
	}
	v, _, _ = strings.Cut(strings.TrimSpace(v), "\n")
	return clipRunes(v, 120)
}

type UsageView struct {
	In        int     `json:"in"`
	Out       int     `json:"out"`
	Cost      float64 `json:"cost,omitempty"`
	CostKnown bool    `json:"costKnown,omitempty"`
}

type Live struct {
	Draft   *liveDraft       `json:"draft,omitempty"`
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
	Usage   *UsageView       `json:"usage,omitempty"`
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

type queuedPrompt struct {
	prompt string
	block  Block
}

type project struct {
	srv  *Server
	id   string
	path string

	mu            sync.Mutex
	sess          Session
	blocks        []Block
	loaded        bool
	starting      bool
	done          chan struct{}
	live          Live
	cancel        context.CancelFunc
	queue         []queuedPrompt
	yield         chan struct{}
	stream        string
	renderAt      time.Time
	toolSeq       int
	updated       time.Time
	mode          string
	usageIn       int
	usageOut      int
	usageCost     float64
	usageKnown    bool
	activeThread  string
	threads       []ThreadSummary
	threadsLoaded bool
}

func projectID(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:6])
}

// Move only saved projects: a newly opened case variant must never inherit another project's log.
func migrateProjectLog(dir, path string) {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(path))))
	oldID, id := hex.EncodeToString(sum[:6]), projectID(path)
	if oldID == id {
		return
	}
	oldPath := filepath.Join(dir, "desktop", oldID+".jsonl")
	newPath := filepath.Join(dir, "desktop", id+".jsonl")
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		return
	}
	if err := os.Rename(oldPath, newPath); err != nil && !os.IsNotExist(err) {
		log.Printf("desktop: migrate project history: %v", err)
	}
}

func (p *project) file() string {
	p.loadThreadsLocked()
	return p.threadFile(p.activeThread)
}

func (p *project) loadLocked() {
	if p.loaded {
		return
	}
	p.loaded = true
	p.loadThreadsLocked()
	p.blocks, p.updated = p.readThreadBlocks(p.activeThread)
	p.syncActiveThreadLocked()
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
	if b.Kind == "user" || b.Kind == "assistant" {
		p.syncActiveThreadLocked()
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

func cloneBlock(b Block) Block {
	if b.File != nil {
		cp := *b.File
		b.File = &cp
	}
	if len(b.Attachments) > 0 {
		b.Attachments = append([]File(nil), b.Attachments...)
	}
	return b
}

func queuedTexts(q []queuedPrompt) []string {
	if len(q) == 0 {
		return nil
	}
	out := make([]string, len(q))
	for i, item := range q {
		if item.block.Text != "" {
			out[i] = item.block.Text
		} else {
			out[i] = item.prompt
		}
	}
	return out
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
		blocks[i] = cloneBlock(b)
	}
	live := p.live
	if live.Draft != nil {
		d := *live.Draft
		live.Draft = &d
	}
	if len(live.Plan) > 0 {
		live.Plan = append([]agent.PlanItem(nil), live.Plan...)
	}
	if len(live.Subs) > 0 {
		subs := make([]*liveSub, len(live.Subs))
		for i, s := range live.Subs {
			if s != nil {
				cp := *s
				subs[i] = &cp
			}
		}
		live.Subs = subs
	}
	if len(live.Tools) > 0 {
		tools := make([]*liveTool, len(live.Tools))
		for i, t := range live.Tools {
			if t != nil {
				cp := *t
				tools[i] = &cp
			}
		}
		live.Tools = tools
	}
	if live.Ask != nil {
		a := *live.Ask
		a.Findings = append([]string(nil), live.Ask.Findings...)
		a.reply = nil
		live.Ask = &a
	}
	if live.Context != nil {
		ctxCopy := make(map[string]int, len(live.Context))
		for k, v := range live.Context {
			ctxCopy[k] = v
		}
		live.Context = ctxCopy
	}
	if live.Usage != nil {
		u := *live.Usage
		live.Usage = &u
	}
	live.Queued = queuedTexts(p.queue)
	threads := append([]ThreadSummary(nil), p.threads...)
	return map[string]any{
		"id":      p.id,
		"path":    p.path,
		"name":    filepath.Base(p.path),
		"blocks":  blocks,
		"live":    live,
		"thread":  p.activeThread,
		"threads": threads,
	}
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
	p.loadThreadsLocked()
	if p.sess != nil {
		s := p.sess
		p.mu.Unlock()
		return s, nil
	}
	s, err := p.srv.host.Open(p.path)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	if p.mode != "" {
		_ = s.SetMode(p.mode)
	}
	if ts, ok := s.(ThreadSession); ok {
		ts.SetThread(p.activeThread)
	}
	p.sess = s
	p.mu.Unlock()
	if lbs, ok := s.(LiveBrowserSession); ok {
		lbs.SetBrowserObserver(func() {
			p.emit("browser", map[string]any{"browser": p.srv.browserState(p)})
		})
	}
	if ebs, ok := s.(ExtendedBrowserSession); ok {
		ebs.SetBrowserEventObserver(func(event string, payload map[string]any) {
			p.emit(event, payload)
		})
	}
	return s, nil
}

func (p *project) sendWithAttachments(text string, attachments []File) error {
	text = strings.TrimSpace(text)
	var clean []File
	for _, a := range attachments {
		if strings.TrimSpace(a.Name) == "" {
			continue
		}
		clean = append(clean, a)
	}
	if text == "" && len(clean) == 0 {
		return errors.New("empty message")
	}

	prompt := text
	if len(clean) > 0 {
		var sb strings.Builder
		if prompt != "" {
			sb.WriteString(prompt)
			sb.WriteString("\n\n")
		}
		for _, a := range clean {
			if strings.HasPrefix(a.Type, "image/") {
				fmt.Fprintf(&sb, "[Attached image: %s (%s) — inspect with view_image if needed]\n", a.Name, a.Base)
			} else {
				fmt.Fprintf(&sb, "[Attached file: %s (%s)]\n", a.Name, a.Base)
			}
		}
		prompt = strings.TrimSpace(sb.String())
	}

	displayText := text
	if displayText == "" && len(clean) > 0 {
		names := make([]string, len(clean))
		for i, a := range clean {
			names[i] = a.Base
		}
		displayText = strings.Join(names, ", ")
	}
	userBlk := Block{Kind: "user", Text: displayText, Attachments: clean}

	p.mu.Lock()
	p.loadLocked()
	if p.live.Running || p.starting {
		p.queue = append(p.queue, queuedPrompt{prompt: prompt, block: userBlk})
		if p.yield != nil {
			close(p.yield)
			p.yield = nil
		}
		q := queuedTexts(p.queue)
		p.mu.Unlock()
		p.emit("queued", map[string]any{"queued": q})
		return nil
	}
	p.starting = true
	p.mu.Unlock()
	sess, err := p.session()
	p.mu.Lock()
	p.starting = false
	if err != nil {
		p.mu.Unlock()
		return err
	}
	user := p.appendLocked(userBlk)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	started := time.Now().UnixMilli()
	p.cancel, p.done = cancel, done
	p.live = Live{Running: true, Started: started}
	p.stream, p.toolSeq = "", 0
	p.mu.Unlock()
	p.emit("user", map[string]any{"block": user})
	p.emit("state", map[string]any{"running": true, "started": started})
	go p.run(ctx, cancel, done, sess, prompt)
	return nil
}

func (p *project) stop() { _ = p.stopAndDone() }

func (p *project) stopAndDone() <-chan struct{} {
	p.mu.Lock()
	cancel, ask, done := p.cancel, p.live.Ask, p.done
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
	return done
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
	prompts := make([]string, len(items))
	for i, it := range items {
		prompts[i] = it.prompt
		blocks = append(blocks, p.appendLocked(it.block))
	}
	p.mu.Unlock()
	if flushed != nil {
		p.emit("block", map[string]any{"block": *flushed})
	}
	for _, b := range blocks {
		p.emit("user", map[string]any{"block": b, "taken": true})
	}
	return prompts
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
	text := stripInternalNotes(p.stream)
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
			s = stripInternalNotes(s)
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
			snap := *t
			p.live.Tools = append(p.live.Tools, t)
			p.live.Thought = ""
			p.live.Draft = nil
			p.mu.Unlock()
			if flushed != nil {
				p.emit("block", map[string]any{"block": *flushed})
			}
			p.emit("tool_start", map[string]any{"tool": snap})
			if name == "browser" || name == "browser_agent" {
				p.emit("browser", map[string]any{"browser": p.srv.browserState(p)})
			}
		},
		OnToolDraft: func(name string, size int, head string) {
			if name == agent.ToolPlan {
				return
			}
			label, _ := tui.ToolHeading(name, "{}")
			d := &liveDraft{Name: name, Label: label, Arg: draftArg(head), Size: size}
			snap := *d
			p.mu.Lock()
			p.live.Draft = d
			p.mu.Unlock()
			p.emit("tool_draft", map[string]any{"draft": snap})
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
			if name == "browser" || name == "browser_agent" {
				p.emit("browser", map[string]any{"browser": p.srv.browserState(p)})
			}
		},
		OnToolEnd: func(name, args, result string, took time.Duration) {
			if name == agent.ToolPlan {
				return
			}
			label, arg := tui.ToolHeading(name, args)
			b := Block{Kind: "tool", Name: name, Label: label, Arg: arg, Args: clipRunes(args, outputLimit), Summary: agent.StepLine(name, args, result),
				Output: clipRunes(result, outputLimit), Failed: agent.StepFailed(name, args, result)}
			if name == "send_file" && !b.Failed {
				if f := p.sentFile(args); f != nil {
					b = Block{Kind: "file", File: f}
				}
			}
			p.mu.Lock()
			id := 0
			matchIdx := -1
			for i, t := range p.live.Tools {
				if t.Name == name && t.args == args {
					matchIdx = i
					break
				}
			}
			if matchIdx < 0 {
				for i, t := range p.live.Tools {
					if t.Name == name {
						matchIdx = i
						break
					}
				}
			}
			if matchIdx >= 0 {
				id = p.live.Tools[matchIdx].ID
				p.live.Tools = append(p.live.Tools[:matchIdx], p.live.Tools[matchIdx+1:]...)
			}
			b = p.appendLocked(b)
			p.mu.Unlock()
			p.emit("tool_end", map[string]any{"id": id, "block": b, "took": took.Milliseconds()})
			if name == "browser" || name == "browser_agent" {
				p.emit("browser", map[string]any{"browser": p.srv.browserState(p)})
			}
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

func (p *project) run(ctx context.Context, cancel context.CancelFunc, done chan struct{}, sess Session, text string) {
	defer cancel()
	defer close(done)
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
	var modelName string
	var cost float64
	var hasCost bool
	if res != nil {
		modelName = p.srv.host.Info().Model
		cost, hasCost = llm.EstimateCost(modelName, res.Usage)
	}
	p.mu.Lock()
	var out []Block
	partial := stripInternalNotes(p.stream)
	p.stream, p.live.HTML = "", ""
	answer := ""
	if res != nil {
		answer = stripInternalNotes(res.Answer)
		p.usageIn += res.Usage.PromptTokens
		p.usageOut += res.Usage.CompletionTokens
		if hasCost {
			p.usageCost += cost
			p.usageKnown = true
		}
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
	lastCtx := p.live.Context
	uv := &UsageView{In: p.usageIn, Out: p.usageOut, Cost: p.usageCost, CostKnown: p.usageKnown}
	p.live = Live{Context: lastCtx, Usage: uv}
	p.cancel, p.done = nil, nil
	p.mu.Unlock()
	took := time.Since(started)
	doneEv := map[string]any{"blocks": out, "took": took.Milliseconds(), "stopped": stopped, "context": lastCtx, "usage": uv}
	if stopped && len(queued) > 0 {
		doneEv["requeue"] = queuedTexts(queued)
	}
	p.emit("done", doneEv)
	if took > 30*time.Second && !stopped {
		p.srv.notify("Macron finished", filepath.Base(p.path))
	}
	if len(queued) > 0 && !stopped {
		_ = p.startQueued(queued)
	}
}

func (p *project) startQueued(items []queuedPrompt) error {
	if len(items) == 0 {
		return nil
	}
	prompts := make([]string, len(items))
	for i, it := range items {
		prompts[i] = it.prompt
	}
	joined := strings.Join(prompts, "\n\n")
	p.mu.Lock()
	p.loadLocked()
	if p.live.Running || p.starting {
		p.queue = append(p.queue, items...)
		q := queuedTexts(p.queue)
		p.mu.Unlock()
		p.emit("queued", map[string]any{"queued": q})
		return nil
	}
	p.starting = true
	p.mu.Unlock()
	sess, err := p.session()
	p.mu.Lock()
	p.starting = false
	if err != nil {
		p.mu.Unlock()
		return err
	}
	var added []Block
	for _, it := range items {
		added = append(added, p.appendLocked(it.block))
	}
	ctx, cancel := context.WithCancel(context.Background())
	nextDone := make(chan struct{})
	started := time.Now().UnixMilli()
	p.cancel, p.done = cancel, nextDone
	p.live = Live{Running: true, Started: started}
	p.stream, p.toolSeq = "", 0
	p.mu.Unlock()
	for _, u := range added {
		p.emit("user", map[string]any{"block": u})
	}
	p.emit("state", map[string]any{"running": true, "started": started})
	go p.run(ctx, cancel, nextDone, sess, joined)
	return nil
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

func (p *project) sentFile(args string) *File {
	var in struct {
		Filename string `json:"filename"`
		Caption  string `json:"caption"`
	}
	if json.Unmarshal([]byte(args), &in) != nil || strings.TrimSpace(in.Filename) == "" {
		return nil
	}
	p.mu.Lock()
	sess := p.sess
	p.mu.Unlock()
	if sess == nil {
		return nil
	}
	abs, info, err := sess.FilePath(in.Filename)
	if err != nil {
		return nil
	}
	return &File{Name: strings.TrimSpace(in.Filename), Base: filepath.Base(abs), Size: info.Size(), Type: fileType(abs), Caption: clipRunes(strings.TrimSpace(in.Caption), 300)}
}

func fileType(name string) string {
	t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if i := strings.IndexByte(t, ';'); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}
