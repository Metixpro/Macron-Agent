package tools

import (
	"context"
	"sort"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

// PluginTools is a source of tools beyond the built-in ones: the plugins
// and MCP servers the admins installed (pkg/plugins). Only the tools of the
// plugins that are on are listed, so the others cost no prompt tokens; a
// built-in tool's name always stays the built-in tool's.
type PluginTools interface {
	Tools() []llm.Tool
	Has(name string) bool
	Allowed(name, sessionID string) bool
	Call(ctx context.Context, sessionID, name string, args map[string]any) (string, error)
}

type pluginSource struct{ PluginTools }

// SetPlugins gives the registry the plugins' tools (nil: none).
func (r *Registry) SetPlugins(p PluginTools) {
	if p == nil {
		r.plugins.Store(nil)
		return
	}
	r.plugins.Store(&pluginSource{p})
}

// Builtin reports a tool of the registry itself (not a plugin's).
func (r *Registry) Builtin(name string) bool { _, ok := r.handler[name]; return ok }

// Plugin reports a tool a plugin or an MCP server gives (not a built-in one).
func (r *Registry) Plugin(name string) bool {
	p := r.pluginTools()
	return p != nil && !r.Builtin(name) && p.Has(name)
}

func (r *Registry) pluginTools() PluginTools {
	if s := r.plugins.Load(); s != nil {
		return s.PluginTools
	}
	return nil
}

// withPlugins adds the plugins' tools to the built-in list (in name order).
func (r *Registry) withPlugins(list []llm.Tool) []llm.Tool {
	p := r.pluginTools()
	if p == nil {
		return list
	}
	extra := p.Tools()
	if len(extra) == 0 {
		return list
	}
	for _, t := range extra {
		if !r.Builtin(t.Function.Name) {
			list = append(list, t)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Function.Name < list[j].Function.Name })
	return list
}
