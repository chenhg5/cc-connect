// Package ymsprofile reads yms-rca connection profiles to discover the
// environment variable names referenced by their mcp.token_env field, and
// parses cc-connect prompts to extract /connect targets.
//
// It is intentionally a small, dependency-light helper that both
// agent/yms-rca and daemon import. It must NOT live under core/, which is
// stdlib-only (see project CLAUDE.md). YAML parsing requires gopkg.in/yaml.v3.
package ymsprofile

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProfileTokenEnv pairs a discovered env-var name with the profile file that
// declared it. The profile file is used for diagnostic messages so the user
// can locate the source declaration; the file path is intentionally the
// basename only when the caller wants to keep absolute paths out of logs.
type ProfileTokenEnv struct {
	EnvName     string // e.g. "IUAPYYS_MCP_TOKEN"
	ProfileFile string // basename of the source yaml file, e.g. "yms-dev.yaml"
}

// envNameRegexp matches a valid POSIX-style env variable identifier.
var envNameRegexp = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IsValidEnvName reports whether s is a syntactically valid env-var name.
func IsValidEnvName(s string) bool {
	return envNameRegexp.MatchString(s)
}

// DefaultConnectionsDir returns ~/.yms-rca/connections (the directory
// yms-rca CLI uses by default). Returns "" if the user home cannot be
// resolved.
func DefaultConnectionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".yms-rca", "connections")
}

// profileShape is the minimal subset of a yms-rca connection profile we
// need to read. Other fields (host, user, ssh.*, etc.) are intentionally
// ignored so we never log secrets we don't have to.
type profileShape struct {
	MCP struct {
		TokenEnv string `yaml:"token_env"`
	} `yaml:"mcp"`
}

// DiscoverConnectionTokenEnvNames walks dir for *.yaml files (skipping
// *.example) and returns a deduplicated, sorted list of (env_name,
// profile_file) pairs derived from each profile's mcp.token_env.
//
// Profiles whose token_env is empty are skipped silently. Profiles whose
// token_env fails the env-name regex are skipped with a single warning
// returned in the error; valid entries are still returned. Read/parse
// errors on individual files are skipped with warnings appended to the
// error chain (use errors.Is/As-friendly handling at the call site —
// the caller is expected to log rather than fail).
//
// If dir does not exist or cannot be read, the returned slice is nil and
// err describes the directory-level failure. The caller decides whether
// to escalate that to a hard failure.
func DiscoverConnectionTokenEnvNames(dir string) ([]ProfileTokenEnv, error) {
	if dir == "" {
		return nil, fmt.Errorf("ymsprofile: empty dir")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("ymsprofile: read dir %s: %w", dir, err)
	}

	seen := make(map[string]string) // envName -> profileFile (first occurrence)
	var warnings []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".yaml") && !strings.HasSuffix(lower, ".yml") {
			continue
		}
		// Ignore example fixtures: foo.example.yaml or foo.yaml.example.
		if strings.Contains(lower, ".example") {
			continue
		}

		full := filepath.Join(dir, name)
		data, readErr := os.ReadFile(full)
		if readErr != nil {
			warnings = append(warnings, fmt.Sprintf("read %s: %v", name, readErr))
			continue
		}
		var p profileShape
		if err := yaml.Unmarshal(data, &p); err != nil {
			warnings = append(warnings, fmt.Sprintf("parse %s: %v", name, err))
			continue
		}
		tokenEnv := strings.TrimSpace(p.MCP.TokenEnv)
		if tokenEnv == "" {
			continue
		}
		if !envNameRegexp.MatchString(tokenEnv) {
			warnings = append(warnings, fmt.Sprintf("profile %s declares invalid env name %q", name, tokenEnv))
			continue
		}
		if _, exists := seen[tokenEnv]; !exists {
			seen[tokenEnv] = name
		}
	}

	out := make([]ProfileTokenEnv, 0, len(seen))
	for envName, profileFile := range seen {
		out = append(out, ProfileTokenEnv{EnvName: envName, ProfileFile: profileFile})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].EnvName != out[j].EnvName {
			return out[i].EnvName < out[j].EnvName
		}
		return out[i].ProfileFile < out[j].ProfileFile
	})

	if len(warnings) > 0 {
		return out, fmt.Errorf("ymsprofile: %s", strings.Join(warnings, "; "))
	}
	return out, nil
}

// FindProfileForConnection returns the profile file (basename) in dir whose
// name (minus extension) equals connection. Returns "" if not found.
//
// yms-rca CLI conventions use the profile filename stem as the connection
// name (e.g. "yms-dev" -> "yms-dev.yaml"). The lookup is case-sensitive to
// match the underlying filesystem semantics on macOS/Linux.
func FindProfileForConnection(dir, connection string) string {
	if dir == "" || connection == "" {
		return ""
	}
	for _, ext := range []string{".yaml", ".yml"} {
		candidate := connection + ext
		full := filepath.Join(dir, candidate)
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// ReadTokenEnv reads dir/<connection>.yaml (or .yml) and returns its
// mcp.token_env value. Returns ("", "", nil) when no profile matches —
// the caller decides whether that's an error. The second return value is
// the basename of the matched profile for diagnostics.
func ReadTokenEnv(dir, connection string) (envName, profileFile string, err error) {
	profile := FindProfileForConnection(dir, connection)
	if profile == "" {
		return "", "", nil
	}
	data, err := os.ReadFile(filepath.Join(dir, profile))
	if err != nil {
		return "", profile, fmt.Errorf("ymsprofile: read %s: %w", profile, err)
	}
	var p profileShape
	if err := yaml.Unmarshal(data, &p); err != nil {
		return "", profile, fmt.Errorf("ymsprofile: parse %s: %w", profile, err)
	}
	return strings.TrimSpace(p.MCP.TokenEnv), profile, nil
}
