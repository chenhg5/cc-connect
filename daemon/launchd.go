//go:build darwin

package daemon

import (
	"encoding/xml"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	launchdLabel = "com.cc-connect.service"
)

var runLaunchctl = func(args ...string) (string, error) {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

type launchdManager struct{}

// CheckLinger always returns true on macOS: launchd user agents persist
// independently of login sessions, so no "linger" warning is needed.
func CheckLinger() (enabled bool, user string) {
	return true, ""
}

func newPlatformManager() (Manager, error) {
	return &launchdManager{}, nil
}

func (*launchdManager) Platform() string { return "launchd" }

func (m *launchdManager) Install(cfg Config) error {
	plistPath := launchdPlistPath()

	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	// Unload existing service first (ignore errors) so we do not leave a stale
	// job behind when switching between GUI and headless sessions.
	bootoutLaunchdTargets()

	plist := buildPlist(cfg)
	// 0600: plist may contain captured secret values (config.toml ${ENV}
	// placeholders and any EnvDiscoverer extension output). User-only
	// LaunchAgents path; root can still read but that is the user's own
	// machine boundary. os.WriteFile only applies perm on create, so
	// Chmod afterwards is required to harden reinstalls of files that
	// pre-existed at 0644 from earlier cc-connect versions.
	if err := os.WriteFile(plistPath, []byte(plist), 0600); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	if err := os.Chmod(plistPath, 0600); err != nil {
		return fmt.Errorf("chmod plist: %w", err)
	}

	domain := preferredLaunchdDomain()
	if out, err := runLaunchctl("bootstrap", domain, plistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap: %s (%w)", out, err)
	}

	if _, err := runLaunchctl("kickstart", "-kp", launchdTarget(domain)); err != nil {
		return fmt.Errorf("launchctl kickstart: %w", err)
	}
	return nil
}

func (m *launchdManager) Uninstall() error {
	bootoutLaunchdTargets()

	plistPath := launchdPlistPath()
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

func (*launchdManager) Start() error {
	if _, target, _, ok := loadedLaunchdTarget(); ok {
		out, err := runLaunchctl("kickstart", "-kp", target)
		if err != nil {
			return fmt.Errorf("start: %s (%w)", out, err)
		}
		return nil
	}

	domain := preferredLaunchdDomain()
	plistPath := launchdPlistPath()
	var out string
	if _, err := runLaunchctl("bootstrap", domain, plistPath); err != nil {
		// already bootstrapped — try kickstart
		out, err = runLaunchctl("kickstart", "-kp", launchdTarget(domain))
		if err != nil {
			return fmt.Errorf("start: %s (%w)", out, err)
		}
	}
	return nil
}

func (*launchdManager) Stop() error {
	var lastOut string
	var lastErr error
	for _, target := range launchdTargets() {
		out, err := runLaunchctl("bootout", target)
		if err == nil {
			return nil
		}
		lastOut = out
		lastErr = err
	}
	if lastErr != nil {
		return fmt.Errorf("stop: %s (%w)", lastOut, lastErr)
	}
	return nil
}

// launchdRestartInPlaceTimeout is the wall-clock budget for an in-place
// `launchctl kickstart -k` to deliver SIGTERM and reap the old daemon. The
// actual SIGTERM-to-exit time depends on what the daemon is doing (draining
// interactive agent sessions, flushing logs, etc.) and can legitimately take
// 5–30 s on a busy host. If the kickstart does not complete within this
// budget we surface the error rather than fall through to bootout, because
// the in-place path is precisely the one that avoids the bootout/bootstrap
// race — silently retrying via bootout would re-introduce the race.
const launchdRestartInPlaceTimeout = 60 * time.Second

// launchdRestartWaitPoll is how often waitLaunchdTargetsGone re-checks
// whether launchd has actually removed the job. launchd bootout is
// asynchronous; the time between `launchctl bootout` returning and the job
// disappearing from the domain can be several seconds when the daemon has
// agent sessions in flight (see #1833). 200 ms strikes a balance between
// responsiveness and launchctl exec overhead.
const launchdRestartWaitPoll = 200 * time.Millisecond

// launchdRestartWaitDefault is the default deadline for waitLaunchdTargetsGone
// in the bootout/bootstrap fallback path. 30 s is comfortably above the
// reporter's observed 5 s teardown window for a busy daemon, while still
// failing loud if launchd genuinely never finishes removing the job.
const launchdRestartWaitDefault = 30 * time.Second

func (*launchdManager) Restart() error {
	domain := preferredLaunchdDomain()
	if loadedDomain, _, _, ok := loadedLaunchdTarget(); ok && domain != launchdGUIDomain() {
		domain = loadedDomain
	}
	target := launchdTarget(domain)
	plistPath := launchdPlistPath()

	// Fast path: if the job is already loaded AND the canonical plist still
	// exists on disk (i.e. the user hasn't uninstalled it out from under us),
	// prefer an in-place `launchctl kickstart -k <target>`. This sends
	// SIGTERM to the running daemon and waits for it to exit before
	// relaunching — launchd handles the sequencing, so the bootout/bootstrap
	// race documented in #1833 cannot occur on this path.
	//
	// We deliberately gate on "plist still on disk" rather than trying to
	// diff plist contents: if a user has replaced the plist (binary path,
	// env, etc.) they almost always also want a full reinstall, which goes
	// through Install() rather than Restart(). The bootout path below is the
	// fallback for the rare case where the job is not loaded or the plist
	// has been removed between the daemon writing it and us restarting.
	if _, _, _, loaded := loadedLaunchdTarget(); loaded {
		if _, err := os.Stat(plistPath); err == nil {
			slog.Info("daemon: launchd: in-place restart via kickstart -k",
				"target", target)
			out, err := runLaunchctl("kickstart", "-k", target)
			if err != nil {
				return fmt.Errorf("restart (in-place): %s (%w)", out, err)
			}
			return nil
		}
	}

	// Fallback path: the job is not loaded (cold start, or it died) or the
	// plist has been removed (reinstall scenario). bootout is async, so we
	// must wait for launchd to actually remove the job before bootstrap will
	// succeed — see #1833 for the race that occurs when the daemon has agent
	// sessions in flight and needs several seconds to exit.
	bootoutLaunchdTargets()
	waitLaunchdTargetsGone(launchdRestartWaitDefault)

	// launchd bootout is asynchronous; retry bootstrap with backoff
	// to avoid "Bootstrap failed: 5" race condition. This 3 × 500 ms safety
	// net is now reached *after* waitLaunchdTargetsGone has confirmed the
	// label is gone, so it only fires for genuine bootstrap races rather
	// than the earlier bootout/agent-teardown race.
	var out string
	var err error
	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		out, err = runLaunchctl("bootstrap", domain, plistPath)
		if err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("restart: %s (%w)", out, err)
	}
	if _, err := runLaunchctl("kickstart", "-kp", target); err != nil {
		return fmt.Errorf("restart kickstart: %w", err)
	}
	return nil
}

// waitLaunchdTargetsGone polls loadedLaunchdTarget every
// launchdRestartWaitPoll until launchd reports the job is no longer loaded
// in any domain, or until deadline elapses. It is used between `bootout`
// and `bootstrap` in the Restart fallback path to close the async-removal
// race documented in #1833. A timeout is treated as a soft success: the
// caller proceeds to bootstrap and the existing 3 × 500 ms retry handles
// the residual race. We log a WARN on timeout so operators see when launchd
// is unusually slow.
func waitLaunchdTargetsGone(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		if _, _, _, loaded := loadedLaunchdTarget(); !loaded {
			return
		}
		if time.Now().After(deadline) {
			slog.Warn("daemon: launchd: timed out waiting for job to disappear after bootout",
				"timeout", timeout)
			return
		}
		time.Sleep(launchdRestartWaitPoll)
	}
}

