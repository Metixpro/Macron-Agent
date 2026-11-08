package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// settingsWithoutPanel are the settings the panel does not show, and why.
// A key read from the database with no way to change it in the panel must be
// listed here, so the next audit sees it; anything else is dead code.
var settingsWithoutPanel = map[string]string{
	"SettingPremiumEmoji":   "off by default; turned on with «macron config set premium_emoji true» (then managed from 🖥 showing the work)",
	"SettingActiveProvider": "set by choosing ⭐ in the providers panel (a:pv), not a switch of its own",
	"settingIdleTimeout":    "the idle brake of a job, 15 minutes by default (shown in 🦾 the agent); «macron config set idle_timeout 30»",
	// The bot's own bookkeeping, not choices of the owner.
	"settingEvalProvider": "state: the provider and model the last exam ran on",
	"settingEvalNight":    "state: the day of the last nightly exam",
	"settingWeeklySent":   "state: when the last weekly report went",
	"settingSightW":       "state: when the owner was last warned about a group's privacy mode",
	// Kept by the agent's own tools (plugins and MCP servers are added in the chat).
	"settingPluginPrefix": "plugin state, written by the plugin tools",
	"settingMCPServers":   "MCP servers, written by the plugin tools",
	"settingLearnAuto":    "learning: adopted at once in the personal edition (see the learning panel)",
	// Dead: read, but nothing sets it any more (🪟 context window writes
	// settingContextWindow). Reported until it is removed.
	"settingOwnerContext": "DEAD: read in access.go, never written; superseded by settingContextWindow",
}

// TestEverySettingIsUsedAndReachable: each setting key the code defines is
// read somewhere, and the owner can change it from the panel (or it is listed
// above with its reason).
func TestEverySettingIsUsedAndReachable(t *testing.T) {
	type def struct{ name, file string }
	var defs []def
	src := map[string]string{} // file → its text
	fset := token.NewFileSet()
	for _, dir := range []string{".", "../../pkg/store"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			src[f] = string(b)
			af, err := parser.ParseFile(fset, f, b, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range af.Decls {
				g, ok := d.(*ast.GenDecl)
				if !ok || g.Tok != token.CONST {
					continue
				}
				for _, sp := range g.Specs {
					for _, n := range sp.(*ast.ValueSpec).Names {
						if regexp.MustCompile(`^[sS]etting[A-Z]`).MatchString(n.Name) {
							defs = append(defs, def{n.Name, f})
						}
					}
				}
			}
		}
	}
	if len(defs) < 30 {
		t.Fatalf("found only %d settings; is the scan broken?", len(defs))
	}
	var report []string
	for _, d := range defs {
		word := regexp.MustCompile(`\b` + d.name + `\b`)
		used, panel := false, false
		for f, text := range src {
			n := len(word.FindAllStringIndex(text, -1))
			if f == d.file {
				n-- // its own definition
			}
			if n <= 0 {
				continue
			}
			used = true
			if strings.Contains(text, "panelView") {
				panel = true
			}
		}
		if !used {
			t.Errorf("%s (%s) is defined but never read: dead code", d.name, d.file)
			continue
		}
		if why, ok := settingsWithoutPanel[d.name]; ok {
			report = append(report, d.name+": "+why)
			continue
		}
		if !panel {
			t.Errorf("%s (%s) is read but no panel changes it: add it to the panel or to settingsWithoutPanel with a reason", d.name, d.file)
		}
	}
	sort.Strings(report)
	t.Logf("settings outside the panel:\n%s", strings.Join(report, "\n"))
	for _, r := range report {
		if strings.Contains(r, "DEAD:") {
			t.Logf("audit: %s", r)
		}
	}
}
