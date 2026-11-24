package desk

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ThemePalette defines an OpenBot-style live color palette for Macron Desktop.
// Users can drop custom `<id>.json` files into `~/.macron/themes/` and switch
// themes live in Settings without restarting.
type ThemePalette struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Appearance string            `json:"appearance"` // "dark" or "light"
	Builtin    bool              `json:"builtin,omitempty"`
	Swatches   [3]string         `json:"swatches"`
	Colors     map[string]string `json:"colors"`
}

var (
	themeIDRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,48}$`)
	hexColorRe = regexp.MustCompile(`^#([0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)
)

func builtinThemePalettes() []ThemePalette {
	return []ThemePalette{
		{
			ID:         "obsidian",
			Name:       "Obsidian",
			Appearance: "dark",
			Builtin:    true,
			Swatches:   [3]string{"#0d0e11", "#181a1f", "#45b887"},
			Colors: map[string]string{
				"frame":   "#0b0c0f",
				"sidebar": "#0f1014",
				"canvas":  "#15171c",
				"surface": "#1d2027",
				"sunk":    "#111318",
				"accent":  "#45b887",
				"link":    "#7aa7ff",
			},
		},
		{
			ID:         "midnight-cobalt",
			Name:       "Midnight Cobalt",
			Appearance: "dark",
			Builtin:    true,
			Swatches:   [3]string{"#070b1a", "#101936", "#60a5fa"},
			Colors: map[string]string{
				"frame":   "#060916",
				"sidebar": "#0a1026",
				"canvas":  "#0f1730",
				"surface": "#162142",
				"sunk":    "#0b1228",
				"accent":  "#60a5fa",
				"link":    "#93c5fd",
			},
		},
		{
			ID:         "tokyo-night",
			Name:       "Tokyo Night",
			Appearance: "dark",
			Builtin:    true,
			Swatches:   [3]string{"#13141c", "#1a1b26", "#7aa2f7"},
			Colors: map[string]string{
				"frame":   "#11121a",
				"sidebar": "#161722",
				"canvas":  "#1a1b26",
				"surface": "#24283b",
				"sunk":    "#141520",
				"accent":  "#7aa2f7",
				"link":    "#7dcfff",
			},
		},
		{
			ID:         "graphite-amber",
			Name:       "Graphite Gold",
			Appearance: "dark",
			Builtin:    true,
			Swatches:   [3]string{"#101010", "#1b1a18", "#e5a84b"},
			Colors: map[string]string{
				"frame":   "#0d0d0d",
				"sidebar": "#141413",
				"canvas":  "#1a1917",
				"surface": "#242220",
				"sunk":    "#131312",
				"accent":  "#e5a84b",
				"link":    "#f0be6e",
			},
		},
		{
			ID:         "alabaster",
			Name:       "Alabaster",
			Appearance: "light",
			Builtin:    true,
			Swatches:   [3]string{"#eaeae6", "#ffffff", "#1d7f5a"},
			Colors: map[string]string{
				"frame":   "#e7e7e2",
				"sidebar": "#eeefe9",
				"canvas":  "#fafaf8",
				"surface": "#ffffff",
				"sunk":    "#f1f1ed",
				"accent":  "#1d7f5a",
				"link":    "#1f54c7",
			},
		},
	}
}

func loadUserThemePalettes(dataDir string) []ThemePalette {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	dir := filepath.Join(dataDir, "themes")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []ThemePalette
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(ent.Name(), ".json")
		if !themeIDRe.MatchString(id) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil || len(raw) > 32<<10 {
			continue
		}
		var p ThemePalette
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		p.ID = id
		p.Builtin = false
		if strings.TrimSpace(p.Name) == "" {
			p.Name = id
		}
		if p.Appearance != "light" && p.Appearance != "dark" {
			p.Appearance = "dark"
		}
		clean := map[string]string{}
		for k, v := range p.Colors {
			v = strings.TrimSpace(v)
			if hexColorRe.MatchString(v) {
				clean[k] = v
			}
		}
		if len(clean) == 0 {
			continue
		}
		p.Colors = clean
		p.Swatches = [3]string{
			firstNonEmpty(clean["frame"], clean["sidebar"], "#141414"),
			firstNonEmpty(clean["canvas"], clean["surface"], "#1a1a1a"),
			firstNonEmpty(clean["accent"], "#60a5fa"),
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// themesAPI returns built-in curated palettes plus any custom JSON palettes
// found in `<dataDir>/themes/*.json`.
func (s *Server) themesAPI(w http.ResponseWriter, r *http.Request) {
	list := builtinThemePalettes()
	list = append(list, loadUserThemePalettes(s.host.DataDir())...)
	writeJSON(w, map[string]any{
		"themes": list,
		"dir":    filepath.Join(s.host.DataDir(), "themes"),
	})
}
