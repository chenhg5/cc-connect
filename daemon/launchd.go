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
)

const (
	launchdLabel       = "com.cc-connect.service"
	legacyLaunchdLabel = "com.cc-connect.retry"
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
	removeLegacyLaunchdPlist()

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

	for _, plistPath := range launchdPlistPaths() {
		if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove plist: %w", err)
		}
	}
	return nil
}

func (*launchdManager) Start() error {
	if job := inspectLaunchdLabel(launchdLabel); job.loaded {
		out, err := runLaunchctl("kickstart", "-kp", job.target)
		if err != nil {
			return fmt.Errorf("start: %s (%w)", out, err)
		}
		// The current service is serving, so the legacy fallback can go; leaving
		// it loaded would let the old agent come back alongside the current one.
		retireLegacyLaunchdService()
		return nil
	}

	domain := preferredLaunchdDomain()
	plistPath := launchdPlistPath()
	if _, err := os.Stat(plistPath); err != nil {
		// Nothing to bootstrap. Booting out the legacy service here would take
		// away the only daemon the user has and then fail, so leave it running and
		// say what is actually wrong.
		return fmt.Errorf("start: current service is not installed (%s missing: %w); run 'cc-connect daemon install'", plistPath, err)
	}
	if _, err := runLaunchctl("bootstrap", domain, plistPath); err != nil {
		// already bootstrapped — try kickstart
		out, kickErr := runLaunchctl("kickstart", "-kp", launchdTarget(domain))
		if kickErr != nil {
			return fmt.Errorf("start: %s (%w)", out, kickErr)
		}
	}
	// Only now that the current service is up is it safe to drop the fallback.
	retireLegacyLaunchdService()
	return nil
}

func (*launchdManager) Stop() error {
	var lastOut string
	var lastErr error
	stopped := false
	// Boot out every label in every domain instead of returning after the first
	// success: an upgrade can leave both the current and the legacy agent loaded,
	// and stopping only one of them leaves the daemon running.
	for _, target := range launchdTargets() {
		out, err := runLaunchctl("bootout", target)
		if err != nil {
			lastOut, lastErr = out, err
			continue
		}
		stopped = true
	}
	if !stopped && lastErr != nil {
		return fmt.Errorf("stop: %s (%w)", lastOut, lastErr)
	}
	return nil
}

func (*launchdManager) Restart() error {
	if _, target, _, ok := loadedLaunchdTarget(); ok {
		// bootout also kills commands launched by the daemon. A single
		// kickstart transaction lets launchd replace the running service even
		// when the restart request originates from one of its own sessions.
		if out, err := runLaunchctl("kickstart", "-kp", target); err != nil {
			return fmt.Errorf("restart kickstart: %s (%w)", out, err)
		}
		return nil
	}

	domain := preferredLaunchdDomain()
	plistPath := launchdPlistPath()
	if _, err := os.Stat(plistPath); err != nil {
		// Refuse before touching anything: without the current plist a restart can
		// only fail, and it must not take a working legacy service down with it.
		return fmt.Errorf("restart: current service is not installed (%s missing: %w); run 'cc-connect daemon install'", plistPath, err)
	}
	if out, err := runLaunchctl("bootstrap", domain, plistPath); err != nil {
		return fmt.Errorf("restart bootstrap: %s (%w)", out, err)
	}
	// Drop the legacy fallback only once the current service is bootstrapped.
	removeLegacyLaunchdPlist()
	if out, err := runLaunchctl("kickstart", "-kp", launchdTarget(domain)); err != nil {
		return fmt.Errorf("restart kickstart: %s (%w)", out, err)
	}
	return nil
}

// Status reports the daemon's state across both labels.
//
// Installed is deliberately based on any cc-connect agent plist rather than the
// current label's: a legacy-only installation has to stay reachable, otherwise
// start/stop/restart refuse to run at all ("not installed") and leave the machine
// with an agent the user can no longer manage or migrate.
func (*launchdManager) Status() (*Status, error) {
	st := &Status{Platform: "launchd"}

	if !launchdAnyPlistExists() {
		return st, nil
	}
	st.Installed = true

	// The current label is authoritative only when it is actually serving. An
	// upgrade can leave the legacy agent running while the current one is loaded
	// but stopped, and inspecting just the current label would then report the
	// daemon as stopped (PID 0) while it is still there.
	for _, label := range []string{launchdLabel, legacyLaunchdLabel} {
		job := inspectLaunchdLabel(label)
		if !job.running {
			continue
		}
		st.Running = true
		st.PID = job.pid
		break
	}
	return st, nil
}

// ── helpers ─────────────────────────────────────────────────

func launchdPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

func legacyLaunchdPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", legacyLaunchdLabel+".plist")
}

