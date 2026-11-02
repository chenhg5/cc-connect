//go:build darwin

package daemon

import (
	"encoding/xml"
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
	// launchd.plist(5): SuccessfulExit=true means restart ONLY after a successful
	// (exit 0) exit; false means restart ONLY after an unsuccessful exit. cc-connect
	// returns 0 on graceful SIGTERM shutdown but a non-zero status on crash, so the
	// daemon's "restart on failure but not on graceful stop" intent maps to
	// SuccessfulExit=false. The previous wiring used <true/>, which was the inverse:
	// it respawned after every clean SIGTERM shutdown and did NOT recover from
	// crashes. Pin the correct value here so a future edit can't silently re-invert
	// it.
	if !strings.Contains(xml, "<key>SuccessfulExit</key>\n\t\t<false/>") {
		t.Fatalf("plist must set SuccessfulExit=false so crashes restart and clean SIGTERM does not respawn; got:\n%s", xml)
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

	// Status reports Installed from the presence of a plist, so it has to be one
	// this test owns rather than whatever the machine has installed.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	seedLaunchdPlist(t, launchdPlistPath())

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

func TestLaunchdStatusRecognizesLegacyRetryLabel(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	t.Setenv("HOME", dir)

	guiDomain := launchdGUIDomain()
	userDomain := launchdUserDomain()
	legacyGUI := legacyLaunchdTarget(guiDomain)
	legacyUser := legacyLaunchdTarget(userDomain)
	legacyPlist := legacyLaunchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(legacyPlist), 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(legacyPlist, []byte("plist"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	runLaunchctl = func(args ...string) (string, error) {
		if len(args) < 2 || args[0] != "print" {
			return "", nil
		}
		switch args[1] {
		case guiDomain, launchdGUIDomain() + "/" + launchdLabel:
			return "Bootstrap failed: 125: Domain does not support specified action", fmt.Errorf("exit status 125")
		case userDomain:
			return "subsystem", nil
		case legacyGUI, legacyUser:
			return "\tstate = running\n\tpid = 9001", nil
		default:
			return "", fmt.Errorf("unexpected target %q", args[1])
		}
	}

	mgr := &launchdManager{}
	st, err := mgr.Status()
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !st.Installed {
		t.Fatal("Status().Installed = false, want true")
	}
	if !st.Running {
		t.Fatal("Status().Running = false, want true")
	}
	if st.PID != 9001 {
		t.Fatalf("Status().PID = %d, want 9001", st.PID)
	}
}

func TestLaunchdStatusIgnoresNestedActiveState(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	t.Setenv("HOME", dir)

	guiDomain := launchdGUIDomain()
	userDomain := launchdUserDomain()
	currentGUI := launchdTarget(guiDomain)
	currentUser := launchdTarget(userDomain)
	currentPlist := launchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(currentPlist), 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(currentPlist, []byte("plist"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	runLaunchctl = func(args ...string) (string, error) {
		if len(args) < 2 || args[0] != "print" {
			return "", nil
		}
		switch args[1] {
		case guiDomain, userDomain:
			return "subsystem", nil
		case currentGUI, currentUser:
			return "state = spawn scheduled\n\t\tstate = running\n\t\tpid = 999", nil
		default:
			return "", fmt.Errorf("unexpected target %q", args[1])
		}
	}

	mgr := &launchdManager{}
	st, err := mgr.Status()
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !st.Installed {
		t.Fatal("Status().Installed = false, want true")
	}
	if st.Running {
		t.Fatalf("Status().Running = true, want false")
	}
	if st.PID != 0 {
		t.Fatalf("Status().PID = %d, want 0", st.PID)
	}
}

func seedLaunchdPlist(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte("plist"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// Stop must reconcile both labels: returning after the first successful bootout
// (the current one) leaves a loaded legacy agent running.
func TestLaunchdStopStopsBothTheCurrentAndLegacyLabels(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	guiDomain := launchdGUIDomain()
	currentGUI := launchdTarget(guiDomain)
	legacyGUI := legacyLaunchdTarget(guiDomain)

	var calls []string
	runLaunchctl = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) >= 2 && args[0] == "print" && args[1] == guiDomain {
			return "subsystem", nil
		}
		// Every bootout succeeds: a Stop that stops at the first success would
		// never reach the legacy label.
		return "", nil
	}

	mgr := &launchdManager{}
	if err := mgr.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !containsCall(calls, "bootout "+currentGUI) {
		t.Fatalf("current label was not stopped; calls = %#v", calls)
	}
	if !containsCall(calls, "bootout "+legacyGUI) {
		t.Fatalf("legacy label was not stopped; calls = %#v", calls)
	}
}

// current loaded-but-stopped while legacy is running: reading only the current
// label reports the daemon as stopped/PID 0 even though it is serving.
func TestLaunchdStatusFallsBackToRunningLegacyWhenCurrentIsStopped(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	t.Setenv("HOME", dir)

	guiDomain := launchdGUIDomain()
	userDomain := launchdUserDomain()
	currentGUI := launchdTarget(guiDomain)
	currentUser := launchdTarget(userDomain)
	legacyGUI := legacyLaunchdTarget(guiDomain)
	legacyUser := legacyLaunchdTarget(userDomain)

	seedLaunchdPlist(t, launchdPlistPath())

	runLaunchctl = func(args ...string) (string, error) {
		if len(args) < 2 || args[0] != "print" {
			return "", nil
		}
		switch args[1] {
		case guiDomain, userDomain:
			return "subsystem", nil
		case currentGUI, currentUser:
			return "state = spawn scheduled\n\t\tstate = active", nil
		case legacyGUI, legacyUser:
			return "state = running\npid = 9001", nil
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
		t.Fatal("Status().Running = false, want true (legacy agent is running)")
	}
	if st.PID != 9001 {
		t.Fatalf("Status().PID = %d, want the running legacy pid 9001", st.PID)
	}
}

// legacy-only installation: Start has no current plist to bootstrap, so it must
// not boot out the only working service before failing.
func TestLaunchdStartKeepsLegacyWhenCurrentServiceIsNotInstalled(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	t.Setenv("HOME", dir)

	guiDomain := launchdGUIDomain()
	userDomain := launchdUserDomain()
	currentGUI := launchdTarget(guiDomain)
	currentUser := launchdTarget(userDomain)
	legacyGUI := legacyLaunchdTarget(guiDomain)
	legacyUser := legacyLaunchdTarget(userDomain)

	seedLaunchdPlist(t, legacyLaunchdPlistPath())

	var calls []string
	runLaunchctl = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) < 2 {
			return "", nil
		}
		if args[0] != "print" {
			return "", nil
		}
		switch args[1] {
		case guiDomain, userDomain:
			return "subsystem", nil
		case currentGUI, currentUser:
			return "Could not find service", fmt.Errorf("exit status 113")
		case legacyGUI, legacyUser:
			return "state = running\npid = 9001", nil
		default:
			return "", fmt.Errorf("unexpected target %q", args[1])
		}
	}

	mgr := &launchdManager{}
	if err := mgr.Start(); err == nil {
		t.Fatal("Start() = nil, want an error because the current service is not installed")
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "bootout ") {
			t.Fatalf("Start() tore down the legacy service before it could start the current one: %#v", calls)
		}
	}
	if _, err := os.Stat(legacyLaunchdPlistPath()); err != nil {
		t.Fatalf("legacy plist was removed by a failed Start: %v", err)
	}
}

// Upgrade path: the current service is installed but not loaded yet, legacy is
// running. Start must bootstrap/kickstart current first and only then retire
// legacy.
func TestLaunchdStartMigratesFromLegacyOnceCurrentIsInstalled(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	t.Setenv("HOME", dir)

	guiDomain := launchdGUIDomain()
	userDomain := launchdUserDomain()
	currentGUI := launchdTarget(guiDomain)
	currentUser := launchdTarget(userDomain)
	legacyGUI := legacyLaunchdTarget(guiDomain)
	legacyUser := legacyLaunchdTarget(userDomain)

	seedLaunchdPlist(t, launchdPlistPath())
	seedLaunchdPlist(t, legacyLaunchdPlistPath())

	var calls []string
	runLaunchctl = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) >= 2 && args[0] == "print" {
			switch args[1] {
			case guiDomain, userDomain:
				return "subsystem", nil
			case currentGUI, currentUser:
				return "Could not find service", fmt.Errorf("exit status 113")
			case legacyGUI, legacyUser:
				return "state = running\npid = 9001", nil
			}
		}
		return "", nil
	}

	mgr := &launchdManager{}
	if err := mgr.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !containsCall(calls, "bootstrap "+guiDomain+" "+launchdPlistPath()) {
		t.Fatalf("Start() did not bootstrap the current service; calls = %#v", calls)
	}
	// bootstrap succeeds, so the service starts via RunAtLoad; only kickstart
	// would be needed had it been bootstrapped already.
	if !containsCall(calls, "bootout "+legacyGUI) {
		t.Fatalf("Start() did not retire the legacy service; calls = %#v", calls)
	}
	if _, err := os.Stat(legacyLaunchdPlistPath()); !os.IsNotExist(err) {
		t.Fatalf("legacy plist still exists after a successful Start: %v", err)
	}
}

// Both labels loaded, current stopped: kickstart current, then drop legacy.
func TestLaunchdStartRetiresLegacyWhenCurrentIsAlreadyLoaded(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	t.Setenv("HOME", dir)

	guiDomain := launchdGUIDomain()
	currentGUI := launchdTarget(guiDomain)
	legacyGUI := legacyLaunchdTarget(guiDomain)

	seedLaunchdPlist(t, launchdPlistPath())
	seedLaunchdPlist(t, legacyLaunchdPlistPath())

	var calls []string
	runLaunchctl = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) >= 2 && args[0] == "print" {
			switch args[1] {
			case guiDomain:
				return "subsystem", nil
			case currentGUI:
				return "state = spawn scheduled\n\t\tstate = active", nil
			case legacyGUI:
				return "state = running\npid = 9001", nil
			}
		}
		return "", nil
	}

	mgr := &launchdManager{}
	if err := mgr.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !containsCall(calls, "kickstart -kp "+currentGUI) {
		t.Fatalf("Start() did not kickstart the loaded current service; calls = %#v", calls)
	}
	if !containsCall(calls, "bootout "+legacyGUI) {
		t.Fatalf("Start() left the legacy service loaded; calls = %#v", calls)
	}
}

