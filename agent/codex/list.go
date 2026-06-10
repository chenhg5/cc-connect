package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// resolveCodexHomeDir returns the effective CODEX_HOME directory.
// Priority: explicit config value > CODEX_HOME env > ~/.codex
func resolveCodexHomeDir(explicit string) string {
	if h := strings.TrimSpace(explicit); h != "" {
		return h
	}
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".codex")
}

// defaultInternalSummaryPrefixes is the built-in ignore list applied on top of
// any user-configured prefixes. It catches the common internal/system Codex
// prompts that get persisted as user-role content in the JSONL transcript
// (diagnosis bots, skill selectors, guardian reviews, etc.) so they don't
// pollute /list. See #1271.
var defaultInternalSummaryPrefixes = []string{
	"You are the final diagnosis layer",
	"You are the decision layer",
	"You are selecting one best skill",
	"You are a senior game backend engineer diagnosing",
	"The following is the Codex agent history whose request action you are assessing",
}

// effectiveIgnorePrefixes merges defaults with user-configured prefixes.
// User-configured entries are appended after the defaults; matching is
// case-sensitive, anchored at the start of the trimmed prompt text.
func effectiveIgnorePrefixes(userPrefixes []string) []string {
	out := make([]string, 0, len(defaultInternalSummaryPrefixes)+len(userPrefixes))
	out = append(out, defaultInternalSummaryPrefixes...)
	out = append(out, userPrefixes...)
	return out
}

// listCodexSessions scans the codex sessions directory for JSONL transcript
// files whose cwd matches workDir. Sessions whose first user prompt matches
// any prefix in ignorePrefixes (after the built-in defaults) are hidden.
func listCodexSessions(workDir, codexHome string, ignorePrefixes []string) ([]core.AgentSessionInfo, error) {
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		absWorkDir = workDir
	}

	sessionsDir := filepath.Join(resolveCodexHomeDir(codexHome), "sessions")

	var files []string
	_ = filepath.Walk(sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})

	if len(files) == 0 {
		return nil, nil
	}

	var sessions []core.AgentSessionInfo
	for _, f := range files {
		info := parseCodexSessionFile(f, absWorkDir, ignorePrefixes)
		if info != nil {
			patchSessionSource(info.ID, codexHome)
			sessions = append(sessions, *info)
		}
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}

// parseCodexSessionFile reads a Codex JSONL transcript.
// Returns nil if the session's cwd doesn't match filterCwd, or if the
// first user prompt matches one of ignorePrefixes (internal/system session).
func parseCodexSessionFile(path, filterCwd string, ignorePrefixes []string) *core.AgentSessionInfo {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil
	}

	var sessionID string
	var sessionCwd string
	var summary string
	var firstUserPrompt string
	var msgCount int
	userMsgSeen := 0

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var entry struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		switch entry.Type {
		case "session_meta":
			var meta struct {
				ID  string `json:"id"`
				Cwd string `json:"cwd"`
			}
			if json.Unmarshal(entry.Payload, &meta) == nil {
				sessionID = meta.ID
				sessionCwd = meta.Cwd
			}

		case "response_item":
			var item struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			if json.Unmarshal(entry.Payload, &item) == nil {
				if item.Role == "user" {
					userMsgSeen++
					msgCount++
					// Track the first user prompt (before filtering) so
					// we can detect internal/system sessions even when
					// the first message is the only message.
					if firstUserPrompt == "" {
						for _, c := range item.Content {
							if c.Type == "input_text" && c.Text != "" {
								firstUserPrompt = c.Text
								break
							}
						}
					}
					// The actual user prompt is the last user response_item
					// (earlier ones are system/AGENTS.md instructions).
					// Pick the last content block that looks like a real prompt.
					for _, c := range item.Content {
						if c.Type == "input_text" && c.Text != "" && isUserPrompt(c.Text, ignorePrefixes) {
							summary = c.Text
						}
					}
				} else if item.Role == "assistant" {
					msgCount++
				}
			}
		}
	}

	// Filter by cwd
	if filterCwd != "" && sessionCwd != "" && sessionCwd != filterCwd {
		return nil
	}

	if sessionID == "" {
		return nil
	}

	// Filter out internal/system-generated sessions whose first user
	// prompt matches any configured ignore prefix (built-in defaults +
	// user-configured). This catches single-prompt internal sessions
	// (e.g. "You are the decision layer for X") and prompts that match
	// a user-configured prefix. A session whose first prompt is
	// internal context but later turns to a real user request will
	// still be filtered — that matches the issue reporter's request
	// (#1271) and keeps /list focused on real user activity.
	prefixes := effectiveIgnorePrefixes(ignorePrefixes)
	if isInternalSummary(firstUserPrompt, prefixes) {
		return nil
	}

	if len([]rune(summary)) > 60 {
		summary = string([]rune(summary)[:60]) + "..."
	}

	return &core.AgentSessionInfo{
		ID:           sessionID,
		Summary:      summary,
		MessageCount: msgCount,
		ModifiedAt:   stat.ModTime(),
	}
}

