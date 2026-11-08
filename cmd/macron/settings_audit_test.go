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

var settingsWithoutPanel = map[string]string{
	"SettingReactions":      "legacy runtime preference; reaction controls removed from settings",
	"SettingReactionEmoji":  "legacy runtime preference; reaction controls removed from settings",
	"settingDLCookies":      "managed by the downloader panel through cookie input and delete handlers",
	"SettingActiveProvider": "set by choosing ⭐ in the providers panel (a:pv), not a switch of its own",
	"settingIdleTimeout":    "the idle brake of a job, 15 minutes by default (shown in 🦾 the agent); «macron config set idle_timeout 30»",

	"settingEvalProvider": "state: the provider and model the last exam ran on",
	"settingEvalNight":    "state: the day of the last nightly exam",
	"settingWeeklySent":   "state: when the last weekly report went",
	"settingLearnJobs":    "state: the jobs since the last learning review",
	"settingHookSecret":   "a secret made at random on first use; /dashboard shows it",
	"settingDashToken":    "a secret made at random on first use; /dashboard gives the link",
	"settingSightW":       "state: when the owner was last warned about a group's privacy mode",

	"settingPluginPrefix": "plugin state, written by the plugin tools",
	"settingMCPServers":   "MCP servers, written by the plugin tools",
	"settingLearnAuto":    "learning: adopted at once in the personal edition (see the learning panel)",

	"settingOwnerContext": "DEAD: read in access.go, never written; superseded by settingContextWindow",
}

func TestEverySettingIsUsedAndReachable(t *testing.T) {
	type def struct{ name, file string }
	var defs []def
	src := map[string]string{}
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
				n--
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
