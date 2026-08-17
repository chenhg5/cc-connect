package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIsNewer(t *testing.T) {
	tests := []struct {
		latest, current string
		want            bool
	}{
		// Basic semver
		{"v1.2.3", "v1.2.2", true},
		{"v1.2.2", "v1.2.3", false},
		{"v1.2.3", "v1.2.3", false},
		{"v2.0.0", "v1.9.9", true},

		// Pre-release vs stable
		{"v1.2.3", "v1.2.3-beta.1", true},
		{"v1.2.3-beta.1", "v1.2.3", false},

		// Pre-release numeric ordering
		{"v1.2.3-beta.10", "v1.2.3-beta.2", true},
		{"v1.2.3-beta.2", "v1.2.3-beta.10", false},
		{"v1.2.3-beta.2", "v1.2.3-beta.2", false},

		// rc > beta lexicographically
		{"v1.2.3-rc.1", "v1.2.3-beta.9", true},

		// Dev builds always upgradeable
		{"v1.0.0", "dev", true},

		// Empty
		{"", "v1.0.0", false},
		{"v1.0.0", "", false},
	}
	for _, tt := range tests {
		got := isNewer(tt.latest, tt.current)
		if got != tt.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
		}
	}
}

func TestGetUpdateHintIfAvailable_NeverBlocks(t *testing.T) {
	origVersion := version
	defer func() { version = origVersion }()
	version = "v1.0.0"

	// Clear cache to force cache miss
	cachedLatestVersion.mu.Lock()
	cachedLatestVersion.version = ""
	cachedLatestVersion.timestamp = time.Time{}
	cachedLatestVersion.mu.Unlock()

	// getUpdateHintIfAvailable should return "" immediately on cache miss
	// (async fetch is kicked off in background but does not block)
	start := time.Now()
	hint := getUpdateHintIfAvailable()
	elapsed := time.Since(start)

	if hint != "" {
		t.Errorf("expected empty hint on cache miss, got: %q", hint)
	}
	if elapsed > 2*time.Second {
		t.Errorf("getUpdateHintIfAvailable blocked for %v, should return immediately", elapsed)
	}
}

func TestGetUpdateHintIfAvailable_UsesCache(t *testing.T) {
	origVersion := version
	defer func() { version = origVersion }()
	version = "v1.0.0"

	// Populate cache with a newer version
	cachedLatestVersion.mu.Lock()
	cachedLatestVersion.version = "v2.0.0"
	cachedLatestVersion.timestamp = time.Now()
	cachedLatestVersion.mu.Unlock()

	hint := getUpdateHintIfAvailable()
	if hint == "" {
		t.Error("expected update hint when cache has newer version")
	}

	// Populate cache with same version — should return empty
	cachedLatestVersion.mu.Lock()
	cachedLatestVersion.version = "v1.0.0"
	cachedLatestVersion.timestamp = time.Now()
	cachedLatestVersion.mu.Unlock()

	hint = getUpdateHintIfAvailable()
	if hint != "" {
		t.Errorf("expected no hint when versions match, got: %q", hint)
	}
}

func TestGetUpdateHintIfAvailable_DevSkipped(t *testing.T) {
	origVersion := version
	defer func() { version = origVersion }()
	version = "dev"

	hint := getUpdateHintIfAvailable()
	if hint != "" {
		t.Errorf("expected empty hint for dev version, got: %q", hint)
	}
}

func TestDetectUpdateInstallation_NPMGlobalPackage(t *testing.T) {
	prefix := t.TempDir()
	pkgDir := filepath.Join(prefix, "lib", "node_modules", "cc-connect-next")
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"cc-connect-next","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	got, err := detectUpdateInstallation(filepath.Join(binDir, "cc-connect-next"))
	if err != nil {
		t.Fatalf("detectUpdateInstallation: %v", err)
	}
	if got.Kind != updateInstallNPM {
		t.Fatalf("kind = %q, want %q", got.Kind, updateInstallNPM)
	}
	if got.PackageDir != pkgDir {
		t.Errorf("package dir = %q, want %q", got.PackageDir, pkgDir)
	}
	if got.NPMPrefix != prefix {
		t.Errorf("npm prefix = %q, want %q", got.NPMPrefix, prefix)
	}
}

func TestDetectUpdateInstallation_NPMWindowsStylePrefix(t *testing.T) {
	prefix := t.TempDir()
	pkgDir := filepath.Join(prefix, "node_modules", "cc-connect-next")
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"cc-connect-next","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	got, err := detectUpdateInstallation(filepath.Join(binDir, "cc-connect-next.exe"))
	if err != nil {
		t.Fatalf("detectUpdateInstallation: %v", err)
	}
	if got.Kind != updateInstallNPM {
		t.Fatalf("kind = %q, want %q", got.Kind, updateInstallNPM)
	}
	if got.NPMPrefix != prefix {
		t.Errorf("npm prefix = %q, want %q", got.NPMPrefix, prefix)
	}
}

