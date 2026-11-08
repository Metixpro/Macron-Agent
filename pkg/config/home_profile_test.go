package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProfiles(t *testing.T) {
	t.Setenv(EnvHome, "")
	h, _ := os.UserHomeDir()
	t.Setenv(EnvProfile, "")
	if Home() != filepath.Join(h, ".macron") || ServiceName() != "macron" {
		t.Fatalf("default: %s %s", Home(), ServiceName())
	}
	t.Setenv(EnvProfile, "work")
	if Home() != filepath.Join(h, ".macron", "profiles", "work") || ServiceName() != "macron-work" {
		t.Fatalf("work: %s %s", Home(), ServiceName())
	}
	for _, bad := range []string{"../etc", "Work", "a b", "default"} {
		t.Setenv(EnvProfile, bad)
		if Profile() != "" {
			t.Errorf("%q is not a profile", bad)
		}
	}
	t.Setenv(EnvProfile, "work")
	t.Setenv(EnvHome, "/srv/agent")
	if Home() != "/srv/agent" {
		t.Error("MACRON_HOME wins")
	}
}
