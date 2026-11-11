package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type TeamRole string

const (
	RoleAdmin  TeamRole = "admin"
	RoleMember TeamRole = "member"
	RoleViewer TeamRole = "viewer"
)

type TeamMember struct {
	UserID   int64    `json:"user_id"`
	Username string   `json:"username"`
	Name     string   `json:"name"`
	Role     TeamRole `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
}

type TeamWorkspace struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	OwnerID     int64                  `json:"owner_id"`
	Members     map[int64]*TeamMember  `json:"members"`
	SharedMemory []string              `json:"shared_memory,omitempty"`
	CreatedAt   time.Time              `json:"created_at"`
	mu          sync.RWMutex           `json:"-"`
}

func NewTeamWorkspace(id, name string, ownerID int64) *TeamWorkspace {
	tw := &TeamWorkspace{
		ID:        id,
		Name:      name,
		OwnerID:   ownerID,
		Members:   make(map[int64]*TeamMember),
		CreatedAt: time.Now(),
	}
	tw.Members[ownerID] = &TeamMember{
		UserID:   ownerID,
		Role:     RoleAdmin,
		JoinedAt: time.Now(),
	}
	return tw
}

func (t *TeamWorkspace) AddMember(userID int64, username, name string, role TeamRole) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Members[userID] = &TeamMember{
		UserID:   userID,
		Username: username,
		Name:     name,
		Role:     role,
		JoinedAt: time.Now(),
	}
}

func (t *TeamWorkspace) CanMutate(userID int64) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	m, ok := t.Members[userID]
	if !ok {
		return false
	}
	return m.Role == RoleAdmin || m.Role == RoleMember
}

func (t *TeamWorkspace) AddSharedMemory(fact string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.SharedMemory = append(t.SharedMemory, fact)
}

func (t *TeamWorkspace) ContextNote() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.SharedMemory) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[Team Workspace: %s — Shared Memory]\n", t.Name))
	for _, m := range t.SharedMemory {
		sb.WriteString("- " + m + "\n")
	}
	return strings.TrimSpace(sb.String())
}
