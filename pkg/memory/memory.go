package memory

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"unicode"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const DefaultBudget = 8000

const (
	MinBudget = 2000
	MaxBudget = 1000000
)

const (
	maxTurns         = 600
	keepTurns        = 300
	keepRecent       = 6
	keepMinimum      = 2
	summaryMaxTokens = 2000
	chunkTokens      = 60000
)

const droppedNote = "(Some older parts of the conversation were dropped to save space.)"

func EstimateTokens(s string) int {
	ascii, other := 0, 0
	for _, r := range s {
		switch {
		case r < 128:
			ascii++
		case unicode.IsSpace(r):
			ascii++
		default:
			other++
		}
	}
	n := ascii/4 + (other+1)/2
	if n == 0 && s != "" {
		n = 1
	}
	return n
}

type Manager struct {
	store  *store.Store
	llm    llm.ChatClient
	budget func() int

	UserBudget func(ctx context.Context, userID int64) int

	locks sync.Map
}

func New(st *store.Store, client llm.ChatClient, budget func() int) *Manager {
	if budget == nil {
		budget = func() int { return DefaultBudget }
	}
	return &Manager{store: st, llm: client, budget: budget}
}

func (m *Manager) budgetFor(ctx context.Context, userID int64) int {
	if m.UserBudget != nil {
		if n := m.UserBudget(ctx, userID); n > 0 {
			return n
		}
	}
	return m.budget()
}

