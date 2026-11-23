package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// AvatarMood represents the live emotional/activity state of the StarBuddy mascot.
type AvatarMood int

const (
	AvatarIdle AvatarMood = iota
	AvatarWorking
	AvatarStreaming
	AvatarAsking
)

type avatarTickMsg time.Time

func avatarTick() tea.Cmd {
	return tea.Tick(650*time.Millisecond, func(t time.Time) tea.Msg {
		return avatarTickMsg(t)
	})
}

func (m model) avatarMood() AvatarMood {
	switch {
	case m.ask != nil:
		return AvatarAsking
	case m.running && strings.TrimSpace(m.stream) != "":
		return AvatarStreaming
	case m.running || m.busy != "":
		return AvatarWorking
	default:
		return AvatarIdle
	}
}

// StarBuddyHero renders a multi-frame ANSI/Unicode portrait of the StarBuddy
// mascot (a glowing soft-white 5-point star with dark vertical oval eyes and
// rounded paws peeking over a cobalt-blue planet rim under twinkling stars).
func StarBuddyHero(frame int, mood AvatarMood) string {
	starBody := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#1D4ED8", Dark: "#F8FAFC"}).Bold(true)
	starGlow := lipgloss.NewStyle().Foreground(colAccent)
	planetRim := lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	skyStar := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#3B82F6", Dark: "#93C5FD"})
	eyeStyle := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#0F172A", Dark: "#60A5FA"}).Bold(true)
	if mood == AvatarAsking {
		planetRim = lipgloss.NewStyle().Foreground(colWarn).Bold(true)
		starGlow = lipgloss.NewStyle().Foreground(colWarn)
	}

	// Twinkling upper starfield frames (each half is 14 columns wide so the 4-col crown centers at col 14..17)
	skies := [4][2]string{
		{"  ✦    ·   ✧  ", "  ✧   ·    ✦  "},
		{"  ·    ✦   ·  ", "  ·   ✦    ·  "},
		{"  ✧    ·   ✦  ", "  ✦   ·    ✧  "},
		{"  ·    ✧   ·  ", "  ·   ✧    ·  "},
	}
	sky := skies[((frame%4)+4)%4]

	// Eye expressions by mood and frame (6 columns wide)
	eyes := "0    0"
	switch mood {
	case AvatarWorking:
		workEyes := [4]string{"•    •", "°    °", "•    •", "0    0"}
		eyes = workEyes[((frame%4)+4)%4]
	case AvatarStreaming:
		streamEyes := [4]string{"✦    ✦", "•    •", "✦    ✦", "0    0"}
		eyes = streamEyes[((frame%4)+4)%4]
	case AvatarAsking:
		askEyes := [4]string{"?    ?", "0    0", "?    ?", "•    •"}
		eyes = askEyes[((frame%4)+4)%4]
	default:
		// Idle: gentle look around + periodic blink
		idleEyes := [8]string{"0    0", "0    0", "•    •", "0    0", "-    -", "0    0", "°    °", "0    0"}
		eyes = idleEyes[((frame%8)+8)%8]
	}

	// Paws on the planet horizon
	leftPaw, rightPaw := "(▀▀)", "(▀▀)"
	if (mood == AvatarWorking || mood == AvatarStreaming) && frame%2 == 1 {
		leftPaw, rightPaw = "(▀▄)", "(▄▀)"
	}

	lines := []string{
		skyStar.Render(sky[0]) + starGlow.Render("▄██▄") + skyStar.Render(sky[1]),
		"            " + starGlow.Render("▄") + starBody.Render("██████") + starGlow.Render("▄") + "            ",
		"       " + starGlow.Render("▄▄▄▄") + starBody.Render("██████████") + starGlow.Render("▄▄▄▄") + "       ",
		"      " + starBody.Render("█████  ") + eyeStyle.Render(eyes) + starBody.Render("  █████") + "      ",
		"   " + planetRim.Render("╭───") + starBody.Render(leftPaw) + planetRim.Render("──────────") + starBody.Render(rightPaw) + planetRim.Render("───╮") + "   ",
	}
	return strings.Join(lines, "\n")
}

// StarBuddyBadge returns a compact single-line animated StarBuddy indicator
// for the TUI activity bar and narrow terminals.
func StarBuddyBadge(frame int, mood AvatarMood) string {
	glow := stAccent
	if mood == AvatarAsking {
		glow = stWarn
	}
	switch mood {
	case AvatarWorking:
		frames := [4]string{"✦(•_•)✦", "✧(°_°)✧", "✦(•_•)✦", "✧(0_0)✧"}
		return glow.Bold(true).Render(frames[((frame%4)+4)%4])
	case AvatarStreaming:
		frames := [4]string{"★(✦_✦)★", "✦(•_•)✦", "★(✦_✦)★", "✧(0_0)✧"}
		return glow.Bold(true).Render(frames[((frame%4)+4)%4])
	case AvatarAsking:
		frames := [2]string{"✦(?_?)✦", "✧(•_•)✧"}
		return glow.Bold(true).Render(frames[((frame%2)+2)%2])
	default:
		frames := [8]string{"✦(0_0)✦", "✦(0_0)✦", "✧(•_•)✧", "✦(0_0)✦", "✦(-_-)✦", "✦(0_0)✦", "✧(°_°)✧", "✦(0_0)✦"}
		return glow.Render(frames[((frame%8)+8)%8])
	}
}
