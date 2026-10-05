# Windows UTF-8 shell for Codex

This optional helper fixes Chinese PowerShell tool output on Windows systems
whose console code page is CP936. It starts PowerShell with console code page
65001, forwards stdin/stdout/stderr, and preserves the command's exit code.
The next `pwsh.exe` in `PATH` is selected dynamically; no user or runtime path is
hardcoded. Codex's sandbox and permission mode still apply.

When Codex launches a detached tool, the wrapper reexecutes itself with
`CREATE_NO_WINDOW` to obtain a console code page without creating a visible
window or Windows Terminal tab. It does not allocate a window and hide it later.

## 安装

适用于 Windows、已安装 PowerShell 7、已有 `cc-connect daemon` 的环境。
安装脚本旁若有同版本的预编译 `pwsh.exe` 包装程序，安装时不需要 Go。
从源码仓库安装时需要 Go（版本要求见 `go.mod`）；编译后运行时不需要 Go。
确保真实的 `pwsh.exe` 在 `PATH` 中，在仓库或解压后的发布包目录执行：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\tools\windows-utf8-shell\install.ps1
# 当前机器人任务结束后应用启动环境：
cc-connect daemon restart
```

非默认数据目录可加 `-DataDir 'D:\cc-connect-data'`。脚本在数据目录安装
`utf8-shell\pwsh.exe`，备份后台启动脚本和已有包装程序，并将包装程序的目录
放到后台 `PATH` 首位。重复安装不会重复追加同一目录；`daemon restart`
和 Codex 插件再次启动时会沿用这一环境。

必须修改后台启动环境：cc-connect 的任务环境会重新注入 `PATH`，仅在
`[projects.agent.options.env]` 中添加包装程序目录会被覆盖。此脚本不修改
`config.toml`、系统 `PATH` 或 Codex 的全局配置。重新执行 `daemon install`
会生成新的后台脚本，需要再次执行本安装脚本。

## 验证和回滚

在飞书让机器人查询当前目录，并列出带中文名称的文件或目录。新的工具输出
应完整显示中文，旧卡片中已经丢失的字符不会自动恢复。也可执行
`go test ./tools/windows-utf8-shell` 检查中文输出、标准错误、退出码及后台环境覆盖回归。

回滚时恢复安装脚本输出的备份目录中的 `cc-connect-daemon.ps1`，待当前任务
结束后执行 `cc-connect daemon restart`。如果原先已有包装程序，可一并恢复
备份中的 `pwsh.exe`。
