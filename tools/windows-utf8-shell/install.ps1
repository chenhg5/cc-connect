param(
    [string]$DataDir = (Join-Path $env:USERPROFILE '.cc-connect')
)

$ErrorActionPreference = 'Stop'
$scriptPath = Join-Path $DataDir 'cc-connect-daemon.ps1'
if (-not (Test-Path -LiteralPath $scriptPath -PathType Leaf)) {
    throw 'Install cc-connect daemon before installing the UTF-8 shell.'
}
$script = [IO.File]::ReadAllText($scriptPath)
$pathPattern = '(?m)^\$env:PATH = ''((?:[^''\r\n]|'''')*)''\r?$'
$pathLines = [regex]::Matches($script, $pathPattern)
if ($pathLines.Count -ne 1) {
    throw 'Expected exactly one PATH declaration in cc-connect-daemon.ps1.'
}
$shellDir = Join-Path ([IO.Path]::GetFullPath($DataDir)) 'utf8-shell'
$originalPath = $pathLines[0].Groups[1].Value.Replace("''", "'")
$pathEntries = @($originalPath.Split(';') | Where-Object {
    $_.TrimEnd('\') -ine $shellDir.TrimEnd('\')
})
$newPath = $shellDir + ';' + ($pathEntries -join ';')
$pathLine = '$env:PATH = ' + "'" + $newPath.Replace("'", "''") + "'"
$fixedScript = $script.Remove($pathLines[0].Index, $pathLines[0].Length).Insert($pathLines[0].Index, $pathLine)
$tokens = $null
$parseErrors = $null
[Management.Automation.Language.Parser]::ParseInput($fixedScript, [ref]$tokens, [ref]$parseErrors) | Out-Null
if ($parseErrors.Count -ne 0) {
    throw 'Modified daemon script failed its PowerShell syntax check.'
}

New-Item -ItemType Directory -Path $shellDir -Force | Out-Null
$temporaryExe = Join-Path $shellDir 'pwsh.new.exe'
$targetExe = Join-Path $shellDir 'pwsh.exe'
try {
    $bundledExe = Join-Path $PSScriptRoot 'pwsh.exe'
    if (Test-Path -LiteralPath $bundledExe -PathType Leaf) {
        Copy-Item -LiteralPath $bundledExe -Destination $temporaryExe
    } else {
        $go = (Get-Command go -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
        & $go build -trimpath -ldflags '-s -w' -o $temporaryExe (Join-Path $PSScriptRoot 'main_windows.go')
        if ($LASTEXITCODE -ne 0) {
            throw 'Failed to build the UTF-8 shell.'
        }
    }
    $backupDir = Join-Path $DataDir ('backups\utf8-shell-' + (Get-Date -Format 'yyyyMMdd-HHmmss-fff'))
    New-Item -ItemType Directory -Path $backupDir | Out-Null
    Copy-Item -LiteralPath $scriptPath -Destination (Join-Path $backupDir 'cc-connect-daemon.ps1')
    if (Test-Path -LiteralPath $targetExe) {
        Copy-Item -LiteralPath $targetExe -Destination (Join-Path $backupDir 'pwsh.exe')
    }
    Move-Item -LiteralPath $temporaryExe -Destination $targetExe -Force
    [IO.File]::WriteAllText($scriptPath, $fixedScript, [Text.UTF8Encoding]::new($false))
    Write-Output "Installed UTF-8 shell. Backup: $backupDir"
    Write-Output 'Run cc-connect daemon restart when the current task is finished.'
} finally {
    if (Test-Path -LiteralPath $temporaryExe) {
        Remove-Item -LiteralPath $temporaryExe -Force
    }
}
