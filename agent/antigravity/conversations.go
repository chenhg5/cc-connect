package antigravity

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"

	_ "modernc.org/sqlite"
)

// Newer agy releases (1.4+) no longer write ~/.gemini/tmp/<slug>/chats/*.jsonl.
// Each conversation is stored as ~/.gemini/antigravity-cli/conversations/<id>.db
// and indexed in ~/.gemini/antigravity-cli/conversation_summaries.db.

func agyCLIDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("antigravity: cannot determine home dir: %w", err)
	}
	return filepath.Join(homeDir, ".gemini", "antigravity-cli"), nil
}

func agyConversationsDir() string {
	dir, err := agyCLIDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "conversations")
}

// listAgyConversations reads the agy conversation index. found=false means the
// index does not exist, so callers can fall back to the legacy chats layout.
func listAgyConversations(cliDir, workDir string) (sessions []core.AgentSessionInfo, found bool, err error) {
	dbPath := filepath.Join(cliDir, "conversation_summaries.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, false, nil
	}

	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil, true, fmt.Errorf("antigravity: open conversation index: %w", err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query(`SELECT conversation_id, title, preview, step_count, last_modified_time, workspace_uris
		FROM conversation_summaries WHERE parent_conversation_id = ''`)
	if err != nil {
		return nil, true, fmt.Errorf("antigravity: query conversation index: %w", err)
	}
	defer func() { _ = rows.Close() }()

	want := filepath.Clean(absPath(workDir))
	convDir := filepath.Join(cliDir, "conversations")
	for rows.Next() {
		var id, title, preview, modified, workspaces string
		var steps int
		if err := rows.Scan(&id, &title, &preview, &steps, &modified, &workspaces); err != nil {
			continue
		}
		if !workspaceMatches(workspaces, want) {
			continue
		}
		// The index may outlive a deleted conversation file.
		if _, err := os.Stat(filepath.Join(convDir, id+".db")); err != nil {
			continue
		}
		summary := strings.TrimSpace(title)
		if summary == "" {
			summary = strings.TrimSpace(preview)
		}
		if summary == "" {
			summary = id
		}
		if utf8.RuneCountInString(summary) > 60 {
			summary = string([]rune(summary)[:60]) + "..."
		}
		sessions = append(sessions, core.AgentSessionInfo{
			ID:           id,
			Summary:      summary,
			MessageCount: steps,
			ModifiedAt:   parseAgyTime(modified),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, true, fmt.Errorf("antigravity: read conversation index: %w", err)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})
	return sessions, true, nil
}

func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// workspaceMatches reports whether a JSON list of file:// URIs contains dir.
func workspaceMatches(raw, dir string) bool {
	var uris []string
	if json.Unmarshal([]byte(raw), &uris) != nil {
		return false
	}
	for _, u := range uris {
		parsed, err := url.Parse(u)
		if err != nil || parsed.Scheme != "file" {
			continue
		}
		if filepath.Clean(parsed.Path) == dir {
			return true
		}
	}
	return false
}

func parseAgyTime(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		time.RFC3339Nano,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// snapshotAgyConversations records existing conversation files so a new one
// can be identified after the first turn.
func snapshotAgyConversations() map[string]bool {
	seen := make(map[string]bool)
	dir := agyConversationsDir()
	if dir == "" {
		return seen
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return seen
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".db") {
			seen[e.Name()] = true
		}
	}
	return seen
}

// detectNewAgyConversation returns the newest conversation file created since
// the snapshot, or "".
func detectNewAgyConversation(pre map[string]bool, since time.Time) string {
	dir := agyConversationsDir()
	if dir == "" {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var best string
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") || pre[e.Name()] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !since.IsZero() && info.ModTime().Before(since.Add(-2*time.Second)) {
			continue
		}
		if best == "" || info.ModTime().After(bestMod) {
			best, bestMod = strings.TrimSuffix(e.Name(), ".db"), info.ModTime()
		}
	}
	return best
}

// deleteAgyConversation removes a conversation file. found=false means no
// such file in the new layout.
func deleteAgyConversation(sessionID string) (found bool, err error) {
	dir := agyConversationsDir()
	if dir == "" || sessionID == "" || strings.ContainsAny(sessionID, `/\`) || strings.Contains(sessionID, "..") {
		return false, nil
	}
	path := filepath.Join(dir, sessionID+".db")
	if _, err := os.Stat(path); err != nil {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		return true, err
	}
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	return true, nil
}