func (m *Manager) lock(userID int64) func() {
	v, _ := m.locks.LoadOrStore(userID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

type Context struct {
	Summary      string
	History      []llm.Message
	Tokens       int
	Budget       int
	Messages     int
	Compressions int
}

func (m *Manager) Load(ctx context.Context, userID int64) (*Context, error) {
	mem, err := m.store.LoadMemory(ctx, userID)
	if err != nil {
		return nil, err
	}
	c := &Context{Summary: mem.Summary, Tokens: mem.Tokens(), Budget: m.budgetFor(ctx, userID), Messages: len(mem.Messages), Compressions: mem.Compressions}
	for _, msg := range mem.Messages {
		c.History = append(c.History, llm.Message{Role: msg.Role, Content: msg.Content})
	}
	return c, nil
}

func (m *Manager) Save(ctx context.Context, userID int64, userText, assistantText string) (compressed bool, err error) {
	if err := m.Append(ctx, userID, userText, assistantText); err != nil {
		return false, err
	}
	return m.Tidy(ctx, userID)
}

func (m *Manager) Tidy(ctx context.Context, userID int64) (bool, error) {
	return m.compressIfNeeded(ctx, userID, false)
}

func (m *Manager) Append(ctx context.Context, userID int64, userText, assistantText string) error {

	maxRunes := max(m.budgetFor(ctx, userID), 1000)
	origUser, origAssistant := userText, assistantText
	userText = textutil.TruncateRunes(strings.TrimSpace(userText), maxRunes, "… [long message shortened]")
	assistantText = textutil.TruncateRunes(strings.TrimSpace(assistantText), maxRunes, "… [long message shortened]")
	var turns []store.ContextMessage
	if userText != "" {
		turns = append(turns, store.ContextMessage{Role: "user", Content: userText, Tokens: EstimateTokens(userText)})
	}
	if assistantText != "" {
		turns = append(turns, store.ContextMessage{Role: "assistant", Content: assistantText, Tokens: EstimateTokens(assistantText)})
	}
	if err := m.store.AppendContext(ctx, userID, turns...); err != nil {
		return err
	}

	var kept []store.ArchiveTurn
	if t := strings.TrimSpace(origUser); t != "" {
		kept = append(kept, store.ArchiveTurn{Role: "user", Content: t})
	}
	if t := strings.TrimSpace(origAssistant); t != "" {
		kept = append(kept, store.ArchiveTurn{Role: "assistant", Content: t})
	}
	if err := m.store.Archive(ctx, userID, kept...); err != nil {
		log.Printf("memory: archive: %v", err)
	}
	return nil
}

func (m *Manager) Compress(ctx context.Context, userID int64) (bool, error) {
	return m.compressIfNeeded(ctx, userID, true)
}

func (m *Manager) Reset(ctx context.Context, userID int64) error {
	unlock := m.lock(userID)
	defer unlock()
	return m.store.ClearMemory(ctx, userID)
}

func (m *Manager) compressIfNeeded(ctx context.Context, userID int64, force bool) (bool, error) {
	unlock := m.lock(userID)
	defer unlock()

	mem, err := m.store.LoadMemory(ctx, userID)
	if err != nil {
		return false, err
	}
	budget := m.budgetFor(ctx, userID)
	kept := mem.Tokens() - mem.SummaryTokens
	if !force && kept <= budget && len(mem.Messages) <= maxTurns {
		return false, nil
	}
	if len(mem.Messages) <= keepMinimum {
		return false, nil
	}

	target, turns, floor := budget/2, keepTurns, keepMinimum
	if force {
		target, floor = 0, keepRecent
	}
	cut, remaining := 0, kept
	for cut < len(mem.Messages)-floor && (remaining > target || len(mem.Messages)-cut > turns) {
		remaining -= mem.Messages[cut].Tokens
		cut++
	}

	for cut < len(mem.Messages)-1 && mem.Messages[cut].Role != "user" {
		cut++
	}
	if cut == 0 {
		return false, nil
	}
	old := mem.Messages[:cut]

	summary, err := m.summarize(ctx, mem.Summary, old)
	if err != nil && !force && kept <= 2*budget && len(mem.Messages) <= 2*maxTurns {

		log.Printf("memory: summarizing for user %d failed (%v); trying again later", userID, err)
		return false, nil
	}
	if err != nil {

		log.Printf("memory: summarizing for user %d failed (%v); dropping %d old turns", userID, err, len(old))
		summary = strings.TrimSpace(mem.Summary)
		if !strings.Contains(summary, droppedNote) {
			summary = strings.TrimSpace(summary + "\n" + droppedNote)
		}
	}
	summary = textutil.TruncateRunes(summary, summaryMaxTokens*3, "…")
	if err := m.store.Compact(ctx, userID, old[len(old)-1].ID, summary, EstimateTokens(summary)); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Manager) summarize(ctx context.Context, previous string, msgs []store.ContextMessage) (string, error) {
	if m.llm == nil {
		return "", fmt.Errorf("no language model")
	}
	summary := strings.TrimSpace(previous)
	var chunk strings.Builder
	tokens := 0
	flush := func() error {
		if chunk.Len() == 0 {
			return nil
		}
		next, err := m.summarizeChunk(ctx, summary, chunk.String())
		if err != nil {
			return err
		}
		summary, tokens = next, 0
		chunk.Reset()
		return nil
	}
	for _, msg := range msgs {
		who := "User"
		if msg.Role == "assistant" {
			who = "Assistant"
		}
		text := msg.Content
		if msg.Tokens > chunkTokens/2 {
			text = textutil.TruncateRunes(text, chunkTokens, "… [long message shortened]")
		}
		if tokens+msg.Tokens > chunkTokens {
			if err := flush(); err != nil {
				return "", err
			}
		}
		fmt.Fprintf(&chunk, "%s: %s\n\n", who, text)
		tokens += msg.Tokens
	}
	if err := flush(); err != nil {
		return "", err
	}
	return summary, nil
}

func (m *Manager) summarizeChunk(ctx context.Context, previous, transcript string) (string, error) {
	prompt := fmt.Sprintf(`You maintain the long-term memory of a chat assistant for one user.
Update the memory with the new part of the conversation below.

Rules:
- Keep: facts about the user (name, preferences, projects, decisions, recurring requests), open tasks and promises, important results (file names, links, numbers) and the conversation's language and tone.
- Drop small talk, repeated content and anything already finished that won't matter later.
- Write compact bullet points in the user's language (Persian if the conversation is Persian). At most about %d words.
- Output only the updated memory, no introduction.

Current memory:
%s

New conversation:
<transcript>
%s
</transcript>`, summaryMaxTokens/2, orNone(previous), transcript)

	var out string
	for _, limit := range []int{4 * summaryMaxTokens, 8 * summaryMaxTokens} {
		resp, err := m.llm.Complete(ctx, llm.Request{Messages: []llm.Message{{Role: "user", Content: prompt}}, MaxTokens: limit})
		if err != nil {
			return "", err
		}
		out = strings.TrimSpace(resp.Message.Content)
		if out != "" && resp.FinishReason != "length" {
			return out, nil
		}
	}
	if out == "" {
		return "", fmt.Errorf("empty summary")
	}
	return out, nil
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(empty)"
	}
	return s
}
