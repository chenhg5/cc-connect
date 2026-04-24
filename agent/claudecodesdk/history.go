package claudecodesdk

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

// ListSessions returns sessions stored by Claude Code in ~/.claude/projects/.
func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("claudecodesdk: cannot determine home dir: %w", err)
	}

	absWorkDir, err := filepath.Abs(a.workDir)
	if err != nil {
		return nil, fmt.Errorf("claudecodesdk: resolve work_dir: %w", err)
	}

	projectDir := findProjectDir(homeDir, absWorkDir)
	if projectDir == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("claudecodesdk: read project dir: %w", err)
	}

	var sessions []core.AgentSessionInfo
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}

		sessionID := strings.TrimSuffix(name, ".jsonl")
		info, err := entry.Info()
		if err != nil {
			continue
		}

		summary, msgCount := scanSessionMeta(filepath.Join(projectDir, name))

		sessions = append(sessions, core.AgentSessionInfo{
			ID:           sessionID,
			Summary:      summary,
			MessageCount: msgCount,
			ModifiedAt:   info.ModTime(),
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}

// DeleteSession implements core.SessionDeleter.
func (a *Agent) DeleteSession(_ context.Context, sessionID string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("claudecodesdk: cannot determine home dir: %w", err)
	}
	absWorkDir, err := filepath.Abs(a.workDir)
	if err != nil {
		return fmt.Errorf("claudecodesdk: resolve work_dir: %w", err)
	}
	projectDir := findProjectDir(homeDir, absWorkDir)
	if projectDir == "" {
		return fmt.Errorf("claudecodesdk: session not found")
	}
	path := filepath.Join(projectDir, sessionID+".jsonl")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("claudecodesdk: session file not found: %s", sessionID)
	}
	return os.Remove(path)
}

// GetSessionHistory reads the Claude Code JSONL transcript and returns user/assistant messages.
func (a *Agent) GetSessionHistory(_ context.Context, sessionID string, limit int) ([]core.HistoryEntry, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	absWorkDir, _ := filepath.Abs(a.workDir)
	projectDir := findProjectDir(homeDir, absWorkDir)
	if projectDir == "" {
		return nil, fmt.Errorf("claudecodesdk: project dir not found")
	}

	path := filepath.Join(projectDir, sessionID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("claudecodesdk: open session file: %w", err)
	}
	defer f.Close()

	var entries []core.HistoryEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		var raw struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &raw) != nil {
			continue
		}
		if raw.Type != "user" && raw.Type != "assistant" {
			continue
		}

		ts, _ := time.Parse(time.RFC3339Nano, raw.Timestamp)
		text := extractTextContent(raw.Message.Content)
		if text == "" {
			continue
		}

		entries = append(entries, core.HistoryEntry{
			Role:      raw.Type,
			Content:   text,
			Timestamp: ts,
		})
	}

	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

// scanSessionMeta reads a JSONL session file to extract the first user message
// as a summary and counts the total user+assistant messages.
func scanSessionMeta(path string) (string, int) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	var summary string
	var count int

	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Type == "user" || entry.Type == "assistant" {
			count++
			if entry.Type == "user" && entry.Message.Content != "" {
				summary = entry.Message.Content
			}
		}
	}
	summary = stripXMLTags(summary)
	summary = strings.TrimSpace(summary)
	if utf8.RuneCountInString(summary) > 40 {
		summary = string([]rune(summary)[:40]) + "..."
	}
	return summary, count
}

// extractTextContent extracts readable text from Claude Code message content.
// Content can be a plain string or an array of content blocks.
func extractTextContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try plain string first
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	// Try array of content blocks
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}

	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			return b.Text
		}
	}
	return ""
}

var xmlTagRe = regexp.MustCompile(`<[^>]+>`)

func stripXMLTags(s string) string {
	return xmlTagRe.ReplaceAllString(s, "")
}

// encodeClaudeProjectKey encodes an absolute path the same way Claude Code does
// for its project directory key:
//  1. Replace slashes (/), colons (:), and underscores (_) with "-"
//  2. Replace non-ASCII characters with "-"
func encodeClaudeProjectKey(absPath string) string {
	normalized := strings.ReplaceAll(absPath, "\\", "/")
	var result strings.Builder
	for _, r := range normalized {
		if r == '/' || r == ':' || r == '_' {
			result.WriteRune('-')
		} else if r < 128 {
			result.WriteRune(r)
		} else {
			result.WriteRune('-')
		}
	}
	return result.String()
}

// findProjectDir locates the Claude Code session directory for a given work dir.
// Claude Code stores sessions at ~/.claude/projects/{projectKey}/.
func findProjectDir(homeDir, absWorkDir string) string {
	projectsBase := filepath.Join(homeDir, ".claude", "projects")

	candidates := []string{
		encodeClaudeProjectKey(absWorkDir),
		strings.ReplaceAll(absWorkDir, string(filepath.Separator), "-"),
		strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(absWorkDir),
		strings.NewReplacer("/", "-", "\\", "-", ":", "-", "_", "-").Replace(absWorkDir),
	}
	fwd := strings.ReplaceAll(absWorkDir, "\\", "/")
	candidates = append(candidates, strings.ReplaceAll(fwd, "/", "-"))

	for _, key := range candidates {
		dir := filepath.Join(projectsBase, key)
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
	}

	// Fallback: scan projects directory for a match.
	entries, err := os.ReadDir(projectsBase)
	if err != nil {
		return ""
	}

	encodedWorkDir := encodeClaudeProjectKey(absWorkDir)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if entry.Name() == encodedWorkDir || strings.EqualFold(entry.Name(), encodedWorkDir) {
			return filepath.Join(projectsBase, entry.Name())
		}
	}

	slog.Debug("claudecodesdk: project dir not found", "workDir", absWorkDir)
	return ""
}