func TestDetectUpdateInstallation_StandaloneBinary(t *testing.T) {
	execPath := filepath.Join(t.TempDir(), "bin", "cc-connect-next")
	if err := os.MkdirAll(filepath.Dir(execPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := detectUpdateInstallation(execPath)
	if err != nil {
		t.Fatalf("detectUpdateInstallation: %v", err)
	}
	if got.Kind != updateInstallStandalone {
		t.Fatalf("kind = %q, want %q", got.Kind, updateInstallStandalone)
	}
}

func TestValidateStableUpdateArgs_RejectsPrereleaseFlags(t *testing.T) {
	for _, arg := range []string{"--pre", "--beta"} {
		err := validateStableUpdateArgs([]string{arg})
		if err == nil {
			t.Fatalf("validateStableUpdateArgs(%q) succeeded, want error", arg)
		}
		if !strings.Contains(err.Error(), "stable") {
			t.Errorf("error = %q, want stable-only guidance", err)
		}
	}
	if err := validateStableUpdateArgs(nil); err != nil {
		t.Fatalf("validateStableUpdateArgs(nil): %v", err)
	}
}

func TestValidateStableRelease_RejectsPrerelease(t *testing.T) {
	for _, release := range []*githubRelease{
		{TagName: "v1.2.0-beta.1", Prerelease: true},
		{TagName: "v1.2.0-beta.1"},
		{TagName: "v1.2.0", Prerelease: true},
	} {
		if err := validateStableRelease(release); err == nil {
			t.Fatalf("validateStableRelease(%+v) succeeded, want error", release)
		}
	}
	if err := validateStableRelease(&githubRelease{TagName: "v1.2.0"}); err != nil {
		t.Fatalf("validateStableRelease(stable): %v", err)
	}
}

func TestUpdateNPMInstallation_UsesExactStableVersionAndPrefix(t *testing.T) {
	prefix := t.TempDir()
	pkgDir := filepath.Join(prefix, "lib", "node_modules", "cc-connect-next")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	packageJSON := filepath.Join(pkgDir, "package.json")
	if err := os.WriteFile(packageJSON, []byte(`{"name":"cc-connect-next","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	installation := updateInstallation{
		Kind:           updateInstallNPM,
		ExecutablePath: filepath.Join(pkgDir, "bin", "cc-connect-next"),
		NPMPrefix:      prefix,
		PackageDir:     pkgDir,
	}
	var gotName string
	var gotArgs []string
	var verifiedPath, verifiedTag string
	runner := func(name string, args ...string) error {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return os.WriteFile(packageJSON, []byte(`{"name":"cc-connect-next","version":"1.2.3"}`), 0o644)
	}

	verifier := func(path, tag string) error {
		verifiedPath = path
		verifiedTag = tag
		return nil
	}

	if err := updateNPMInstallation(installation, "v1.2.3", runner, verifier); err != nil {
		t.Fatalf("updateNPMInstallation: %v", err)
	}
	if gotName != npmExecutableName() {
		t.Errorf("command = %q, want %q", gotName, npmExecutableName())
	}
	wantArgs := []string{"install", "--global", "--prefix", installation.NPMPrefix, "cc-connect-next@1.2.3"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("args = %#v, want %#v", gotArgs, wantArgs)
	}
	if verifiedPath != installation.ExecutablePath || verifiedTag != "v1.2.3" {
		t.Errorf("verified (%q, %q), want (%q, %q)", verifiedPath, verifiedTag, installation.ExecutablePath, "v1.2.3")
	}
}

func TestVersionOutputMatches_RequiresExactStableVersion(t *testing.T) {
	if !versionOutputMatches("cc-connect-next v1.2.3\ncommit: abc\n", "v1.2.3") {
		t.Fatal("exact stable version did not match")
	}
	for _, output := range []string{
		"cc-connect-next v1.2.3-beta.1\n",
		"cc-connect-next v1.2.4\n",
		"other v1.2.3\n",
	} {
		if versionOutputMatches(output, "v1.2.3") {
			t.Errorf("unexpected match for %q", output)
		}
	}
}

func TestParseReleaseChecksum_ExactAssetOnly(t *testing.T) {
	asset := "cc-connect-next-v1.2.3-darwin-arm64.tar.gz"
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("expected archive")))
	other := fmt.Sprintf("%x", sha256.Sum256([]byte("other archive")))
	manifest := fmt.Sprintf("%s  other.tar.gz\n%s  %s\n", other, want, asset)

	got, err := parseReleaseChecksum(manifest, asset)
	if err != nil {
		t.Fatalf("parseReleaseChecksum: %v", err)
	}
	if got != want {
		t.Errorf("checksum = %q, want %q", got, want)
	}
	if _, err := parseReleaseChecksum(manifest, "missing.tar.gz"); err == nil {
		t.Fatal("missing asset succeeded, want error")
	}
}

func TestVerifyReleaseChecksum_RefusesMismatch(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "release.tar.gz")
	if err := os.WriteFile(archive, []byte("release archive"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("release archive")))
	if err := verifyReleaseChecksum(archive, want); err != nil {
		t.Fatalf("verifyReleaseChecksum(valid): %v", err)
	}
	bad := fmt.Sprintf("%x", sha256.Sum256([]byte("tampered archive")))
	if err := verifyReleaseChecksum(archive, bad); err == nil {
		t.Fatal("verifyReleaseChecksum(mismatch) succeeded, want error")
	}
}

func TestReplaceExecutable_RestoresBackupWhenVerificationFails(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cc-connect-next")
	source := filepath.Join(dir, "new-cc-connect-next")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.WriteFile(source, []byte("new binary"), 0o755); err != nil {
		t.Fatalf("write source: %v", err)
	}

	err := replaceExecutable(target, source, func(path string) error {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if string(content) != "new binary" {
			return fmt.Errorf("verifier saw %q", content)
		}
		return fmt.Errorf("simulated version mismatch")
	})
	if err == nil {
		t.Fatal("replaceExecutable succeeded, want verification error")
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read restored target: %v", err)
	}
	if string(content) != "old binary" {
		t.Errorf("restored target = %q, want old binary", content)
	}
	if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
		t.Errorf("backup remains after restore: %v", err)
	}
}
