package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

const codexRolloutTailBytes int64 = 1 << 20
const codexContextBaselineTokens = 12000

type codexTokenUsage struct {
	TotalTokens           int `json:"totalTokens"`
	InputTokens           int `json:"inputTokens"`
	CachedInputTokens     int `json:"cachedInputTokens"`
	OutputTokens          int `json:"outputTokens"`
	ReasoningOutputTokens int `json:"reasoningOutputTokens"`
}

type codexSnakeTokenUsage struct {
	TotalTokens           int `json:"total_tokens"`
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

type appServerThreadTokenUsageNotification struct {
	ThreadID   string `json:"threadId"`
	TurnID     string `json:"turnId"`
	TokenUsage struct {
		Total              codexTokenUsage `json:"total"`
		Last               codexTokenUsage `json:"last"`
		ModelContextWindow int             `json:"modelContextWindow"`
	} `json:"tokenUsage"`
}

func mapAppServerTokenUsage(notif appServerThreadTokenUsageNotification) *core.ContextUsage {
	return contextUsageFromCamel(notif.TokenUsage.Last, notif.TokenUsage.ModelContextWindow)
}

func contextUsageFromCamel(usage codexTokenUsage, contextWindow int) *core.ContextUsage {
	return contextUsageFromParts(
		currentContextTokens(usage.TotalTokens, usage.InputTokens, usage.OutputTokens),
		usage.TotalTokens,
		usage.InputTokens,
		usage.CachedInputTokens,
		usage.OutputTokens,
		usage.ReasoningOutputTokens,
		contextWindow,
	)
}

func contextUsageFromSnake(usage codexSnakeTokenUsage, contextWindow int) *core.ContextUsage {
	return contextUsageFromParts(
		currentContextTokens(usage.TotalTokens, usage.InputTokens, usage.OutputTokens),
		usage.TotalTokens,
		usage.InputTokens,
		usage.CachedInputTokens,
		usage.OutputTokens,
		usage.ReasoningOutputTokens,
		contextWindow,
	)
}

func currentContextTokens(totalTokens, inputTokens, outputTokens int) int {
	if totalTokens > 0 {
		return totalTokens
	}
	if inputTokens > 0 || outputTokens > 0 {
		return inputTokens + outputTokens
	}
	return 0
}

func contextUsageFromParts(usedTokens, totalTokens, inputTokens, cachedInputTokens, outputTokens, reasoningOutputTokens, contextWindow int) *core.ContextUsage {
	if totalTokens <= 0 && inputTokens <= 0 && outputTokens <= 0 {
		return nil
	}
	if contextWindow <= 0 {
		return nil
	}
	return &core.ContextUsage{
		UsedTokens:            usedTokens,
		BaselineTokens:        codexContextBaselineTokens,
		TotalTokens:           totalTokens,
		InputTokens:           inputTokens,
		CachedInputTokens:     cachedInputTokens,
		OutputTokens:          outputTokens,
		ReasoningOutputTokens: reasoningOutputTokens,
		ContextWindow:         contextWindow,
	}
}

func cloneContextUsage(usage *core.ContextUsage) *core.ContextUsage {
	if usage == nil {
		return nil
	}
	cloned := *usage
	return &cloned
}

func loadContextUsageFromRollout(extraEnv []string, sessionID, cachedPath string) (*core.ContextUsage, string, error) {
	path := strings.TrimSpace(cachedPath)
	if path != "" {
		usage, err := readContextUsageFromRollout(path)
		if err == nil && usage != nil {
			return usage, path, nil
		}
	}

	codexHome, err := resolveCodexHome(extraEnv)
	if err != nil {
		return nil, "", err
	}
	path = findSessionFileInCodexHome(codexHome, sessionID)
	if path == "" {
		return nil, "", fmt.Errorf("session file not found for %s", sessionID)
	}
	usage, err := readContextUsageFromRollout(path)
	if err != nil {
		return nil, path, err
	}
	if usage == nil {
		return nil, path, fmt.Errorf("context usage not found in rollout")
	}
	return usage, path, nil
}

func resolveCodexHome(extraEnv []string) (string, error) {
	if value := getenvFromList(extraEnv, "CODEX_HOME"); value != "" {
		return strings.TrimSpace(value), nil
	}
	if value := strings.TrimSpace(os.Getenv("CODEX_HOME")); value != "" {
		return value, nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(homeDir, ".codex"), nil
}

func getenvFromList(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		entry := env[i]
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(entry, prefix))
		}
	}
	return ""
}

func findSessionFileInCodexHome(codexHome, sessionID string) string {
	if strings.TrimSpace(codexHome) == "" || strings.TrimSpace(sessionID) == "" {
		return ""
	}

	pattern := filepath.Join(codexHome, "sessions", "*", "*", "*", "rollout-*"+sessionID+".jsonl")
	if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
		sort.Strings(matches)
		return matches[len(matches)-1]
	}

	sessionsDir := filepath.Join(codexHome, "sessions")
	var found string
	_ = filepath.Walk(sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() || found != "" {
			return nil
		}
		if strings.Contains(filepath.Base(path), sessionID) {
			found = path
		}
		return nil
	})
	return found
}

func readContextUsageFromRollout(path string) (*core.ContextUsage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if usage, err := readContextUsageFromRolloutTail(f); err != nil {
		return nil, err
	} else if usage != nil {
		return usage, nil
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return scanContextUsageFromRollout(f)
}

func readContextUsageFromRolloutTail(f *os.File) (*core.ContextUsage, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() <= 0 {
		return nil, nil
	}

	start := int64(0)
	if info.Size() > codexRolloutTailBytes {
		start = info.Size() - codexRolloutTailBytes
	}
	buf := make([]byte, int(info.Size()-start))
	n, err := f.ReadAt(buf, start)
	if err != nil && err != io.EOF {
		return nil, err
	}
	buf = buf[:n]
	if start > 0 {
		if idx := bytes.IndexByte(buf, '\n'); idx >= 0 {
			buf = buf[idx+1:]
		}
	}
	return parseContextUsageFromRolloutBytes(buf), nil
}

func parseContextUsageFromRolloutBytes(data []byte) *core.ContextUsage {
	lines := bytes.Split(data, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		if usage := parseContextUsageFromRolloutLine(line); usage != nil {
			return usage
		}
	}
	return nil
}

func scanContextUsageFromRollout(r io.Reader) (*core.ContextUsage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

	var last *core.ContextUsage
	for scanner.Scan() {
		if usage := parseContextUsageFromRolloutLine(scanner.Bytes()); usage != nil {
			last = usage
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return last, nil
}

func parseContextUsageFromRolloutLine(line []byte) *core.ContextUsage {
	var entry struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(line, &entry); err != nil {
		return nil
	}
	if entry.Type != "event_msg" {
		return nil
	}

	var payload struct {
		Type string `json:"type"`
		Info *struct {
			TotalTokenUsage    codexSnakeTokenUsage `json:"total_token_usage"`
			LastTokenUsage     codexSnakeTokenUsage `json:"last_token_usage"`
			ModelContextWindow int                  `json:"model_context_window"`
		} `json:"info"`
	}
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return nil
	}
	if payload.Type != "token_count" || payload.Info == nil {
		return nil
	}
	return contextUsageFromSnake(payload.Info.LastTokenUsage, payload.Info.ModelContextWindow)
}
