package ymsagent

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// profileStoreVersion is the on-disk schema version. Incompatible loads warn
// and start with an empty store, preserving the old file until next write.
const profileStoreVersion = 1

// profileStorePathEnv lets tests override the default file path.
const profileStorePathEnv = "CC_CONNECT_YMS_RCA_PROFILE_STATE"

// profileNameRegexp restricts profile names to a conservative character set
// covering all current yms-rca profiles (pre, yms-dev, cn-bj1, prod_v2 ...).
// Validated three places: store load, Set, and hidden /connect prompt build.
var profileNameRegexp = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func isValidProfileName(name string) bool {
	return profileNameRegexp.MatchString(name)
}

// profileEntry is one persisted record per (project, session_key).
type profileEntry struct {
	Profile   string `json:"profile"`
	UpdatedAt string `json:"updated_at"`
}

// profileStoreData is the on-disk JSON shape. project → session_key → entry.
type profileStoreData struct {
	Version  int                                `json:"version"`
	Projects map[string]map[string]profileEntry `json:"projects"`
}

// profileStore persists "last successful yms-rca profile" per
// (CC_PROJECT, CC_SESSION_KEY). All public methods are safe for concurrent
// use; the on-disk file is rewritten atomically on each Set/Clear.
type profileStore struct {
	mu   sync.Mutex
	path string
	data profileStoreData
}

// newProfileStore loads the file at path (best-effort). Bad JSON or missing
// file yields an empty in-memory store; the file is overwritten on the next
// successful Set.
func newProfileStore(path string) *profileStore {
	s := &profileStore{path: path}
	s.data = profileStoreData{Version: profileStoreVersion, Projects: map[string]map[string]profileEntry{}}
	s.loadLocked()
	return s
}

// loadLocked reads the file (no lock taken — caller is constructor or holds
// the mutex). Invalid entries are dropped with a warning.
func (s *profileStore) loadLocked() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("yms-rca: profile store read failed, starting empty", "path", s.path, "err", err)
		}
		return
	}
	var data profileStoreData
	if err := json.Unmarshal(raw, &data); err != nil {
		slog.Warn("yms-rca: profile store parse failed, starting empty", "path", s.path, "err", err)
		return
	}
	if data.Version != profileStoreVersion {
		slog.Warn("yms-rca: profile store version mismatch, starting empty",
			"path", s.path, "got", data.Version, "want", profileStoreVersion)
		return
	}
	clean := map[string]map[string]profileEntry{}
	for project, sessions := range data.Projects {
		if project == "" || sessions == nil {
			continue
		}
		for sessionKey, entry := range sessions {
			if sessionKey == "" {
				continue
			}
			if !isValidProfileName(entry.Profile) {
				slog.Warn("yms-rca: profile store skipping invalid entry",
					"project", project, "session_key", sessionKey, "profile", entry.Profile)
				continue
			}
			if _, ok := clean[project]; !ok {
				clean[project] = map[string]profileEntry{}
			}
			clean[project][sessionKey] = entry
		}
	}
	s.data.Projects = clean
}

// Get returns the persisted profile for (project, sessionKey), or "" if none.
func (s *profileStore) Get(project, sessionKey string) string {
	if project == "" || sessionKey == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessions, ok := s.data.Projects[project]; ok {
		if entry, ok := sessions[sessionKey]; ok {
			return entry.Profile
		}
	}
	return ""
}

// Set persists profile under (project, sessionKey). No-op if any input is
// empty or the profile name fails character-set validation.
func (s *profileStore) Set(project, sessionKey, profile string) {
	if project == "" || sessionKey == "" || profile == "" {
		return
	}
	if !isValidProfileName(profile) {
		slog.Warn("yms-rca: profile store rejecting invalid name",
			"project", project, "session_key", sessionKey, "profile", profile)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Projects == nil {
		s.data.Projects = map[string]map[string]profileEntry{}
	}
	if _, ok := s.data.Projects[project]; !ok {
		s.data.Projects[project] = map[string]profileEntry{}
	}
	s.data.Projects[project][sessionKey] = profileEntry{
		Profile:   profile,
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
	s.saveLocked()
}

// Clear removes the entry for (project, sessionKey). Removes the project
// container too if its last entry was cleared.
func (s *profileStore) Clear(project, sessionKey string) {
	if project == "" || sessionKey == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions, ok := s.data.Projects[project]
	if !ok {
		return
	}
	if _, ok := sessions[sessionKey]; !ok {
		return
	}
	delete(sessions, sessionKey)
	if len(sessions) == 0 {
		delete(s.data.Projects, project)
	}
	s.saveLocked()
}

// saveLocked writes the current data to disk atomically with mode 0600.
// Caller must hold s.mu.
func (s *profileStore) saveLocked() {
	s.data.Version = profileStoreVersion
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		slog.Warn("yms-rca: profile store marshal failed", "err", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		slog.Warn("yms-rca: profile store mkdir failed", "path", s.path, "err", err)
		return
	}
	if err := core.AtomicWriteFile(s.path, raw, 0o600); err != nil {
		slog.Warn("yms-rca: profile store write failed", "path", s.path, "err", err)
	}
}

// defaultProfileStorePath returns the path used when no env override is set.
// In order of preference:
//
//  1. $CC_CONNECT_YMS_RCA_PROFILE_STATE (test/operator override)
//  2. ~/.cc-connect/yms-rca-profiles.json
//  3. ./yms-rca-profiles.json (HOME unavailable — last resort)
func defaultProfileStorePath() string {
	if p := os.Getenv(profileStorePathEnv); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "yms-rca-profiles.json"
	}
	return filepath.Join(home, ".cc-connect", "yms-rca-profiles.json")
}

// sharedProfileStore is a process-wide singleton: multiple yms-rca Agent
// instances (one per [projects.xxx] block) share one store object and one
// file. Concurrent writes are serialised by profileStore.mu.
var (
	sharedProfileStoreOnce sync.Once
	sharedProfileStore     *profileStore
)

func getSharedProfileStore() *profileStore {
	sharedProfileStoreOnce.Do(func() {
		sharedProfileStore = newProfileStore(defaultProfileStorePath())
	})
	return sharedProfileStore
}