// With only the legacy service loaded and the current plist missing, a restart must
// still restart the daemon: it may not unload anything, and it must keep the legacy
// plist the loaded service would need to come back up.
func TestRestartRestartsTheLoadedLegacyServiceWhenTheCurrentPlistIsMissing(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	t.Setenv("HOME", dir)

	guiDomain := launchdGUIDomain()
	userDomain := launchdUserDomain()
	legacyGUI := legacyLaunchdTarget(guiDomain)
	legacyUser := legacyLaunchdTarget(userDomain)

	seedLaunchdPlist(t, legacyLaunchdPlistPath())

	var calls []string
	runLaunchctl = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) >= 2 && args[0] == "print" {
			switch args[1] {
			case guiDomain, userDomain:
				return "subsystem", nil
			case legacyGUI, legacyUser:
				return "state = running\npid = 9001", nil
			}
			return "", fmt.Errorf("Could not find service")
		}
		return "", nil
	}

	mgr := &launchdManager{}
	if err := mgr.Restart(); err != nil {
		t.Fatalf("Restart() error = %v, want the loaded legacy service restarted", err)
	}
	kickstarted := false
	for _, call := range calls {
		if strings.HasPrefix(call, "bootout ") {
			t.Fatalf("Restart() unloaded a service it could not replace: %#v", calls)
		}
		if strings.HasPrefix(call, "kickstart ") && strings.Contains(call, legacyLaunchdLabel) {
			kickstarted = true
		}
	}
	if !kickstarted {
		t.Fatalf("Restart() did not restart the loaded legacy service: %#v", calls)
	}
	if _, err := os.Stat(legacyLaunchdPlistPath()); err != nil {
		t.Fatalf("legacy plist was removed by Restart(): %v", err)
	}
}

