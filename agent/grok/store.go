package grok

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type grokSessionSummary struct {
	Info struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	} `json:"info"`
	SessionSummary  string `json:"session_summary"`
	GeneratedTitle  string `json:"generated_title"`
	NumMessages     int    `json:"num_messages"`
	NumChatMessages int    `json:"num_chat_messages"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	LastActiveAt    string `json:"last_active_at"`
}

type grokSessionRecord struct {
	dir  string
	info core.AgentSessionInfo
}

// canonicalizeGrokModelID maps wire / usage keys onto models_cache.json ids.
// Grok headless emits message.model="grok-4.6" but modelUsage keys like
// "grok-4.6-build"; both must resolve to the same cache entry.
func canonicalizeGrokModelID(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if strings.HasSuffix(model, "-build") {
		return strings.TrimSuffix(model, "-build")
	}
	return model
}

func grokModelContextWindow(processEnv []string, workDir, model string) int {
	model = canonicalizeGrokModelID(model)
	if model == "" {
		return 0
	}
	data, err := os.ReadFile(filepath.Join(resolveGrokHome(processEnv, workDir), "models_cache.json"))
	if err != nil {
		return 0
	}
	var cache struct {
		Models map[string]struct {
			Info struct {
				ContextWindow int `json:"context_window"`
			} `json:"info"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &cache) != nil {
		return 0
	}
	if entry, ok := cache.Models[model]; ok {
		return entry.Info.ContextWindow
	}
	// Fall back to build-suffixed key if cache ever stores that form.
	if entry, ok := cache.Models[model+"-build"]; ok {
		return entry.Info.ContextWindow
	}
	return 0
}

