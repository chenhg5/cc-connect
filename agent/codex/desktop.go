package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ValidateDesktopThread checks exact rollout metadata rather than filename substrings.
func (a *Agent) ValidateDesktopThread(id string) error {
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()
	return a.ValidateDesktopThreadAt(id, workDir)
}

// Validate without creating a workspace agent or changing the agent's directory.
func (a *Agent) ValidateDesktopThreadAt(id, directory string) (err error) {
	a.mu.RLock()
	home := a.codexHome
	a.mu.RUnlock()
	f, err := os.Open(findSessionFileInCodexHome(resolveCodexHomeDir(home), id))
	if err != nil {
		return fmt.Errorf("desktop thread is unavailable")
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("desktop thread metadata could not be closed")
		}
	}()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 1024*1024)
	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Payload struct {
				ID     string `json:"id"`
				Cwd    string `json:"cwd"`
				Source string `json:"source"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Type != "session_meta" {
			continue
		}
		actual, e1 := filepath.EvalSymlinks(entry.Payload.Cwd)
		expected, e2 := filepath.EvalSymlinks(directory)
		if e1 == nil && e2 == nil && actual == expected && entry.Payload.ID == id && entry.Payload.Source == "vscode" {
			return nil
		}
		break
	}
	return fmt.Errorf("thread does not belong to this desktop workspace")
}
