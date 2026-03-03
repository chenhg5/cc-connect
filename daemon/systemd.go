//go:build linux

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	systemdServiceName = ServiceName + ".service"
)

type systemdManager struct{}

func newPlatformManager() (Manager, error) {
	if err := checkSystemctlAvailable(); err != nil {
		return nil, err
	}
	return &systemdManager{}, nil
}

func (*systemdManager) Platform() string { return "systemd (user)" }

func (m *systemdManager) Install(cfg Config) error {
	unitPath := systemdUnitPath()

	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		return fmt.Errorf("create systemd dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	unit := buildSystemdUnit(cfg)
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", systemdServiceName},
		{"--user", "restart", systemdServiceName},
	} {
		if out, err := runSystemctl(args...); err != nil {
			return fmt.Errorf("systemctl %s: %s (%w)", strings.Join(args, " "), out, err)
		}
	}

	return nil
}

func (m *systemdManager) Uninstall() error {
	runSystemctl("--user", "disable", "--now", systemdServiceName)

	unitPath := systemdUnitPath()
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit: %w", err)
	}

	runSystemctl("--user", "daemon-reload")
	return nil
}

func (*systemdManager) Start() error {
	out, err := runSystemctl("--user", "start", systemdServiceName)
	if err != nil {
		return fmt.Errorf("start: %s (%w)", out, err)
	}
	return nil
}

func (*systemdManager) Stop() error {
	out, err := runSystemctl("--user", "stop", systemdServiceName)
	if err != nil {
		return fmt.Errorf("stop: %s (%w)", out, err)
	}
	return nil
}

func (*systemdManager) Restart() error {
	out, err := runSystemctl("--user", "restart", systemdServiceName)
	if err != nil {
		return fmt.Errorf("restart: %s (%w)", out, err)
	}
	return nil
}

func (*systemdManager) Status() (*Status, error) {
	st := &Status{Platform: "systemd (user)"}

	unitPath := systemdUnitPath()
	if _, err := os.Stat(unitPath); err != nil {
		return st, nil
	}
	st.Installed = true

	out, err := runSystemctl("--user", "show", systemdServiceName,
		"--no-page", "--property", "ActiveState,MainPID")
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

func systemdUnitPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", systemdServiceName)
}

func buildSystemdUnit(cfg Config) string {
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
	sb.WriteString("WantedBy=default.target\n")
	return sb.String()
}

func runSystemctl(args ...string) (string, error) {
	cmd := exec.Command("systemctl", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func checkSystemctlAvailable() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemctl not found; systemd user services are required on Linux")
	}
	out, err := runSystemctl("--user", "status")
	if err != nil {
		detail := strings.ToLower(out)
		if strings.Contains(detail, "failed to connect") ||
			strings.Contains(detail, "no such file or directory") {
			return fmt.Errorf("systemd user session not available.\n" +
				"  If running in WSL2, add [boot]\\nsystemd=true to /etc/wsl.conf and restart WSL.\n" +
				"  If running via SSH, try: loginctl enable-linger $USER")
		}
		if strings.Contains(detail, "not been booted") ||
			strings.Contains(detail, "not supported") {
			return fmt.Errorf("systemd not running in this environment.\n" +
				"  If running in a container, systemd is typically not available.\n" +
				"  Consider using nohup, tmux, or screen instead.")
		}
	}
	return nil
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