// findSessionFile locates the JSONL transcript for a given session ID.
func findSessionFile(sessionID, codexHome string) string {
	sessionsDir := filepath.Join(resolveCodexHomeDir(codexHome), "sessions")

	var found string
	_ = filepath.Walk(sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found != "" {
			return nil
		}
		if strings.Contains(filepath.Base(path), sessionID) {
			found = path
		}
		return nil
	})
	return found
}

// getSessionHistory reads the JSONL transcript and returns user/assistant messages.
func getSessionHistory(sessionID, codexHome string, limit int, ignorePrefixes []string) ([]core.HistoryEntry, error) {
	path := findSessionFile(sessionID, codexHome)
	if path == "" {
		return nil, fmt.Errorf("session file not found for %s", sessionID)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	prefixes := effectiveIgnorePrefixes(ignorePrefixes)
	var entries []core.HistoryEntry

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var raw struct {
			Timestamp string          `json:"timestamp"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		if raw.Type != "response_item" {
			continue
		}

		var item struct {
			Role    string `json:"role"`
			Type    string `json:"type"`
			Text    string `json:"text"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw.Payload, &item) != nil {
			continue
		}

		ts, _ := time.Parse(time.RFC3339Nano, raw.Timestamp)

		switch {
		case item.Role == "user" && len(item.Content) > 0:
			for _, c := range item.Content {
				if c.Type == "input_text" && c.Text != "" && isUserPrompt(c.Text, prefixes) {
					entries = append(entries, core.HistoryEntry{
						Role: "user", Content: c.Text, Timestamp: ts,
					})
				}
			}
		case item.Role == "assistant" && len(item.Content) > 0:
			for _, c := range item.Content {
				if c.Type == "output_text" && c.Text != "" {
					entries = append(entries, core.HistoryEntry{
						Role: "assistant", Content: c.Text, Timestamp: ts,
					})
				}
			}
		case item.Type == "reasoning" && item.Text != "":
			// skip reasoning items
		}
	}

	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

// patchSessionSource rewrites the session_meta line in a Codex JSONL transcript
// so that source="cli" and originator="codex_cli_rs", making the session visible
// in the interactive `codex` terminal.
func patchSessionSource(sessionID, codexHome string) {
	path := findSessionFile(sessionID, codexHome)
	if path == "" {
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	idx := bytes.IndexByte(data, '\n')
	if idx < 0 {
		return
	}
	firstLine := data[:idx]

	// Only patch if it's actually an exec-sourced session
	if !bytes.Contains(firstLine, []byte(`"source":"exec"`)) {
		return
	}

	patched := bytes.Replace(firstLine, []byte(`"source":"exec"`), []byte(`"source":"cli"`), 1)
	patched = bytes.Replace(patched, []byte(`"originator":"codex_exec"`), []byte(`"originator":"codex_cli_rs"`), 1)

	if bytes.Equal(patched, firstLine) {
		return
	}

	out := make([]byte, 0, len(patched)+len(data)-idx)
	out = append(out, patched...)
	out = append(out, data[idx:]...)

	_ = os.WriteFile(path, out, 0o644)
}

// isUserPrompt returns true if the text looks like an actual user prompt
// rather than system context (AGENTS.md, environment_context, permissions, etc.)
// or an internal/system prompt template (configured by ignorePrefixes).
func isUserPrompt(text string, ignorePrefixes []string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	// Skip XML-style system context
	if strings.HasPrefix(t, "<") {
		return false
	}
	// Skip AGENTS.md instructions injected by Codex
	if strings.HasPrefix(t, "# AGENTS.md") || strings.HasPrefix(t, "#AGENTS.md") {
		return false
	}
	// Skip internal/system prompt templates (default + user-configured prefixes).
	// Matching is anchored at the start of the trimmed text, case-sensitive.
	for _, prefix := range ignorePrefixes {
		if prefix == "" {
			continue
		}
		if strings.HasPrefix(t, prefix) {
			return false
		}
	}
	return true
}

// isInternalSummary returns true if the final session summary (last real user
// prompt) matches one of the configured internal/system prefixes. Sessions
// whose summary matches are hidden from /list. A session with no summary
// (e.g. empty after filtering) is never treated as internal — the caller
// decides whether to render it.
func isInternalSummary(summary string, ignorePrefixes []string) bool {
	t := strings.TrimSpace(summary)
	if t == "" {
		return false
	}
	for _, prefix := range ignorePrefixes {
		if prefix == "" {
			continue
		}
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}
