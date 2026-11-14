package core

import "strings"

// GetProviderModels returns the configured model options for the active provider.
func GetProviderModels(providers []ProviderConfig, activeIdx int) []ModelOption {
	if activeIdx < 0 || activeIdx >= len(providers) {
		return nil
	}
	return providers[activeIdx].Models
}

// GetProviderModel returns the configured model for the active provider.
// If the active provider has no explicit model, fallback is returned.
func GetProviderModel(providers []ProviderConfig, activeIdx int, fallback string) string {
	if activeIdx < 0 || activeIdx >= len(providers) {
		return fallback
	}
	if model := providers[activeIdx].Model; model != "" {
		return model
	}
	return fallback
}

// SetProviderModel returns a copy of providers with the named provider's model updated.
// The second return value indicates whether a provider matched the given name.
func SetProviderModel(providers []ProviderConfig, name, model string) ([]ProviderConfig, bool) {
	updated := make([]ProviderConfig, len(providers))
	copy(updated, providers)
	for i := range updated {
		if updated[i].Name == name {
			updated[i].Model = model
			return updated, true
		}
	}
	return updated, false
}

// FindProviderConfig returns the provider with the given name.
func FindProviderConfig(providers []ProviderConfig, name string) (ProviderConfig, bool) {
	for _, p := range providers {
		if p.Name == name {
			return p, true
		}
	}
	return ProviderConfig{}, false
}

// ResolveProviderModel maps the model part of "/goto <provider>/<model>" onto
// the name to persist, using the provider's own model list the way the picker
// does (configured model, listed name or alias). scoped reports whether the
// agent stores provider-scoped names: then a bare resolved name is prefixed
// with the provider, while a name that already carries a "<transport>/" prefix
// (e.g. the ChatGPT provider's "openai/gpt-5.6-sol") is kept as is. The second
// result reports whether the provider offers the model at all.
func ResolveProviderModel(p ProviderConfig, providerName, model string, scoped bool) (string, bool) {
	if resolved, ok := ProviderModelFor(p, model); ok {
		return scopeProviderModel(providerName, resolved, scoped), true
	}
	if scoped {
		if resolved, ok := ProviderModelFor(p, strings.TrimPrefix(model, providerName+"/")); ok {
			return scopeProviderModel(providerName, resolved, scoped), true
		}
	}
	return "", false
}

// scopeProviderModel prefixes a bare model name for agents that store
// provider-scoped names, leaving transport-scoped names untouched.
func scopeProviderModel(providerName, model string, scoped bool) string {
	if scoped && !strings.Contains(model, "/") {
		return providerName + "/" + model
	}
	return model
}

// ProviderModelFor returns the model name a provider offers for the given value:
// the configured model itself, a listed model's name, or the name behind an alias.
// The second result reports whether the provider offers it at all, which is what
// tells a caller whether a stored workspace model may be handed to that provider.
func ProviderModelFor(p ProviderConfig, model string) (string, bool) {
	if model == "" {
		return "", false
	}
	if p.Model == model {
		return p.Model, true
	}
	for _, m := range p.Models {
		if m.Name == model {
			return m.Name, true
		}
	}
	for _, m := range p.Models {
		if m.Name != "" && m.Alias == model {
			return m.Name, true
		}
	}
	return "", false
}
