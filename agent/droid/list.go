package droid

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

func listDroidSessionsFromBase(baseDir, workDir string) ([]core.AgentSessionInfo, error) {
	files, err := collectDroidSessionFiles(baseDir, workDir)
	if err != nil {
		return nil, err
	}

	var sessions []core.AgentSessionInfo
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}

		sid, title, msgCount := scanDroidSession(path, workDir)
		if sid == "" {
			continue
		}
		if title == "" {
			title = sid
		}

		sessions = append(sessions, core.AgentSessionInfo{
			ID:           sid,
			Summary:      title,
			MessageCount: msgCount,
			ModifiedAt:   info.ModTime(),
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}

func findDroidSessionFileFromBase(baseDir, workDir, sessionID string) (string, error) {
	files, err := collectDroidSessionFiles(baseDir, workDir)
	if err != nil {
		return "", err
	}

	for _, path := range files {
		sid, _, _ := scanDroidSession(path, workDir)
		if sid == sessionID {
			return path, nil
		}
	}

	return "", nil
}

func collectDroidSessionFiles(baseDir, workDir string) ([]string, error) {
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		absWorkDir = workDir
	}

	var files []string
	seen := make(map[string]struct{})

	for _, key := range droidSessionDirKeys(absWorkDir) {
		dir := filepath.Join(baseDir, key)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			files = append(files, path)
		}
	}

	if len(files) > 0 {
		return files, nil
	}

	dirs, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("droid: read sessions dir: %w", err)
	}

	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(baseDir, dir.Name()))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			path := filepath.Join(baseDir, dir.Name(), entry.Name())
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			files = append(files, path)
		}
	}

	return files, nil
}

func droidSessionDirKeys(absWorkDir string) []string {
	candidates := []string{
		strings.ReplaceAll(absWorkDir, string(filepath.Separator), "-"),
		strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(absWorkDir),
		strings.NewReplacer("/", "-", "\\", "-", ":", "-", "_", "-").Replace(absWorkDir),
	}
	forward := strings.ReplaceAll(absWorkDir, "\\", "/")
	candidates = append(candidates, strings.ReplaceAll(forward, "/", "-"))

	uniq := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		uniq = append(uniq, c)
	}
	return uniq
}

func scanDroidSession(path, workDir string) (sessionID string, title string, msgCount int) {
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		absWorkDir = workDir
	}
	absWorkDir = filepath.Clean(absWorkDir)

	f, err := os.Open(path)
	if err != nil {
		return "", "", 0
	}
	defer f.Close()

	var firstUser string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		var raw map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}

		t, _ := raw["type"].(string)
		switch t {
		case "session_start":
			if sid, ok := raw["id"].(string); ok {
				sessionID = sid
			}
			if st, ok := raw["title"].(string); ok {
				title = strings.TrimSpace(st)
			}
			if cwd, ok := raw["cwd"].(string); ok && cwd != "" {
				if filepath.Clean(cwd) != absWorkDir {
					return "", "", 0
				}
			}

		case "message":
			msg, _ := raw["message"].(map[string]any)
			if msg == nil {
				continue
			}
			role, _ := msg["role"].(string)
			if role != "user" && role != "assistant" {
				continue
			}
			msgCount++
			if role == "user" && firstUser == "" {
				if txt := extractDroidMessageText(msg["content"]); txt != "" {
					firstUser = txt
				}
			}
		}
	}

	if title == "" {
		title = firstUser
	}
	title = strings.TrimSpace(strings.Join(strings.Fields(title), " "))
	if len([]rune(title)) > 80 {
		title = string([]rune(title)[:80]) + "..."
	}

	return sessionID, title, msgCount
}

func extractDroidMessageText(content any) string {
	parts, ok := content.([]any)
	if !ok {
		return ""
	}
	for _, part := range parts {
		m, ok := part.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t != "text" {
			continue
		}
		if text, ok := m["text"].(string); ok && text != "" {
			return text
		}
	}
	return ""
}
