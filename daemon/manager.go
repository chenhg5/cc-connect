package daemon

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/chenhg5/cc-connect/ymsprofile"
)

const (
	DefaultLogMaxSize = 10 * 1024 * 1024 // 10 MB
	ServiceName       = "cc-connect"
)

type Config struct {
	BinaryPath string
	WorkDir    string
	LogFile    string
	LogMaxSize int64
	EnvPATH    string            // capture user's PATH so agents are accessible
	EnvExtra   map[string]string // selected environment variables needed by the service runtime
	// NoCaptureSecrets, when true, restricts the install-time env capture to
	// proxy-related variables only and skips yms-rca profile-derived
	// mcp.token_env names. Operators who'd rather inject secrets via
	// keychain / `secret-tool` / EnvironmentFile= set this to keep token
	// values out of the service manager files on disk.
	NoCaptureSecrets bool
	// ConnectionsDir overrides the yms-rca connections directory scanned at
	// install time to discover mcp.token_env names. Empty = default
	// (~/.yms-rca/connections).
	ConnectionsDir string
}

type Status struct {
	Installed bool
	Running   bool
	PID       int
	Platform  string // "systemd", "launchd", "schtasks"
}

type Manager interface {
	Install(cfg Config) error
	Uninstall() error
	Start() error
	Stop() error
	Restart() error
	Status() (*Status, error)
	Platform() string
}

// NewManager returns a platform-specific daemon manager.
func NewManager() (Manager, error) {
	return newPlatformManager()
}

func DefaultLogFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cc-connect", "logs", "cc-connect.log")
}

func DefaultDataDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cc-connect")
}

// ── Metadata ────────────────────────────────────────────────
// Stored at ~/.cc-connect/daemon.json so that `logs`, `status`,
// etc. can locate the log file without parsing service definitions.

type Meta struct {
	LogFile     string `json:"log_file"`
	LogMaxSize  int64  `json:"log_max_size"`
	WorkDir     string `json:"work_dir"`
	BinaryPath  string `json:"binary_path"`
	InstalledAt string `json:"installed_at"`
}

func metaPath() string {
	return filepath.Join(DefaultDataDir(), "daemon.json")
}

func SaveMeta(m *Meta) error {
	if err := os.MkdirAll(filepath.Dir(metaPath()), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(metaPath(), data, 0644)
}

func LoadMeta() (*Meta, error) {
	data, err := os.ReadFile(metaPath())
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func RemoveMeta() {
	os.Remove(metaPath())
}

func NowISO() string {
	return time.Now().Format(time.RFC3339)
}

func Resolve(cfg *Config) error {
	if cfg.BinaryPath == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("cannot detect binary path: %w", err)
		}
		real, err := filepath.EvalSymlinks(exe)
		if err == nil {
			exe = real
		}
		cfg.BinaryPath = exe
	}
	if cfg.WorkDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("cannot detect working directory: %w", err)
		}
		cfg.WorkDir = wd
	}
	if cfg.LogFile == "" {
		cfg.LogFile = DefaultLogFile()
	}
	if cfg.LogMaxSize <= 0 {
		cfg.LogMaxSize = DefaultLogMaxSize
	}
	if cfg.EnvPATH == "" {
		cfg.EnvPATH = os.Getenv("PATH")
	}
	if len(cfg.EnvExtra) == 0 {
		cfg.EnvExtra = captureDaemonEnv(cfg.NoCaptureSecrets, cfg.ConnectionsDir)
	}
	return nil
}

// captureDaemonEnv builds the EnvExtra map that gets baked into the
// installed service file. The proxy allowlist is always included; the
// yms-rca profile-derived mcp.token_env names are included unless the
// caller opts out via NoCaptureSecrets.
//
// Token values are *read* from os.LookupEnv to populate the result map,
// but never logged. Missing variables are silently skipped — the
// agent/yms-rca New() warn path handles per-profile user diagnostics.
func captureDaemonEnv(noCaptureSecrets bool, connectionsDir string) map[string]string {
	env := make(map[string]string)
	proxyKeys := []string{
		"http_proxy", "https_proxy", "no_proxy",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
		"all_proxy", "ALL_PROXY",
	}
	for _, key := range proxyKeys {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}

	if noCaptureSecrets {
		return env
	}

	dir := connectionsDir
	if dir == "" {
		dir = ymsprofile.DefaultConnectionsDir()
	}
	if dir == "" {
		return env
	}
	entries, err := ymsprofile.DiscoverConnectionTokenEnvNames(dir)
	if err != nil {
		// Discovery warnings (invalid env names, parse errors, or
		// dir-missing) are non-fatal: do not block install. The
		// agent/yms-rca New() startup will also surface profile-level
		// warnings at runtime.
		slog.Warn("daemon: yms-rca profile discovery had warnings",
			"dir", dir, "err", err)
	}
	for _, e := range entries {
		// envNameRegexp inside ymsprofile already filters invalid names;
		// double-check belt-and-suspenders because daemon files render
		// these as keys into systemd/launchd/PowerShell.
		if !ymsprofile.IsValidEnvName(e.EnvName) {
			slog.Warn("daemon: dropping invalid env name from profile",
				"profile", e.ProfileFile, "env", e.EnvName)
			continue
		}
		if v, ok := os.LookupEnv(e.EnvName); ok && v != "" {
			env[e.EnvName] = v
		}
	}
	return env
}
