//go:build darwin

package daemon

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestRestartPrefersInPlaceKickstartWhenLoadedAndPlistPresent exercises the
// fast path introduced for #1833: when launchd already has the job loaded
// AND the canonical plist file still exists on disk, Restart() must use
// `launchctl kickstart -k <target>` (in-place) instead of the bootout /
// bootstrap sequence. The in-place path sends SIGTERM and lets launchd
// sequence the relaunch, which avoids the bootout/agent-teardown race that
// otherwise fails the first three bootstrap attempts with
// "Bootstrap failed: 5: Input/output error".
func TestRestartPrefersInPlaceKickstartWhenLoadedAndPlistPresent(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	t.Setenv("HOME", dir)

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
				// gui target not loaded
				return "Bootstrap failed: 113: Could not find service", fmt.Errorf("exit status 113")
			case userTarget:
				return "pid = 4321\nstate = running", nil
			default:
				return "", fmt.Errorf("unexpected print target %q", args[1])
			}
		case "kickstart":
			// In-place restart uses `kickstart -k` (kill+relaunch).
			// We do NOT expect `kickstart -kp` on the in-place path.
			if containsCall(calls[:len(calls)-1], "kickstart -k "+userTarget) {
				t.Fatalf("kickstart -k called more than once")
			}
			return "", nil
		default:
			return "", fmt.Errorf("in-place path must not call %q (calls = %#v)", args[0], calls)
		}
	}

	mgr := &launchdManager{}
	if err := mgr.Restart(); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	// The in-place path must call kickstart -k on the user target (where
	// the job is loaded) and must not touch bootout/bootstrap at all.
	if !containsCall(calls, "kickstart -k "+userTarget) {
		t.Fatalf("expected in-place `kickstart -k %s`, calls = %#v", userTarget, calls)
	}
	if containsCall(calls, "bootout "+userTarget) || containsCall(calls, "bootout "+guiTarget) {
		t.Fatalf("in-place path must not call bootout, calls = %#v", calls)
	}
	if containsCall(calls, "bootstrap "+userDomain+" "+plistPath) ||
		containsCall(calls, "bootstrap "+guiDomain+" "+plistPath) {
		t.Fatalf("in-place path must not call bootstrap, calls = %#v", calls)
	}
	if containsCall(calls, "kickstart -kp "+userTarget) ||
		containsCall(calls, "kickstart -kp "+guiTarget) {
		t.Fatalf("in-place path must not call `kickstart -kp`, calls = %#v", calls)
	}
}

// TestRestartFallbackToBootoutWhenJobNotLoaded covers the case where the
// job is not currently loaded by launchd (cold start, or it died between
// attempts). In that case the in-place kickstart path is unavailable and
// Restart() must fall through to the bootout / waitLaunchdTargetsGone /
// bootstrap sequence. The 3 × 500 ms bootstrap retry remains as the safety
// net for genuine launchd-side races.
func TestRestartFallbackToBootoutWhenJobNotLoaded(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	t.Setenv("HOME", dir)

	plistPath := launchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(plistPath, []byte("plist"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	guiDomain := launchdGUIDomain()
	guiTarget := launchdTarget(guiDomain)

	var calls []string
	runLaunchctl = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) < 2 {
			return "", nil
		}
		switch args[0] {
		case "print":
			// gui domain available, but neither target is loaded → triggers
			// fallback path.
			switch args[1] {
			case guiDomain:
				return "subsystem", nil
			case guiTarget:
				return "Bootstrap failed: 113: Could not find service", fmt.Errorf("exit status 113")
			default:
				return "Bootstrap failed: 113: Could not find service", fmt.Errorf("exit status 113")
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

	// Fallback path: bootout (both targets, since loadedLaunchdTarget
	// returned ok=false for the lookup but bootoutLaunchdTargets iterates
	// over all), waitLaunchdTargetsGone (poll only, no writes), then
	// bootstrap + kickstart -kp.
	if !containsCall(calls, "bootout "+guiTarget) {
		t.Fatalf("expected bootout %s in fallback path, calls = %#v", guiTarget, calls)
	}
	if !containsCall(calls, "bootstrap "+guiDomain+" "+plistPath) {
		t.Fatalf("expected bootstrap in fallback path, calls = %#v", calls)
	}
	if !containsCall(calls, "kickstart -kp "+guiTarget) {
		t.Fatalf("expected `kickstart -kp %s` in fallback path, calls = %#v", guiTarget, calls)
	}
	// In-place path must NOT have been taken.
	for _, c := range calls {
		if strings.HasPrefix(c, "kickstart -k ") {
			t.Fatalf("fallback path must not use in-place `kickstart -k`, calls = %#v", calls)
		}
	}
}

// TestRestartFallbackToBootoutWhenPlistMissing covers the reinstall path:
// the job is loaded but the canonical plist file has been removed (e.g. by
// a concurrent uninstall). In that case in-place kickstart is unsafe
// (launchd would relaunch the old binary indefinitely), so Restart() must
// fall through to bootout + wait + bootstrap.
func TestRestartFallbackToBootoutWhenPlistMissing(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// NOTE: plist file deliberately NOT created.

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
				return "pid = 4321\nstate = running", nil // loaded
			default:
				return "", fmt.Errorf("unexpected print target %q", args[1])
			}
		case "bootout":
			return "", nil
		case "bootstrap":
			// Even though plist is missing on disk, the fallback path
			// still attempts bootstrap (which will surface the missing
			// plist to the operator). This matches the pre-#1833
			// behaviour; the test pins it so we don't silently swallow.
			return "", nil
		case "kickstart":
			return "", nil
		default:
			return "", nil
		}
	}

	mgr := &launchdManager{}
	if err := mgr.Restart(); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	if !containsCall(calls, "bootout "+guiTarget) || !containsCall(calls, "bootout "+userTarget) {
		t.Fatalf("expected bootout of both targets when plist missing, calls = %#v", calls)
	}
	if !containsCall(calls, "bootstrap "+guiDomain+" "+launchdPlistPath()) {
		t.Fatalf("expected bootstrap (fallback) when plist missing, calls = %#v", calls)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "kickstart -k ") {
			t.Fatalf("plist-missing path must not use in-place `kickstart -k`, calls = %#v", calls)
		}
	}
}

