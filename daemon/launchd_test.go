//go:build darwin

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPlist_KeepAliveDoesNotRestartOnCleanExit(t *testing.T) {
	cfg := Config{
		BinaryPath: "/opt/cc-connect/cc-connect",
		WorkDir:    "/tmp/wd",
		LogFile:    "/tmp/log",
		LogMaxSize: 10485760,
		EnvPATH:    "/usr/bin",
	}
	xml := buildPlist(cfg)
	if !strings.Contains(xml, "<key>SuccessfulExit</key>") {
		t.Fatal("plist should use KeepAlive dict with SuccessfulExit so exit 0 does not respawn")
	}
	// Boolean KeepAlive causes launchd to restart after every exit, including SIGTERM shutdown.
	if strings.Contains(xml, "<key>KeepAlive</key>\n\t<true/>") {
		t.Fatal("plist must not use boolean KeepAlive true")
	}
	if !strings.Contains(xml, "<key>LimitLoadToSessionType</key>") ||
		!strings.Contains(xml, "<string>Aqua</string>") ||
		!strings.Contains(xml, "<string>Background</string>") {
		t.Fatal("plist should allow both Aqua and Background sessions")
	}
}

func TestPreferredLaunchdDomainFallsBackToUserWhenGUIDomainUnavailable(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	guiDomain := launchdGUIDomain()
	userDomain := launchdUserDomain()
	runLaunchctl = func(args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "print" && args[1] == guiDomain {
			return "Bootstrap failed: 125: Domain does not support specified action", fmt.Errorf("exit status 125")
		}
		if len(args) >= 2 && args[0] == "print" && args[1] == userDomain {
			return "subsystem", nil
		}
		return "", nil
	}

	if got := preferredLaunchdDomain(); got != userDomain {
		t.Fatalf("preferredLaunchdDomain() = %q, want %q", got, userDomain)
	}
}

func TestLaunchdStatusUsesUserDomainWhenGUIDomainUnavailable(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	guiDomain := launchdGUIDomain()
	userDomain := launchdUserDomain()
	guiTarget := launchdTarget(guiDomain)
	userTarget := launchdTarget(userDomain)
	runLaunchctl = func(args ...string) (string, error) {
		if len(args) < 2 || args[0] != "print" {
			return "", nil
		}
		switch args[1] {
		case guiDomain, guiTarget:
			return "Bootstrap failed: 125: Domain does not support specified action", fmt.Errorf("exit status 125")
		case userDomain:
			return "subsystem", nil
		case userTarget:
			return "pid = 4321\nstate = running", nil
		default:
			return "", fmt.Errorf("unexpected target %q", args[1])
		}
	}

	mgr := &launchdManager{}
	st, err := mgr.Status()
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !st.Running {
		t.Fatal("Status().Running = false, want true")
	}
	if st.PID != 4321 {
		t.Fatalf("Status().PID = %d, want 4321", st.PID)
	}
}

func TestRestartPrefersGUIDomainWhenAvailable(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", dir)
	if origHome != "" {
		t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })
	}
	plistPath := launchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(plistPath, []byte("plist"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	guiDomain := launchdGUIDomain()
	userDomain := launchdUserDomain()
	guiTarget := launchdTarget(guiDomain)
	userTarget := launchdTarget(userDomain)

	var calls []string
	runLaunchctl = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) < 2 {
			return "", nil
		}
		switch args[0] {
		case "print":
			switch args[1] {
			case guiDomain:
				return "subsystem", nil
			case guiTarget:
				return "Bootstrap failed: 113: Could not find service", fmt.Errorf("exit status 113")
			case userTarget:
				return "pid = 4321\nstate = running", nil
			default:
				return "", fmt.Errorf("unexpected print target %q", args[1])
			}
		case "bootout":
			return "", nil
		case "bootstrap":
			if args[1] != guiDomain {
				t.Fatalf("bootstrap domain = %q, want %q", args[1], guiDomain)
			}
			return "", nil
		case "kickstart":
			if args[len(args)-1] != guiTarget {
				t.Fatalf("kickstart target = %q, want %q", args[len(args)-1], guiTarget)
			}
			return "", nil
		default:
			return "", nil
		}
	}

	mgr := &launchdManager{}
	if err := mgr.Restart(); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	if !containsCall(calls, "bootstrap "+guiDomain+" "+plistPath) {
		t.Fatalf("expected bootstrap to gui domain, calls = %#v", calls)
	}
	if !containsCall(calls, "kickstart -kp "+guiTarget) {
		t.Fatalf("expected kickstart to gui target, calls = %#v", calls)
	}
}

