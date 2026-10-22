//go:build integration

package integration

import (
	"strings"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func joinMsgContent(msgs []mockMessage) string {
	var parts []string
	for _, m := range msgs {
		parts = append(parts, m.Content)
	}
	return strings.Join(parts, "\n")
}

func configProviderToCore(p config.ProviderConfig) core.ProviderConfig {
	c := core.ProviderConfig{
		Name: p.Name, APIKey: p.APIKey, BaseURL: p.BaseURL,
		Model: p.Model, Thinking: p.Thinking, Env: p.Env,
	}
	for _, m := range p.Models {
		c.Models = append(c.Models, core.ModelOption{Name: m.Model, Alias: m.Alias})
	}
	if p.Codex != nil {
		c.CodexWireAPI = p.Codex.WireAPI
		c.CodexHTTPHeaders = p.Codex.HTTPHeaders
	}
	return c
}
