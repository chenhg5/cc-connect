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
)

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
	return nil
}
