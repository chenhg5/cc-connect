package core

// GetProviderModels returns the configured model options for the active provider.
func GetProviderModels(providers []ProviderConfig, activeIdx int) []ModelOption {
	if activeIdx < 0 || activeIdx >= len(providers) {
		return nil
	}
	return providers[activeIdx].Models
}