func launchdPlistPaths() []string {
	return []string{launchdPlistPath(), legacyLaunchdPlistPath()}
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

func legacyLaunchdTarget(domain string) string {
	return fmt.Sprintf("%s/%s", domain, legacyLaunchdLabel)
}

func launchdTargets() []string {
	domains := launchdDomains()
	targets := make([]string, 0, len(domains)*2)
	for _, domain := range domains {
		targets = append(targets, launchdTarget(domain))
		targets = append(targets, legacyLaunchdTarget(domain))
	}
	return targets
}

func loadedLaunchdTargetForLabel(label string) (string, string, string, bool) {
	for _, domain := range launchdDomains() {
		target := fmt.Sprintf("%s/%s", domain, label)
		out, err := runLaunchctl("print", target)
		if err == nil {
			return domain, target, out, true
		}
	}
	return "", "", "", false
}

func loadedLaunchdTarget() (string, string, string, bool) {
	if domain, target, out, ok := loadedLaunchdTargetForLabel(launchdLabel); ok {
		return domain, target, out, true
	}
	return loadedLaunchdTargetForLabel(legacyLaunchdLabel)
}

func launchdAnyPlistExists() bool {
	for _, plistPath := range launchdPlistPaths() {
		if _, err := os.Stat(plistPath); err == nil {
			return true
		}
	}
	return false
}

// launchdJobState is the subset of `launchctl print` output the manager needs
// for a single label.
type launchdJobState struct {
	loaded  bool
	running bool
	pid     int
	target  string
}

// inspectLaunchdLabel resolves a label in the preferred domain order and parses
// its `launchctl print` output. A label that is not loaded in any domain comes
// back as a zero state.
func inspectLaunchdLabel(label string) launchdJobState {
	for _, domain := range launchdDomains() {
		target := fmt.Sprintf("%s/%s", domain, label)
		out, err := runLaunchctl("print", target)
		if err != nil {
			continue
		}
		job := launchdJobState{loaded: true, target: target}
		job.pid, job.running = parseLaunchdPrint(out)
		return job
	}
	return launchdJobState{}
}

// parseLaunchdPrint extracts the pid and running state from `launchctl print`
// output. Only top-level keys count: nested dictionaries also carry a `state =`
// line (an inactive service lists spawn-scheduled children), which would
// otherwise make a stopped service look like a running one.
func parseLaunchdPrint(out string) (pid int, running bool) {
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent > 1 {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "pid = "):
			if p, err := strconv.Atoi(strings.TrimPrefix(trimmed, "pid = ")); err == nil && p > 0 {
				pid = p
			}
		case strings.HasPrefix(trimmed, "state = "):
			if strings.Contains(trimmed, "state = running") {
				running = true
			}
		}
	}
	if pid > 0 {
		running = true
	}
	return pid, running
}

// retireLegacyLaunchdService unloads the pre-rename com.cc-connect.retry agent
// and removes its plist so it cannot be resurrected at the next login. Callers
// must have the current service running first: this is the last step of a
// migration, never the first step of a start.
func retireLegacyLaunchdService() {
	for _, domain := range launchdDomains() {
		_, _ = runLaunchctl("bootout", legacyLaunchdTarget(domain))
	}
	removeLegacyLaunchdPlist()
}

func bootoutLaunchdTargets() {
	for _, target := range launchdTargets() {
		_, _ = runLaunchctl("bootout", target)
	}
}

func removeLegacyLaunchdPlist() {
	_ = os.Remove(legacyLaunchdPlistPath())
}

// templateOwnedEnvKeys are keys the plist template renders directly; if
// they also appear in cfg.EnvExtra the template version wins.
var templateOwnedEnvKeys = map[string]struct{}{
	"CC_LOG_FILE":     {},
	"CC_LOG_MAX_SIZE": {},
	"PATH":            {},
	"HOME":            {},
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
	// HOME: launchd LaunchAgents run under the user's uid, but the HOME
	// env var is not always populated (especially when bootstrapped via
	// `launchctl bootstrap gui/...`). Without HOME, os.UserHomeDir()
	// returns an error inside the daemon and any subprocess it spawns;
	// config.Load then falls back to a relative data_dir which agents
	// (cd'd into work_dir) resolve to <work_dir>/.cc-connect instead of
	// <HOME>/.cc-connect.
	homeEntry := ""
	if cfg.HomeDir != "" {
		homeEntry = fmt.Sprintf("\t\t<key>HOME</key>\n\t\t<string>%s</string>\n", xmlEscape(cfg.HomeDir))
	}
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
%s%s	</dict>
	<key>StandardOutPath</key>
	<string>/dev/null</string>
	<key>StandardErrorPath</key>
	<string>/dev/null</string>
</dict>
</plist>
`, launchdLabel, xmlEscape(cfg.BinaryPath), xmlEscape(cfg.WorkDir), xmlEscape(cfg.LogFile), cfg.LogMaxSize, cfg.LogMaxBackups, xmlEscape(envPATH), homeEntry, envExtra)
}
