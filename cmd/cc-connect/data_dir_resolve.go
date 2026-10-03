package main

// data_dir / socket-path resolution helpers shared by client subcommands
// (send / cron / timer / relay / sessions). See Issue #1719: previously
// each subcommand only read --data-dir or CC_DATA_DIR, so a user who
// started the daemon with `cc-connect --config /etc/cc-connect.toml`
// could not make `cc-connect cron list` find the same API socket, even
// though the config file specified data_dir. The new helpers unify the
// resolution order across every client subcommand and expose a single
// function the run* functions can call.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/config"
)

// detectLang picks a Language for the socket-not-found diagnostic.
// Cron jobs and systemd timers usually run unattended, so they cannot
// rely on the OS locale for an interactive UI string. We default to
// English unless CC_LANG is set; the daemon-side error will use the
// richer i18n tables when it is reachable, but this client-side path
// has no daemon yet to ask.
func detectLang() string {
	if v := strings.TrimSpace(os.Getenv("CC_LANG")); v != "" {
		return v
	}
	return "en"
}

// resolveDataDir returns the data directory that the running cc-connect
// daemon uses, given the explicit --data-dir flag value ("" if unset),
// the --config flag value ("" if unset), and the current environment.
//
// Resolution order (highest priority first):
//
//  1. explicitDataDir — what the user passed to --data-dir on this command.
//  2. CC_DATA_DIR env var — set by deploy scripts, systemd units, etc.
//  3. data_dir from the config file at configPath — the daemon used this
//     when it bound its socket, so the client must use the same value.
//  4. ~/.cc-connect — historical default; matches what the daemon falls
//     back to when its own config file has no data_dir.
//
// The function is best-effort: if configPath is set but the file cannot
// be loaded (missing, malformed, permission denied), it returns the
// default rather than failing the whole subcommand. The reasoning is
// that "socket not found" is the user's actual complaint — failing the
// config read first would mask the diagnostic with a different error
// and prevent the operator from seeing the search path. If the
// daemon is actually running with a custom data_dir the user can still
// recover by setting --data-dir or CC_DATA_DIR explicitly.
func resolveDataDir(explicitDataDir, configPath string) string {
	if v := strings.TrimSpace(explicitDataDir); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("CC_DATA_DIR")); v != "" {
		return v
	}
	if v := strings.TrimSpace(configPath); v != "" {
		cfg, err := config.LoadPermissive(v)
		if err == nil && cfg != nil && strings.TrimSpace(cfg.DataDir) != "" {
			return cfg.DataDir
		}
	}
	return defaultDataDir()
}

// defaultDataDir returns ~/.cc-connect if the user's home directory can
// be resolved, otherwise the relative path ".cc-connect". This matches
// the fallback the daemon uses at startup (see config/config.go), so a
// daemon started with no config file and a client started with no
// config file agree on the same socket location.
func defaultDataDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cc-connect")
	}
	return ".cc-connect"
}

// resolveSocketPath returns the full Unix socket path for the running
// daemon given an already-resolved dataDir. Centralising this avoids
// drift between subcommands: every client agrees on
// <dataDir>/run/api.sock, which is where core/api.go NewAPIServer binds.
func resolveSocketPath(dataDir string) string {
	return filepath.Join(dataDir, "run", "api.sock")
}

// searchDirs returns the candidate data dirs the daemon might be using,
// in the order the resolver tries them. Exposed for diagnostic messages
// so the operator can see exactly which paths were considered before
// giving up with "socket not found".
func searchDirs(explicitDataDir, configPath string) []string {
	dirs := make([]string, 0, 4)
	if v := strings.TrimSpace(explicitDataDir); v != "" {
		dirs = append(dirs, v)
	}
	if v := strings.TrimSpace(os.Getenv("CC_DATA_DIR")); v != "" && !containsDir(dirs, v) {
		dirs = append(dirs, v)
	}
	if v := strings.TrimSpace(configPath); v != "" {
		cfg, err := config.LoadPermissive(v)
		if err == nil && cfg != nil && strings.TrimSpace(cfg.DataDir) != "" {
			if !containsDir(dirs, cfg.DataDir) {
				dirs = append(dirs, cfg.DataDir)
			}
		}
	}
	def := defaultDataDir()
	if !containsDir(dirs, def) {
		dirs = append(dirs, def)
	}
	return dirs
}

func containsDir(dirs []string, target string) bool {
	for _, d := range dirs {
		if d == target {
			return true
		}
	}
	return false
}

// printSocketNotFound writes a structured diagnostic to w when no daemon
// socket can be found at the resolved path. It shows the exact path that
// was tried, every candidate data_dir the resolver considered, and the
// most common fixes (env var, --data-dir, --config, --force after
// crash). The previous message was just "socket not found: <path>",
// which made Docker / systemd / chroot bugs impossible to triage.
// See Issue #1719.
func printSocketNotFound(w io.Writer, dataDir, configPath, sockPath string) {
	candidates := searchDirs("", configPath)
	// Build the candidate list, dropping the resolved one (which we show
	// separately as "the path that was tried") and any duplicates.
	seen := map[string]bool{sockPath: true, dataDir: true}
	unique := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if seen[c] {
			continue
		}
		seen[c] = true
		unique = append(unique, c)
	}

	var hint string
	switch detectLang() {
	case "zh", "zh-CN", "chinese":
		hint = "排查建议:\n" +
			"  1. 确认 cc-connect 守护进程已启动: `cc-connect --config " + configPath + "` (或对应的配置文件)\n" +
			"  2. 确认客户端与守护进程使用相同的 data_dir: 设置环境变量 CC_DATA_DIR, 或显式传 --data-dir / --config\n" +
			"  3. 如果 cc-connect 之前崩溃或被 kill -9, 用 `cc-connect --force` 重新启动以清理残留的 PID 锁\n" +
			"  4. Docker / 容器场景下, 确保 data_dir 挂载到可写卷, 且容器内用户对该目录有读写权限 (参见 docs/deployment.md)"
	default:
		hint = "Troubleshooting:\n" +
			"  1. Make sure the cc-connect daemon is running: `cc-connect --config " + configPath + "` (or the relevant config file)\n" +
			"  2. Make sure the client and daemon agree on data_dir: set CC_DATA_DIR, or pass --data-dir / --config explicitly\n" +
			"  3. If cc-connect previously crashed (kill -9, OOM, power loss), restart with `cc-connect --force` to clear the stale PID lock\n" +
			"  4. In Docker / container setups, ensure data_dir is mounted as a writable volume and the in-container user owns it (see docs/deployment.md)"
	}

	_, _ = fmt.Fprintf(w, "Error: cc-connect is not running.\n")
	_, _ = fmt.Fprintf(w, "  Tried socket: %s\n", sockPath)
	if len(unique) > 0 {
		_, _ = fmt.Fprintf(w, "  Other candidate data_dirs: %s\n", strings.Join(unique, ", "))
	}
	if configPath != "" {
		_, _ = fmt.Fprintf(w, "  --config used: %s\n", configPath)
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, hint)
}