func grokModelReasoningEfforts(effectiveEnv []string, workDir, model string) []string {
	data, err := os.ReadFile(filepath.Join(resolveGrokHome(effectiveEnv, workDir), "models_cache.json"))
	if err != nil {
		return nil
	}
	var cache struct {
		Models map[string]struct {
			Info struct {
				ReasoningEfforts []struct {
					ID string `json:"id"`
				} `json:"reasoning_efforts"`
			} `json:"info"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &cache) != nil {
		return nil
	}

	model = canonicalizeGrokModelID(model)
	if model == "" && len(cache.Models) == 1 {
		for onlyModel := range cache.Models {
			model = canonicalizeGrokModelID(onlyModel)
		}
	}
	entry, ok := cache.Models[model]
	if !ok {
		entry, ok = cache.Models[model+"-build"]
	}
	if !ok {
		return nil
	}
	seen := make(map[string]bool)
	efforts := make([]string, 0, len(entry.Info.ReasoningEfforts))
	for _, option := range entry.Info.ReasoningEfforts {
		id := normalizeReasoningEffort(option.ID)
		key := strings.ToLower(id)
		if id == "" || seen[key] {
			continue
		}
		seen[key] = true
		efforts = append(efforts, id)
	}
	return efforts
}

// resolveGrokHome mirrors the environment seen by a launched Grok process.
// An explicitly empty GROK_HOME clears an inherited value and falls back to
// HOME. Relative state directories are resolved from the process work_dir.
func resolveGrokHome(effectiveEnv []string, workDir string) string {
	processEnv := grokProcessEnv(effectiveEnv)
	if value, ok := grokEnvValue(processEnv, "GROK_HOME"); ok {
		if value = strings.TrimSpace(value); value != "" {
			return resolveGrokPath(value, workDir)
		}
	}

	homeDir := grokUserHome(processEnv)
	if homeDir == "" {
		return ""
	}
	return resolveGrokPath(filepath.Join(homeDir, ".grok"), workDir)
}

func grokProcessEnv(effectiveEnv []string) []string {
	return mergeGrokEnv(os.Environ(), effectiveEnv)
}

// mergeGrokEnv makes the last assignment authoritative while preserving the
// remaining entries' order. Grok's layered config, provider, and per-session
// env need one unambiguous value per key.
func mergeGrokEnv(layers ...[]string) []string {
	var flattened []string
	for _, layer := range layers {
		flattened = append(flattened, layer...)
	}
	last := make(map[string]int, len(flattened))
	for i, pair := range flattened {
		if key, _, ok := strings.Cut(pair, "="); ok {
			last[grokEnvKey(key)] = i
		}
	}
	env := make([]string, 0, len(flattened))
	for i, pair := range flattened {
		key, _, ok := strings.Cut(pair, "=")
		if !ok || last[grokEnvKey(key)] == i {
			env = append(env, pair)
		}
	}
	return env
}

func grokEnvValue(env []string, name string) (string, bool) {
	name = grokEnvKey(name)
	for i := len(env) - 1; i >= 0; i-- {
		key, value, ok := strings.Cut(env[i], "=")
		if ok && grokEnvKey(key) == name {
			return value, true
		}
	}
	return "", false
}

func grokEnvKey(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}
	return key
}

func grokUserHome(processEnv []string) string {
	if home, ok := grokEnvValue(processEnv, "HOME"); ok && strings.TrimSpace(home) != "" {
		return strings.TrimSpace(home)
	}
	if home, ok := grokEnvValue(processEnv, "USERPROFILE"); ok && strings.TrimSpace(home) != "" {
		return strings.TrimSpace(home)
	}
	drive, _ := grokEnvValue(processEnv, "HOMEDRIVE")
	path, _ := grokEnvValue(processEnv, "HOMEPATH")
	if home := strings.TrimSpace(drive + path); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return strings.TrimSpace(home)
}

func resolveGrokPath(path, workDir string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if filepath.IsAbs(path) {
		return path
	}
	if strings.TrimSpace(workDir) == "" {
		if cwd, err := os.Getwd(); err == nil {
			workDir = cwd
		}
	}
	if absWorkDir, err := filepath.Abs(workDir); err == nil {
		workDir = absWorkDir
	}
	return filepath.Clean(filepath.Join(workDir, path))
}

func listGrokSessions(grokHome, workDir string) ([]core.AgentSessionInfo, error) {
	records, err := scanGrokSessions(grokHome, workDir)
	if err != nil {
		return nil, err
	}
	sessions := make([]core.AgentSessionInfo, len(records))
	for i := range records {
		sessions[i] = records[i].info
	}
	return sessions, nil
}

// findGrokSessionDir deliberately scans summaries instead of trusting a
// workspace slug. Grok truncates long slugs and appends a hash, while macOS
// may record a symlink-resolved cwd; summary.info.cwd remains authoritative.
func findGrokSessionDir(grokHome, workDir, sessionID string) string {
	if strings.TrimSpace(sessionID) == "" {
		return ""
	}
	records, err := scanGrokSessions(grokHome, workDir)
	if err != nil {
		return ""
	}
	for _, record := range records {
		if record.info.ID == sessionID {
			return record.dir
		}
	}
	return ""
}

func scanGrokSessions(grokHome, workDir string) ([]grokSessionRecord, error) {
	targetCWD, err := canonicalGrokWorkDir(workDir)
	if err != nil {
		return nil, fmt.Errorf("grok: resolve work_dir %q: %w", workDir, err)
	}
	if strings.TrimSpace(grokHome) == "" {
		grokHome = resolveGrokHome(nil, workDir)
	}
	if grokHome == "" {
		return nil, nil
	}

	sessionsDir := filepath.Join(grokHome, "sessions")
	groups, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("grok: read sessions directory: %w", err)
	}

	byID := make(map[string]grokSessionRecord)
	for _, group := range groups {
		if !group.IsDir() {
			continue
		}
		groupDir := filepath.Join(sessionsDir, group.Name())
		groupCWD := grokGroupWorkDir(groupDir, group.Name())
		entries, err := os.ReadDir(groupDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			record, ok := readGrokSessionRecord(filepath.Join(groupDir, entry.Name()), groupCWD, targetCWD)
			if !ok {
				continue
			}
			previous, exists := byID[record.info.ID]
			if !exists || record.info.ModifiedAt.After(previous.info.ModifiedAt) {
				byID[record.info.ID] = record
			}
		}
	}

	records := make([]grokSessionRecord, 0, len(byID))
	for _, record := range byID {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].info.ModifiedAt.Equal(records[j].info.ModifiedAt) {
			return records[i].info.ID < records[j].info.ID
		}
		return records[i].info.ModifiedAt.After(records[j].info.ModifiedAt)
	})
	return records, nil
}

func readGrokSessionRecord(sessionDir, groupCWD, targetCWD string) (grokSessionRecord, bool) {
	summaryPath := filepath.Join(sessionDir, "summary.json")
	data, err := os.ReadFile(summaryPath)
	if err != nil {
		return grokSessionRecord{}, false
	}
	var summary grokSessionSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return grokSessionRecord{}, false
	}

	sessionCWD := strings.TrimSpace(summary.Info.Cwd)
	if sessionCWD == "" {
		sessionCWD = groupCWD
	}
	canonicalCWD, err := canonicalGrokWorkDir(sessionCWD)
	if err != nil || !sameGrokWorkDir(canonicalCWD, targetCWD) {
		return grokSessionRecord{}, false
	}

	sessionID := strings.TrimSpace(summary.Info.ID)
	if sessionID == "" {
		sessionID = filepath.Base(sessionDir)
	}
	summaryText := strings.TrimSpace(summary.SessionSummary)
	if summaryText == "" {
		summaryText = strings.TrimSpace(summary.GeneratedTitle)
	}
	if runes := []rune(summaryText); len(runes) > 60 {
		summaryText = string(runes[:60]) + "..."
	}

	messageCount := summary.NumChatMessages
	if messageCount == 0 {
		messageCount = summary.NumMessages
	}
	modifiedAt := firstGrokTime(summary.UpdatedAt, summary.LastActiveAt, summary.CreatedAt)
	if modifiedAt.IsZero() {
		if info, err := os.Stat(summaryPath); err == nil {
			modifiedAt = info.ModTime()
		}
	}

	return grokSessionRecord{
		dir: sessionDir,
		info: core.AgentSessionInfo{
			ID:           sessionID,
			Summary:      summaryText,
			MessageCount: messageCount,
			ModifiedAt:   modifiedAt,
		},
	}, true
}

func grokGroupWorkDir(groupDir, groupName string) string {
	if data, err := os.ReadFile(filepath.Join(groupDir, ".cwd")); err == nil {
		if cwd := strings.TrimSpace(string(data)); cwd != "" {
			return cwd
		}
	}
	decoded, err := url.PathUnescape(groupName)
	if err == nil && filepath.IsAbs(decoded) {
		return decoded
	}
	return ""
}

func canonicalGrokWorkDir(workDir string) (string, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return "", fmt.Errorf("work directory is empty")
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	return abs, nil
}

func sameGrokWorkDir(left, right string) bool {
	if left == right {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func firstGrokTime(values ...string) time.Time {
	for _, value := range values {
		if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// getGrokSessionHistory reads only the session update stream. It never opens
// Grok configuration or authentication files.
func getGrokSessionHistory(grokHome, workDir, sessionID string, limit int) ([]core.HistoryEntry, error) {
	sessionDir := findGrokSessionDir(grokHome, workDir, sessionID)
	if sessionDir == "" {
		return nil, fmt.Errorf("grok: session %q not found in work_dir %q", sessionID, workDir)
	}

	file, err := os.Open(filepath.Join(sessionDir, "updates.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("grok: open session history: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			slog.Warn("grok: close session history", "error", err)
		}
	}()

	var entries []core.HistoryEntry
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if entry, ok := parseGrokHistoryLine(line); ok {
				last := len(entries) - 1
				if last >= 0 && entries[last].Role == entry.Role {
					entries[last].Content += entry.Content
				} else {
					entries = append(entries, entry)
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, fmt.Errorf("grok: read session history: %w", readErr)
		}
	}

	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

func parseGrokHistoryLine(line []byte) (core.HistoryEntry, bool) {
	var envelope struct {
		Timestamp json.RawMessage `json:"timestamp"`
		Params    struct {
			Update struct {
				SessionUpdate string `json:"sessionUpdate"`
				Content       struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		} `json:"params"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return core.HistoryEntry{}, false
	}
	if envelope.Params.Update.Content.Type != "text" || envelope.Params.Update.Content.Text == "" {
		return core.HistoryEntry{}, false
	}

	role := ""
	switch envelope.Params.Update.SessionUpdate {
	case "user_message_chunk":
		role = "user"
	case "agent_message_chunk":
		role = "assistant"
	default:
		return core.HistoryEntry{}, false
	}
	return core.HistoryEntry{
		Role:      role,
		Content:   envelope.Params.Update.Content.Text,
		Timestamp: parseGrokHistoryTimestamp(envelope.Timestamp),
	}, true
}

func parseGrokHistoryTimestamp(raw json.RawMessage) time.Time {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return time.Time{}
	}
	if raw[0] == '"' {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return time.Time{}
		}
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed
		}
		raw = []byte(value)
	}
	value, err := strconv.ParseFloat(string(raw), 64)
	if err != nil {
		return time.Time{}
	}
	// Current Grok writes Unix seconds, but accept millisecond and
	// microsecond timestamps from older/future builds as well.
	for value >= 1e12 {
		value /= 1000
	}
	seconds := int64(value)
	nanos := int64((value - float64(seconds)) * float64(time.Second))
	return time.Unix(seconds, nanos)
}
