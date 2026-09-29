package cursor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const sessionSidecarSchemaVersion = 1

// meaningfulCursorSessionName reports whether name is a user-visible label from
// Cursor CLI (/rename or auto-title), as opposed to the default placeholder.
func meaningfulCursorSessionName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	switch strings.ToLower(name) {
	case "new agent", "new chat":
		return false
	}
	return true
}

func (a *Agent) GetSessionDisplayName(_ context.Context, sessionID string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cursor: home dir: %w", err)
	}
	dbPath, err := sessionDBPath(homeDir, a.GetWorkDir(), sessionID)
	if err != nil {
		return "", err
	}
	meta := readSessionMeta(dbPath)
	return strings.TrimSpace(meta.Name), nil
}

func (a *Agent) SetSessionDisplayName(_ context.Context, sessionID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("cursor: empty session name")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cursor: home dir: %w", err)
	}
	dbPath, err := sessionDBPath(homeDir, a.GetWorkDir(), sessionID)
	if err != nil {
		return err
	}
	return writeSessionDisplayName(dbPath, name)
}

func sessionDBPath(homeDir, workDir, sessionID string) (string, error) {
	dir, err := findCursorSessionDir(homeDir, workDir, sessionID)
	if err != nil {
		return "", err
	}
	dbPath := filepath.Join(dir, "store.db")
	if _, err := os.Stat(dbPath); err != nil {
		return "", fmt.Errorf("cursor: store.db for session %s: %w", sessionID, err)
	}
	return dbPath, nil
}

func writeSessionDisplayName(dbPath, name string) error {
	sqliteBin, err := exec.LookPath("sqlite3")
	if err != nil {
		return fmt.Errorf("cursor: sqlite3 not found in PATH (required to sync session names)")
	}

	out, err := exec.Command(sqliteBin, dbPath,
		"SELECT value FROM meta WHERE key='0' LIMIT 1;",
	).Output()
	if err != nil {
		return fmt.Errorf("cursor: read meta: %w", err)
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return fmt.Errorf("cursor: meta row missing for session")
	}

	encodedHex := true
	decoded, err := hex.DecodeString(raw)
	if err != nil {
		encodedHex = false
		decoded = []byte(raw)
	}

	var meta map[string]any
	if err := json.Unmarshal(decoded, &meta); err != nil {
		return fmt.Errorf("cursor: decode meta json: %w", err)
	}

	meta["name"] = name

	updated, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("cursor: encode meta json: %w", err)
	}

	var storeValue string
	if encodedHex {
		storeValue = hex.EncodeToString(updated)
	} else {
		storeValue = string(updated)
	}

	// Single-quote escape for sqlite3 CLI literal.
	escaped := strings.ReplaceAll(storeValue, "'", "''")
	query := fmt.Sprintf("UPDATE meta SET value='%s' WHERE key='0';", escaped)
	if out, err := exec.Command(sqliteBin, dbPath, query).CombinedOutput(); err != nil {
		return fmt.Errorf("cursor: write meta: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return writeSessionSidecarTitle(filepath.Dir(dbPath), name, meta)
}

// writeSessionSidecarTitle updates meta.json, which agent ls reads for display titles.
func writeSessionSidecarTitle(sessionDir, title string, storeMeta map[string]any) error {
	metaPath := filepath.Join(sessionDir, "meta.json")
	nowMs := time.Now().UnixMilli()

	sidecar := map[string]any{
		"schemaVersion":   sessionSidecarSchemaVersion,
		"createdAtMs":     sidecarCreatedAtMs(storeMeta),
		"hasConversation": sidecarHasConversation(storeMeta),
		"title":           title,
		"updatedAtMs":     nowMs,
	}

	if data, err := os.ReadFile(metaPath); err == nil {
		var existing map[string]any
		if json.Unmarshal(data, &existing) == nil {
			for _, key := range []string{
				"schemaVersion", "createdAtMs", "hasConversation", "isSubagent", "cwd",
			} {
				if v, ok := existing[key]; ok {
					sidecar[key] = v
				}
			}
		}
	}

	sidecar["title"] = title
	sidecar["updatedAtMs"] = nowMs

	data, err := json.Marshal(sidecar)
	if err != nil {
		return fmt.Errorf("cursor: encode meta.json: %w", err)
	}
	if err := os.WriteFile(metaPath, data, 0o644); err != nil {
		return fmt.Errorf("cursor: write meta.json: %w", err)
	}
	return nil
}

func sidecarCreatedAtMs(storeMeta map[string]any) int64 {
	if v, ok := storeMeta["createdAt"].(float64); ok && v > 0 {
		return int64(v)
	}
	return time.Now().UnixMilli()
}

func sidecarHasConversation(storeMeta map[string]any) bool {
	blob, _ := storeMeta["latestRootBlobId"].(string)
	return strings.TrimSpace(blob) != ""
}
