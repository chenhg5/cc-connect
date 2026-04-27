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

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}
