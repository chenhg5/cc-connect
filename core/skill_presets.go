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
	defaultSkillPresetsURL         = "https://raw.githubusercontent.com/chenhg5/cc-connect/main/skill-presets.json"
	fallbackSkillPresetsURL        = "https://gitee.com/chenhg5/cc-connect/raw/main/skill-presets.json"
	skillPresetsCacheTTL           = 6 * time.Hour
	skillPresetsHTTPTimeout        = 15 * time.Second
	skillPresetsFallbackHTTPTimeout = 10 * time.Second
)

// SkillPreset describes a recommended skill available from the remote presets list.
type SkillPreset struct {
	Name          string        `json:"name"`
	DisplayName   string        `json:"display_name"`
	Description   string        `json:"description,omitempty"`
	DescriptionZh string        `json:"description_zh,omitempty"`
	Version       string        `json:"version,omitempty"`
	Author        string        `json:"author,omitempty"`
	URL           string        `json:"url,omitempty"`
	AgentTypes    []string      `json:"agent_types,omitempty"`
	Tags          []string      `json:"tags,omitempty"`
	Featured      bool          `json:"featured,omitempty"`
	Source        *SkillSource  `json:"source,omitempty"`
	Pricing       *SkillPricing `json:"pricing,omitempty"`
}

// SkillSource describes where the skill is hosted / provided from.
type SkillSource struct {
	Provider string `json:"provider"`           // e.g. "github", "skills.sh", "npm"
	Name     string `json:"name,omitempty"`      // display name, e.g. "GitHub", "Skills.sh"
	URL      string `json:"url,omitempty"`        // provider home page
}

// SkillPricing describes the pricing model for a skill.
type SkillPricing struct {
	Type     string  `json:"type"`               // "free", "paid", "freemium"
	Price    float64 `json:"price,omitempty"`     // 0 for free
	Currency string  `json:"currency,omitempty"`  // "USD", "CNY", etc.
}

// SkillPresetsResponse is the top-level JSON schema for remote skill presets.
type SkillPresetsResponse struct {
	Version   int           `json:"version"`
	UpdatedAt string        `json:"updated_at,omitempty"`
	Skills    []SkillPreset `json:"skills"`
}

type skillPresetsCache struct {
	mu        sync.RWMutex
	data      *SkillPresetsResponse
	fetchedAt time.Time
	url       string
}

var globalSkillPresetsCache = &skillPresetsCache{}

// SetSkillPresetsURL overrides the default skill presets URL.
func SetSkillPresetsURL(url string) {
	globalSkillPresetsCache.mu.Lock()
	defer globalSkillPresetsCache.mu.Unlock()
	globalSkillPresetsCache.url = url
	globalSkillPresetsCache.data = nil
}

// FetchSkillPresets returns cached or freshly-fetched skill presets.
func FetchSkillPresets() (*SkillPresetsResponse, error) {
	return globalSkillPresetsCache.fetch()
}

func (c *skillPresetsCache) fetch() (*SkillPresetsResponse, error) {
	c.mu.RLock()
	if c.data != nil && time.Since(c.fetchedAt) < skillPresetsCacheTTL {
		defer c.mu.RUnlock()
		return c.data, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.data != nil && time.Since(c.fetchedAt) < skillPresetsCacheTTL {
		return c.data, nil
	}

	primaryURL := c.url
	if primaryURL == "" {
		primaryURL = defaultSkillPresetsURL
	}

	result, err := fetchSkillPresetsFromURL(primaryURL, skillPresetsHTTPTimeout)
	if err != nil {
		slog.Warn("primary skill presets fetch failed, trying fallback", "url", primaryURL, "error", err)
		result, err = fetchSkillPresetsFromURL(fallbackSkillPresetsURL, skillPresetsFallbackHTTPTimeout)
	}
	if err != nil {
		if c.data != nil {
			slog.Warn("all skill presets sources failed, using stale cache", "error", err)
			return c.data, nil
		}
		return nil, fmt.Errorf("fetch skill presets: %w", err)
	}

	c.data = result
	c.fetchedAt = time.Now()
	return c.data, nil
}

func fetchSkillPresetsFromURL(url string, timeout time.Duration) (*SkillPresetsResponse, error) {
	slog.Debug("fetching skill presets", "url", url)
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

	var result SkillPresetsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse JSON from %s: %w", url, err)
	}
	return &result, nil
}
