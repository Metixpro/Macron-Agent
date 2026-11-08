package main

import (
	"fmt"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/app"
)

func (b *Bot) ownerNote(owner bool, conf tierConf) string {
	if !owner {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "For the owner: you are %s version %s.", b.app.Agent.Persona.Name, app.Version)
	if p, ok := b.prov.Active(); ok {
		fmt.Fprintf(&sb, " The active provider is %q with the model %q.", p.Name, p.Model)
	}
	if conf.provider > 0 {
		if p, err := b.st.Provider(b.jobCtx, conf.provider); err == nil {
			fmt.Fprintf(&sb, " This request prefers the provider %q (model %q).", p.Name, p.Model)
		}
	}
	return sb.String()
}