func (*launchdManager) Status() (*Status, error) {
	st := &Status{Platform: "launchd"}

	plistPath := launchdPlistPath()
	if _, err := os.Stat(plistPath); err != nil {
		return st, nil
	}
	st.Installed = true

	_, _, out, ok := loadedLaunchdTarget()
	if !ok {
		return st, nil
	}

	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "pid = ") {
			if pid, err := strconv.Atoi(strings.TrimPrefix(trimmed, "pid = ")); err == nil && pid > 0 {
				st.PID = pid
				st.Running = true
			}
		}
		if strings.Contains(trimmed, "state = running") {
			st.Running = true
		}
	}
	return st, nil
}

// ── helpers ─────────────────────────────────────────────────

func launchdPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

func launchdUserDomain() string {
	return fmt.Sprintf("user/%d", os.Getuid())
}

func launchdGUIDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func preferredLaunchdDomain() string {
	guiDomain := launchdGUIDomain()
	if _, err := runLaunchctl("print", guiDomain); err == nil {
		return guiDomain
	}
	return launchdUserDomain()
}

func launchdDomains() []string {
	preferred := preferredLaunchdDomain()
	guiDomain := launchdGUIDomain()
	userDomain := launchdUserDomain()
	if preferred == guiDomain {
		return []string{guiDomain, userDomain}
	}
	return []string{userDomain, guiDomain}
}

