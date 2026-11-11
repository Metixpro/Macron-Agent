package agent

import (
	"strings"
	"testing"
)

func TestTeamWorkspace(t *testing.T) {
	tw := NewTeamWorkspace("team_eng", "Engineering Team", 1001)

	// Admin permissions
	if !tw.CanMutate(1001) {
		t.Error("owner/admin should be able to mutate")
	}

	// Add member
	tw.AddMember(1002, "sara", "Sara", RoleMember)
	if !tw.CanMutate(1002) {
		t.Error("member should be able to mutate")
	}

	// Add viewer
	tw.AddMember(1003, "reza", "Reza", RoleViewer)
	if tw.CanMutate(1003) {
		t.Error("viewer should not be able to mutate")
	}

	// Unknown user
	if tw.CanMutate(9999) {
		t.Error("unknown user should not be able to mutate")
	}

	// Shared memory
	tw.AddSharedMemory("Production deploy requires approval from Sara")
	tw.AddSharedMemory("Database staging port is 5433")

	note := tw.ContextNote()
	if !strings.Contains(note, "Production deploy requires approval") || !strings.Contains(note, "5433") {
		t.Errorf("shared memory note missing facts: %s", note)
	}
}
