//go:build darwin

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return fmt.Errorf("write plist: %w", err)
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

func (*launchdManager) Restart() error {
	domain := preferredLaunchdDomain()
	if loadedDomain, _, _, ok := loadedLaunchdTarget(); ok && domain != launchdGUIDomain() {
		domain = loadedDomain
	}
	target := launchdTarget(domain)
	bootoutLaunchdTargets()

	plistPath := launchdPlistPath()

	// launchd bootout is asynchronous; retry bootstrap with backoff
	// to avoid "Bootstrap failed: 5" race condition.
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

func buildPlist(cfg Config) string {
	envPATH := cfg.EnvPATH
	if envPATH == "" {
		envPATH = "/usr/local/bin:/usr/bin:/bin:/opt/homebrew/bin"
	}
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
		<true/>
	</dict>
	<key>EnvironmentVariables</key>
	<dict>
		<key>CC_LOG_FILE</key>
		<string>%s</string>
		<key>CC_LOG_MAX_SIZE</key>
		<string>%d</string>
		<key>PATH</key>
		<string>%s</string>
	</dict>
	<key>StandardOutPath</key>
	<string>/dev/null</string>
	<key>StandardErrorPath</key>
	<string>/dev/null</string>
</dict>
</plist>
`, launchdLabel, cfg.BinaryPath, cfg.WorkDir, cfg.LogFile, cfg.LogMaxSize, envPATH)
}
