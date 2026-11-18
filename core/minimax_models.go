package core

import (
	"net/url"
	"strings"
)

// GetMiniMaxProviderModels supplies ordinary choices for verified direct MiniMax
// endpoints before external agent discovery can replace them. Explicit lists
// remain authoritative; the selected provider model is never changed.
// A nil result leaves other providers and unverified gateways on their usual path.
func GetMiniMaxProviderModels(providers []ProviderConfig, activeIdx int) []ModelOption {
	if activeIdx < 0 || activeIdx >= len(providers) {
		return nil
	}
	p := providers[activeIdx]
	u, err := url.Parse(p.BaseURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil
	}
	if u.Host != "api.minimax.io" && u.Host != "api.minimaxi.com" {
		return nil
	}
	switch strings.TrimRight(u.Path, "/") {
	case "", "/v1", "/anthropic":
	default:
		return nil
	}
	if len(p.Models) > 0 {
		return append([]ModelOption(nil), p.Models...)
	}
	return []ModelOption{{Name: "MiniMax-M3.1-Flash"}, {Name: "MiniMax-M3"}}
}