func TestRestartKeepsUserDomainWhenGUIDomainUnavailable(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", dir)
	if origHome != "" {
		t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })
	}
	plistPath := launchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(plistPath, []byte("plist"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	guiDomain := launchdGUIDomain()
	userDomain := launchdUserDomain()
	userTarget := launchdTarget(userDomain)

	var calls []string
	runLaunchctl = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) < 2 {
			return "", nil
		}
		switch args[0] {
		case "print":
			switch args[1] {
			case guiDomain:
				return "Bootstrap failed: 125: Domain does not support specified action", fmt.Errorf("exit status 125")
			case userDomain:
				return "subsystem", nil
			case userTarget:
				return "pid = 4321\nstate = running", nil
			default:
				return "", fmt.Errorf("unexpected print target %q", args[1])
			}
		case "bootout":
			return "", nil
		case "bootstrap":
			if args[1] != userDomain {
				t.Fatalf("bootstrap domain = %q, want %q", args[1], userDomain)
			}
			return "", nil
		case "kickstart":
			if args[len(args)-1] != userTarget {
				t.Fatalf("kickstart target = %q, want %q", args[len(args)-1], userTarget)
			}
			return "", nil
		default:
			return "", nil
		}
	}

	mgr := &launchdManager{}
	if err := mgr.Restart(); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	if !containsCall(calls, "bootstrap "+userDomain+" "+plistPath) {
		t.Fatalf("expected bootstrap to user domain, calls = %#v", calls)
	}
	if !containsCall(calls, "kickstart -kp "+userTarget) {
		t.Fatalf("expected kickstart to user target, calls = %#v", calls)
	}
}

func TestBuildPlist_IncludesEnvExtraSorted(t *testing.T) {
	cfg := Config{
		BinaryPath: "/opt/cc/cc",
		WorkDir:    "/tmp/wd",
		LogFile:    "/tmp/log",
		LogMaxSize: 1024,
		EnvPATH:    "/usr/bin",
		EnvExtra: map[string]string{
			"NO_PROXY":           "yonyoucloud.com,yyuap.com",
			"IUAPYYS_MCP_TOKEN":  "tok",
			"HTTPS_PROXY":        "http://1.2.3.4:8080",
		},
	}
	xml := buildPlist(cfg)
	// Sorted order: HTTPS_PROXY, IUAPYYS_MCP_TOKEN, NO_PROXY.
	wantOrder := []string{
		"<key>HTTPS_PROXY</key>",
		"<key>IUAPYYS_MCP_TOKEN</key>",
		"<key>NO_PROXY</key>",
	}
	lastIdx := -1
	for _, k := range wantOrder {
		idx := strings.Index(xml, k)
		if idx < 0 {
			t.Fatalf("plist missing %s; xml=%s", k, xml)
		}
		if idx < lastIdx {
			t.Fatalf("plist EnvExtra keys not in sorted order; first offender %s; xml=%s", k, xml)
		}
		lastIdx = idx
	}
	if !strings.Contains(xml, "<string>tok</string>") {
		t.Fatalf("value missing for IUAPYYS_MCP_TOKEN; xml=%s", xml)
	}
}

func TestBuildPlist_RejectsInvalidEnvName(t *testing.T) {
	cfg := Config{
		BinaryPath: "/x", WorkDir: "/y", LogFile: "/l", LogMaxSize: 1, EnvPATH: "/p",
		EnvExtra: map[string]string{
			"FOO BAR": "v",
			"1FOO":    "v",
			"OK":      "fine",
		},
	}
	xml := buildPlist(cfg)
	if strings.Contains(xml, "FOO BAR") || strings.Contains(xml, "1FOO") {
		t.Fatalf("invalid env names leaked into plist: %s", xml)
	}
	if !strings.Contains(xml, "<key>OK</key>") {
		t.Fatalf("OK should remain: %s", xml)
	}
}

func TestBuildPlist_EscapesXMLInValue(t *testing.T) {
	cfg := Config{
		BinaryPath: "/x", WorkDir: "/y", LogFile: "/l", LogMaxSize: 1, EnvPATH: "/p",
		EnvExtra: map[string]string{
			"TRICKY": `a<b&c"d'e`,
		},
	}
	xml := buildPlist(cfg)
	// Must not contain raw <, & in TRICKY value.
	idx := strings.Index(xml, "<key>TRICKY</key>")
	if idx < 0 {
		t.Fatalf("TRICKY missing: %s", xml)
	}
	// Take a slice starting after <key>TRICKY</key>; the next <string>...</string>
	// must contain only entity-escaped specials.
	tail := xml[idx:]
	endStr := strings.Index(tail, "</string>")
	if endStr < 0 {
		t.Fatalf("malformed plist: %s", xml)
	}
	chunk := tail[:endStr]
	for _, bad := range []string{"a<b", "b&c"} {
		if strings.Contains(chunk, bad) {
			t.Errorf("value not escaped: %s", chunk)
		}
	}
	if !strings.Contains(chunk, "&lt;") {
		t.Errorf("< not escaped: %s", chunk)
	}
	if !strings.Contains(chunk, "&amp;") {
		t.Errorf("& not escaped: %s", chunk)
	}
}

