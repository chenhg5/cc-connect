package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// codexResumeWorkDir resolves a concrete session's saved directory without
// changing the agent's default directory or rewriting its transcripts.
func codexResumeWorkDir(sessionID, codexHome string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", fmt.Errorf("codex: cannot resolve a working directory for an empty session ID")
	}
	type candidate struct {
		path     string
		modified time.Time
		cwd      string
		err      error
	}
	var candidates []candidate
	sessionsDir := filepath.Join(resolveCodexHomeDir(codexHome), "sessions")
	err := filepath.WalkDir(sessionsDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") || !strings.Contains(entry.Name(), sessionID) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		candidates = append(candidates, candidate{path: path, modified: info.ModTime()})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("codex: locate session %s working directory: %w", sessionID, err)
	}
	// Source normalization can rewrite an older segment during /list. Prefer
	// persisted event timestamps so that rewrite cannot make it the newest.
	// Older formats without timestamps fall back to the file's modification time.
	var latest *candidate
	for _, file := range candidates {
		metadata, err := readCodexResumeWorkDir(file.path, sessionID)
		if !metadata.matches && err == nil {
			continue
		}
		file.cwd, file.err = metadata.cwd, err
		if !metadata.modified.IsZero() {
			file.modified = metadata.modified
		}
		if latest == nil || file.modified.After(latest.modified) ||
			(file.modified.Equal(latest.modified) && file.path > latest.path) {
			latest = &file
		}
	}
	if latest == nil {
		return "", fmt.Errorf("codex: session %s not found in %s", sessionID, sessionsDir)
	}
	if latest.err != nil {
		return "", fmt.Errorf("codex: read session %s working directory from %s: %w", sessionID, latest.path, latest.err)
	}
	if !filepath.IsAbs(latest.cwd) {
		return "", fmt.Errorf("codex: session %s has no saved absolute working directory (got %q)", sessionID, latest.cwd)
	}
	info, err := os.Stat(latest.cwd)
	if err != nil {
		return "", fmt.Errorf("codex: session %s working directory %q is unavailable: %w", sessionID, latest.cwd, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("codex: session %s working directory %q is not a directory", sessionID, latest.cwd)
	}
	return latest.cwd, nil
}

type codexResumeMetadata struct {
	cwd      string
	modified time.Time
	matches  bool
}

func readCodexResumeWorkDir(path, sessionID string) (codexResumeMetadata, error) {
	var result codexResumeMetadata
	f, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), maxCodexJSONLLineSize)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record struct {
			Timestamp string          `json:"timestamp"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return result, fmt.Errorf("decode rollout record: %w", err)
		}
		if timestamp, err := time.Parse(time.RFC3339Nano, record.Timestamp); err == nil && timestamp.After(result.modified) {
			result.modified = timestamp
		}
		switch record.Type {
		case "session_meta":
			if result.matches {
				continue // Copied parent metadata is not this rollout's identity.
			}
			var meta struct {
				ID  string `json:"id"`
				Cwd string `json:"cwd"`
			}
			if err := json.Unmarshal(record.Payload, &meta); err != nil {
				return result, fmt.Errorf("decode session metadata: %w", err)
			}
			if meta.ID != sessionID {
				return result, nil
			}
			result.matches, result.cwd = true, meta.Cwd
		case "turn_context":
			if !result.matches {
				continue
			}
			var turn struct {
				Cwd string `json:"cwd"`
			}
			if err := json.Unmarshal(record.Payload, &turn); err != nil {
				return result, fmt.Errorf("decode turn context: %w", err)
			}
			if turn.Cwd != "" {
				result.cwd = turn.Cwd
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("scan rollout: %w", err)
	}
	if !result.matches {
		return result, fmt.Errorf("session metadata is missing")
	}
	return result, nil
}
