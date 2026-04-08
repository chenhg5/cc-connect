package core

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