func launchdTarget(domain string) string {
	return fmt.Sprintf("%s/%s", domain, launchdLabel)
}

func launchdTargets() []string {
	domains := launchdDomains()
	targets := make([]string, 0, len(domains))
	for _, domain := range domains {
		targets = append(targets, launchdTarget(domain))
	}
	return targets
}

func loadedLaunchdTarget() (string, string, string, bool) {
	for _, domain := range launchdDomains() {
		target := launchdTarget(domain)
		out, err := runLaunchctl("print", target)
		if err == nil {
			return domain, target, out, true
		}
	}
	return "", "", "", false
}

func bootoutLaunchdTargets() {
	for _, target := range launchdTargets() {
		_, _ = runLaunchctl("bootout", target)
	}
}

// templateOwnedEnvKeys are keys the plist template renders directly; if
// they also appear in cfg.EnvExtra the template version wins.
var templateOwnedEnvKeys = map[string]struct{}{
	"CC_LOG_FILE":     {},
	"CC_LOG_MAX_SIZE": {},
	"PATH":            {},
}

// renderEnvExtraPlist returns the serialized key/value pairs (without the
// surrounding <dict> wrapper) for cfg.EnvExtra, sorted by key and with
// invalid keys / empty values dropped. Both keys and values are XML-escaped.
func renderEnvExtraPlist(envExtra map[string]string) string {
	if len(envExtra) == 0 {
		return ""
	}
	keys := make([]string, 0, len(envExtra))
	for k := range envExtra {
		if _, owned := templateOwnedEnvKeys[k]; owned {
			continue
		}
		if !isValidEnvName(k) {
			slog.Warn("daemon: launchd: dropping invalid env name from EnvExtra",
				"key", k)
			continue
		}
		if envExtra[k] == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n",
			xmlEscape(k), xmlEscape(envExtra[k]))
	}
	return b.String()
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func buildPlist(cfg Config) string {
	envPATH := cfg.EnvPATH
	if envPATH == "" {
		envPATH = "/usr/local/bin:/usr/bin:/bin:/opt/homebrew/bin"
	}
	envExtra := renderEnvExtraPlist(cfg.EnvExtra)
	// User-supplied paths can legitimately contain XML-special characters
	// ('&', '<', '>', '"', '\''). Without escaping, `launchctl bootstrap`
	// rejects the plist with a parse error and daemon install fails. The
	// label is a hard-coded constant; LogMaxSize is an int; envExtra is
	// escaped by renderEnvExtraPlist.
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>WorkingDirectory</key>
	<string>%s</string>
	<key>RunAtLoad</key>
	<true/>
	<key>LimitLoadToSessionType</key>
	<array>
		<string>Aqua</string>
		<string>Background</string>
	</array>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>EnvironmentVariables</key>
	<dict>
		<key>CC_LOG_FILE</key>
		<string>%s</string>
		<key>CC_LOG_MAX_SIZE</key>
		<string>%d</string>
		<key>CC_LOG_MAX_BACKUPS</key>
		<string>%d</string>
		<key>PATH</key>
		<string>%s</string>
%s	</dict>
	<key>StandardOutPath</key>
	<string>/dev/null</string>
	<key>StandardErrorPath</key>
	<string>/dev/null</string>
</dict>
</plist>
`, launchdLabel, xmlEscape(cfg.BinaryPath), xmlEscape(cfg.WorkDir), xmlEscape(cfg.LogFile), cfg.LogMaxSize, cfg.LogMaxBackups, xmlEscape(envPATH), envExtra)
}
