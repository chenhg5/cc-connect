package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
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
		cfg.EnvExtra = captureDaemonEnv()
	}
	return nil
}

func captureDaemonEnv() map[string]string {
	keys := []string{
		"http_proxy", "https_proxy", "no_proxy",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
		"all_proxy", "ALL_PROXY",
	}
	env := make(map[string]string, len(keys))
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
	return env
}
