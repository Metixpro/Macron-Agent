package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/tui"
)

func (t *terminal) changeProvider(change func(*store.Provider) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := t.prov.ActiveID()
	if id == 0 {
		return errors.New("no model is connected yet")
	}
	p, err := t.st.Provider(ctx, id)
	if err != nil {
		return err
	}
	if err := change(p); err != nil {
		return err
	}
	normalized, err := providers.Normalize(*p)
	if err != nil {
		return err
	}
	if err := t.st.UpdateProvider(ctx, normalized); err != nil {
		return err
	}
	return t.prov.Reload(ctx)
}

func (t *terminal) Tuning() tui.Tuning {
	p, ok := t.prov.Active()
	if !ok {
		return tui.Tuning{}
	}
	api := providers.API(p)
	param, value, support := llm.EffortWire(api, p.Model, p.Effort)
	auto := p
	auto.ContextWindow = 0
	return tui.Tuning{Model: p.Model, API: api, Effort: p.Effort, EffortParam: param, EffortValue: value, EffortSupport: int(support),
		Window: p.ContextWindow, AutoWindow: providerWindow(t.st, auto, true, 0), Temperature: p.Temperature}
}

func (t *terminal) SetEffort(level string) error {
	if !llm.ValidEffort(level) {
		return fmt.Errorf("effort is auto, low, medium, high or ultracode")
	}
	return t.changeProvider(func(p *store.Provider) error {
		p.Effort = level
		return nil
	})
}

func (t *terminal) SetContextWindow(tokens int) error {
	if tokens != 0 && (tokens < 8000 || tokens > 10000000) {
		return fmt.Errorf("the context window is 8k to 10m tokens, or auto")
	}
	return t.changeProvider(func(p *store.Provider) error {
		p.ContextWindow = tokens
		return nil
	})
}

func (t *terminal) SetTemperature(v *float64) error {
	return t.changeProvider(func(p *store.Provider) error {
		if v != nil {
			limit := 2.0
			if providers.API(*p) == llm.APIAnthropic {
				limit = 1
			}
			if math.IsNaN(*v) || *v < 0 || *v > limit {
				return fmt.Errorf("temperature for this model is 0 to %g, or auto", limit)
			}
		}
		p.Temperature = v
		return nil
	})
}
