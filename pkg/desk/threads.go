package desk

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const defaultThreadID = "default"

var validThreadIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validThreadID(tid string) bool {
	return validThreadIDRe.MatchString(tid) && !strings.Contains(tid, "..")
}

type ThreadSummary struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Preview string `json:"preview"`
	Updated int64  `json:"updated"`
	Count   int    `json:"count"`
}

type threadIndex struct {
	Active  string          `json:"active"`
	Threads []ThreadSummary `json:"threads"`
}

type ThreadSession interface {
	SetThread(threadID string)
}

func newThreadID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "t" + hex.EncodeToString(b[:])
}

func (p *project) threadsFile() string {
	return filepath.Join(p.srv.host.DataDir(), "desktop", p.id+".threads.json")
}

func (p *project) threadFile(tid string) string {
	if tid == "" || tid == defaultThreadID {
		return filepath.Join(p.srv.host.DataDir(), "desktop", p.id+".jsonl")
	}
	return filepath.Join(p.srv.host.DataDir(), "desktop", p.id+"-"+tid+".jsonl")
}

func (p *project) loadThreadsLocked() {
	if p.threadsLoaded {
		return
	}
	p.threadsLoaded = true
	raw, err := os.ReadFile(p.threadsFile())
	if err == nil {
		var idx threadIndex
		if json.Unmarshal(raw, &idx) == nil && len(idx.Threads) > 0 {
			var filtered []ThreadSummary
			for _, th := range idx.Threads {
				if validThreadID(th.ID) {
					filtered = append(filtered, th)
				}
			}
			if len(filtered) > 0 {
				p.threads = filtered
				if validThreadID(idx.Active) {
					p.activeThread = idx.Active
				} else {
					p.activeThread = p.threads[0].ID
				}
				return
			}
		}
	}
	p.activeThread = defaultThreadID
	p.threads = []ThreadSummary{{
		ID:      defaultThreadID,
		Title:   "Main conversation",
		Updated: time.Now().UnixMilli(),
	}}
}

func (p *project) saveThreadsLocked() {
	idx := threadIndex{
		Active:  p.activeThread,
		Threads: p.threads,
	}
	raw, err := json.Marshal(idx)
	if err != nil {
		return
	}
	target := p.threadsFile()
	dir := filepath.Dir(target)
	_ = os.MkdirAll(dir, 0o700)
	tmp, err := os.CreateTemp(dir, "."+p.id+"-threads-*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	_ = os.Chmod(tmpName, 0o600)
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
	}
}

func (p *project) syncActiveThreadLocked() {
	p.loadThreadsLocked()
	title := ""
	preview := ""
	count := 0
	var updated int64
	for _, b := range p.blocks {
		if b.Kind == "user" || b.Kind == "assistant" {
			count++
			if title == "" && b.Kind == "user" {
				title = clipRunes(plainText(b.Text), 56)
			}
			preview = clipRunes(plainText(b.Text), previewRunes)
			if b.At > updated {
				updated = b.At
			}
		}
	}
	if title == "" {
		title = "New conversation"
	}
	if updated == 0 && !p.updated.IsZero() {
		updated = p.updated.UnixMilli()
	}
	if updated == 0 {
		updated = time.Now().UnixMilli()
	}

	found := false
	for i := range p.threads {
		if p.threads[i].ID == p.activeThread {
			p.threads[i].Title = title
			p.threads[i].Preview = preview
			p.threads[i].Count = count
			p.threads[i].Updated = updated
			found = true
			break
		}
	}
	if !found {
		p.threads = append([]ThreadSummary{{
			ID:      p.activeThread,
			Title:   title,
			Preview: preview,
			Count:   count,
			Updated: updated,
		}}, p.threads...)
	}
	sort.SliceStable(p.threads, func(i, j int) bool {
		return p.threads[i].Updated > p.threads[j].Updated
	})
	p.saveThreadsLocked()
}

func (p *project) readThreadBlocks(tid string) ([]Block, time.Time) {
	if tid != "" && !validThreadID(tid) {
		return nil, time.Time{}
	}
	f, err := os.Open(p.threadFile(tid))
	if err != nil {
		return nil, time.Time{}
	}
	defer f.Close()
	var blocks []Block
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var b Block
		if json.Unmarshal(sc.Bytes(), &b) == nil {
			if b.Kind == "assistant" {
				b.Text = stripInternalNotes(b.Text)
			}
			blocks = append(blocks, b)
		}
	}
	if len(blocks) > keepBlocks {
		blocks = blocks[len(blocks)-keepBlocks:]
	}
	var updated time.Time
	if n := len(blocks); n > 0 {
		updated = time.UnixMilli(blocks[n-1].At)
	}
	return blocks, updated
}

