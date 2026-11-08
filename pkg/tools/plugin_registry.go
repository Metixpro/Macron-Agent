package tools

import (
	"context"
	"sort"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

type PluginTools interface {
	Tools() []llm.Tool
	Has(name string) bool
	Allowed(name, sessionID string) bool
	Call(ctx context.Context, sessionID, name string, args map[string]any) (string, error)
}

type pluginSource struct{ PluginTools }

func (r *Registry) SetPlugins(p PluginTools) {
	if p == nil {
		r.plugins.Store(nil)
		return
	}
	r.plugins.Store(&pluginSource{p})
}

func (r *Registry) Builtin(name string) bool { _, ok := r.handler[name]; return ok }

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
