package brain

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Notes is a markdown file of short facts, one per bullet line:
//
//	# USER.md — what the agent knows about its owner
//	Edit freely…
//
//	- Lives in Mashhad
//	- Prefers short answers
//
// Only the bullet lines are facts; every other line (the title, a comment of
// the owner, a heading) is kept as it is. A fact's number is its place among
// the bullets, so it changes when the owner edits the file by hand: the agent
// is always shown the numbers it has to use right now.
type Notes struct {
	Path  string
	Title string
	Hint  string

	mu sync.Mutex
}

// Entry is one fact.
type Entry struct {
	ID   int // 1-based place among the bullets
	Text string
}

// Errors.
var (
	ErrFull    = errors.New("the notes are full")
	ErrTooLong = errors.New("the file is too long")
)

func (n *Notes) init() error {
	if _, err := os.Stat(n.Path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeFile(n.Path, []byte("# "+n.Title+"\n"+n.Hint+"\n\n"))
}

func (n *Notes) lines() ([]string, error) {
	data, err := os.ReadFile(n.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	return strings.Split(strings.TrimRight(s, "\n"), "\n"), nil
}

func isBullet(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || t == "-"
}

func bulletText(line string) string {
	t := strings.TrimSpace(line)
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(t, "-"), "*"))
}

// List returns the facts in the file's order.
func (n *Notes) List() ([]Entry, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.list()
}

func (n *Notes) list() ([]Entry, error) {
	lines, err := n.lines()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, l := range lines {
		if isBullet(l) {
			if t := bulletText(l); t != "" {
				out = append(out, Entry{ID: len(out) + 1, Text: t})
			}
		}
	}
	return out, nil
}

// oneLine makes a fact fit a bullet.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > MaxEntryChars {
		s = string(r[:MaxEntryChars-1]) + "…"
	}
	return s
}

// Add appends a fact. A fact that is in the file already is not added twice
// (its number is returned, existed is true).
func (n *Notes) Add(text string) (id int, existed bool, err error) {
	text = oneLine(text)
	if text == "" {
		return 0, false, errors.New("the fact is empty")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	entries, err := n.list()
	if err != nil {
		return 0, false, err
	}
	for _, e := range entries {
		if strings.EqualFold(e.Text, text) {
			return e.ID, true, nil
		}
	}
	if len(entries) >= MaxEntries {
		return 0, false, ErrFull
	}
	lines, err := n.lines()
	if err != nil {
		return 0, false, err
	}
	lines = append(lines, "- "+text)
	if err := n.write(lines); err != nil {
		return 0, false, err
	}
	return len(entries) + 1, false, nil
}

// Replace changes fact id; it reports false when there is no such fact.
func (n *Notes) Replace(id int, text string) (bool, error) {
	text = oneLine(text)
	if text == "" {
		return false, errors.New("the fact is empty")
	}
	return n.edit(id, func(string) (string, bool) { return "- " + text, true })
}

// Delete removes fact id; it reports false when there is no such fact.
func (n *Notes) Delete(id int) (bool, error) {
	return n.edit(id, func(string) (string, bool) { return "", false })
}

func (n *Notes) edit(id int, f func(old string) (string, bool)) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	lines, err := n.lines()
	if err != nil {
		return false, err
	}
	seen := 0
	for i, l := range lines {
		if !isBullet(l) || bulletText(l) == "" {
			continue
		}
		if seen++; seen != id {
			continue
		}
		repl, keep := f(l)
		if keep {
			lines[i] = repl
		} else {
			lines = append(lines[:i], lines[i+1:]...)
		}
		return true, n.write(lines)
	}
	return false, nil
}

// Clear removes every fact (the title and the owner's other lines stay).
func (n *Notes) Clear() (int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	lines, err := n.lines()
	if err != nil {
		return 0, err
	}
	kept, removed := lines[:0:0], 0
	for _, l := range lines {
		if isBullet(l) {
			removed++
			continue
		}
		kept = append(kept, l)
	}
	return removed, n.write(kept)
}

// Raw is the whole file.
func (n *Notes) Raw() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	data, _ := os.ReadFile(n.Path)
	return string(data)
}

// Replace the whole file (the owner sent a new one). The facts are kept
// within the limits: more than MaxEntries bullets, or a file longer than
// MaxFileChars, is refused.
func (n *Notes) SetRaw(text string) error {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if len([]rune(text)) > MaxFileChars {
		return fmt.Errorf("%w: at most %d characters", ErrTooLong, MaxFileChars)
	}
	bullets := 0
	for _, l := range strings.Split(text, "\n") {
		if isBullet(l) {
			bullets++
		}
	}
	if bullets > MaxEntries {
		return fmt.Errorf("%w: at most %d facts", ErrFull, MaxEntries)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return writeFile(n.Path, []byte(strings.TrimRight(text, "\n")+"\n"))
}

func (n *Notes) write(lines []string) error {
	text := strings.Join(lines, "\n") + "\n"
	if len([]rune(text)) > MaxFileChars {
		return ErrTooLong
	}
	return writeFile(n.Path, []byte(text))
}
