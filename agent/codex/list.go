package codex

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	_ "modernc.org/sqlite"
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

// listCodexSessions merges Codex CLI JSONL transcripts with the Codex App
// index/database, then filters sessions whose cwd matches workDir.
func listCodexSessions(workDir, codexHome string) ([]core.AgentSessionInfo, error) {
	home := resolveCodexHomeDir(codexHome)
	filterCwd := core.NormalizeWorkdirPath(workDir)
	projectRoots := loadCodexProjectRoots(home)
	filterIsProjectRoot := core.WorkdirRootForCwd(filterCwd, projectRoots) == filterCwd

	allSessions := listAllCodexSessions(codexHome)
	var sessions []core.AgentSessionInfo
	for _, info := range allSessions {
		if !core.MatchSessionWorkdir(info.Cwd, filterCwd, filterIsProjectRoot) {
			continue
		}
		patchSessionSource(info.ID, codexHome)
		sessions = append(sessions, info)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}

func listCodexWorkdirs(codexHome string) ([]core.AgentWorkdirInfo, error) {
	home := resolveCodexHomeDir(codexHome)
	allSessions := listAllCodexSessions(codexHome)
	state := loadCodexSidebarState(home)
	projectRoots := state.projectRoots
	if state.loaded {
		allSessions = filterCodexProjectSessions(allSessions, projectRoots)
	}
	return core.GroupAgentWorkdirs(allSessions, projectRoots), nil
}

type codexGlobalState struct {
	ProjectOrder            []string `json:"project-order"`
	ElectronSavedWorkspaces []string `json:"electron-saved-workspace-roots"`
	ProjectlessThreadIDs    []string `json:"projectless-thread-ids"`
}

type codexSidebarState struct {
	loaded             bool
	projectRoots       []string
	projectlessThreads map[string]struct{}
}

func loadCodexProjectRoots(codexHome string) []string {
	return loadCodexSidebarState(codexHome).projectRoots
}

func loadCodexSidebarState(codexHome string) codexSidebarState {
	path := filepath.Join(codexHome, ".codex-global-state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return codexSidebarState{}
	}
	var state codexGlobalState
	if json.Unmarshal(data, &state) != nil {
		return codexSidebarState{}
	}
	var roots []string
	seen := make(map[string]struct{})
	for _, raw := range append(state.ProjectOrder, state.ElectronSavedWorkspaces...) {
		root := core.NormalizeWorkdirPath(raw)
		if root == "" {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	projectless := make(map[string]struct{}, len(state.ProjectlessThreadIDs))
	for _, id := range state.ProjectlessThreadIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			projectless[id] = struct{}{}
		}
	}
	return codexSidebarState{
		loaded:             true,
		projectRoots:       roots,
		projectlessThreads: projectless,
	}
}

func filterCodexProjectSessions(sessions []core.AgentSessionInfo, projectRoots []string) []core.AgentSessionInfo {
	if len(projectRoots) == 0 {
		return nil
	}
	filtered := make([]core.AgentSessionInfo, 0, len(sessions))
	for _, session := range sessions {
		if core.WorkdirRootForCwd(session.Cwd, projectRoots) != "" {
			filtered = append(filtered, session)
		}
	}
	return filtered
}

func listAllCodexSessions(codexHome string) []core.AgentSessionInfo {
	home := resolveCodexHomeDir(codexHome)
	index := loadCodexSessionIndex(home)
	archivedIDs := listCodexArchivedSessionIDs(home)
	merged := make(map[string]core.AgentSessionInfo)

	for _, info := range listCodexDatabaseSessions(home) {
		if _, archived := archivedIDs[info.ID]; archived {
			continue
		}
		mergeCodexSession(merged, info)
	}

	for _, path := range listCodexSessionFiles(home) {
		info := parseCodexSessionFile(path, "")
		if info != nil {
			if _, archived := archivedIDs[info.ID]; archived {
				continue
			}
			mergeCodexSession(merged, *info)
		}
	}

	for id, entry := range index {
		if _, archived := archivedIDs[id]; archived {
			continue
		}
		info := merged[id]
		info.ID = id
		if entry.ThreadName != "" {
			info.Summary = entry.ThreadName
		}
		if t := parseCodexIndexTime(entry.UpdatedAt); !t.IsZero() && t.After(info.ModifiedAt) {
			info.ModifiedAt = t
		}
		if info.Summary != "" || !info.ModifiedAt.IsZero() {
			merged[id] = info
		}
	}

	sessions := make([]core.AgentSessionInfo, 0, len(merged))
	for _, info := range merged {
		if info.ID == "" {
			continue
		}
		sessions = append(sessions, info)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})
	return sessions
}

