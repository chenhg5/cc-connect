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
	windowsTaskName         = ServiceName
	windowsScriptName       = "cc-connect-daemon.vbs"
	legacyWindowsScriptName = "cc-connect-daemon.ps1"
)

var runPowerShell = func(script string) (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", strictPowerShell(script))
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func strictPowerShell(script string) string {
	return "$ErrorActionPreference = 'Stop'\n" + script
}

type schtasksManager struct{}

func newPlatformManager() (Manager, error) {
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		return nil, fmt.Errorf("powershell.exe not found: Windows Task Scheduler management requires PowerShell")
	}
	return &schtasksManager{}, nil
}

func (*schtasksManager) Platform() string { return "schtasks" }

func (m *schtasksManager) Install(cfg Config) error {
	if _, err := exec.LookPath("wscript.exe"); err != nil {
		return fmt.Errorf("wscript.exe not found: Windows daemon launcher requires Windows Script Host")
	}
	if err := os.MkdirAll(DefaultDataDir(), 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	scriptPath := windowsTaskScriptPath()
	// 0644 has weak semantics on Windows; the file ACL is what matters.
	// We still write 0600 so the file's POSIX bits do not advertise read
	// access, and rely on the user's own profile ACLs for primary defense
	// (the script lives under %USERPROFILE%\.cc-connect by default).
	// WriteFile only applies perm on create, so Chmod the existing file
	// after writing to harden reinstalls of pre-existing 0644 scripts.
	if err := os.WriteFile(scriptPath, []byte(buildWindowsTaskScript(cfg)), 0600); err != nil {
		return fmt.Errorf("write task script: %w", err)
	}
	if err := os.Chmod(scriptPath, 0600); err != nil {
		return fmt.Errorf("chmod task script: %w", err)
	}

	if err := stopWindowsTask(); err != nil {
		slog.Warn("schtasks: stop existing task failed", "error", err)
	}
	if err := deleteWindowsTask(); err != nil {
		if windowsTaskMatchesAction(scriptPath) {
			if err := m.Start(); err != nil {
				return fmt.Errorf("start existing task: %w", err)
			}
			return nil
		}
		return err
	}

	if err := createWindowsTask(scriptPath); err != nil {
		return err
	}

	if err := m.Start(); err != nil {
		return fmt.Errorf("start task: %w", err)
	}
	if err := removeWindowsTaskScript(legacyWindowsTaskScriptPath()); err != nil {
		slog.Warn("schtasks: remove legacy PowerShell launcher failed", "error", err)
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
	for _, scriptPath := range []string{windowsTaskScriptPath(), legacyWindowsTaskScriptPath()} {
		if err := removeWindowsTaskScript(scriptPath); err != nil {
			return fmt.Errorf("remove task script: %w", err)
		}
	}
	return nil
}

func (*schtasksManager) Start() error {
	return startWindowsTask()
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
	return startWindowsTask()
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

func legacyWindowsTaskScriptPath() string {
	return filepath.Join(DefaultDataDir(), legacyWindowsScriptName)
}

func removeWindowsTaskScript(scriptPath string) error {
	if err := os.Remove(scriptPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func windowsTaskAction(scriptPath string) string {
	return fmt.Sprintf(`wscript.exe %s`, windowsTaskActionArgs(scriptPath))
}

func windowsTaskActionArgs(scriptPath string) string {
	return fmt.Sprintf(`//B //NoLogo "%s"`, scriptPath)
}

func createWindowsTask(scriptPath string) error {
	out, err := runPowerShell(fmt.Sprintf(`
$action = New-ScheduledTaskAction -Execute 'wscript.exe' -Argument %s
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName %s -Action $action -Trigger $trigger -Principal $principal -Force | Out-Null
`, powerShellLiteral(windowsTaskActionArgs(scriptPath)), powerShellLiteral(windowsTaskName)))
	if err != nil {
		return fmt.Errorf("register scheduled task: %s (%w)", out, err)
	}
	return nil
}

func windowsTaskMatchesAction(scriptPath string) bool {
	out, err := runPowerShell(fmt.Sprintf(`
$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue
if ($null -eq $task) { exit 1 }
$expectedArgs = %s
foreach ($action in $task.Actions) {
	if (($action.Execute -ieq 'wscript.exe') -and ($action.Arguments -eq $expectedArgs)) {
		Write-Output 'true'
		exit 0
	}
}
exit 1
`, powerShellLiteral(windowsTaskName), powerShellLiteral(windowsTaskActionArgs(scriptPath))))
	return err == nil && strings.EqualFold(strings.TrimSpace(out), "true")
}

func buildWindowsTaskScript(cfg Config) string {
	var sb strings.Builder
	sb.WriteString("Option Explicit\r\n")
	sb.WriteString("Dim shell, processEnv, exitCode\r\n")
	sb.WriteString("Set shell = CreateObject(\"WScript.Shell\")\r\n")
	sb.WriteString("Set processEnv = shell.Environment(\"Process\")\r\n")
	writeVBScriptEnv(&sb, "CC_LOG_FILE", cfg.LogFile)
	writeVBScriptEnv(&sb, "CC_LOG_MAX_SIZE", strconv.FormatInt(cfg.LogMaxSize, 10))
	writeVBScriptEnv(&sb, "CC_LOG_MAX_BACKUPS", strconv.Itoa(cfg.LogMaxBackups))
	if cfg.EnvPATH != "" {
		writeVBScriptEnv(&sb, "PATH", cfg.EnvPATH)
	}
	if len(cfg.EnvExtra) > 0 {
		keys := make([]string, 0, len(cfg.EnvExtra))
		for key := range cfg.EnvExtra {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if !isValidEnvName(key) {
				slog.Warn("daemon: windows: dropping invalid env name from EnvExtra",
					"key", key)
				continue
			}
			value := cfg.EnvExtra[key]
			if value == "" {
				continue
			}
			writeVBScriptEnv(&sb, key, value)
		}
	}
	fmt.Fprintf(&sb, "shell.CurrentDirectory = %s\r\n", vbScriptLiteral(cfg.WorkDir))
	sb.WriteString("Do\r\n")
	// Window style 0 keeps the console-subsystem cc-connect binary off the interactive desktop.
	fmt.Fprintf(&sb, "  exitCode = shell.Run(Chr(34) & %s & Chr(34), 0, True)\r\n", vbScriptLiteral(cfg.BinaryPath))
	sb.WriteString("  If exitCode = 0 Then WScript.Quit 0\r\n")
	sb.WriteString("  WScript.Sleep 10000\r\n")
	sb.WriteString("Loop\r\n")
	return sb.String()
}

func writeVBScriptEnv(sb *strings.Builder, key, value string) {
	fmt.Fprintf(sb, "processEnv(%s) = %s\r\n", vbScriptLiteral(key), vbScriptLiteral(value))
}

func vbScriptLiteral(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func powerShellLiteral(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func stopWindowsTask() error {
	lockedProcessID, expectedBinary := installedWindowsProcess()
	out, err := runPowerShell(fmt.Sprintf(`
$currentScript = %s
$legacyScript = %s
$lockedProcessId = %d
$expectedBinary = %s
$launcherPids = @(
	Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
		$commandLine = [string]$_.CommandLine
		($_.Name -ieq 'wscript.exe' -or $_.Name -ieq 'powershell.exe') -and
		($commandLine.IndexOf($currentScript, [System.StringComparison]::OrdinalIgnoreCase) -ge 0 -or
		 $commandLine.IndexOf($legacyScript, [System.StringComparison]::OrdinalIgnoreCase) -ge 0)
	} | Select-Object -ExpandProperty ProcessId
)
$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue

if ($null -ne $task -and $task.State -eq 'Running') {
	Stop-ScheduledTask -TaskName %s
}

$taskStopped = $null -eq $task
for ($i = 0; -not $taskStopped -and $i -lt 20; $i++) {
	$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue
	$taskStopped = $null -eq $task -or $task.State -ne 'Running'
	if (-not $taskStopped) { Start-Sleep -Milliseconds 500 }
}
if (-not $taskStopped) {
	Write-Error 'scheduled task did not stop within timeout'
	exit 1
}

$childPids = @(
	Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
		$launcherPids -contains $_.ParentProcessId
	} | Select-Object -ExpandProperty ProcessId
)
foreach ($processId in ($childPids | Sort-Object -Unique)) {
	Stop-Process -Id $processId -Force -ErrorAction SilentlyContinue
	Wait-Process -Id $processId -Timeout 5 -ErrorAction SilentlyContinue
}

if ($lockedProcessId -gt 0) {
	$lockedProcess = Get-CimInstance Win32_Process -Filter "ProcessId = $lockedProcessId" -ErrorAction SilentlyContinue
	$binaryMatches = $null -ne $lockedProcess -and (
		$lockedProcess.Name -ieq 'cc-connect.exe' -and
		($expectedBinary -eq '' -or ([string]$lockedProcess.ExecutablePath).Equals($expectedBinary, [System.StringComparison]::OrdinalIgnoreCase))
	)
	if ($binaryMatches) {
		Stop-Process -Id $lockedProcessId -Force -ErrorAction SilentlyContinue
		Wait-Process -Id $lockedProcessId -Timeout 5 -ErrorAction SilentlyContinue
	}
}
`, powerShellLiteral(windowsTaskScriptPath()), powerShellLiteral(legacyWindowsTaskScriptPath()), lockedProcessID,
		powerShellLiteral(expectedBinary), powerShellLiteral(windowsTaskName), powerShellLiteral(windowsTaskName),
		powerShellLiteral(windowsTaskName)))
	if err != nil {
		return fmt.Errorf("stop scheduled task: %s (%w)", out, err)
	}
	return nil
}

func installedWindowsProcess() (int, string) {
	meta, err := LoadMeta()
	if err != nil || meta.WorkDir == "" {
		return 0, ""
	}
	data, err := os.ReadFile(filepath.Join(meta.WorkDir, ".config.toml.lock"))
	if err != nil {
		return 0, meta.BinaryPath
	}
	processID, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || processID <= 0 {
		return 0, meta.BinaryPath
	}
	return processID, meta.BinaryPath
}

func startWindowsTask() error {
	out, err := runPowerShell(fmt.Sprintf(`
$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue
if ($null -eq $task) { Write-Error 'scheduled task not found'; exit 1 }
if ($task.State -ne 'Running') { Start-ScheduledTask -TaskName %s }
`, powerShellLiteral(windowsTaskName), powerShellLiteral(windowsTaskName)))
	if err != nil {
		return fmt.Errorf("start scheduled task: %s (%w)", out, err)
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

// CheckLinger is a no-op on Windows (always returns false).
func CheckLinger() (enabled bool, user string) {
	return false, ""
}