// Without the current plist and with nothing loaded there is nothing a restart could
// start, so it has to fail with a clear message instead of silently doing nothing.
func TestRestartRefusesWhenNothingIsLoadedAndTheCurrentPlistIsMissing(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	t.Setenv("HOME", dir)

	seedLaunchdPlist(t, legacyLaunchdPlistPath())

	var calls []string
	runLaunchctl = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", fmt.Errorf("Could not find service")
	}

	mgr := &launchdManager{}
	if err := mgr.Restart(); err == nil {
		t.Fatal("Restart() = nil, want an error because the current plist is missing")
	}
	for _, call := range calls {
		if !strings.HasPrefix(call, "print ") {
			t.Fatalf("Restart() ran %q without a current plist: %#v", call, calls)
		}
	}
	if _, err := os.Stat(legacyLaunchdPlistPath()); err != nil {
		t.Fatalf("legacy plist was removed by a failed Restart(): %v", err)
	}
}

func TestRestartKeepsLoadedUserDomainWhenGUIAvailable(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", dir)
	if origHome != "" {
		t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })
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
		case "kickstart":
			if args[len(args)-1] != userTarget {
				t.Fatalf("kickstart target = %q, want %q", args[len(args)-1], userTarget)
			}
			return "", nil
		default:
			t.Fatalf("unexpected launchctl call: %v", args)
			return "", nil
		}
	}

	mgr := &launchdManager{}
	if err := mgr.Restart(); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	if !containsCall(calls, "kickstart -kp "+userTarget) {
		t.Fatalf("expected kickstart of loaded user service, calls = %#v", calls)
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
		case "kickstart":
			if args[len(args)-1] != userTarget {
				t.Fatalf("kickstart target = %q, want %q", args[len(args)-1], userTarget)
			}
			return "", nil
		default:
			t.Fatalf("unexpected launchctl call: %v", args)
			return "", nil
		}
	}

	mgr := &launchdManager{}
	if err := mgr.Restart(); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	if !containsCall(calls, "kickstart -kp "+userTarget) {
		t.Fatalf("expected kickstart to user target, calls = %#v", calls)
	}
}

