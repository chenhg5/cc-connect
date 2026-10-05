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
	"syscall"
)

const (
	windowsTaskName   = ServiceName
	windowsScriptName = "cc-connect-daemon.ps1"
)

var runPowerShell = func(script string) (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", strictPowerShell(script))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
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
	launcherPath := filepath.Join(filepath.Dir(cfg.BinaryPath), "cc-connect-plugin.exe")
	if _, err := os.Stat(launcherPath); err != nil {
		return fmt.Errorf("Windows daemon requires %s alongside cc-connect.exe: %w", launcherPath, err)
	}
	if err := stopWindowsTask(); err != nil {
		return fmt.Errorf("stop existing task: %w", err)
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

	if err := deleteWindowsTask(); err != nil {
		if !cfg.StartWithCodex && windowsTaskMatchesAction(scriptPath, launcherPath) {
			if err := m.Start(); err != nil {
				return fmt.Errorf("start existing task: %w", err)
			}
			return nil
		}
		return err
	}

	if err := createWindowsTask(scriptPath, launcherPath, cfg.StartWithCodex); err != nil {
		return err
	}

	if err := m.Start(); err != nil {
		return fmt.Errorf("start task: %w", err)
	}
	return nil
}

func (*schtasksManager) Uninstall() error {
	if err := stopWindowsTask(); err != nil {
		return fmt.Errorf("stop task: %w", err)
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
		return fmt.Errorf("stop before restart: %w", err)
	}
	return startWindowsTask()
}

func (*schtasksManager) Status() (*Status, error) {
	st := &Status{Platform: "schtasks"}

	out, err := runPowerShell(fmt.Sprintf(`
$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue
if ($null -eq $task) { exit 1 }
Write-Output $task.State
%s
if ($null -ne $child) { Write-Output $child.Id }
`, powerShellLiteral(windowsTaskName), windowsManagedChildScript()))
	if err != nil {
		return st, nil
	}
	st.Installed = true

	fields := strings.Fields(out)
	if len(fields) > 0 && strings.EqualFold(fields[0], "Running") {
		st.Supervising = true
		if len(fields) > 1 {
			st.PID, _ = strconv.Atoi(fields[1])
			st.Running = st.PID > 0
		}
	}
	return st, nil
}

func windowsTaskScriptPath() string {
	return filepath.Join(DefaultDataDir(), windowsScriptName)
}

func windowsProcessStatePath() string {
	return filepath.Join(DefaultDataDir(), "daemon-process.json")
}

// Validate both image and start time before using a saved PID, which Windows may reuse.
func windowsManagedChildScript() string {
	return fmt.Sprintf(`
$child = $null
if (Test-Path -LiteralPath %s) {
    try {
        $state = Get-Content -LiteralPath %s -Raw | ConvertFrom-Json
        $child = Get-Process -Id $state.pid -ErrorAction Stop
        if ($child.Path -ine $state.binary_path -or $child.StartTime.ToUniversalTime().Ticks -ne [long]$state.start_ticks) { $child = $null }
    } catch { $child = $null }
}
`, powerShellLiteral(windowsProcessStatePath()), powerShellLiteral(windowsProcessStatePath()))
}

func windowsTaskAction(scriptPath, launcherPath string) string {
	return fmt.Sprintf(`"%s" %s`, launcherPath, windowsTaskActionArgs(scriptPath))
}

func windowsTaskActionArgs(scriptPath string) string {
	return fmt.Sprintf(`--supervisor "%s"`, scriptPath)
}

func createWindowsTask(scriptPath, launcherPath string, startWithCodex bool) error {
	trigger, triggerArg := "", ""
	if !startWithCodex {
		trigger = "$trigger = New-ScheduledTaskTrigger -AtLogOn -User $user"
		triggerArg = "-Trigger $trigger"
	}
	out, err := runPowerShell(fmt.Sprintf(`
$action = New-ScheduledTaskAction -Execute %s -Argument %s
$user = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
%s
$principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
$settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
Register-ScheduledTask -TaskName %s -Action $action %s -Principal $principal -Settings $settings -Force | Out-Null
`, powerShellLiteral(launcherPath), powerShellLiteral(windowsTaskActionArgs(scriptPath)), trigger, powerShellLiteral(windowsTaskName), triggerArg))
	if err != nil {
		return fmt.Errorf("register scheduled task: %s (%w)", out, err)
	}
	return nil
}

func windowsTaskMatchesAction(scriptPath, launcherPath string) bool {
	out, err := runPowerShell(fmt.Sprintf(`
$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue
if ($null -eq $task) { exit 1 }
$expectedArgs = %s
foreach ($action in $task.Actions) {
	if (($action.Execute -ieq %s) -and ($action.Arguments -eq $expectedArgs)) {
		Write-Output 'true'
		exit 0
	}
}
exit 1
`, powerShellLiteral(windowsTaskName), powerShellLiteral(windowsTaskActionArgs(scriptPath)), powerShellLiteral(launcherPath)))
	return err == nil && strings.EqualFold(strings.TrimSpace(out), "true")
}

func buildWindowsTaskScript(cfg Config) string {
	var sb strings.Builder
	sb.WriteString("$ErrorActionPreference = 'Stop'\r\n")
	writePowerShellEnv(&sb, "CC_LOG_FILE", cfg.LogFile)
	writePowerShellEnv(&sb, "CC_LOG_MAX_SIZE", strconv.FormatInt(cfg.LogMaxSize, 10))
	writePowerShellEnv(&sb, "CC_LOG_MAX_BACKUPS", strconv.Itoa(cfg.LogMaxBackups))
	writePowerShellEnv(&sb, "CC_DAEMON_WINDOWS_SUPERVISED", "1")
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
			if !isValidEnvName(key) {
				slog.Warn("daemon: windows: dropping invalid env name from EnvExtra",
					"key", key)
				continue
			}
			value := cfg.EnvExtra[key]
			if value == "" {
				continue
			}
			writePowerShellEnv(&sb, key, value)
		}
	}
	fmt.Fprintf(&sb, "$statePath = %s\r\n$binary = %s\r\n", powerShellLiteral(windowsProcessStatePath()), powerShellLiteral(cfg.BinaryPath))
	sb.WriteString(`
function Write-SupervisorLog($message) {
    $line = [Text.Encoding]::UTF8.GetBytes("$(Get-Date -Format o) windows supervisor: $message` + "`r`n" + `")
    $log = [IO.File]::Open($env:CC_LOG_FILE, [IO.FileMode]::Append, [IO.FileAccess]::Write, [IO.FileShare]::ReadWrite)
    try { $log.Write($line, 0, $line.Length) } finally { $log.Dispose() }
}
$process = $null
try {
`)
	fmt.Fprintf(&sb, "Set-Location -LiteralPath %s\r\n", powerShellLiteral(cfg.WorkDir))
	if cfg.StartWithCodex {
		sb.WriteString(`
$sessionId = (Get-Process -Id $PID).SessionId
function Find-CodexDesktop {
    Get-Process -Name ChatGPT,Codex -ErrorAction SilentlyContinue | Where-Object {
        $_.SessionId -eq $sessionId -and $_.MainWindowHandle -ne 0 -and [Diagnostics.FileVersionInfo]::GetVersionInfo($_.Path).ProductName -eq 'Codex'
    } | Select-Object -First 1
}
`)
	}
	sb.WriteString("$basePath = $env:PATH\r\nwhile ($true) {\r\n")
	if cfg.StartWithCodex {
		sb.WriteString(`
    if ($null -eq (Find-CodexDesktop)) { Write-SupervisorLog 'Codex desktop closed; stopping'; exit 0 }
    $codexExe = Get-ChildItem -Path "$env:LOCALAPPDATA\OpenAI\Codex\bin\*\codex.exe" -File -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending | Select-Object -First 1
    if ($codexExe) { $env:PATH = $codexExe.DirectoryName + ';' + $basePath }
`)
	}
	sb.WriteString(`
    $start = New-Object System.Diagnostics.ProcessStartInfo
    $start.FileName = $binary
    $start.WorkingDirectory = (Get-Location).Path
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $process = [System.Diagnostics.Process]::Start($start)
    @{ pid = $process.Id; binary_path = $binary; start_ticks = $process.StartTime.ToUniversalTime().Ticks } | ConvertTo-Json | Set-Content -LiteralPath $statePath -Encoding UTF8
`)
	if cfg.StartWithCodex {
		sb.WriteString(`
    while (-not $process.WaitForExit(1000)) {
        if ($null -eq (Find-CodexDesktop)) { Write-SupervisorLog 'Codex desktop closed; stopping'; exit 0 }
    }
`)
	} else {
		sb.WriteString("    $process.WaitForExit()\r\n")
	}
	sb.WriteString(`
    $exitCode = $process.ExitCode
    Remove-Item -LiteralPath $statePath -Force -ErrorAction SilentlyContinue
    $process.Dispose()
    $process = $null
    if ($exitCode -eq 0) { exit 0 }
    Write-SupervisorLog "cc-connect exited with code $exitCode; restarting in 10 seconds"
    Start-Sleep -Seconds 10
}
} catch {
    Write-SupervisorLog $_.Exception.Message
    exit 1
} finally {
    if ($null -ne $process -and -not $process.HasExited) {
        & "$env:SystemRoot\System32\taskkill.exe" /PID $process.Id /T /F | Out-Null
    }
    Remove-Item -LiteralPath $statePath -Force -ErrorAction SilentlyContinue
}
`)
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
%s
if ($task.State -eq 'Running') {
	Stop-ScheduledTask -TaskName %s
}
if ($null -ne $child -and -not $child.HasExited) {
    & "$env:SystemRoot\System32\taskkill.exe" /PID $child.Id /T /F | Out-Null
    if ($LASTEXITCODE -ne 0 -and -not $child.HasExited) { Write-Error 'failed to stop cc-connect process tree'; exit 1 }
}
Remove-Item -LiteralPath %s -Force -ErrorAction SilentlyContinue
for ($i = 0; $i -lt 20; $i++) {
	$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue
	if ($null -eq $task -or $task.State -ne 'Running') { exit 0 }
	Start-Sleep -Milliseconds 500
}
Write-Error 'scheduled task did not stop within timeout'
exit 1
`, powerShellLiteral(windowsTaskName), windowsManagedChildScript(), powerShellLiteral(windowsTaskName), powerShellLiteral(windowsProcessStatePath()), powerShellLiteral(windowsTaskName)))
	if err != nil {
		return fmt.Errorf("stop scheduled task: %s (%w)", out, err)
	}
	return nil
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
