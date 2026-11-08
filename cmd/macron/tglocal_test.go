package main

import (
	"net/netip"
	"testing"
)

func TestTelegramLocalMode(t *testing.T) {
	for _, c := range []struct {
		base, setting string
		want          bool
	}{
		{"http://127.0.0.1:8081", "", true},
		{"https://api.telegram.org", "", false},
		{"https://api.telegram.org", "1", true},
		{"http://127.0.0.1:8081", "0", false},
		{"http://localhost:8081", "", true},
	} {
		if got := telegramLocal(c.base, c.setting); got != c.want {
			t.Errorf("telegramLocal(%q, %q) = %v", c.base, c.setting, got)
		}
	}

	if !pointsHere("localhost", map[netip.Addr]bool{}) {
		t.Error("localhost points here")
	}
	if pointsHere("no-such-host.invalid", ownAddrs()) {
		t.Error("an unknown host does not")
	}
}
