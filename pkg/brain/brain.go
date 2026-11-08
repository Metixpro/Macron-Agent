// Package brain keeps what the agent is and knows as plain files the owner
// can open in any editor:
//
//	SOUL.md     who the agent is: personality, tone, values, red lines
//	USER.md     what it knows about its owner (one fact per line)
//	MEMORY.md   what it learned about its work, projects and machine
//
// The files are the truth: the agent changes them with its tools, the owner
// changes them by hand or from Telegram, and both see the other's edits at
// the next message. Nothing here is a cache of a database.
package brain

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Brain is the folder of the files.
type Brain struct {
	Dir         string
	DefaultSoul string // the soul written when SOUL.md does not exist yet

	User   *Notes
	Memory *Notes

	mu       sync.Mutex
	soul     string
	soulTime time.Time
	soulSize int64
}

// File names.
const (
	SoulFile   = "SOUL.md"
	UserFile   = "USER.md"
	MemoryFile = "MEMORY.md"
)

// Limits: what goes into every prompt stays small.
const (
	MaxEntries    = 80  // facts in one notes file
	MaxEntryChars = 400 // one fact
	MaxFileChars  = 12000
	MaxSoulBytes  = 64 << 10
)

// Open prepares the folder: it creates it and writes the three files if they
// are missing (the soul from defaultSoul).
func Open(dir, defaultSoul string) (*Brain, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	b := &Brain{Dir: dir, DefaultSoul: defaultSoul}
	b.User = &Notes{Path: filepath.Join(dir, UserFile), Title: "USER.md — what the agent knows about its owner",
		Hint: "One fact per line, starting with “- ”. Edit freely: the agent reads this file before it answers."}
	b.Memory = &Notes{Path: filepath.Join(dir, MemoryFile), Title: "MEMORY.md — what the agent learned about its work and machine",
		Hint: "One note per line, starting with “- ”: projects, paths, servers, habits, things that worked. Edit freely."}
	if _, err := os.Stat(b.soulPath()); errors.Is(err, os.ErrNotExist) {
		if err := writeFile(b.soulPath(), []byte(strings.TrimSpace(defaultSoul)+"\n")); err != nil {
			return nil, err
		}
	}
	for _, n := range []*Notes{b.User, b.Memory} {
		if err := n.init(); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func (b *Brain) soulPath() string { return filepath.Join(b.Dir, SoulFile) }

// Soul is the text of SOUL.md ("" when it is empty or missing: the caller
// falls back to its default). It is read again when the file changes.
func (b *Brain) Soul() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	fi, err := os.Stat(b.soulPath())
	if err != nil {
		return ""
	}
	if fi.ModTime().Equal(b.soulTime) && fi.Size() == b.soulSize {
		return b.soul
	}
	data, err := os.ReadFile(b.soulPath())
	if err != nil {
		return b.soul
	}
	b.soul, b.soulTime, b.soulSize = strings.TrimSpace(string(data)), fi.ModTime(), fi.Size()
	return b.soul
}

// SoulText is the soul as the owner sees it in the file.
func (b *Brain) SoulText() string {
	if s := b.Soul(); s != "" {
		return s
	}
	return strings.TrimSpace(b.DefaultSoul)
}

// IsCustomSoul reports whether SOUL.md differs from the built-in soul.
func (b *Brain) IsCustomSoul() bool {
	return strings.TrimSpace(b.Soul()) != strings.TrimSpace(b.DefaultSoul)
}

// SetSoul replaces SOUL.md; the previous one is kept as SOUL.md.bak.
func (b *Brain) SetSoul(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("the soul is empty")
	}
	if len(text) > MaxSoulBytes {
		return fmt.Errorf("the soul is larger than %d KB", MaxSoulBytes>>10)
	}
	if !utf8.ValidString(text) {
		return errors.New("the soul is not valid UTF-8 text")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.soulTime = time.Time{} // read it again, whatever its timestamp says
	if old, err := os.ReadFile(b.soulPath()); err == nil {
		_ = writeFile(b.soulPath()+".bak", old)
	}
	return writeFile(b.soulPath(), []byte(text+"\n"))
}

// ResetSoul writes the built-in soul back.
func (b *Brain) ResetSoul() error { return b.SetSoul(b.DefaultSoul) }

// writeFile writes atomically with owner-only permissions.
func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
