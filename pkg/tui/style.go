package tui

import (
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/glamour"
	gansi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	colPlan   = lipgloss.AdaptiveColor{Light: "#7545A8", Dark: "#BEA0FF"}
	colPanel  = lipgloss.AdaptiveColor{Light: "#F3F1EB", Dark: "#191B20"}
	colText   = lipgloss.AdaptiveColor{Light: "#24272D", Dark: "#E6E4DF"}
	colAccent = lipgloss.AdaptiveColor{Light: "#3165B5", Dark: "#78AAFF"}
	colOK     = lipgloss.AdaptiveColor{Light: "#1A7F37", Dark: "#3FB950"}
	colFail   = lipgloss.AdaptiveColor{Light: "#CF222E", Dark: "#F85149"}
	colWarn   = lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#D29922"}
	colDim    = lipgloss.AdaptiveColor{Light: "#6E7781", Dark: "#8B949E"}
	colFaint  = lipgloss.AdaptiveColor{Light: "#AFB8C1", Dark: "#484F58"}
	colAdd    = lipgloss.AdaptiveColor{Light: "#116329", Dark: "#7EE787"}
	colDel    = lipgloss.AdaptiveColor{Light: "#82071E", Dark: "#FFA198"}

	stAccent = lipgloss.NewStyle().Foreground(colAccent)
	stBold   = lipgloss.NewStyle().Bold(true)
	stOK     = lipgloss.NewStyle().Foreground(colOK)
	stFail   = lipgloss.NewStyle().Foreground(colFail)
	stWarn   = lipgloss.NewStyle().Foreground(colWarn)
	stDim    = lipgloss.NewStyle().Foreground(colDim)
	stFaint  = lipgloss.NewStyle().Foreground(colFaint)
	stAdd    = lipgloss.NewStyle().Foreground(colAdd)
	stDel    = lipgloss.NewStyle().Foreground(colDel)
	stTitle  = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
)

const (
	bullet = "●"
	elbow  = "⎿"
)

func resolveTheme(saved string) string {
	for _, v := range []string{os.Getenv("MACRON_THEME"), saved} {
		switch v = strings.ToLower(strings.TrimSpace(v)); v {
		case "dark", "light":
			return v
		}
	}
	if v := os.Getenv("COLORFGBG"); v != "" {
		parts := strings.Split(v, ";")
		if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			if n == 7 || n >= 9 && n <= 15 {
				return "light"
			}
			return "dark"
		}
	}
	return ""
}

func applyTheme(theme string) {
	if theme != "" {
		lipgloss.SetHasDarkBackground(theme != "light")
	}
}

func markdownStyle() string {
	if os.Getenv("NO_COLOR") != "" {
		return "notty"
	}
	if lipgloss.HasDarkBackground() {
		return "dark"
	}
	return "light"
}

func markdownStyles(name string) gansi.StyleConfig {
	c, accent := styles.DarkStyleConfig, "#E2AD77"
	switch name {
	case "light":
		c, accent = styles.LightStyleConfig, "#945529"
	case "notty":
		return styles.NoTTYStyleConfig
	}
	bold := true
	for i, h := range []*gansi.StyleBlock{&c.H1, &c.H2, &c.H3, &c.H4, &c.H5, &c.H6} {
		h.Prefix, h.Suffix, h.BackgroundColor, h.Bold = "", "", nil, &bold
		if i < 2 {
			h.Color = &accent
		}
	}
	return c
}

type markdown struct {
	style string
	width int
	r     *glamour.TermRenderer
}

func (md *markdown) render(text string, width int) string {
	width = max(width-2, 20)
	if md.r == nil || md.width != width {
		r, err := glamour.NewTermRenderer(glamour.WithStyles(markdownStyles(md.style)), glamour.WithWordWrap(width), glamour.WithEmoji())
		if err != nil {
			return wrap(text, width)
		}
		md.r, md.width = r, width
	}
	out, err := md.r.Render(text)
	if err != nil {
		return wrap(text, width)
	}
	return trimBlank(out)
}

func trimBlank(s string) string {
	lines := strings.Split(s, "\n")
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[0])) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}

func withBullet(block string, mark string) string {
	lines := strings.Split(block, "\n")
	if rest, ok := dropMargin(lines[0], 2); ok {
		lines[0] = mark + " " + rest
		return strings.Join(lines, "\n")
	}
	lines[0] = mark + " " + lines[0]
	for i := 1; i < len(lines); i++ {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n")
}

func dropMargin(s string, n int) (string, bool) {
	var b strings.Builder
	i := 0
	for i < len(s) && n > 0 {
		switch {
		case s[i] == 0x1b:
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
					j++
				}
				j++
			}
			j = min(j, len(s))
			b.WriteString(s[i:j])
			i = j
		case s[i] == ' ':
			n--
			i++
		default:
			return s, false
		}
	}
	b.WriteString(s[i:])
	return b.String(), n == 0
}

func keepBg(s string, bg lipgloss.TerminalColor) string {
	probe := lipgloss.NewStyle().Background(bg).Render(" ")
	i := strings.Index(probe, " ")
	if i <= 0 {
		return s
	}
	seq := probe[:i]
	return strings.NewReplacer("\x1b[0m", "\x1b[0m"+seq, "\x1b[m", "\x1b[m"+seq).Replace(s)
}

func wrap(text string, width int) string {
	return ansi.Wrap(text, max(width, 10), " ")
}

func clip(s string, width int) string {
	if width <= 1 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

func clean(s string) string {
	s = ansi.Strip(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r == '\n' || r >= 0x20 && r != 0x7f && !(r >= 0x80 && r < 0xa0):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(clean(s)), " ")
}
