//go:build windows

package daemon

import (
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
	windowsTaskName   = ServiceName
	windowsScriptName = "cc-connect-daemon.ps1"
)

var runSchtasks = func(args ...string) (string, error) {
	cmd := exec.Command("schtasks", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

var runPowerShell = func(script string) (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

type schtasksManager struct{}

func newPlatformManager() (Manager, error) {
	if _, err := exec.LookPath("schtasks.exe"); err != nil {
		return nil, fmt.Errorf("schtasks.exe not found: Windows Task Scheduler is required")
	}
	return &schtasksManager{}, nil
}

func (*schtasksManager) Platform() string { return "schtasks" }

func (m *schtasksManager) Install(cfg Config) error {
	if err := os.MkdirAll(DefaultDataDir(), 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	scriptPath := windowsTaskScriptPath()
	if err := os.WriteFile(scriptPath, []byte(buildWindowsTaskScript(cfg)), 0644); err != nil {
		return fmt.Errorf("write task script: %w", err)
	}

	if err := stopWindowsTask(); err != nil {
		slog.Warn("schtasks: stop existing task failed", "error", err)
	}
	if err := deleteWindowsTask(); err != nil {
		slog.Warn("schtasks: delete existing task failed", "error", err)
	}

	action := windowsTaskAction(scriptPath)
	if out, err := runSchtasks(
		"/Create",
		"/TN", windowsTaskName,
		"/TR", action,
		"/SC", "ONLOGON",
		"/F",
	); err != nil {
		return fmt.Errorf("schtasks create: %s (%w)", out, err)
	}

	if err := m.Start(); err != nil {
		return fmt.Errorf("start task: %w", err)
	}
	return nil
}

func (*schtasksManager) Uninstall() error {
	if err := stopWindowsTask(); err != nil {
		slog.Warn("schtasks: stop task failed", "error", err)
	}
	if err := deleteWindowsTask(); err != nil {
		return err
	}
	if err := os.Remove(windowsTaskScriptPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove task script: %w", err)
	}
	return nil
}

func (*schtasksManager) Start() error {
	out, err := runSchtasks("/Run", "/TN", windowsTaskName)
	if err != nil && !windowsTaskAlreadyRunning(out) {
		return fmt.Errorf("schtasks run: %s (%w)", out, err)
	}
	return nil
}

func (*schtasksManager) Stop() error {
	if err := stopWindowsTask(); err != nil {
		return err
	}
	return nil
}

func (*schtasksManager) Restart() error {
	if err := stopWindowsTask(); err != nil {
		slog.Warn("schtasks: stop before restart failed", "error", err)
	}
	out, err := runSchtasks("/Run", "/TN", windowsTaskName)
	if err != nil && !windowsTaskAlreadyRunning(out) {
		return fmt.Errorf("schtasks run: %s (%w)", out, err)
	}
	return nil
}

func (*schtasksManager) Status() (*Status, error) {
	st := &Status{Platform: "schtasks"}

	out, err := runPowerShell(fmt.Sprintf(`
$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue
if ($null -eq $task) { exit 1 }
Write-Output $task.State
`, powerShellLiteral(windowsTaskName)))
	if err != nil {
		return st, nil
	}
	st.Installed = true

	taskStatus := strings.TrimSpace(out)
	if strings.EqualFold(taskStatus, "Running") {
		st.Running = true
	}
	return st, nil
}

func windowsTaskScriptPath() string {
	return filepath.Join(DefaultDataDir(), windowsScriptName)
}

func windowsTaskAction(scriptPath string) string {
	return fmt.Sprintf(`powershell.exe -WindowStyle Hidden -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "%s"`, scriptPath)
}

func buildWindowsTaskScript(cfg Config) string {
	var sb strings.Builder
	sb.WriteString("$ErrorActionPreference = 'Stop'\r\n")
	writePowerShellEnv(&sb, "CC_LOG_FILE", cfg.LogFile)
	writePowerShellEnv(&sb, "CC_LOG_MAX_SIZE", strconv.FormatInt(cfg.LogMaxSize, 10))
	if cfg.EnvPATH != "" {
		writePowerShellEnv(&sb, "PATH", cfg.EnvPATH)
	}
	if len(cfg.EnvExtra) > 0 {
		keys := make([]string, 0, len(cfg.EnvExtra))
		for key := range cfg.EnvExtra {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			writePowerShellEnv(&sb, key, cfg.EnvExtra[key])
		}
	}
	fmt.Fprintf(&sb, "Set-Location -LiteralPath %s\r\n", powerShellLiteral(cfg.WorkDir))
	sb.WriteString("while ($true) {\r\n")
	fmt.Fprintf(&sb, "  & %s\r\n", powerShellLiteral(cfg.BinaryPath))
	sb.WriteString("  $exitCode = $LASTEXITCODE\r\n")
	sb.WriteString("  if ($exitCode -eq 0) { exit 0 }\r\n")
	sb.WriteString("  Start-Sleep -Seconds 10\r\n")
	sb.WriteString("}\r\n")
	return sb.String()
}

func writePowerShellEnv(sb *strings.Builder, key, value string) {
	fmt.Fprintf(sb, "$env:%s = %s\r\n", key, powerShellLiteral(value))
}

func powerShellLiteral(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func stopWindowsTask() error {
	out, err := runPowerShell(fmt.Sprintf(`
$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue
if ($null -eq $task) { exit 0 }
if ($task.State -eq 'Running') {
	Stop-ScheduledTask -TaskName %s
}
for ($i = 0; $i -lt 20; $i++) {
	$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue
	if ($null -eq $task -or $task.State -ne 'Running') { exit 0 }
	Start-Sleep -Milliseconds 500
}
Write-Error 'scheduled task did not stop within timeout'
exit 1
`, powerShellLiteral(windowsTaskName), powerShellLiteral(windowsTaskName), powerShellLiteral(windowsTaskName)))
	if err != nil {
		return fmt.Errorf("stop scheduled task: %s (%w)", out, err)
	}
	return nil
}

func deleteWindowsTask() error {
	out, err := runPowerShell(fmt.Sprintf(`
$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue
if ($null -eq $task) { exit 0 }
Unregister-ScheduledTask -TaskName %s -Confirm:$false
`, powerShellLiteral(windowsTaskName), powerShellLiteral(windowsTaskName)))
	if err != nil {
		return fmt.Errorf("delete scheduled task: %s (%w)", out, err)
	}
	return nil
}

func windowsTaskAlreadyRunning(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "already running") ||
		strings.Contains(lower, "is currently running")
}