func TestBuildPlist_SkipsEmptyValuesAndTemplateOwnedKeys(t *testing.T) {
	cfg := Config{
		BinaryPath: "/x", WorkDir: "/y", LogFile: "/l", LogMaxSize: 1, EnvPATH: "/expected-path",
		EnvExtra: map[string]string{
			"EMPTY":           "",
			"PATH":            "/should-not-override",
			"CC_LOG_FILE":     "/should-not-override",
			"CC_LOG_MAX_SIZE": "999999",
			"REAL":            "ok",
		},
	}
	xml := buildPlist(cfg)
	if strings.Contains(xml, "<key>EMPTY</key>") {
		t.Errorf("empty value should be skipped: %s", xml)
	}
	if strings.Contains(xml, "/should-not-override") {
		t.Errorf("template-owned key was overridden: %s", xml)
	}
	if !strings.Contains(xml, "<string>/expected-path</string>") {
		t.Errorf("expected template PATH preserved: %s", xml)
	}
	if !strings.Contains(xml, "<key>REAL</key>") {
		t.Errorf("REAL key missing: %s", xml)
	}
}

func TestInstallLaunchd_WritesPlistAt0600(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })
	runLaunchctl = func(args ...string) (string, error) { return "", nil }

	mgr := &launchdManager{}
	cfg := Config{
		BinaryPath: "/bin/true",
		WorkDir:    t.TempDir(),
		LogFile:    filepath.Join(t.TempDir(), "cc.log"),
		LogMaxSize: 1024,
		EnvPATH:    "/usr/bin",
		EnvExtra:   map[string]string{"NO_PROXY": "yonyoucloud.com"},
	}
	if err := mgr.Install(cfg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	info, err := os.Stat(launchdPlistPath())
	if err != nil {
		t.Fatalf("stat plist: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("plist mode = %o, want 0600", info.Mode().Perm())
	}
}

// TestInstallLaunchd_TightensExistingPlistFrom0644 covers the upgrade
// path: a user from an earlier cc-connect version may already have a
// 0644 plist on disk; os.WriteFile would truncate-in-place and *keep*
// the old permissions, leaving captured token values world-readable.
// Install must explicitly tighten the existing file to 0600.
func TestInstallLaunchd_TightensExistingPlistFrom0644(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })
	runLaunchctl = func(args ...string) (string, error) { return "", nil }

	plistPath := launchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Seed a legacy 0644 plist as a prior cc-connect version would have left it.
	if err := os.WriteFile(plistPath, []byte("<plist>old</plist>\n"), 0o644); err != nil {
		t.Fatalf("seed legacy plist: %v", err)
	}
	if info, _ := os.Stat(plistPath); info.Mode().Perm() != 0o644 {
		t.Fatalf("precondition: seeded file mode = %o, want 0644", info.Mode().Perm())
	}

	mgr := &launchdManager{}
	cfg := Config{
		BinaryPath: "/bin/true",
		WorkDir:    t.TempDir(),
		LogFile:    filepath.Join(t.TempDir(), "cc.log"),
		LogMaxSize: 1024,
		EnvPATH:    "/usr/bin",
		EnvExtra:   map[string]string{"IUAPYYS_MCP_TOKEN": "captured"},
	}
	if err := mgr.Install(cfg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	info, err := os.Stat(plistPath)
	if err != nil {
		t.Fatalf("stat after Install: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("plist mode after reinstall = %o, want 0600", info.Mode().Perm())
	}
}

// TestLaunchdUninstall_RemovesPlist guards against captured-secret
// residue: a `cc-connect daemon install` may have baked an
// IUAPYYS_MCP_TOKEN value into the plist; uninstall must delete the
// file, not just bootout the service.
func TestLaunchdUninstall_RemovesPlist(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })
	runLaunchctl = func(args ...string) (string, error) { return "", nil }

	plistPath := launchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(plistPath, []byte("<plist>fake</plist>\n"), 0o600); err != nil {
		t.Fatalf("seed plist: %v", err)
	}

	mgr := &launchdManager{}
	if err := mgr.Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("plist must be removed after Uninstall; stat err=%v", err)
	}
}

func TestLaunchdUninstall_IdempotentWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })
	runLaunchctl = func(args ...string) (string, error) { return "", nil }

	mgr := &launchdManager{}
	if err := mgr.Uninstall(); err != nil {
		t.Fatalf("Uninstall on absent install must be a no-op: %v", err)
	}
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}
