package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

const ToolSearch = "tool_search"

const deferMin = 6

const searchResults = 5

var coreTools = map[string]bool{
	"web_search": true, "web_fetch": true, "bash_execute": true, tools.ToolRunCode: true, tools.ToolShellJobs: true,
	"read_file": true, "write_file": true, tools.ToolEditFile: true, tools.ToolApplyPatch: true, "list_files": true,
	tools.ToolSearchCode: true, tools.ToolFindFiles: true, "send_file": true, "view_image": true, "ask_user": true,
	"react_to_message": true, "remember": true, "forget": true, "search_past_chats": true, "recall_history": true,
	"schedule": true, "list_schedules": true, "cancel_schedule": true, ToolPlan: true, ToolDelegate: true,
	tools.ToolReadSkill: true, tools.ToolSaveSkill: true, ToolSearch: true, ToolMoreTools: true,
}

type shelf struct {
	mu        sync.Mutex
	tools     map[string]llm.Tool
	loaded    map[string]bool
	loadOrder []string
	order     []string
}

func (a *Agent) deferTools(defs []llm.Tool, req Request) *shelf {
	if req.depth > 0 {
		return nil
	}
	s := &shelf{tools: map[string]llm.Tool{}, loaded: map[string]bool{}}
	for _, t := range defs {
		if n := t.Function.Name; !coreTools[n] {
			s.tools[n] = t
			s.order = append(s.order, n)
		}
	}
	if len(s.tools) <= deferMin {
		return nil
	}
	sort.Strings(s.order)
	for _, n := range req.Preload {
		s.load(n)
	}
	text := focusText(req)
	for _, n := range s.order {
		if len(n) > 4 && strings.Contains(text, n) {
			s.load(n)
		}
	}
	want := focusWant(defs, text)
	for _, n := range s.order {
		if want[n] {
			s.load(n)
		}
	}
	for _, n := range req.FocusPick {
		s.load(n)
	}
	s.preload(req)
	return s
}

func (s *shelf) load(name string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tools[name]; !ok || s.loaded[name] {
		return false
	}
	s.loaded[name] = true
	s.loadOrder = append(s.loadOrder, name)
	return true
}

const preloadMax = 8

func (s *shelf) preload(req Request) {
	msg := req.Input.Content
	for _, p := range req.Input.Parts {
		msg += " " + p.Text
	}
	words := map[string]bool{}
	for _, w := range searchWords(msg) {
		if len(w) >= 3 {
			words[w] = true
		}
	}
	byFamily := map[string][]string{}
	for _, n := range s.order {
		f, _, _ := strings.Cut(strings.ToLower(n), "_")
		if words[f] {
			byFamily[f] = append(byFamily[f], n)
		}
	}
	fams := make([]string, 0, len(byFamily))
	for f := range byFamily {
		fams = append(fams, f)
	}
	sort.Strings(fams)
	for _, f := range fams {
		names := byFamily[f]
		score := func(n string) int {
			k := 0
			for _, w := range searchWords(n + " " + s.tools[n].Function.Description) {
				if words[w] {
					k++
				}
			}
			return k
		}
		sort.SliceStable(names, func(i, j int) bool { return score(names[i]) > score(names[j]) })
		for _, n := range names[:min(len(names), preloadMax)] {
			s.load(n)
		}
	}
}

func (s *shelf) hide(defs []llm.Tool) []llm.Tool {
	if s == nil {
		return defs
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]llm.Tool, 0, len(defs)+len(s.loadOrder))
	for _, t := range defs {
		if _, shelved := s.tools[t.Function.Name]; !shelved {
			out = append(out, t)
		}
	}
	for _, n := range s.loadOrder {
		out = append(out, s.tools[n])
	}
	return out
}

func (s *shelf) note() string {
	if s == nil {
		return ""
	}
	return fmt.Sprintf("\n[tools] Beyond the tools in your list, these %d wait unloaded: %s. To use one, call %s with its "+
		"name (several names, comma-separated, are fine) or with what it should do, in a few English words; it can be called "+
		"from the next step. Calling one by its name also loads it.", len(s.order), strings.Join(s.order, ", "), ToolSearch)
}

func (s *shelf) loadedNames() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.loadOrder...)
}

func (s *shelf) searchTool() LocalTool {
	return LocalTool{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name: ToolSearch,
			Description: "Load tools that are not in your list yet (the [tools] note names them). Give their names, or say what " +
				"the tool should do in a few English words; the tools loaded can be called from the next step.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Tool names, or what the tool should do, e.g. \"pdf_tool\", \"convert video to mp3\""},
			}, "required": []string{"query"}},
		}},
		Handler: func(_ context.Context, args map[string]any) (ToolOutput, error) {
			q, _ := args["query"].(string)
			return ToolOutput{Text: s.search(q)}, nil
		},
	}
}

func (s *shelf) search(q string) string {
	var picked []string
	for _, part := range strings.FieldsFunc(q, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == ';' }) {
		if _, ok := s.tools[strings.TrimSpace(part)]; ok {
			picked = append(picked, strings.TrimSpace(part))
		}
	}
	if len(picked) == 0 {
		words := searchWords(q)
		type hit struct {
			name  string
			score int
		}
		var hits []hit
		for _, n := range s.order {
			t := s.tools[n]
			name, about := searchWords(n), searchWords(t.Function.Description)
			score := 0
			for _, w := range words {
				for _, x := range name {
					if x == w {
						score += 4
					} else if len(w) > 3 && strings.HasPrefix(x, w) {
						score += 2
					}
				}
				for _, x := range about {
					if x == w {
						score++
					}
				}
			}
			if score > 0 {
				hits = append(hits, hit{n, score})
			}
		}
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
		for _, h := range hits[:min(len(hits), searchResults)] {
			picked = append(picked, h.name)
		}
	}
	if len(picked) == 0 {
		return fmt.Sprintf("No tool matches %q. The tools that wait unloaded are: %s. Try one of these names, or do the job "+
			"with the tools you have.", q, strings.Join(s.order, ", "))
	}
	var lines []string
	for _, n := range picked {
		s.load(n)
		lines = append(lines, "- "+n+": "+firstSentence(s.tools[n].Function.Description, 160))
	}
	return "Loaded; call them from the next step:\n" + strings.Join(lines, "\n")
}

func searchWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
