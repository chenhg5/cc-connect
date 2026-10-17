package main

// Tests for the data_dir / socket-path resolution helpers added for
// Issue #1719. The helpers live in cmd/cc-connect/data_dir_resolve.go
// and are exercised by send.go, cron.go, timer.go, relay.go,
// sessions.go and session_id.go.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultDataDir(t *testing.T) {
	// Save and restore HOME so the test doesn't leak into other tests
	// (defaultDataDir consults os.UserHomeDir which honours HOME).
	t.Setenv("HOME", "/tmp/fake-home-1719")
	got := defaultDataDir()
	want := filepath.Join("/tmp/fake-home-1719", ".cc-connect")
	if got != want {
		t.Fatalf("defaultDataDir() = %q, want %q", got, want)
	}
}

func TestResolveSocketPath(t *testing.T) {
	got := resolveSocketPath("/var/lib/cc-connect")
	want := filepath.Join("/var/lib/cc-connect", "run", "api.sock")
	if got != want {
		t.Fatalf("resolveSocketPath() = %q, want %q", got, want)
	}
}

func TestResolveDataDir_PriorityOrder(t *testing.T) {
	// 1. Explicit --data-dir flag wins over everything.
	t.Setenv("CC_DATA_DIR", "/from/env")
	t.Setenv("HOME", "/tmp/fake-home-1719")
	got := resolveDataDir("/from/flag", "")
	if got != "/from/flag" {
		t.Fatalf("explicit --data-dir should win, got %q", got)
	}

	// 2. CC_DATA_DIR env wins when --data-dir is empty.
	got = resolveDataDir("", "")
	if got != "/from/env" {
		t.Fatalf("CC_DATA_DIR should win when --data-dir is empty, got %q", got)
	}

	// 3. Default ~/.cc-connect when nothing is set.
	t.Setenv("CC_DATA_DIR", "")
	got = resolveDataDir("", "")
	want := filepath.Join("/tmp/fake-home-1719", ".cc-connect")
	if got != want {
		t.Fatalf("default data_dir should be ~/.cc-connect, got %q want %q", got, want)
	}
}

func TestResolveDataDir_FromConfigFile(t *testing.T) {
	t.Setenv("CC_DATA_DIR", "")
	t.Setenv("HOME", "/tmp/fake-home-1719")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	// config.LoadPermissive still requires at least one [[projects]] entry
	// (it only skips the per-project platform check), so include a minimal
	// one in the fixture.
	body := `data_dir = "/srv/cc-connect-data"
[[projects]]
name = "demo"
[projects.agent]
type = "claudecode"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got := resolveDataDir("", cfgPath)
	if got != "/srv/cc-connect-data" {
		t.Fatalf("resolveDataDir from --config should read data_dir, got %q", got)
	}
}

func TestResolveDataDir_MissingConfigFile(t *testing.T) {
	// Best-effort: if --config is set but the file is missing, fall
	// through to the default rather than failing the whole subcommand.
	// The reasoning is documented in resolveDataDir's doc comment.
	t.Setenv("CC_DATA_DIR", "")
	t.Setenv("HOME", "/tmp/fake-home-1719")
	got := resolveDataDir("", "/nonexistent/path/config.toml")
	want := filepath.Join("/tmp/fake-home-1719", ".cc-connect")
	if got != want {
		t.Fatalf("missing --config should fall through to default, got %q want %q", got, want)
	}
}

func TestSearchDirs_DeduplicatesAndOrdersByPriority(t *testing.T) {
	t.Setenv("CC_DATA_DIR", "")
	t.Setenv("HOME", "/tmp/fake-home-1719")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	body := `data_dir = "/srv/cc-connect-data"
[[projects]]
name = "demo"
[projects.agent]
type = "claudecode"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	dirs := searchDirs("/from/flag", cfgPath)

	// 1. --data-dir flag is first
	if dirs[0] != "/from/flag" {
		t.Fatalf("searchDirs[0] = %q, want /from/flag", dirs[0])
	}
	// 2. config file's data_dir appears in the list
	// 3. ~/.cc-connect must always be present as last fallback
	wantDefault := filepath.Join("/tmp/fake-home-1719", ".cc-connect")
	if !containsDir(dirs, wantDefault) {
		t.Fatalf("searchDirs missing default %q in %v", wantDefault, dirs)
	}
	if !containsDir(dirs, "/srv/cc-connect-data") {
		t.Fatalf("searchDirs missing config data_dir in %v", dirs)
	}
	// No duplicates
	seen := map[string]bool{}
	for _, d := range dirs {
		if seen[d] {
			t.Fatalf("searchDirs contains duplicate %q in %v", d, dirs)
		}
		seen[d] = true
	}
}

func TestSearchDirs_IncludesEnvVar(t *testing.T) {
	t.Setenv("CC_DATA_DIR", "/from/env")
	t.Setenv("HOME", "/tmp/fake-home-1719")
	dirs := searchDirs("", "")
	if !containsDir(dirs, "/from/env") {
		t.Fatalf("searchDirs missing CC_DATA_DIR value: %v", dirs)
	}
}

func TestPrintSocketNotFound_ContainsUsefulHints(t *testing.T) {
	// The diagnostic must show the socket path that was tried, the
	// resolved data_dir, the candidate list, and a recovery hint.
	// This is the contract operators rely on to triage Docker / systemd
	// issues — see Issue #1719.
	var buf bytes.Buffer
	printSocketNotFound(&buf, "/var/lib/cc-connect", "/etc/cc-connect.toml", "/var/lib/cc-connect/run/api.sock")
	out := buf.String()

	wants := []string{
		"cc-connect is not running",
		"/var/lib/cc-connect/run/api.sock", // tried socket
		"/var/lib/cc-connect",              // active data_dir
		"/etc/cc-connect.toml",             // config path
		"Troubleshooting",                  // recovery hint
		"CC_DATA_DIR",                      // env-var hint
		"--force",                          // crash recovery hint
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Fatalf("printSocketNotFound output missing %q\nfull output:\n%s", w, out)
		}
	}
}

func TestPrintSocketNotFound_NoConfigStillUseful(t *testing.T) {
	// When --config is empty (the common Docker / nohup case) the
	// diagnostic must still mention the home-dir fallback so the
	// operator can spot a config-less daemon.
	var buf bytes.Buffer
	printSocketNotFound(&buf, "/home/me/.cc-connect", "", "/home/me/.cc-connect/run/api.sock")
	out := buf.String()
	if !strings.Contains(out, "cc-connect is not running") {
		t.Fatalf("missing 'cc-connect is not running' in output: %s", out)
	}
	if !strings.Contains(out, "/home/me/.cc-connect") {
		t.Fatalf("missing active data_dir in output: %s", out)
	}
}
