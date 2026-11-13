package providers

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/store"
)

func Normalize(p store.Provider) (store.Provider, error) {
	p.Name, p.Model, p.APIKey = strings.TrimSpace(p.Name), strings.TrimSpace(p.Model), strings.TrimSpace(p.APIKey)
	var err error
	p, err = normalizeEndpoint(p)
	if err != nil {
		return p, err
	}
	if p.Name == "" || utf8.RuneCountInString(p.Name) > 64 || strings.ContainsAny(p.Name, "\r\n\x00") {
		return p, fmt.Errorf("provider name must contain 1–64 characters")
	}
	if p.Model == "" || len(p.Model) > 256 || strings.ContainsAny(p.Model, "\r\n\x00") {
		return p, fmt.Errorf("a valid model name is required")
	}
	if strings.ContainsAny(p.APIKey, "\r\n\x00") {
		return p, fmt.Errorf("API key must be a single line")
	}
	if p.API != "" && p.API != llm.APIOpenAI && p.API != llm.APIResponses && p.API != llm.APIAnthropic {
		return p, fmt.Errorf("unsupported API type")
	}
	if p.ContextWindow != 0 && (p.ContextWindow < 8000 || p.ContextWindow > 10000000) {
		return p, fmt.Errorf("context window must be 0 or 8000–10000000 tokens")
	}
	if p.Priority < 0 {
		return p, fmt.Errorf("priority must not be negative")
	}
	if !llm.ValidEffort(p.Effort) {
		return p, fmt.Errorf("unsupported reasoning effort")
	}
	if p.Temperature != nil {
		max := 2.0
		if API(p) == llm.APIAnthropic {
			max = 1
		}
		t := *p.Temperature
		if math.IsNaN(t) || math.IsInf(t, 0) || t < 0 || t > max {
			return p, fmt.Errorf("temperature must be between 0 and %g for this API", max)
		}
	}
	return p, nil
}
func sameConnection(a, b store.Provider) bool {
	sameTemp := a.Temperature == nil && b.Temperature == nil || a.Temperature != nil && b.Temperature != nil && *a.Temperature == *b.Temperature
	return a.BaseURL == b.BaseURL && a.APIKey == b.APIKey && a.Model == b.Model && a.API == b.API && a.Effort == b.Effort && a.NoStream == b.NoStream && a.Enabled == b.Enabled && sameTemp
}

func Configure(ctx context.Context, st *store.Store, name, endpoint, key, model string) (int64, error) {
	list, err := st.Providers(ctx)
	if err != nil {
		return 0, err
	}
	active, _ := st.Setting(store.SettingActiveProvider)
	var selected *store.Provider
	for _, p := range list {
		p := p
		if name != "" && p.Name == name || name == "" && fmt.Sprint(p.ID) == active {
			selected = &p
			break
		}
	}
	if selected == nil && name == "" {
		for _, p := range list {
			p := p
			if p.Enabled || p.Name == "default" {
				selected = &p
				break
			}
		}
	}
	p := store.Provider{Name: name, Enabled: true, Vision: true, Priority: 10}
	if selected != nil {
		p = *selected
	} else if name == "" {
		p.Name = "default"
	}
	p.BaseURL, p.API = llm.CleanBaseURL(endpoint)
	p.APIKey, p.Model, p.Enabled = key, model, true
	p, err = Normalize(p)
	if err != nil {
		return 0, err
	}
	id := p.ID
	if id == 0 {
		id, err = st.AddProvider(ctx, p)
	} else {
		err = st.UpdateProvider(ctx, p)
	}
	if err != nil {
		return 0, err
	}
	if err = st.SetSetting(ctx, store.SettingActiveProvider, fmt.Sprint(id)); err != nil {
		return 0, err
	}
	return id, nil
}

func normalizeEndpoint(p store.Provider) (store.Provider, error) {
	base, detected := llm.CleanBaseURL(p.BaseURL)
	p.BaseURL = strings.TrimRight(base, "/")
	if p.API == "" && detected != "" {
		p.API = detected
	}
	if err := ValidateBaseURL(p.BaseURL); err != nil {
		return p, err
	}
	if p.API != "" && p.API != llm.APIOpenAI && p.API != llm.APIResponses && p.API != llm.APIAnthropic {
		return p, fmt.Errorf("unsupported API type")
	}
	if strings.ContainsAny(p.APIKey, "\r\n\x00") {
		return p, fmt.Errorf("API key must be a single line")
	}
	return p, nil
}

type redactedError struct {
	cause   error
	message string
}

func (e redactedError) Error() string { return e.message }
func (e redactedError) Unwrap() error { return e.cause }
func privateError(err error, key string) error {
	if err == nil || key == "" {
		return err
	}
	return redactedError{cause: err, message: strings.ReplaceAll(err.Error(), key, "[hidden]")}
}
