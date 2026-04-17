package core

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	defaultPresetsURL         = "https://raw.githubusercontent.com/chenhg5/cc-connect/main/provider-presets.json"
	fallbackPresetsURL        = "https://gitee.com/chenhg5/cc-connect/raw/main/provider-presets.json"
	presetsCacheTTL           = 6 * time.Hour
	presetsHTTPTimeout        = 15 * time.Second
	presetsFallbackHTTPTimeout = 10 * time.Second
)

// ProviderPreset describes a recommended provider available from the remote presets list.
type ProviderPreset struct {
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name"`
	BaseURL     string            `json:"base_url"`                // default base URL
	Endpoints   map[string]string `json:"endpoints,omitempty"`     // per-agent-type base URL overrides (e.g. {"claudecode": "https://x/anthropic", "codex": "https://x/v1"})
	Models      []string          `json:"models,omitempty"`        // common models across agents
	AgentModels map[string]string `json:"agent_models,omitempty"`  // per-agent-type default model (e.g. {"codex": "gpt-5.4"})
	Agents      []string          `json:"agents,omitempty"`        // supported agent types
	InviteURL   string            `json:"invite_url,omitempty"`
	Description string            `json:"description,omitempty"`
	DescriptionZh string          `json:"description_zh,omitempty"`
	Features    []string          `json:"features,omitempty"`
	Thinking    string            `json:"thinking,omitempty"`
	Tier        int               `json:"tier"`                    // sponsor ranking: lower = higher priority
	Featured    bool              `json:"featured,omitempty"`      // show a star badge when true
	Website     string            `json:"website,omitempty"`
}

// ProviderPresetsResponse is the top-level JSON schema for remote presets.
type ProviderPresetsResponse struct {
	Version   int              `json:"version"`
	UpdatedAt string           `json:"updated_at,omitempty"`
	Providers []ProviderPreset `json:"providers"`
}

type presetsCache struct {
	mu        sync.RWMutex
	data      *ProviderPresetsResponse
	fetchedAt time.Time
	url       string
}

var globalPresetsCache = &presetsCache{}

// SetPresetsURL overrides the default presets URL. Call before first fetch.
func SetPresetsURL(url string) {
	globalPresetsCache.mu.Lock()
	defer globalPresetsCache.mu.Unlock()
	globalPresetsCache.url = url
	globalPresetsCache.data = nil // invalidate cache on URL change
}

// FetchProviderPresets returns cached or freshly-fetched provider presets.
func FetchProviderPresets() (*ProviderPresetsResponse, error) {
	return globalPresetsCache.fetch()
}

func (c *presetsCache) fetch() (*ProviderPresetsResponse, error) {
	c.mu.RLock()
	if c.data != nil && time.Since(c.fetchedAt) < presetsCacheTTL {
		defer c.mu.RUnlock()
		return c.data, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// double-check after acquiring write lock
	if c.data != nil && time.Since(c.fetchedAt) < presetsCacheTTL {
		return c.data, nil
	}

	primaryURL := c.url
	if primaryURL == "" {
		primaryURL = defaultPresetsURL
	}

	result, err := fetchPresetsFromURL(primaryURL, presetsHTTPTimeout)
	if err != nil {
		slog.Warn("primary presets fetch failed, trying fallback", "url", primaryURL, "error", err)
		result, err = fetchPresetsFromURL(fallbackPresetsURL, presetsFallbackHTTPTimeout)
	}
	if err != nil {
		if c.data != nil {
			slog.Warn("all presets sources failed, using stale cache", "error", err)
			return c.data, nil
		}
		return nil, fmt.Errorf("fetch presets: %w", err)
	}

	c.data = result
	c.fetchedAt = time.Now()
	return c.data, nil
}

func fetchPresetsFromURL(url string, timeout time.Duration) (*ProviderPresetsResponse, error) {
	slog.Debug("fetching provider presets", "url", url)
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP GET %s: status %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read body from %s: %w", url, err)
	}

	var result ProviderPresetsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse JSON from %s: %w", url, err)
	}
	return &result, nil
}
