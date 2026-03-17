package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type codexThreadMeta struct {
	DisplayName string
	Title       string
}

func codexHomeDir() string {
	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		return codexHome
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return filepath.Join(homeDir, ".codex")
}

func loadCodexThreadMeta(codexHome, workDir string) map[string]codexThreadMeta {
	threadMeta := make(map[string]codexThreadMeta)
	indexPath := filepath.Join(codexHome, "session_index.jsonl")
	titlesPath := filepath.Join(codexHome, "state_5.sqlite")

	for id, title := range loadCodexThreadTitles(titlesPath, workDir) {
		meta := threadMeta[id]
		meta.Title = normalizeCodexSessionLabel(title)
		threadMeta[id] = meta
	}

	for id, threadName := range loadCodexThreadNames(indexPath) {
		meta := threadMeta[id]
		meta.DisplayName = normalizeCodexSessionLabel(threadName)
		threadMeta[id] = meta
	}

	return threadMeta
}

func loadCodexThreadNames(path string) map[string]string {
	threadNames := make(map[string]string)
	if path == "" {
		return threadNames
	}

	f, err := os.Open(path)
	if err != nil {
		return threadNames
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var entry struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.ID == "" || strings.TrimSpace(entry.ThreadName) == "" {
			continue
		}

		threadNames[entry.ID] = strings.TrimSpace(entry.ThreadName)
	}

	return threadNames
}

func loadCodexThreadTitles(dbPath, workDir string) map[string]string {
	titles := make(map[string]string)
	if dbPath == "" {
		return titles
	}
	if _, err := os.Stat(dbPath); err != nil {
		return titles
	}

	sqlite3, err := exec.LookPath("sqlite3")
	if err != nil {
		return titles
	}

	escapedWorkDir := strings.ReplaceAll(workDir, "'", "''")
	query := fmt.Sprintf(
		"SELECT id, substr(title, 1, 512) AS title FROM threads WHERE cwd = '%s'",
		escapedWorkDir,
	)
	output, err := exec.Command(sqlite3, dbPath, "-json", query).Output()
	if err != nil {
		return titles
	}

	var rows []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(output, &rows); err != nil {
		return titles
	}

	for _, row := range rows {
		if row.ID == "" || strings.TrimSpace(row.Title) == "" {
			continue
		}
		titles[row.ID] = row.Title
	}

	return titles
}

func normalizeCodexSessionLabel(text string) string {
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.Join(strings.Fields(text), " ")
	return strings.TrimSpace(text)
}

func resolveCodexDisplayName(meta codexThreadMeta) string {
	displayName := normalizeCodexSessionLabel(meta.DisplayName)
	if displayName != "" {
		return displayName
	}
	return normalizeCodexSessionLabel(meta.Title)
}

// listCodexSessions scans ~/.codex/sessions/ for JSONL transcript files
// whose cwd matches workDir.
func listCodexSessions(workDir string) ([]core.AgentSessionInfo, error) {
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		absWorkDir = workDir
	}

	codexHome := codexHomeDir()
	if codexHome == "" {
		return nil, fmt.Errorf("resolve CODEX_HOME: empty path")
	}
	sessionsDir := filepath.Join(codexHome, "sessions")
	threadMeta := loadCodexThreadMeta(codexHome, absWorkDir)

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
		info := parseCodexSessionFile(f, absWorkDir, threadMeta)
		if info != nil {
			patchSessionSource(info.ID)
			sessions = append(sessions, *info)
		}
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}

// parseCodexSessionFile reads a Codex JSONL transcript.
// Returns nil if the session's cwd doesn't match filterCwd.
func parseCodexSessionFile(path, filterCwd string, threadMeta map[string]codexThreadMeta) *core.AgentSessionInfo {
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
	var msgCount int

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
			if sessionID != "" {
				continue
			}

			var sessionMeta struct {
				ID  string `json:"id"`
				Cwd string `json:"cwd"`
			}
			if json.Unmarshal(entry.Payload, &sessionMeta) == nil {
				sessionID = sessionMeta.ID
				sessionCwd = sessionMeta.Cwd
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
					msgCount++
					// The actual user prompt is the last user response_item
					// (earlier ones are system/AGENTS.md instructions).
					// Pick the last content block that looks like a real prompt.
					for _, c := range item.Content {
						if c.Type == "input_text" && c.Text != "" && isUserPrompt(c.Text) {
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

	if len([]rune(summary)) > 60 {
		summary = string([]rune(summary)[:60]) + "..."
	}

	meta := threadMeta[sessionID]
	return &core.AgentSessionInfo{
		ID:           sessionID,
		DisplayName:  resolveCodexDisplayName(meta),
		Summary:      summary,
		MessageCount: msgCount,
		ModifiedAt:   stat.ModTime(),
	}
}

// findSessionFile locates the JSONL transcript for a given session ID.
func findSessionFile(sessionID string) string {
	codexHome := codexHomeDir()
	if codexHome == "" {
		return ""
	}
	sessionsDir := filepath.Join(codexHome, "sessions")

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
func getSessionHistory(sessionID string, limit int) ([]core.HistoryEntry, error) {
	path := findSessionFile(sessionID)
	if path == "" {
		return nil, fmt.Errorf("session file not found for %s", sessionID)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

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
				if c.Type == "input_text" && c.Text != "" && isUserPrompt(c.Text) {
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
func patchSessionSource(sessionID string) {
	path := findSessionFile(sessionID)
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
func isUserPrompt(text string) bool {
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
	return true
}
