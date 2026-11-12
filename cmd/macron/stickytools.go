package main

import "sync"

const stickyMax = 30

type stickyTools struct {
	mu    sync.Mutex
	users map[int64][]string
}

func (s *stickyTools) get(uid int64) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.users[uid]...)
}

func (s *stickyTools) keep(uid int64, loaded []string) {
	if len(loaded) == 0 {
		return
	}
	if len(loaded) > stickyMax {
		loaded = loaded[len(loaded)-stickyMax:]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users == nil {
		s.users = map[int64][]string{}
	}
	s.users[uid] = append([]string(nil), loaded...)
}
