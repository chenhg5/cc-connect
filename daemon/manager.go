package daemon

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
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
	// NoCaptureSecrets, when true, restricts the install-time env capture
	// to proxy-related variables only and skips both the config.toml ${ENV}
	// placeholder scan and any extension discoverers registered via
	// RegisterEnvDiscoverer. Operators who'd rather inject secrets via
	// keychain / `secret-tool` / EnvironmentFile= set this to keep token
	// values out of the service manager files on disk.
	NoCaptureSecrets bool
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
		cfg.EnvExtra = captureDaemonEnv(cfg.NoCaptureSecrets)
		if !cfg.NoCaptureSecrets {
			captureConfigEnvPlaceholders(filepath.Join(cfg.WorkDir, "config.toml"), cfg.EnvExtra)
		}
	}
	return nil
}

// captureDaemonEnv builds the EnvExtra map baked into the installed
// service file. Proxy-related vars are always captured. When
// noCaptureSecrets is false, every registered EnvDiscoverer is also
// invoked and its (envName -> value) pairs are merged in.
//
// Discoverer errors are logged but never fail the install — the
// daemon's job is to install the service; plugins surface their own
// per-feature warnings at runtime.
func captureDaemonEnv(noCaptureSecrets bool) map[string]string {
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

	for i, d := range snapshotEnvDiscoverers() {
		extra, err := d()
		if err != nil {
			slog.Warn("daemon: env discoverer reported warnings",
				"index", i, "err", err)
		}
		for k, v := range extra {
			if !isValidEnvName(k) {
				slog.Warn("daemon: dropping invalid env name from discoverer",
					"index", i, "key", k)
				continue
			}
			if v == "" {
				continue
			}
			env[k] = v
		}
	}
	return env
}

var configEnvPlaceholderPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// captureConfigEnvPlaceholders scans configPath for ${ENV_NAME} placeholders
// and, for each one set in the current process environment, copies it into
// env. cc-connect resolves these placeholders at startup using os.ExpandEnv;
// if the daemon's service file doesn't carry the values, the started daemon
// process will see empty strings and fail to authenticate to any platform.
//
// Errors are logged and swallowed: a broken or missing config.toml must not
// abort `daemon install`. Empty / unset env names are skipped silently.
func captureConfigEnvPlaceholders(configPath string, env map[string]string) {
	if strings.TrimSpace(configPath) == "" || env == nil {
		return
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("daemon: config env placeholder discovery failed",
				"path", configPath, "err", err)
		}
		return
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		slog.Warn("daemon: config env placeholder discovery failed",
			"path", configPath, "err", err)
		return
	}
	captureConfigEnvPlaceholdersInValue(reflect.ValueOf(raw), env)
}

func captureConfigEnvPlaceholdersInValue(v reflect.Value, env map[string]string) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			captureConfigEnvPlaceholdersInValue(v.Elem(), env)
		}
	case reflect.String:
		captureConfigEnvPlaceholdersInString(v.String(), env)
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			captureConfigEnvPlaceholdersInValue(v.Index(i), env)
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			captureConfigEnvPlaceholdersInValue(iter.Value(), env)
		}
	}
}

func captureConfigEnvPlaceholdersInString(s string, env map[string]string) {
	matches := configEnvPlaceholderPattern.FindAllStringSubmatch(s, -1)
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		name := match[1]
		if v, ok := os.LookupEnv(name); ok && v != "" {
			env[name] = v
		}
	}
}