func TestRestartBootstrapsUnloadedService(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	// Restart refuses before bootstrap when the current plist is missing, so this
	// test has to install one under its own HOME.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	seedLaunchdPlist(t, launchdPlistPath())

	guiDomain := launchdGUIDomain()
	guiTarget := launchdTarget(guiDomain)
	var calls []string
	runLaunchctl = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "print" && args[1] == guiDomain {
			return "subsystem", nil
		}
		if args[0] == "print" {
			return "not loaded", fmt.Errorf("exit status 113")
		}
		if args[0] == "bootstrap" || args[0] == "kickstart" {
			return "", nil
		}
		t.Fatalf("unexpected launchctl call: %v", args)
		return "", nil
	}

	if err := (&launchdManager{}).Restart(); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if !containsCall(calls, "bootstrap "+guiDomain+" "+launchdPlistPath()) ||
		!containsCall(calls, "kickstart -kp "+guiTarget) {
		t.Fatalf("expected bootstrap and kickstart, calls = %#v", calls)
	}
}

// collectXMLText walks the XML stream and returns every chardata text node.
// Used by the plist-escape test to verify path values round-trip through
// xml.Decoder regardless of how deeply they are nested.
func collectXMLText(t *testing.T, data []byte) []string {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	var out []string
	for {
		tok, err := dec.Token()
		if tok == nil {
			break
		}
		if err != nil {
			t.Fatalf("xml decode token: %v", err)
		}
		if cd, ok := tok.(xml.CharData); ok {
			s := strings.TrimSpace(string(cd))
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

// TestBuildPlist_EscapesXMLSpecialCharsInPaths pins the bug where unescaped
// '&', '<', '>', quotes, and apostrophes in cfg paths produced malformed XML that
// `launchctl bootstrap` rejected.
// The legacy plist must survive a restart that could not bring the current service
// up: removing it first would leave a legacy-only installation with nothing to run.
func TestRestartKeepsTheLegacyPlistWhenTheBootstrapFails(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	seedLaunchdPlist(t, launchdPlistPath())
	seedLaunchdPlist(t, legacyLaunchdPlistPath())

	guiDomain := launchdGUIDomain()
	runLaunchctl = func(args ...string) (string, error) {
		switch {
		case args[0] == "print" && args[1] == guiDomain:
			return "subsystem", nil
		case args[0] == "print":
			return "not loaded", fmt.Errorf("exit status 113")
		case args[0] == "bootstrap":
			return "Bootstrap failed: 5", fmt.Errorf("exit status 5")
		}
		return "", nil
	}

	if err := (&launchdManager{}).Restart(); err == nil {
		t.Fatal("Restart() = nil, want the bootstrap failure")
	}
	if _, err := os.Stat(legacyLaunchdPlistPath()); err != nil {
		t.Fatalf("legacy plist was removed before the current service was up: %v", err)
	}
}

// Once the current service is up the legacy fallback is retired.
func TestRestartRemovesTheLegacyPlistOnceTheCurrentServiceIsUp(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	seedLaunchdPlist(t, launchdPlistPath())
	seedLaunchdPlist(t, legacyLaunchdPlistPath())

	guiDomain := launchdGUIDomain()
	runLaunchctl = func(args ...string) (string, error) {
		if args[0] == "print" {
			if args[1] == guiDomain {
				return "subsystem", nil
			}
			return "not loaded", fmt.Errorf("exit status 113")
		}
		return "", nil
	}

	if err := (&launchdManager{}).Restart(); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if _, err := os.Stat(legacyLaunchdPlistPath()); !os.IsNotExist(err) {
		t.Fatalf("legacy plist still exists after a successful restart: %v", err)
	}
}

// With both labels loaded, restart must target the current one: kickstarting the
// legacy agent would restart the service the user is migrating away from.
func TestRestartKickstartsTheCurrentLabelWhenBothAreLoaded(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	t.Setenv("HOME", dir)

	guiDomain := launchdGUIDomain()
	userDomain := launchdUserDomain()
	currentGUI := launchdTarget(guiDomain)
	currentUser := launchdTarget(userDomain)
	legacyGUI := legacyLaunchdTarget(guiDomain)
	legacyUser := legacyLaunchdTarget(userDomain)

	var calls []string
	runLaunchctl = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "print" {
			switch args[1] {
			case guiDomain, userDomain:
				return "subsystem", nil
			case currentGUI, currentUser, legacyGUI, legacyUser:
				return "state = running\npid = 9001", nil
			}
			return "not loaded", fmt.Errorf("exit status 113")
		}
		return "", nil
	}

	if err := (&launchdManager{}).Restart(); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if !containsCall(calls, "kickstart -kp "+currentGUI) {
		t.Fatalf("Restart() did not kickstart the current label: %#v", calls)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "kickstart ") && strings.Contains(call, legacyLaunchdLabel) {
			t.Fatalf("Restart() kickstarted the legacy label: %#v", calls)
		}
	}
}

func TestBuildPlist_EscapesXMLSpecialCharsInPaths(t *testing.T) {
	cfg := Config{
		BinaryPath: "/opt/cc-connect/bin & <tools>/cc-connect",
		WorkDir:    "/Users/jane/Projects/dev & test/cc-connect",
		LogFile:    "/Users/jane/Library/Logs/cc \"connect\".log",
		LogMaxSize: 10485760,
		EnvPATH:    "/usr/bin:/path/with'apostrophe/bin",
	}
	out := buildPlist(cfg)

	// 1) Result must parse as well-formed XML — without escaping, bare '&'
	//    or unbalanced '<' inside <string> elements break the parser.
	if err := xml.Unmarshal([]byte(out), new(struct{ XMLName xml.Name })); err != nil {
		t.Fatalf("buildPlist output is not valid XML: %v\n%s", err, out)
	}

	// 2) Round-trip the values through the XML parser and make sure the
	//    original characters survive the encode/decode cycle. Walk every
	//    text node rather than relying on a positional path, since the
	//    plist nests <array>/<dict>/<string> at multiple depths.
	values := collectXMLText(t, []byte(out))
	mustContain := []string{cfg.BinaryPath, cfg.WorkDir, cfg.LogFile, cfg.EnvPATH}
	for _, want := range mustContain {
		found := false
		for _, got := range values {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("decoded plist does not contain expected value %q\nvalues = %#v", want, values)
		}
	}

	// 3) The raw XML output must not contain a bare '&' followed by anything
	//    other than a recognized entity reference — that's what would crash
	//    launchctl.
	for i := 0; i < len(out); i++ {
		if out[i] != '&' {
			continue
		}
		rest := out[i:]
		if !(strings.HasPrefix(rest, "&amp;") ||
			strings.HasPrefix(rest, "&lt;") ||
			strings.HasPrefix(rest, "&gt;") ||
			strings.HasPrefix(rest, "&quot;") ||
			strings.HasPrefix(rest, "&apos;") ||
			strings.HasPrefix(rest, "&#")) {
			t.Fatalf("bare '&' at offset %d (not a valid entity ref): %q", i, rest[:min(len(rest), 30)])
		}
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
			"NO_PROXY":     "example.com",
			"HTTPS_PROXY":  "http://1.2.3.4:8080",
			"CUSTOM_TOKEN": "tok",
		},
	}
	xml := buildPlist(cfg)
	wantOrder := []string{
		"<key>CUSTOM_TOKEN</key>",
		"<key>HTTPS_PROXY</key>",
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
		t.Fatalf("value missing for CUSTOM_TOKEN; xml=%s", xml)
	}
}

