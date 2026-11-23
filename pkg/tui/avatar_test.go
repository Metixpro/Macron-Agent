package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestStarBuddyHeroAndBadgeFrames(t *testing.T) {
	seenHero := map[string]bool{}
	seenBadge := map[string]bool{}
	for _, mood := range []AvatarMood{AvatarIdle, AvatarWorking, AvatarStreaming, AvatarAsking} {
		for f := 0; f < 8; f++ {
			hero := StarBuddyHero(f, mood)
			lines := strings.Split(hero, "\n")
			if len(lines) != 5 {
				t.Fatalf("expected 5 lines in StarBuddyHero(frame=%d, mood=%d), got %d", f, mood, len(lines))
			}
			plain := ansi.Strip(hero)
			if !strings.Contains(plain, "╭───") {
				t.Fatalf("expected planet horizon arc in StarBuddyHero:\n%s", plain)
			}
			seenHero[plain] = true

			badge := ansi.Strip(StarBuddyBadge(f, mood))
			if badge == "" {
				t.Fatalf("empty StarBuddyBadge(frame=%d, mood=%d)", f, mood)
			}
			seenBadge[badge] = true
		}
	}
	if len(seenHero) < 6 {
		t.Fatalf("expected multi-frame variation in StarBuddyHero, got only %d distinct frames", len(seenHero))
	}
	if len(seenBadge) < 5 {
		t.Fatalf("expected multi-frame variation in StarBuddyBadge, got only %d distinct badges", len(seenBadge))
	}
}
