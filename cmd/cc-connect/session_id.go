package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func runAgentSID(args []string) {
	var project, sessionKey, dataDir string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 < len(args) {
				i++
				project = args[i]
			}
		case "--session-key", "-s":
			if i+1 < len(args) {
				i++
				sessionKey = args[i]
			}
		case "--data-dir":
			if i+1 < len(args) {
				i++
				dataDir = args[i]
			}
		case "--help", "-h":
			printAgentSIDUsage()
			return
		}
	}

	if project == "" {
		project = os.Getenv("CC_PROJECT")
	}
	if sessionKey == "" {
		sessionKey = os.Getenv("CC_SESSION_KEY")
	}
	dataDir = resolveDataDir(dataDir)

	if project == "" {
		fmt.Fprintln(os.Stderr, "Error: project is required (set CC_PROJECT env or use --project)")
		os.Exit(1)
	}
	if sessionKey == "" {
		fmt.Fprintln(os.Stderr, "Error: session key is required (set CC_SESSION_KEY env or use --session-key)")
		os.Exit(1)
	}

	agentID, err := findAgentSessionID(dataDir, project, sessionKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(agentID)
}

// findAgentSessionID searches all session files matching the project name
// for the given session key and returns the agent session ID.
//
// The engine uses different naming schemes depending on configuration:
//   - Without work_dir: <project>.json
//   - With work_dir:    <project>_<hash>.json
//   - Multi-workspace:  <project>_ws_<hash>.json
//
// Legacy files may also live directly in dataDir (without sessions/ subdir)
// or use the older .sessions.json naming.
//
// This function scans all matching files and returns the agent session ID
// from the file that contains the requested session key. When multiple files
// contain the same key, the one with the newest UpdatedAt wins. If a file
// has the key but an empty agent_session_id, the error is recorded but
// scanning continues in case a newer valid match exists.
func findAgentSessionID(dataDir, project, sessionKey string) (string, error) {
	// Candidate directories: sessions/ subdir (current) and dataDir root (legacy).
	dirs := []string{
		filepath.Join(dataDir, "sessions"),
		dataDir,
	}

	type candidate struct {
		agentID   string
		updatedAt int64 // unix nano from session UpdatedAt
	}
	var best *candidate
	var errCandidate *candidate // tracks the newest file where key was found but ID unavailable
	var definiteErr error

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue // directory may not exist (e.g. legacy dir)
			}
			// Permission or other I/O errors should not be silently ignored.
			return "", fmt.Errorf("cannot read sessions directory %s: %w", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if !matchesProject(entry.Name(), project) {
				continue
			}

			agentID, updatedAt, found, err := readAgentSessionID(filepath.Join(dir, entry.Name()), sessionKey)
			if err != nil {
				// Key found but agent ID unavailable; record with its timestamp.
				if errCandidate == nil || updatedAt > errCandidate.updatedAt {
					errCandidate = &candidate{updatedAt: updatedAt}
					definiteErr = err
				}
				continue
			}
			if found {
				if best == nil || updatedAt > best.updatedAt {
					best = &candidate{agentID: agentID, updatedAt: updatedAt}
				}
			}
		}
	}

	// If the newest match has a valid agent ID, return it.
	// If an error match is newer than the best valid match, prefer the error
	// (the newest session is still starting and the older ID is stale).
	if best != nil {
		if errCandidate != nil && errCandidate.updatedAt > best.updatedAt {
			return "", definiteErr
		}
		return best.agentID, nil
	}
	if definiteErr != nil {
		return "", definiteErr
	}
	return "", fmt.Errorf("no session found for project %q with key %q", project, sessionKey)
}

// matchesProject checks if a filename belongs to the given project.
// Matches: <project>.json, <project>_<hash>.json, <project>_ws_<hash>.json,
// <project>.sessions.json (legacy).
//
// The suffix after <project>_ must look like a hash (hex) or follow the
// ws_<hash> pattern to avoid false positives with other projects whose
// name starts with the same prefix (e.g. "mybot_extra" vs "mybot").
func matchesProject(filename, project string) bool {
	if !strings.HasSuffix(filename, ".json") {
		return false
	}
	base := strings.TrimSuffix(filename, ".json")
	// Try exact match first (covers <project>.json).
	if base == project {
		return true
	}
	// Try legacy .sessions.json naming: only strip the suffix if the
	// remaining base equals the project name (avoids false positives
	// for projects whose name ends in ".sessions").
	if strings.HasSuffix(base, ".sessions") {
		if strings.TrimSuffix(base, ".sessions") == project {
			return true
		}
	}
	// Try hashed variants: <project>_<hex> or <project>_ws_<hex>.
	if !strings.HasPrefix(base, project+"_") {
		return false
	}
	suffix := strings.TrimPrefix(base[len(project)+1:], "ws_")
	return isHex(suffix)
}

// isHex returns true if s is a non-empty string of hex characters.
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// readAgentSessionID reads a session file and looks up the agent session ID
// for the given session key. Returns:
//   - (id, updatedAt, true, nil)  — key found, agent session ID available
//   - ("", 0, false, nil)         — key not in this file, or file unreadable/malformed (skip)
//   - ("", 0, false, err)         — key found but agent ID unavailable (definitive error)
func readAgentSessionID(path, sessionKey string) (string, int64, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, false, nil // file unreadable, skip
	}
	var fd sessionFileData
	if err := json.Unmarshal(data, &fd); err != nil {
		return "", 0, false, nil // malformed, skip
	}
	activeID, ok := fd.ActiveSession[sessionKey]
	if !ok {
		return "", 0, false, nil // key not in this file
	}
	// Key found in this file — errors from here are definitive.
	// Use the file's modtime as fallback when the session entry is missing or
	// has a zero UpdatedAt, so error-candidate timestamps can still compete
	// with valid candidates from other files.
	fileMod := fileModTime(path)

	sess := fd.Sessions[activeID]
	if sess == nil {
		return "", fileMod, false, fmt.Errorf("session %q referenced by key %q not found in %s", activeID, sessionKey, filepath.Base(path))
	}
	ts := sess.UpdatedAt.UnixNano()
	if ts == 0 {
		ts = fileMod
	}
	if sess.AgentSessionID == "" {
		return "", ts, false, fmt.Errorf("agent session ID not yet available (session may still be starting)")
	}
	return sess.AgentSessionID, ts, true, nil
}

// fileModTime returns the file's modification time as UnixNano, or 0 on error.
func fileModTime(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.ModTime().UnixNano()
}

func printAgentSIDUsage() {
	fmt.Println(`Usage: cc-connect agent-sid [options]

Print the agent session ID (e.g. Claude Code, Codex, Gemini CLI) for the
current session. This is the ID used for --resume.

The command reads from the persisted session file; no running cc-connect
instance is required.

Options:
  -p, --project <name>       Project name (auto-detected from CC_PROJECT env)
  -s, --session-key <key>    Session key  (auto-detected from CC_SESSION_KEY env)
      --data-dir <path>      Data directory (default: ~/.cc-connect)
  -h, --help                 Show this help

Examples:
  cc-connect agent-sid                         Auto-detect from env (inside a session)
  cc-connect agent-sid -p mybot -s "discord:123:456"`)
}