func (p *project) startNewThread(sess Session) ThreadSummary {
	p.mu.Lock()
	p.loadLocked()
	p.syncActiveThreadLocked()

	// Reuse current thread if it is already empty.
	hasMessages := false
	for _, b := range p.blocks {
		if b.Kind == "user" || b.Kind == "assistant" {
			hasMessages = true
			break
		}
	}

	var next ThreadSummary
	if hasMessages {
		tid := newThreadID()
		next = ThreadSummary{
			ID:      tid,
			Title:   "New conversation",
			Updated: time.Now().UnixMilli(),
		}
		p.activeThread = tid
		p.threads = append([]ThreadSummary{next}, p.threads...)
		p.blocks = nil
		p.updated = time.Now()
		p.live = Live{}
		p.usageIn, p.usageOut, p.usageCost, p.usageKnown = 0, 0, 0, false
		p.saveThreadsLocked()
	} else {
		p.blocks = nil
		p.live = Live{}
		p.usageIn, p.usageOut, p.usageCost, p.usageKnown = 0, 0, 0, false
		for _, t := range p.threads {
			if t.ID == p.activeThread {
				next = t
				break
			}
		}
	}
	tid := p.activeThread
	p.mu.Unlock()

	if ts, ok := sess.(ThreadSession); ok {
		ts.SetThread(tid)
	}
	return next
}

func (p *project) switchThread(tid string) error {
	tid = strings.TrimSpace(tid)
	if tid == "" || !validThreadID(tid) {
		return errors.New("invalid thread id")
	}
	p.mu.Lock()
	if p.live.Running || p.starting {
		p.mu.Unlock()
		return errors.New("stop the current job first")
	}
	p.loadLocked()
	p.syncActiveThreadLocked()

	found := false
	for _, t := range p.threads {
		if t.ID == tid {
			found = true
			break
		}
	}
	if !found {
		p.mu.Unlock()
		return errors.New("conversation thread not found")
	}
	p.activeThread = tid
	blocks, updated := p.readThreadBlocks(tid)
	p.blocks = blocks
	p.updated = updated
	p.live = Live{}
	p.usageIn, p.usageOut, p.usageCost, p.usageKnown = 0, 0, 0, false
	p.saveThreadsLocked()
	sess := p.sess
	p.mu.Unlock()

	if sess != nil {
		if ts, ok := sess.(ThreadSession); ok {
			ts.SetThread(tid)
		}
	}
	return nil
}

func (p *project) deleteThread(tid string) error {
	tid = strings.TrimSpace(tid)
	if tid == "" || !validThreadID(tid) {
		return errors.New("invalid thread id")
	}
	p.mu.Lock()
	if p.live.Running || p.starting {
		p.mu.Unlock()
		return errors.New("stop the current job first")
	}
	p.loadLocked()
	idx := -1
	for i, t := range p.threads {
		if t.ID == tid {
			idx = i
			break
		}
	}
	if idx < 0 {
		p.mu.Unlock()
		return errors.New("conversation thread not found")
	}
	_ = os.Remove(p.threadFile(tid))
	p.threads = append(p.threads[:idx], p.threads[idx+1:]...)

	switched := false
	if len(p.threads) == 0 {
		fresh := ThreadSummary{
			ID:      newThreadID(),
			Title:   "New conversation",
			Updated: time.Now().UnixMilli(),
		}
		p.threads = []ThreadSummary{fresh}
		p.activeThread = fresh.ID
		p.blocks = nil
		p.updated = time.Now()
		switched = true
	} else if p.activeThread == tid {
		p.activeThread = p.threads[0].ID
		p.blocks, p.updated = p.readThreadBlocks(p.activeThread)
		switched = true
	}
	p.live = Live{}
	p.saveThreadsLocked()
	active := p.activeThread
	sess := p.sess
	p.mu.Unlock()

	if switched && sess != nil {
		if ts, ok := sess.(ThreadSession); ok {
			ts.SetThread(active)
		}
	}
	return nil
}

func (s *Server) threadsAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	p.mu.Lock()
	p.loadLocked()
	p.syncActiveThreadLocked()
	threads := append([]ThreadSummary(nil), p.threads...)
	active := p.activeThread
	p.mu.Unlock()
	writeJSON(w, map[string]any{"active": active, "threads": threads})
}

func (s *Server) switchThreadAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	tid := r.PathValue("tid")
	if err := p.switchThread(tid); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	p.emit("thread", map[string]any{"thread": tid})
	writeJSON(w, p.snapshot())
}

func (s *Server) deleteThreadAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	tid := r.PathValue("tid")
	if err := p.deleteThread(tid); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	p.mu.Lock()
	threads := append([]ThreadSummary(nil), p.threads...)
	active := p.activeThread
	p.mu.Unlock()
	p.emit("thread", map[string]any{"thread": active})
	writeJSON(w, map[string]any{"active": active, "threads": threads, "snapshot": p.snapshot()})
}
