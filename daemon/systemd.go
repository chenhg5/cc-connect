//go:build linux

package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	systemdServiceName = ServiceName + ".service"
)

type systemdManager struct {
	system bool // true = system-level (/etc/systemd/system), false = user-level (~/.config/systemd/user)
}

func newPlatformManager() (Manager, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil, fmt.Errorf("systemctl not found: systemd is required on Linux; if running in a container without systemd, use nohup, tmux, or screen instead")
	}

	isRoot := os.Getuid() == 0

	if isRoot {
		if err := checkSystemdRunning(true); err != nil {
			return nil, err
		}
		return &systemdManager{system: true}, nil
	}

	if err := checkSystemdRunning(false); err != nil {
		return nil, err
	}
	return &systemdManager{system: false}, nil
}

func (m *systemdManager) Platform() string {
	if m.system {
		return "systemd (system)"
	}
	return "systemd (user)"
}

func (m *systemdManager) Install(cfg Config) error {
	unitPath := m.unitPath()

	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		return fmt.Errorf("create systemd dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	unit := m.buildUnit(cfg)
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	for _, cmdArgs := range [][]string{
		m.sysArgs("daemon-reload"),
		m.sysArgs("enable", systemdServiceName),
		m.sysArgs("restart", systemdServiceName),
	} {
		if out, err := runSystemctl(cmdArgs...); err != nil {
			return fmt.Errorf("systemctl %s: %s (%w)", strings.Join(cmdArgs, " "), out, err)
		}
	}

	return nil
}

func (m *systemdManager) Uninstall() error {
	if _, err := runSystemctl(m.sysArgs("disable", "--now", systemdServiceName)...); err != nil {
		slog.Warn("systemd: disable failed", "error", err)
	}

	unitPath := m.unitPath()
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit: %w", err)
	}

	if _, err := runSystemctl(m.sysArgs("daemon-reload")...); err != nil {
		slog.Warn("systemd: daemon-reload failed", "error", err)
	}
	return nil
}

func (m *systemdManager) Start() error {
	out, err := runSystemctl(m.sysArgs("start", systemdServiceName)...)
	if err != nil {
		return fmt.Errorf("start: %s (%w)", out, err)
	}
	return nil
}

func (m *systemdManager) Stop() error {
	out, err := runSystemctl(m.sysArgs("stop", systemdServiceName)...)
	if err != nil {
		return fmt.Errorf("stop: %s (%w)", out, err)
	}
	return nil
}

func (m *systemdManager) Restart() error {
	out, err := runSystemctl(m.sysArgs("restart", systemdServiceName)...)
	if err != nil {
		return fmt.Errorf("restart: %s (%w)", out, err)
	}
	return nil
}

func (m *systemdManager) Status() (*Status, error) {
	st := &Status{Platform: m.Platform()}

	unitPath := m.unitPath()
	if _, err := os.Stat(unitPath); err != nil {
		return st, nil
	}
	st.Installed = true

	out, err := runSystemctl(m.sysArgs("show", systemdServiceName,
		"--no-page", "--property", "ActiveState,MainPID")...)
	if err != nil {
		return st, nil
	}

	props := parseKeyValue(out)
	if strings.EqualFold(props["ActiveState"], "active") {
		st.Running = true
	}
	if pid, err := strconv.Atoi(props["MainPID"]); err == nil && pid > 0 {
		st.PID = pid
	}
	return st, nil
}

// ── helpers ─────────────────────────────────────────────────

// sysArgs prepends --user flag for user-level managers.
func (m *systemdManager) sysArgs(args ...string) []string {
	if m.system {
		return args
	}
	return append([]string{"--user"}, args...)
}

func (m *systemdManager) unitPath() string {
	if m.system {
		return filepath.Join("/etc/systemd/system", systemdServiceName)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", systemdServiceName)
}

func (m *systemdManager) buildUnit(cfg Config) string {
	var sb strings.Builder
	sb.WriteString("[Unit]\n")
	sb.WriteString("Description=cc-connect - AI Agent Chat Bridge\n")
	sb.WriteString("After=network-online.target\n")
	sb.WriteString("Wants=network-online.target\n\n")

	sb.WriteString("[Service]\n")
	sb.WriteString("Type=simple\n")
	fmt.Fprintf(&sb, "ExecStart=%s\n", cfg.BinaryPath)
	fmt.Fprintf(&sb, "WorkingDirectory=%s\n", cfg.WorkDir)
	sb.WriteString("Restart=on-failure\n")
	sb.WriteString("RestartSec=10\n")
	fmt.Fprintf(&sb, "Environment=CC_LOG_FILE=%s\n", cfg.LogFile)
	fmt.Fprintf(&sb, "Environment=CC_LOG_MAX_SIZE=%d\n", cfg.LogMaxSize)
	if cfg.EnvPATH != "" {
		fmt.Fprintf(&sb, "Environment=PATH=%s\n", cfg.EnvPATH)
	}
	sb.WriteString("\n[Install]\n")
	if m.system {
		sb.WriteString("WantedBy=multi-user.target\n")
	} else {
		sb.WriteString("WantedBy=default.target\n")
	}
	return sb.String()
}

func runSystemctl(args ...string) (string, error) {
	cmd := exec.Command("systemctl", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func checkSystemdRunning(system bool) error {
	var args []string
	if system {
		args = []string{"is-system-running"}
	} else {
		args = []string{"--user", "is-system-running"}
	}

	out, _ := runSystemctl(args...)
	state := strings.TrimSpace(strings.ToLower(out))

	// These states all mean systemd is usable for managing services
	switch state {
	case "running", "degraded", "starting", "initializing":
		return nil
	}

	// "offline" = systemd exists but is not PID 1 (WSL2 without systemd, some containers)
	// "not been booted" / empty = no systemd at all
	wsl := isWSL2()

	if system {
		if wsl {
			return fmt.Errorf("systemd is not active in this WSL2 instance.\n" +
				"  Add the following to /etc/wsl.conf and restart WSL (wsl --shutdown):\n" +
				"    [boot]\n" +
				"    systemd=true\n" +
				"  Or use: nohup cc-connect > cc-connect.log 2>&1 &")
		}
		if state == "offline" || strings.Contains(state, "not been booted") {
			return fmt.Errorf("systemd is not active (state: %s).\n"+
				"  If running in a container, systemd is typically not available.\n"+
				"  Use nohup, tmux, or screen instead:\n"+
				"    nohup cc-connect > cc-connect.log 2>&1 &", state)
		}
		return fmt.Errorf("systemd check failed (state: %s).\n"+
			"  Use nohup as alternative: nohup cc-connect > cc-connect.log 2>&1 &", state)
	}

	// User-level failures
	if wsl {
		return fmt.Errorf("systemd user session not available in WSL2.\n" +
			"  Add the following to /etc/wsl.conf and restart WSL (wsl --shutdown):\n" +
			"    [boot]\n" +
			"    systemd=true\n" +
			"  Or use: nohup cc-connect > cc-connect.log 2>&1 &")
	}

	user := os.Getenv("USER")
	return fmt.Errorf("systemd user session not available.\n"+
		"  This often happens when connecting via SSH without a systemd login session.\n"+
		"  Try one of:\n"+
		"    1. Run as root: sudo cc-connect daemon install (uses system-level systemd)\n"+
		"    2. loginctl enable-linger %s && export XDG_RUNTIME_DIR=/run/user/$(id -u)\n"+
		"    3. Use nohup/tmux instead: nohup cc-connect > cc-connect.log 2>&1 &", user)
}

func isWSL2() bool {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(data))
	return strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl")
}

func parseKeyValue(text string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}