func mergeCodexSession(merged map[string]core.AgentSessionInfo, next core.AgentSessionInfo) {
	if next.ID == "" {
		return
	}
	cur := merged[next.ID]
	if cur.ID == "" {
		merged[next.ID] = next
		return
	}
	if strings.TrimSpace(cur.Summary) == "" && strings.TrimSpace(next.Summary) != "" {
		cur.Summary = next.Summary
	}
	if strings.TrimSpace(cur.Cwd) == "" && strings.TrimSpace(next.Cwd) != "" {
		cur.Cwd = next.Cwd
	}
	if cur.MessageCount == 0 && next.MessageCount != 0 {
		cur.MessageCount = next.MessageCount
	}
	if next.ModifiedAt.After(cur.ModifiedAt) {
		cur.ModifiedAt = next.ModifiedAt
	}
	merged[next.ID] = cur
}

func listCodexSessionFiles(codexHome string) []string {
	var files []string
	root := filepath.Join(codexHome, "sessions")
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	return files
}

type codexSessionIndexEntry struct {
	ID         string `json:"id"`
	ThreadName string `json:"thread_name"`
	UpdatedAt  string `json:"updated_at"`
}

func loadCodexSessionIndex(codexHome string) map[string]codexSessionIndexEntry {
	path := filepath.Join(codexHome, "session_index.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	index := make(map[string]codexSessionIndexEntry)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry codexSessionIndexEntry
		if json.Unmarshal([]byte(line), &entry) != nil || entry.ID == "" {
			continue
		}
		index[entry.ID] = entry
	}
	return index
}

func listCodexDatabaseSessions(codexHome string) []core.AgentSessionInfo {
	path := filepath.Join(codexHome, "state_5.sqlite")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil
	}
	defer func() { _ = db.Close() }()

	query := `
		select id, cwd, title, updated_at, updated_at_ms
		from threads
	`
	if codexThreadsHasColumn(db, "archived") {
		query += ` where archived = 0`
	}
	rows, err := db.Query(query)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()

	var sessions []core.AgentSessionInfo
	for rows.Next() {
		var id, cwd, title sql.NullString
		var updatedAt, updatedAtMS sql.NullInt64
		if err := rows.Scan(&id, &cwd, &title, &updatedAt, &updatedAtMS); err != nil {
			continue
		}
		if !id.Valid || id.String == "" {
			continue
		}
		modified := time.Time{}
		if updatedAtMS.Valid && updatedAtMS.Int64 > 0 {
			modified = time.UnixMilli(updatedAtMS.Int64)
		} else if updatedAt.Valid && updatedAt.Int64 > 0 {
			modified = time.Unix(updatedAt.Int64, 0)
		}
		sessions = append(sessions, core.AgentSessionInfo{
			ID:         id.String,
			Summary:    title.String,
			ModifiedAt: modified,
			Cwd:        cwd.String,
		})
	}
	return sessions
}

func codexThreadsHasColumn(db *sql.DB, name string) bool {
	rows, err := db.Query(`pragma table_info(threads)`)
	if err != nil {
		return false
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var cid int
		var colName, colType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &defaultValue, &pk); err != nil {
			continue
		}
		if strings.EqualFold(colName, name) {
			return true
		}
	}
	return false
}

func listCodexArchivedSessionIDs(codexHome string) map[string]struct{} {
	path := filepath.Join(codexHome, "state_5.sqlite")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil
	}
	defer func() { _ = db.Close() }()

	var where []string
	if codexThreadsHasColumn(db, "archived") {
		where = append(where, `coalesce(archived, 0) != 0`)
	}
	if codexThreadsHasColumn(db, "archived_at") {
		where = append(where, `archived_at is not null`)
	}
	if len(where) == 0 {
		return nil
	}

	rows, err := db.Query(`select id from threads where ` + strings.Join(where, ` or `))
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()

	ids := make(map[string]struct{})
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			continue
		}
		if id.Valid && strings.TrimSpace(id.String) != "" {
			ids[id.String] = struct{}{}
		}
	}
	return ids
}

func parseCodexIndexTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999-07:00", "2006-01-02 15:04:05-07:00"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t
		}
	}
	return time.Time{}
}

// parseCodexSessionFile reads a Codex JSONL transcript.
// Returns nil if the session's cwd doesn't match filterCwd.
func parseCodexSessionFile(path, filterCwd string) *core.AgentSessionInfo {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		return nil
	}

	var sessionID string
	var sessionCwd string
	var summary string
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
				switch item.Role {
				case "user":
					userMsgSeen++
					msgCount++
					// The actual user prompt is the last user response_item
					// (earlier ones are system/AGENTS.md instructions).
					// Pick the last content block that looks like a real prompt.
					for _, c := range item.Content {
						if c.Type == "input_text" && c.Text != "" && isUserPrompt(c.Text) {
							summary = c.Text
						}
					}
				case "assistant":
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

	return &core.AgentSessionInfo{
		ID:           sessionID,
		Summary:      summary,
		MessageCount: msgCount,
		ModifiedAt:   stat.ModTime(),
		Cwd:          sessionCwd,
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
func getSessionHistory(sessionID, codexHome string, limit int) ([]core.HistoryEntry, error) {
	path := findSessionFile(sessionID, codexHome)
	if path == "" {
		return nil, fmt.Errorf("session file not found for %s", sessionID)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

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
