package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

// Deferred tools: the plugins' and MCP servers' tools wait unloaded when
// there are many of them. Their schemas would cost every step thousands of
// tokens and crowd the built-in tools out of a call's limit (MaxTools); the
// model sees how many there are and loads what a job needs with tool_search.
// A tool the conversation names, or that the job already used, is loaded by
// itself. Calls to a tool that is not loaded still run, if the model knows
// its name.

// ToolSearch finds and loads the tools of the plugins and MCP servers.
const ToolSearch = "tool_search"

// deferAbove is how many plugin tools go as they are: a few cost little.
const deferAbove = 16

// searchResults is how many tools one search loads at most.
const searchResults = 8

// shelf holds the unloaded tools of one request.
type shelf struct {
	mu     sync.Mutex
	tools  map[string]llm.Tool
	loaded map[string]bool
	order  []string
}

// deferPlugins shelves the plugins' tools of a request when there are more
// than deferAbove (nil: none are shelved). A sub-agent gets no plugin tools.
func (a *Agent) deferPlugins(defs []llm.Tool, req Request) *shelf {
	if req.depth > 0 || a.Registry == nil {
		return nil
	}
	s := &shelf{tools: map[string]llm.Tool{}, loaded: map[string]bool{}}
	for _, t := range defs {
		if a.Registry.Plugin(t.Function.Name) {
			s.tools[t.Function.Name] = t
			s.order = append(s.order, t.Function.Name)
		}
	}
	if len(s.tools) <= deferAbove {
		return nil
	}
	sort.Strings(s.order)
	text := focusText(req) // a tool the conversation names is loaded
	for _, n := range s.order {
		if len(n) > 4 && strings.Contains(text, n) {
			s.loaded[n] = true
		}
	}
	s.preload(req)
	return s
}

// preloadMax is how many tools a family the message names loads at once.
const preloadMax = 8

// preload loads, before the first step, the tools of a family the message
// names («از دیتابیس sqlite…» → sqlite_*): no round goes to tool_search for
// what the request already says. The family's tools the message's other
// words point to come first.
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
	for _, names := range byFamily {
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
			s.loaded[n] = true
		}
	}
}

// hide leaves out the shelved tools a job has not loaded.
func (s *shelf) hide(defs []llm.Tool) []llm.Tool {
	if s == nil {
		return defs
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]llm.Tool, 0, len(defs))
	for _, t := range defs {
		if _, shelved := s.tools[t.Function.Name]; !shelved || s.loaded[t.Function.Name] {
			out = append(out, t)
		}
	}
	return out
}

// note tells the model what waits unloaded (the plugins' name parts).
func (s *shelf) note() string {
	if s == nil {
		return ""
	}
	note := fmt.Sprintf("[tools] %d more tools from plugins and MCP servers wait unloaded (%s). If a job needs one, call %s with "+
		"what it should do; the tools it finds can be called from the next step.", len(s.tools), s.families(), ToolSearch)
	if loaded := s.loadedNames(); len(loaded) > 0 {
		note += " Loaded for this request: " + strings.Join(loaded, ", ") + "."
	}
	return note
}

func (s *shelf) loadedNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, n := range s.order {
		if s.loaded[n] {
			out = append(out, n)
		}
	}
	return out
}

// families are the first words of the shelved tools' names, the most common first.
func (s *shelf) families() string {
	count := map[string]int{}
	for _, n := range s.order {
		f, _, _ := strings.Cut(n, "_")
		count[f]++
	}
	fams := make([]string, 0, len(count))
	for f := range count {
		fams = append(fams, f)
	}
	sort.Slice(fams, func(i, j int) bool {
		return count[fams[i]] > count[fams[j]] || count[fams[i]] == count[fams[j]] && fams[i] < fams[j]
	})
	if len(fams) > 12 {
		fams = append(fams[:12], "…")
	}
	return strings.Join(fams, ", ")
}

func (s *shelf) searchTool() LocalTool {
	return LocalTool{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name: ToolSearch,
			Description: "Find and load tools of the plugins and MCP servers (they are not loaded until needed). " +
				"Say what the tool should do, in a few English words; the tools found can be called from the next step.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "What the tool should do, e.g. \"sqlite query\", \"dns lookup\""},
			}, "required": []string{"query"}},
		}},
		Handler: func(_ context.Context, args map[string]any) (ToolOutput, error) {
			q, _ := args["query"].(string)
			return ToolOutput{Text: s.search(q)}, nil
		},
	}
}

// search loads the shelved tools that best match the query.
func (s *shelf) search(q string) string {
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
	if len(hits) > searchResults {
		hits = hits[:searchResults]
	}
	if len(hits) == 0 {
		return fmt.Sprintf("No plugin tool matches %q. The plugins' tools start with: %s. Try other words, or do the job with "+
			"the tools you have.", q, s.families())
	}
	s.mu.Lock()
	var lines []string
	for _, h := range hits {
		s.loaded[h.name] = true
		lines = append(lines, "- "+h.name+": "+firstSentence(s.tools[h.name].Function.Description, 160))
	}
	s.mu.Unlock()
	return "Loaded; call them from the next step:\n" + strings.Join(lines, "\n")
}

// searchWords are the lower-case words of a text (names split at _ - . too).
func searchWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
