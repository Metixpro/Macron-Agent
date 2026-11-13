package messenger

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/cmdgate"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/memory"
)

type Memory interface {
	Load(context.Context, int64) (*memory.Context, error)
	Save(context.Context, int64, string, string) (bool, error)
	Reset(context.Context, int64) error
}
type Runner struct {
	Platform   string
	Client     Transport
	Owners     map[string]bool
	Agent      *agent.Agent
	Memory     Memory
	Timeout    time.Duration
	Cursor     string
	SaveCursor func(context.Context, string) error
	OnUser     func(context.Context, int64) error
	Private    func(context.Context, Message) (bool, error)
	mu         sync.Mutex
	active     map[string]context.CancelFunc
	approvals  map[string]chan cmdgate.Choice
	suspended  map[string]*agent.Suspended
	loaded     map[string][]string
}

func UserID(platform, user string) int64 {
	sum := sha256.Sum256([]byte(platform + ":" + user))
	return -int64(binary.BigEndian.Uint64(sum[:8])&((1<<62)-1) | (1 << 62))
}
func (r *Runner) Run(ctx context.Context) error {
	ctx, cancelRun := context.WithCancel(ctx)
	if len(r.Owners) == 0 {
		cancelRun()
		return fmt.Errorf("%s owners are required", r.Platform)
	}
	if r.Timeout <= 0 {
		r.Timeout = 30 * time.Minute
	}
	r.active = map[string]context.CancelFunc{}
	r.approvals = map[string]chan cmdgate.Choice{}
	r.suspended = map[string]*agent.Suspended{}
	r.loaded = map[string][]string{}
	queues := map[string]chan Message{}
	var workers sync.WaitGroup
	for owner := range r.Owners {
		queue := make(chan Message, 8)
		queues[owner] = queue
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case m := <-queue:
					r.turn(ctx, m)
				}
			}
		}()
	}
	defer func() { cancelRun(); workers.Wait() }()
	cursor := r.Cursor
	for ctx.Err() == nil {
		messages, next, err := r.Client.Poll(ctx, cursor)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Printf("%s polling: %v", r.Platform, err)
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, m := range messages {
			if !r.Owners[m.UserID] {
				continue
			}
			private := m.Private
			if r.Private != nil {
				private, err = r.Private(ctx, m)
				if err != nil {
					log.Printf("%s chat check: %v", r.Platform, err)
					continue
				}
			}
			if !private {
				continue
			}
			r.mu.Lock()
			approval := r.approvals[m.UserID]
			stop := r.active[m.UserID]
			r.mu.Unlock()
			command := strings.ToLower(strings.TrimSpace(m.Text))
			if command == "/stop" {
				if stop != nil {
					stop()
				}
				_ = r.Client.Send(ctx, m.ChatID, "متوقف شد.")
				continue
			}
			if approval != nil {
				choice := cmdgate.Choice("")
				switch command {
				case "/approve", "بله", "yes":
					choice = cmdgate.Once
				case "/deny", "نه", "no":
					choice = cmdgate.Deny
				}
				if choice != "" {
					select {
					case approval <- choice:
					default:
					}
					continue
				}
			}
			select {
			case queues[m.UserID] <- m:
			default:
				_ = r.Client.Send(ctx, m.ChatID, "صف پر است؛ پس از پایان کار دوباره بفرست.")
			}
		}
		if next != cursor {
			if r.SaveCursor != nil {
				if err := r.SaveCursor(ctx, next); err != nil {
					return err
				}
			}
			cursor = next
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	return ctx.Err()
}
func (r *Runner) turn(parent context.Context, m Message) {
	ctx, cancel := context.WithTimeout(parent, r.Timeout)
	defer cancel()
	r.mu.Lock()
	r.active[m.UserID] = cancel
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.active, m.UserID); delete(r.approvals, m.UserID); r.mu.Unlock() }()
	defer func() {
		if p := recover(); p != nil {
			log.Printf("%s request panicked: %v", r.Platform, p)
			_ = r.Client.Send(parent, m.ChatID, "خطایی در اجرای درخواست رخ داد.")
		}
	}()
	uid := UserID(r.Platform, m.UserID)
	if r.OnUser != nil {
		if err := r.OnUser(ctx, uid); err != nil {
			_ = r.Client.Send(ctx, m.ChatID, "خطای ذخیره‌سازی: "+err.Error())
			return
		}
	}
	if strings.TrimSpace(m.Text) == "/new" {
		r.mu.Lock()
		delete(r.suspended, m.UserID)
		delete(r.loaded, m.UserID)
		r.mu.Unlock()
		if err := r.Memory.Reset(ctx, uid); err != nil {
			_ = r.Client.Send(ctx, m.ChatID, "خطای حافظه: "+err.Error())
			return
		}
		_ = r.Client.Send(ctx, m.ChatID, "گفتگوی جدید شروع شد.")
		return
	}
	if strings.TrimSpace(m.Text) == "/start" {
		_ = r.Client.Send(ctx, m.ChatID, "مکرون آماده است. درخواستت را بفرست؛ /stop کار فعلی را متوقف می‌کند و /new گفتگو را پاک می‌کند.")
		return
	}
	history, err := r.Memory.Load(ctx, uid)
	if err != nil {
		_ = r.Client.Send(ctx, m.ChatID, "خطای حافظه: "+err.Error())
		return
	}
	ctx = cmdgate.WithAsker(ctx, func(ctx context.Context, q cmdgate.Question) cmdgate.Choice {
		answer := make(chan cmdgate.Choice, 1)
		r.mu.Lock()
		r.approvals[m.UserID] = answer
		r.mu.Unlock()
		defer func() { r.mu.Lock(); delete(r.approvals, m.UserID); r.mu.Unlock() }()
		if err := r.Client.Send(ctx, m.ChatID, "اجازهٔ اجرا؟\n"+q.Command+"\n/approve برای تأیید یک‌باره یا /deny برای رد"); err != nil {
			return cmdgate.Deny
		}
		select {
		case <-ctx.Done():
			return cmdgate.Deny
		case <-time.After(5 * time.Minute):
			return cmdgate.Deny
		case choice := <-answer:
			return choice
		}
	})
	session := fmt.Sprintf("%s-%d", r.Platform, -uid)
	asked := ""
	ask := agent.LocalTool{Def: llm.Tool{Type: "function", Function: llm.ToolDef{Name: "ask_user", Description: "Ask the owner a question and wait for their next message.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"question": map[string]string{"type": "string"}}, "required": []string{"question"}}}}, Handler: func(_ context.Context, args map[string]any) (agent.ToolOutput, error) {
		q, _ := args["question"].(string)
		if strings.TrimSpace(q) == "" {
			return agent.ToolOutput{Text: "error: a non-empty question is required"}, nil
		}
		asked = q
		return agent.ToolOutput{Text: q, EndTurn: true}, nil
	}}
	r.mu.Lock()
	resume, preload := r.suspended[m.UserID], r.loaded[m.UserID]
	delete(r.suspended, m.UserID)
	r.mu.Unlock()
	res, err := r.Agent.Do(ctx, agent.Request{SessionID: session, Owner: true, Input: llm.Message{Role: "user", Content: m.Text}, History: history.History, Memory: history.Summary, Tools: []agent.LocalTool{ask}, Resume: resume, Preload: preload, FocusTools: true, About: "This is the owner's private " + r.Platform + " chat. Reply with ordinary text. Tools run on the host machine."})
	if res == nil {
		if err != nil && parent.Err() == nil {
			_ = r.Client.Send(parent, m.ChatID, "خطای اجرا: "+err.Error())
		}
		return
	}
	r.mu.Lock()
	r.suspended[m.UserID] = res.Suspended
	if len(res.Loaded) > 0 {
		r.loaded[m.UserID] = append([]string(nil), res.Loaded[max(0, len(res.Loaded)-30):]...)
	}
	r.mu.Unlock()
	answer := res.Answer
	if res.EndedBy == "ask_user" && asked != "" {
		answer = asked
	}
	if answer == "" && err != nil {
		answer = "خطای اجرا: " + err.Error()
	}
	if answer == "" {
		answer = "درخواست پایان یافت."
	}
	sendCtx, stop := context.WithTimeout(parent, 30*time.Second)
	defer stop()
	if err := r.Client.Send(sendCtx, m.ChatID, answer); err != nil {
		log.Printf("%s answer delivery: %v", r.Platform, err)
	}
	if _, err := r.Memory.Save(sendCtx, uid, m.Text, answer); err != nil {
		log.Printf("%s saving history: %v", r.Platform, err)
	}
}
