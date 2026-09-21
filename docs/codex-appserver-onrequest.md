# Codex app-server on-request install

This branch changes Codex app-server `suggest` mode to start threads with:

```toml
approvalPolicy = "on-request"
sandbox = "workspace-write"
```

## One-line install from a fork branch

PowerShell:

```powershell
$repo='https://github.com/<your-user>/cc-connect.git'; $branch='codex-appserver-onrequest-workspace'; $dir="$env:TEMP\cc-connect-patched"; Remove-Item -Recurse -Force $dir -ErrorAction SilentlyContinue; git clone --depth 1 --branch $branch $repo $dir; Push-Location $dir; go build -tags no_web -o cc-connect.exe ./cmd/cc-connect; New-Item -ItemType Directory -Force "$env:APPDATA\npm\node_modules\cc-connect\bin" | Out-Null; Copy-Item .\cc-connect.exe "$env:APPDATA\npm\node_modules\cc-connect\bin\cc-connect.exe" -Force; Pop-Location; cc-connect --version
```

Replace `<your-user>` with the fork owner.

Recommended `config.toml` for Feishu/Codex approval cards:

```toml
[projects.agent.options]
work_dir = "C:/Users/Administrator"
mode = "suggest"
backend = "app_server"
app_server_url = "stdio"

[projects.platforms.options]
enable_feishu_card = true
```