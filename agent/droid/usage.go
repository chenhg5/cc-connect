package droid

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// GetUsage reports persisted token usage for the latest adapter-started session.
// Until an explicit resume ID or a stream event identifies that session, usage is
// unavailable; choosing the newest file could select an old /status or /cost probe.
func (a *Agent) GetUsage(ctx context.Context) (*core.UsageReport, error) {
	a.mu.RLock()
	homeDir, err := droidHomeDir(a.sessionEnvLocked())
	workDir, workErr := filepath.Abs(a.workDir)
	sessionID := a.usageSessionID
	if homeDir != a.usageHomeDir || workDir != a.usageWorkDir {
		sessionID = ""
	}
	a.mu.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("droid usage unavailable: %w", err)
	}
	if workErr != nil {
		return nil, fmt.Errorf("droid usage unavailable: resolve workspace: %w", workErr)
	}
	return readDroidSessionUsage(ctx, homeDir, workDir, sessionID)
}

func readDroidSessionUsage(ctx context.Context, homeDir, workDir, sessionID string) (*core.UsageReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if homeDir == "" || sessionID == "" {
		return nil, fmt.Errorf("droid usage unavailable: start or resume a conversation first")
	}
	path, err := findDroidSessionFileFromBase(filepath.Join(homeDir, ".factory", "sessions"), workDir, sessionID)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, fmt.Errorf("droid usage unavailable: conversation has no persisted session")
	}
	data, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".settings.json")
	if err != nil {
		return nil, fmt.Errorf("droid usage unavailable: read conversation settings: %w", err)
	}
	model, summary := parseTokenUsageSettings(data)
	if summary == "" {
		return nil, fmt.Errorf("droid usage unavailable: no tokenUsage in conversation settings")
	}
	report := &core.UsageReport{Provider: "droid", AccountID: "droid", Plan: summary}
	if model != "" {
		report.Plan = model + " | " + summary
	}
	return report, nil
}

func parseTokenUsageSettings(data []byte) (model, summary string) {
	var cfg struct {
		Model      string `json:"model"`
		TokenUsage struct {
			InputTokens         int `json:"inputTokens"`
			OutputTokens        int `json:"outputTokens"`
			CacheCreationTokens int `json:"cacheCreationTokens"`
			CacheReadTokens     int `json:"cacheReadTokens"`
			ThinkingTokens      int `json:"thinkingTokens"`
		} `json:"tokenUsage"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", ""
	}

	parts := make([]string, 0, 5)
	if cfg.TokenUsage.InputTokens > 0 {
		parts = append(parts, fmt.Sprintf("input %d", cfg.TokenUsage.InputTokens))
	}
	if cfg.TokenUsage.OutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("output %d", cfg.TokenUsage.OutputTokens))
	}
	if cfg.TokenUsage.ThinkingTokens > 0 {
		parts = append(parts, fmt.Sprintf("thinking %d", cfg.TokenUsage.ThinkingTokens))
	}
	if cfg.TokenUsage.CacheReadTokens > 0 {
		parts = append(parts, fmt.Sprintf("cache_read %d", cfg.TokenUsage.CacheReadTokens))
	}
	if cfg.TokenUsage.CacheCreationTokens > 0 {
		parts = append(parts, fmt.Sprintf("cache_create %d", cfg.TokenUsage.CacheCreationTokens))
	}

	return strings.TrimSpace(cfg.Model), strings.Join(parts, ", ")
}