func TestBuildPlist_RejectsInvalidEnvName(t *testing.T) {
	cfg := Config{
		BinaryPath: "/x", WorkDir: "/y", LogFile: "/l", LogMaxSize: 1, EnvPATH: "/p",
		EnvExtra: map[string]string{"FOO BAR": "v", "1FOO": "v", "OK": "fine"},
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
		EnvExtra: map[string]string{"TRICKY": `a<b&c"d'e`},
	}
	xml := buildPlist(cfg)
	idx := strings.Index(xml, "<key>TRICKY</key>")
	if idx < 0 {
		t.Fatalf("TRICKY missing: %s", xml)
	}
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
		EnvExtra:   map[string]string{"NO_PROXY": "example.com"},
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
		EnvExtra:   map[string]string{"CUSTOM_TOKEN": "captured"},
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

// TestBuildPlist_IncludesHOME regresses the "Append system prompt file not
// found" bug: launchd LaunchAgents do not always propagate HOME, so the
// daemon (and Claude / Codex / etc. subprocesses it spawns) would call
// os.UserHomeDir(), get an error, and config.Load would fall back to a
// relative data_dir that agent subprocesses resolved against work_dir.
func TestBuildPlist_IncludesHOME(t *testing.T) {
	cfg := Config{
		BinaryPath: "/bin/true",
		WorkDir:    "/tmp/wd",
		LogFile:    "/tmp/log",
		LogMaxSize: 1024,
		EnvPATH:    "/usr/bin",
		HomeDir:    "/home/app",
	}
	out := buildPlist(cfg)
	if !strings.Contains(out, "<key>HOME</key>\n\t\t<string>/home/app</string>") {
		t.Fatalf("plist should include HOME=/home/app; got:\n%s", out)
	}
}

// TestBuildPlist_OmitsHOMEWhenEmpty ensures we do not emit an empty
// <string></string> for HOME when the caller could not determine one.
func TestBuildPlist_OmitsHOMEWhenEmpty(t *testing.T) {
	cfg := Config{
		BinaryPath: "/bin/true",
		WorkDir:    "/tmp/wd",
		LogFile:    "/tmp/log",
		LogMaxSize: 1024,
		EnvPATH:    "/usr/bin",
	}
	out := buildPlist(cfg)
	if strings.Contains(out, "<key>HOME</key>") {
		t.Fatalf("plist should omit HOME when HomeDir empty; got:\n%s", out)
	}
}

// TestBuildPlist_EnvExtraHOMEDoesNotOverrideTemplateHOME pins that the
// template-owned HOME wins over any HOME leaking in through EnvExtra —
// the same guarantee we make for PATH and CC_LOG_FILE.
func TestBuildPlist_EnvExtraHOMEDoesNotOverrideTemplateHOME(t *testing.T) {
	cfg := Config{
		BinaryPath: "/bin/true",
		WorkDir:    "/tmp/wd",
		LogFile:    "/tmp/log",
		LogMaxSize: 1024,
		EnvPATH:    "/usr/bin",
		HomeDir:    "/home/app",
		EnvExtra:   map[string]string{"HOME": "/should-not-override"},
	}
	out := buildPlist(cfg)
	if strings.Contains(out, "/should-not-override") {
		t.Fatalf("EnvExtra HOME leaked past template ownership: %s", out)
	}
	if !strings.Contains(out, "<string>/home/app</string>") {
		t.Fatalf("expected template HOME /home/app to survive; got:\n%s", out)
	}
}

func TestInstallLaunchd_RemovesLegacyRetryPlist(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })
	runLaunchctl = func(args ...string) (string, error) { return "", nil }

	legacyPath := legacyLaunchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte("<plist>legacy</plist>\n"), 0o644); err != nil {
		t.Fatalf("seed legacy plist: %v", err)
	}

	mgr := &launchdManager{}
	cfg := Config{
		BinaryPath: "/bin/true",
		WorkDir:    t.TempDir(),
		LogFile:    filepath.Join(t.TempDir(), "cc.log"),
		LogMaxSize: 1024,
		EnvPATH:    "/usr/bin",
	}
	if err := mgr.Install(cfg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy plist still exists after install: %v", err)
	}
	if _, err := os.Stat(launchdPlistPath()); err != nil {
		t.Fatalf("current plist missing after install: %v", err)
	}
}
