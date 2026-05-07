//go:build darwin

package daemon

import (
	"encoding/xml"
	"fmt"
	"io"
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

func TestBuildPlist_IncludesEnvExtra(t *testing.T) {
	cfg := Config{
		BinaryPath: "/opt/cc-connect/cc-connect",
		WorkDir:    "/tmp/wd",
		LogFile:    "/tmp/log",
		LogMaxSize: 10485760,
		EnvPATH:    "/usr/bin",
		EnvExtra: map[string]string{
			"HTTPS_PROXY": "http://127.0.0.1:7890",
			// '&' inside a proxy query string must be XML-escaped or the
			// resulting plist is unparseable.
			"HTTP_PROXY": "http://1.2.3.4:8080?a=1&b=2",
			"NO_PROXY":   "localhost,127.0.0.1",
		},
	}
	xml := buildPlist(cfg)

	for _, want := range []string{
		"<key>HTTPS_PROXY</key>",
		"<string>http://127.0.0.1:7890</string>",
		"<key>HTTP_PROXY</key>",
		"<string>http://1.2.3.4:8080?a=1&amp;b=2</string>",
		"<key>NO_PROXY</key>",
		"<string>localhost,127.0.0.1</string>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("plist missing %q\nfull plist:\n%s", want, xml)
		}
	}

	// Raw '&' would make the plist invalid XML; the only '&' allowed in the
	// document body is the start of an entity reference like &amp;.
	if strings.Contains(xml, "a=1&b=2") {
		t.Errorf("plist contains unescaped '&' from proxy query string:\n%s", xml)
	}

	// Keys must be emitted in deterministic (sorted) order so reinstalls do
	// not churn the on-disk plist.
	idxHTTPS := strings.Index(xml, "<key>HTTPS_PROXY</key>")
	idxHTTP := strings.Index(xml, "<key>HTTP_PROXY</key>")
	idxNO := strings.Index(xml, "<key>NO_PROXY</key>")
	if !(idxHTTPS < idxHTTP && idxHTTP < idxNO) {
		t.Errorf("EnvExtra keys not sorted: HTTPS=%d HTTP=%d NO=%d", idxHTTPS, idxHTTP, idxNO)
	}
}

func TestBuildPlist_NoEnvExtraIsValid(t *testing.T) {
	cfg := Config{
		BinaryPath: "/opt/cc-connect/cc-connect",
		WorkDir:    "/tmp/wd",
		LogFile:    "/tmp/log",
		LogMaxSize: 10485760,
		EnvPATH:    "/usr/bin",
	}
	got := buildPlist(cfg)
	// Missing the EnvironmentVariables dict closing tag would mean the
	// EnvExtra splice broke the template.
	if !strings.Contains(got, "<key>PATH</key>\n\t\t<string>/usr/bin</string>\n\t</dict>") {
		t.Errorf("plist EnvironmentVariables dict not properly closed:\n%s", got)
	}
}

// TestBuildPlist_XMLWellFormed walks the generated plist with encoding/xml to
// catch any future change that emits unescaped XML special characters
// (notably '&' from proxy query strings), which would silently produce a
// plist that launchd refuses to bootstrap.
func TestBuildPlist_XMLWellFormed(t *testing.T) {
	cfg := Config{
		BinaryPath: "/opt/cc-connect/cc-connect",
		WorkDir:    "/tmp/wd",
		LogFile:    "/tmp/log",
		LogMaxSize: 10485760,
		EnvPATH:    "/usr/bin",
		EnvExtra: map[string]string{
			"HTTP_PROXY":  "http://1.2.3.4:8080?a=1&b=2",
			"HTTPS_PROXY": "http://127.0.0.1:7890",
		},
	}
	dec := xml.NewDecoder(strings.NewReader(buildPlist(cfg)))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("plist failed XML parse: %v", err)
		}
	}
}
