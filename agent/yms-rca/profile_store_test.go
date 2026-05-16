package ymsagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Each test isolates its own store file under t.TempDir() to avoid touching
// the user's real ~/.cc-connect/yms-rca-profiles.json.

func newTempStore(t *testing.T) (*profileStore, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "yms-rca-profiles.json")
	return newProfileStore(path), path
}

func TestProfileStorePersistsPerProjectSession(t *testing.T) {
	s, _ := newTempStore(t)

	s.Set("yms-rca-youzone", "youzone:conv1:user1", "pre")
	s.Set("yms-rca-youzone", "youzone:conv2:user2", "yms-dev")
	s.Set("other-project", "youzone:conv1:user1", "prod")

	if got := s.Get("yms-rca-youzone", "youzone:conv1:user1"); got != "pre" {
		t.Errorf("Get(yms-rca-youzone, conv1) = %q, want pre", got)
	}
	if got := s.Get("yms-rca-youzone", "youzone:conv2:user2"); got != "yms-dev" {
		t.Errorf("Get(yms-rca-youzone, conv2) = %q, want yms-dev", got)
	}
	if got := s.Get("other-project", "youzone:conv1:user1"); got != "prod" {
		t.Errorf("Get(other-project, conv1) = %q, want prod", got)
	}
	// missing entry returns empty
	if got := s.Get("yms-rca-youzone", "missing-session"); got != "" {
		t.Errorf("Get(missing) = %q, want empty", got)
	}
}

func TestProfileStoreReloadFromDisk(t *testing.T) {
	s, path := newTempStore(t)
	s.Set("p1", "k1", "pre")
	s.Set("p2", "k2", "yms-dev")

	// Re-create store from same path; should see the persisted entries.
	s2 := newProfileStore(path)
	if got := s2.Get("p1", "k1"); got != "pre" {
		t.Errorf("after reload Get(p1,k1) = %q, want pre", got)
	}
	if got := s2.Get("p2", "k2"); got != "yms-dev" {
		t.Errorf("after reload Get(p2,k2) = %q, want yms-dev", got)
	}
}

func TestProfileStoreClearRemovesEntry(t *testing.T) {
	s, _ := newTempStore(t)
	s.Set("p1", "k1", "pre")
	s.Set("p1", "k2", "yms-dev")

	s.Clear("p1", "k1")

	if got := s.Get("p1", "k1"); got != "" {
		t.Errorf("after Clear, Get(p1,k1) = %q, want empty", got)
	}
	// untouched entry remains
	if got := s.Get("p1", "k2"); got != "yms-dev" {
		t.Errorf("Clear should not affect siblings, Get(p1,k2) = %q", got)
	}
}

func TestProfileStoreClearLastEntryRemovesProject(t *testing.T) {
	s, path := newTempStore(t)
	s.Set("p1", "k1", "pre")
	s.Clear("p1", "k1")

	// Reload and confirm the file is well-formed and project entry gone.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	var data profileStoreData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal store: %v", err)
	}
	if _, ok := data.Projects["p1"]; ok {
		t.Errorf("expected project p1 to be removed when last entry cleared, got %#v", data.Projects)
	}
}

func TestProfileStoreIgnoresEmptyProfile(t *testing.T) {
	s, _ := newTempStore(t)
	s.Set("p1", "k1", "")
	if got := s.Get("p1", "k1"); got != "" {
		t.Errorf("Set with empty profile should be no-op, Get = %q", got)
	}
}

func TestProfileStoreIgnoresEmptyKeyOrProject(t *testing.T) {
	s, _ := newTempStore(t)
	s.Set("", "k1", "pre")
	s.Set("p1", "", "pre")
	if got := s.Get("", "k1"); got != "" {
		t.Errorf("empty project should not persist, got %q", got)
	}
	if got := s.Get("p1", ""); got != "" {
		t.Errorf("empty session_key should not persist, got %q", got)
	}
}

func TestProfileStoreRejectsInvalidProfileName(t *testing.T) {
	s, _ := newTempStore(t)
	cases := []string{
		"bad name",       // space
		"bad/name",       // slash
		"../etc/passwd",  // path traversal chars
		"bad\nname",      // newline
		"bad\tname",      // tab
		"bad;name",       // shell metachar
		"bad`name",       // backtick
		string([]byte{0x00}), // null byte
	}
	for _, c := range cases {
		s.Set("p1", "k1", c)
		if got := s.Get("p1", "k1"); got != "" {
			t.Errorf("Set(invalid %q) should be no-op, Get = %q", c, got)
		}
	}
}

func TestProfileStoreLoadBadJSONStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yms-rca-profiles.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write bad json: %v", err)
	}
	s := newProfileStore(path)
	if got := s.Get("p1", "k1"); got != "" {
		t.Errorf("expected empty store after bad-json load, got %q", got)
	}
	// Subsequent Set must succeed and overwrite the corrupt file.
	s.Set("p1", "k1", "pre")
	if got := s.Get("p1", "k1"); got != "pre" {
		t.Errorf("Set after bad-json reload failed, got %q", got)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"pre"`) {
		t.Errorf("after recovery, file should contain new entry, got: %s", string(raw))
	}
}

func TestProfileStoreLoadSkipsInvalidEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yms-rca-profiles.json")
	// Manually craft a file with one valid + one invalid profile name.
	raw := []byte(`{
		"version": 1,
		"projects": {
			"p1": {
				"k1": {"profile": "pre", "updated_at": "2026-05-15T14:36:32+08:00"},
				"k2": {"profile": "bad name", "updated_at": "2026-05-15T14:36:32+08:00"}
			}
		}
	}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := newProfileStore(path)
	if got := s.Get("p1", "k1"); got != "pre" {
		t.Errorf("valid entry not loaded, got %q", got)
	}
	if got := s.Get("p1", "k2"); got != "" {
		t.Errorf("invalid entry should be skipped, got %q", got)
	}
}

func TestProfileStoreFileMode0600(t *testing.T) {
	s, path := newTempStore(t)
	s.Set("p1", "k1", "pre")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %o, want 0600", mode)
	}
}

func TestProfileStoreVersionRoundTrip(t *testing.T) {
	s, path := newTempStore(t)
	s.Set("p1", "k1", "pre")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var data profileStoreData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Version != profileStoreVersion {
		t.Errorf("version = %d, want %d", data.Version, profileStoreVersion)
	}
}

func TestProfileStoreVersionMismatchStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yms-rca-profiles.json")
	raw := []byte(`{"version": 99, "projects": {"p1": {"k1": {"profile": "pre"}}}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := newProfileStore(path)
	if got := s.Get("p1", "k1"); got != "" {
		t.Errorf("future version should be treated as empty, got %q", got)
	}
	// Fresh Set should overwrite with current version.
	s.Set("p1", "k1", "pre")
	if got := s.Get("p1", "k1"); got != "pre" {
		t.Errorf("after overwrite Get = %q", got)
	}
}

func TestProfileStoreCreatesMissingParentDir(t *testing.T) {
	dir := t.TempDir()
	// Nested path under a directory that doesn't exist yet.
	path := filepath.Join(dir, "deeper", "nested", "yms-rca-profiles.json")
	s := newProfileStore(path)
	s.Set("p1", "k1", "pre")
	if got := s.Get("p1", "k1"); got != "pre" {
		t.Errorf("Set into missing parent failed: Get = %q", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file at %q, stat err: %v", path, err)
	}
}

func TestProfileStoreConcurrentAccess(t *testing.T) {
	s, _ := newTempStore(t)
	const goroutines = 16
	const opsPerG = 40
	done := make(chan struct{}, goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			project := fmt.Sprintf("p%d", g%4)
			for i := 0; i < opsPerG; i++ {
				sessionKey := fmt.Sprintf("k%d", i%3)
				switch i % 3 {
				case 0:
					s.Set(project, sessionKey, "pre")
				case 1:
					_ = s.Get(project, sessionKey)
				case 2:
					s.Clear(project, sessionKey)
				}
			}
		}(g)
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
	// Smoke check: store survives, file still parses.
	s.Set("survives", "yes", "pre")
	if got := s.Get("survives", "yes"); got != "pre" {
		t.Errorf("post-stress Set/Get failed: %q", got)
	}
}

func TestDefaultProfileStorePathRespectsEnvOverride(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, "custom.json")
	t.Setenv(profileStorePathEnv, custom)
	got := defaultProfileStorePath()
	if got != custom {
		t.Errorf("defaultProfileStorePath = %q, want %q", got, custom)
	}
}

func TestDefaultProfileStorePathFallsBackToHome(t *testing.T) {
	t.Setenv(profileStorePathEnv, "")
	got := defaultProfileStorePath()
	// Should end with the .cc-connect/yms-rca-profiles.json suffix unless
	// HOME is unavailable in the test env (in which case we'd see the
	// last-resort cwd-relative name).
	if !strings.HasSuffix(got, "yms-rca-profiles.json") {
		t.Errorf("default path %q should end with yms-rca-profiles.json", got)
	}
}

func TestIsValidProfileName(t *testing.T) {
	good := []string{"pre", "yms-dev", "cn-bj1", "prod_v2", "v1.0", "a"}
	bad := []string{"", "bad name", "bad/name", "../x", "bad\nname", "bad;rm", "x y", "*"}
	for _, c := range good {
		if !isValidProfileName(c) {
			t.Errorf("isValidProfileName(%q) = false, want true", c)
		}
	}
	for _, c := range bad {
		if isValidProfileName(c) {
			t.Errorf("isValidProfileName(%q) = true, want false", c)
		}
	}
}