// TestWaitLaunchdTargetsGoneReturnsImmediatelyWhenAlreadyGone ensures the
// helper does not waste a poll interval when launchd has already removed
// the job before we even start polling.
func TestWaitLaunchdTargetsGoneReturnsImmediatelyWhenAlreadyGone(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	runLaunchctl = func(args ...string) (string, error) {
		// Always report not loaded.
		return "Bootstrap failed: 113: Could not find service", fmt.Errorf("exit status 113")
	}

	start := time.Now()
	waitLaunchdTargetsGone(5 * time.Second)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("waitLaunchdTargetsGone should return immediately when job is already gone; took %v", elapsed)
	}
}

// TestWaitLaunchdTargetsGoneReturnsAfterJobDisappears ensures the helper
// returns within a poll interval after launchd reports the job as gone.
// We simulate the daemon hanging around for two polls by returning
// "loaded" for the first two print calls and "not loaded" for the third.
func TestWaitLaunchdTargetsGoneReturnsAfterJobDisappears(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	guiDomain := launchdGUIDomain()

	calls := 0
	runLaunchctl = func(args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "print" && args[1] == guiDomain {
			// First call: report the gui domain is available so
			// loadedLaunchdTarget iterates into targets.
			return "subsystem", nil
		}
		calls++
		if calls <= 2 {
			// Still loaded for first two polls.
			return "pid = 4321\nstate = running", nil
		}
		// Then gone.
		return "Bootstrap failed: 113: Could not find service", fmt.Errorf("exit status 113")
	}

	start := time.Now()
	waitLaunchdTargetsGone(5 * time.Second)
	elapsed := time.Since(start)
	// Two polls of 200ms each → ~400ms minimum, plus slack.
	if elapsed < 300*time.Millisecond {
		t.Fatalf("waitLaunchdTargetsGone returned too fast (%v); expected at least 2 polls", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("waitLaunchdTargetsGone returned too slow (%v); expected ~400ms after two polls", elapsed)
	}
}

// TestWaitLaunchdTargetsGoneHonoursTimeout ensures the helper gives up
// after the configured timeout when launchd never removes the job. We use
// a tight timeout so the test stays fast.
func TestWaitLaunchdTargetsGoneHonoursTimeout(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	runLaunchctl = func(args ...string) (string, error) {
		// Always report loaded.
		return "pid = 4321\nstate = running", nil
	}

	start := time.Now()
	// 5× the poll interval is enough margin that a tight CI runner
	// won't flake, but still proves the timeout fires well before
	// the production default of 30 s.
	waitLaunchdTargetsGone(5 * launchdRestartWaitPoll)
	elapsed := time.Since(start)
	minExpected := 4 * launchdRestartWaitPoll
	maxExpected := 10 * launchdRestartWaitPoll
	if elapsed < minExpected {
		t.Fatalf("waitLaunchdTargetsGone returned too fast (%v); expected at least %v", elapsed, minExpected)
	}
	if elapsed > maxExpected {
		t.Fatalf("waitLaunchdTargetsGone returned too slow (%v); expected <%v", elapsed, maxExpected)
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
