package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ccPermissionHookEntry represents one PermissionRequest hook entry from
// Claude Code's settings.json.
type ccPermissionHookEntry struct {
	Matcher string `json:"matcher"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	} `json:"hooks"`
}

// ccHooksSection is the "hooks" section of Claude Code settings.json.
type ccHooksSection struct {
	PermissionRequest []ccPermissionHookEntry `json:"PermissionRequest"`
}

// ccSettings is the relevant subset of Claude Code settings.json.
type ccSettings struct {
	Hooks ccHooksSection `json:"hooks"`
}

// ccHookDecision is the parsed decision from a hook's stdout.
type ccHookDecision struct {
	Behavior string // "allow", "deny", or "" (ask/fallthrough)
	Message  string // reason for deny (optional)
}

// ccPermissionHookRunner reads and executes Claude Code PermissionRequest
// hooks. Settings are cached for 30 seconds.
type ccPermissionHookRunner struct {
	workDir string

	mu        sync.RWMutex
	cached    *ccSettings
	cacheTime time.Time
	cacheTTL  time.Duration
}

func newCCPermissionHookRunner(workDir string) *ccPermissionHookRunner {
	return &ccPermissionHookRunner{
		workDir:  workDir,
		cacheTTL: 30 * time.Second,
	}
}

// tryHook finds and executes a matching PermissionRequest hook.
// Returns (decision, true) if a hook matched and produced allow/deny.
// Returns (_, false) if no hook matched, hook returned "ask", or any error.
func (r *ccPermissionHookRunner) tryHook(
	ctx context.Context,
	toolName string,
	input map[string]any,
	sessionID string,
) (ccHookDecision, bool) {
	settings, err := r.loadSettings()
	if err != nil {
		slog.Debug("ccHooks: no settings loaded", "error", err)
		return ccHookDecision{}, false
	}

	for _, entry := range settings.Hooks.PermissionRequest {
		if !matchHookEntry(entry.Matcher, toolName) {
			continue
		}
		for _, h := range entry.Hooks {
			if h.Type != "command" || h.Command == "" {
				continue
			}
			stdinData := buildHookStdin(toolName, input, r.workDir, sessionID)
			decision, err := runHookCommand(ctx, h.Command, stdinData)
			if err != nil {
				slog.Warn("ccHooks: hook command failed",
					"command", truncateStr(h.Command, 80), "error", err)
				continue
			}
			if decision.Behavior == "allow" || decision.Behavior == "deny" {
				return decision, true
			}
			// "ask" or empty — fall through to next hook / platform
		}
	}
	return ccHookDecision{}, false
}

// loadSettings reads and merges settings.json files. Cached for cacheTTL.
func (r *ccPermissionHookRunner) loadSettings() (*ccSettings, error) {
	r.mu.RLock()
	if r.cached != nil && time.Since(r.cacheTime) < r.cacheTTL {
		s := r.cached
		r.mu.RUnlock()
		return s, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	// Double-check after acquiring write lock.
	if r.cached != nil && time.Since(r.cacheTime) < r.cacheTTL {
		return r.cached, nil
	}

	merged := &ccSettings{}
	found := false
	for _, p := range settingsPaths(r.workDir) {
		s, err := readSettingsFile(p)
		if err != nil {
			continue
		}
		merged.Hooks.PermissionRequest = append(merged.Hooks.PermissionRequest, s.Hooks.PermissionRequest...)
		found = true
	}
	if !found {
		return nil, fmt.Errorf("no settings files found")
	}

	r.cached = merged
	r.cacheTime = time.Now()
	return merged, nil
}

// settingsPaths returns the settings.json paths to read, in order.
func settingsPaths(workDir string) []string {
	configHome := claudeConfigHomeDir()
	paths := []string{
		filepath.Join(configHome, "settings.json"),
		filepath.Join(configHome, "settings.local.json"),
	}
	if workDir != "" {
		paths = append(paths,
			filepath.Join(workDir, ".claude", "settings.json"),
			filepath.Join(workDir, ".claude", "settings.local.json"),
		)
	}
	return paths
}

// readSettingsFile reads a single settings.json, stripping JSONC comments.
func readSettingsFile(path string) (*ccSettings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cleaned := stripJSONC(data)
	var s ccSettings
	if err := json.Unmarshal(cleaned, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &s, nil
}

// stripJSONC removes // and /* */ comments from JSONC, preserving strings.
func stripJSONC(data []byte) []byte {
	var out bytes.Buffer
	inString := false
	escaped := false
	i := 0
	for i < len(data) {
		ch := data[i]

		if escaped {
			out.WriteByte(ch)
			escaped = false
			i++
			continue
		}

		if inString {
			out.WriteByte(ch)
			if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			i++
			continue
		}

		// Not in string.
		if ch == '"' {
			inString = true
			out.WriteByte(ch)
			i++
			continue
		}

		if ch == '/' && i+1 < len(data) {
			next := data[i+1]
			if next == '/' {
				// Line comment — skip to end of line.
				i += 2
				for i < len(data) && data[i] != '\n' {
					i++
				}
				continue
			}
			if next == '*' {
				// Block comment — skip to closing */.
				i += 2
				for i+1 < len(data) {
					if data[i] == '*' && data[i+1] == '/' {
						i += 2
						break
					}
					i++
				}
				continue
			}
		}

		out.WriteByte(ch)
		i++
	}
	return out.Bytes()
}

// matchHookEntry checks if toolName matches the matcher.
// Empty or "*" matcher matches everything. Otherwise exact match.
func matchHookEntry(matcher, toolName string) bool {
	if matcher == "" || matcher == "*" {
		return true
	}
	return strings.EqualFold(matcher, toolName)
}

// runHookCommand executes a hook command with tool info on stdin.
// Returns the parsed decision. Timeout: 10s.
func runHookCommand(
	ctx context.Context,
	command string,
	stdinData map[string]any,
) (ccHookDecision, error) {
	stdinJSON, err := json.Marshal(stdinData)
	if err != nil {
		return ccHookDecision{}, fmt.Errorf("marshal stdin: %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(stdinJSON)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Strip the skip flag so the hook does real work when cc-connect
	// calls it (even if the host environment has it set).
	cmd.Env = filterEnv(os.Environ(), "CC_CONNECT_PERMISSION_HOOK_SKIP")

	if err := cmd.Run(); err != nil {
		return ccHookDecision{}, fmt.Errorf("hook exec: %w (stderr: %s)", err, truncateStr(strings.TrimSpace(stderr.String()), 200))
	}

	return parseHookOutput(stdout.Bytes())
}

// parseHookOutput parses hook stdout into a decision.
func parseHookOutput(data []byte) (ccHookDecision, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return ccHookDecision{}, nil // empty = ask/fallthrough
	}

	// Try plain text first: "allow", "deny", "ask".
	text := strings.ToLower(string(trimmed))
	switch text {
	case "allow":
		return ccHookDecision{Behavior: "allow"}, nil
	case "deny":
		return ccHookDecision{Behavior: "deny"}, nil
	case "ask":
		return ccHookDecision{}, nil
	}

	// Try structured JSON output.
	var out struct {
		HookSpecificOutput struct {
			Decision struct {
				Behavior string `json:"behavior"`
				Message  string `json:"message"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return ccHookDecision{}, fmt.Errorf("parse hook output: %w", err)
	}
	behavior := strings.ToLower(out.HookSpecificOutput.Decision.Behavior)
	if behavior == "allow" || behavior == "deny" {
		return ccHookDecision{
			Behavior: behavior,
			Message:  out.HookSpecificOutput.Decision.Message,
		}, nil
	}
	return ccHookDecision{}, nil
}

// buildHookStdin constructs the JSON payload for the hook's stdin.
func buildHookStdin(toolName string, input map[string]any, cwd, sessionID string) map[string]any {
	m := map[string]any{
		"session_id": sessionID,
		"tool_name":  toolName,
		"tool_input": input,
	}
	if cwd != "" {
		m["cwd"] = cwd
	}
	return m
}

// truncateStr truncates s to maxLen characters, appending "..." if truncated.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
