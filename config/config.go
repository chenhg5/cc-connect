package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"
)

// configMu serializes read-modify-write cycles to prevent lost updates.
var configMu sync.Mutex

// ConfigPath stores the path to the config file for saving
var ConfigPath string

type Config struct {
	DataDir     string          `toml:"data_dir"` // session store directory, default ~/.cc-connect
	Projects    []ProjectConfig `toml:"projects"`
	Commands    []CommandConfig `toml:"commands"`     // global custom slash commands
	Aliases     []AliasConfig   `toml:"aliases"`     // global command aliases
	BannedWords []string        `toml:"banned_words"` // messages containing any of these words are blocked
	Log         LogConfig       `toml:"log"`
	Language    string          `toml:"language"` // "en" or "zh", default is "en"
	Speech      SpeechConfig    `toml:"speech"`
	TTS         TTSConfig       `toml:"tts"`
	Display       DisplayConfig       `toml:"display"`
	StreamPreview StreamPreviewConfig `toml:"stream_preview"` // real-time streaming preview
	RateLimit     RateLimitConfig     `toml:"rate_limit"`     // per-session rate limiting
	Quiet            *bool               `toml:"quiet,omitempty"`              // global default for quiet mode; project-level overrides this
	Cron             CronConfig          `toml:"cron"`
	Webhook          WebhookConfig       `toml:"webhook"`
	Bridge           BridgeConfig        `toml:"bridge"`
	IdleTimeoutMins  *int                `toml:"idle_timeout_mins,omitempty"`  // max minutes between agent events; 0 = no timeout; default 120
}

// CronConfig controls cron job behavior.
type CronConfig struct {
	Silent *bool `toml:"silent"` // suppress cron start notification; default false
}

// WebhookConfig controls the external HTTP webhook endpoint.
type WebhookConfig struct {
	Enabled *bool  `toml:"enabled"`           // default false
	Port    int    `toml:"port,omitempty"`     // listen port; default 9111
	Token   string `toml:"token,omitempty"`    // shared secret for authentication; empty = no auth
	Path    string `toml:"path,omitempty"`     // URL path prefix; default "/hook"
}

// BridgeConfig controls the WebSocket bridge for external platform adapters.
type BridgeConfig struct {
	Enabled *bool  `toml:"enabled"`        // default false
	Port    int    `toml:"port,omitempty"`  // listen port; default 9810
	Token   string `toml:"token,omitempty"` // shared secret for authentication; required
	Path    string `toml:"path,omitempty"`  // URL path; default "/bridge/ws"
}

// DisplayConfig controls how intermediate messages (thinking, tool output) are shown.
type DisplayConfig struct {
	ThinkingMaxLen  *int `toml:"thinking_max_len"`    // max chars for thinking messages; 0 = no truncation; default 300
	ToolMaxLen      *int `toml:"tool_max_len"`        // max chars for tool use messages; 0 = no truncation; default 500
}

// StreamPreviewConfig controls real-time streaming preview in IM.
type StreamPreviewConfig struct {
	Enabled           *bool    `toml:"enabled"`                     // default true
	DisabledPlatforms []string `toml:"disabled_platforms,omitempty"` // platforms where preview is disabled (e.g. ["feishu"])
	IntervalMs        *int     `toml:"interval_ms"`                  // min ms between updates; default 1500
	MinDeltaChars     *int     `toml:"min_delta_chars"`              // min new chars before update; default 30
	MaxChars          *int     `toml:"max_chars"`                    // max preview length; default 2000
}

// RateLimitConfig controls per-session message rate limiting.
type RateLimitConfig struct {
	MaxMessages *int `toml:"max_messages"` // max messages per window; 0 = disabled; default 20
	WindowSecs  *int `toml:"window_secs"`  // window size in seconds; default 60
}

// SpeechConfig configures speech-to-text for voice messages.
type SpeechConfig struct {
	Enabled  bool   `toml:"enabled"`
	Provider string `toml:"provider"` // "openai" | "groq" | "qwen"
	Language string `toml:"language"` // e.g. "zh", "en"; empty = auto-detect
	OpenAI   struct {
		APIKey  string `toml:"api_key"`
		BaseURL string `toml:"base_url"`
		Model   string `toml:"model"`
	} `toml:"openai"`
	Groq struct {
		APIKey string `toml:"api_key"`
		Model  string `toml:"model"`
	} `toml:"groq"`
	Qwen struct {
		APIKey  string `toml:"api_key"`
		BaseURL string `toml:"base_url"`
		Model   string `toml:"model"`
	} `toml:"qwen"`
}

// TTSConfig configures text-to-speech output (mirrors SpeechConfig style).
type TTSConfig struct {
	Enabled     bool   `toml:"enabled"`
	Provider    string `toml:"provider"`     // "qwen" | "openai" | "minimax"
	Voice       string `toml:"voice"`        // default voice name
	TTSMode     string `toml:"tts_mode"`     // "voice_only" (default) | "always"
	MaxTextLen  int    `toml:"max_text_len"` // max rune count before skipping TTS; 0 = no limit
	OpenAI      struct {
		APIKey  string `toml:"api_key"`
		BaseURL string `toml:"base_url"`
		Model   string `toml:"model"`
	} `toml:"openai"`
	Qwen struct {
		APIKey  string `toml:"api_key"`
		BaseURL string `toml:"base_url"`
		Model   string `toml:"model"`
	} `toml:"qwen"`
	MiniMax struct {
		APIKey  string `toml:"api_key"`
		BaseURL string `toml:"base_url"`
		Model   string `toml:"model"`
	} `toml:"minimax"`
}

// HeartbeatConfig controls periodic heartbeat for a project.
type HeartbeatConfig struct {
	Enabled      *bool  `toml:"enabled"`                 // default false
	IntervalMins *int   `toml:"interval_mins,omitempty"` // minutes between heartbeats; default 30
	OnlyWhenIdle *bool  `toml:"only_when_idle,omitempty"`// only fire when the session is not busy; default true
	SessionKey   string `toml:"session_key,omitempty"`   // target session key (e.g. "telegram:123:123"); required
	Prompt       string `toml:"prompt,omitempty"`        // explicit prompt; if empty, reads HEARTBEAT.md from work_dir
	Silent       *bool  `toml:"silent,omitempty"`        // suppress heartbeat notification; default true
	TimeoutMins  *int   `toml:"timeout_mins,omitempty"`  // max execution time; default 30
}

// ProjectConfig binds one agent (with a specific work_dir) to one or more platforms.
type ProjectConfig struct {
	Name             string           `toml:"name"`
	Mode             string           `toml:"mode,omitempty"`     // "" or "multi-workspace"
	BaseDir          string           `toml:"base_dir,omitempty"` // parent dir for workspaces
	Agent            AgentConfig      `toml:"agent"`
	Platforms        []PlatformConfig `toml:"platforms"`
	Heartbeat        HeartbeatConfig  `toml:"heartbeat"`
	Quiet            *bool            `toml:"quiet,omitempty"`              // project-level quiet mode; overrides global setting
	InjectSender     *bool            `toml:"inject_sender,omitempty"`      // prepend sender identity (platform + user ID) to each message sent to the agent
	DisabledCommands []string         `toml:"disabled_commands,omitempty"`  // commands to disable for this project (e.g. ["restart", "upgrade"])
	AdminFrom        string           `toml:"admin_from,omitempty"`         // comma-separated user IDs allowed to run privileged commands; "*" = all allowed users
}

type AgentConfig struct {
	Type      string           `toml:"type"`
	Options   map[string]any   `toml:"options"`
	Providers []ProviderConfig `toml:"providers"`
}

type ProviderConfig struct {
	Name     string            `toml:"name"`
	APIKey   string            `toml:"api_key"`
	BaseURL  string            `toml:"base_url,omitempty"`
	Model    string            `toml:"model,omitempty"`
	Thinking string            `toml:"thinking,omitempty"`
	Env      map[string]string `toml:"env,omitempty"`
}

type PlatformConfig struct {
	Type    string         `toml:"type"`
	Options map[string]any `toml:"options"`
}

// AliasConfig maps a trigger string to a command (e.g. "帮助" → "/help").
type AliasConfig struct {
	Name    string `toml:"name"`    // trigger text (e.g. "帮助")
	Command string `toml:"command"` // target command (e.g. "/help")
}

// CommandConfig defines a user-customizable slash command that expands a prompt template or executes a shell command.
type CommandConfig struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
	Prompt      string `toml:"prompt"`      // prompt template (mutually exclusive with Exec)
	Exec        string `toml:"exec"`        // shell command to execute (mutually exclusive with Prompt)
	WorkDir     string `toml:"work_dir"`    // optional: working directory for exec command
}

type LogConfig struct {
	Level string `toml:"level"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := &Config{
		Log: LogConfig{Level: "info"},
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.DataDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.DataDir = filepath.Join(home, ".cc-connect")
		} else {
			cfg.DataDir = ".cc-connect"
		}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if len(c.Projects) == 0 {
		return fmt.Errorf("config: at least one [[projects]] entry is required")
	}
	for i, proj := range c.Projects {
		prefix := fmt.Sprintf("projects[%d]", i)
		if proj.Name == "" {
			return fmt.Errorf("config: %s.name is required", prefix)
		}
		if proj.Agent.Type == "" {
			return fmt.Errorf("config: %s.agent.type is required", prefix)
		}
		if len(proj.Platforms) == 0 {
			return fmt.Errorf("config: %s needs at least one [[projects.platforms]]", prefix)
		}
		for j, p := range proj.Platforms {
			if p.Type == "" {
				return fmt.Errorf("config: %s.platforms[%d].type is required", prefix, j)
			}
		}
		if proj.Mode == "multi-workspace" {
			if proj.BaseDir == "" {
				return fmt.Errorf("project %q: multi-workspace mode requires base_dir", proj.Name)
			}
			if _, ok := proj.Agent.Options["work_dir"]; ok {
				return fmt.Errorf("project %q: multi-workspace mode conflicts with agent work_dir (use base_dir instead)", proj.Name)
			}
		}
	}
	return nil
}

// SaveActiveProvider persists the active provider name for a project.
func SaveActiveProvider(projectName, providerName string) error {
	configMu.Lock()
	defer configMu.Unlock()
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == projectName {
			if cfg.Projects[i].Agent.Options == nil {
				cfg.Projects[i].Agent.Options = make(map[string]any)
			}
			cfg.Projects[i].Agent.Options["provider"] = providerName
			break
		}
	}
	return saveConfig(cfg)
}

// AddProviderToConfig adds a provider to a project's agent config and saves.
func AddProviderToConfig(projectName string, provider ProviderConfig) error {
	configMu.Lock()
	defer configMu.Unlock()
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	found := false
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == projectName {
			for _, existing := range cfg.Projects[i].Agent.Providers {
				if existing.Name == provider.Name {
					return fmt.Errorf("provider %q already exists in project %q", provider.Name, projectName)
				}
			}
			cfg.Projects[i].Agent.Providers = append(cfg.Projects[i].Agent.Providers, provider)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("project %q not found in config", projectName)
	}
	return saveConfig(cfg)
}

// RemoveProviderFromConfig removes a provider from a project's agent config and saves.
func RemoveProviderFromConfig(projectName, providerName string) error {
	configMu.Lock()
	defer configMu.Unlock()
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	found := false
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == projectName {
			providers := cfg.Projects[i].Agent.Providers
			for j := range providers {
				if providers[j].Name == providerName {
					cfg.Projects[i].Agent.Providers = append(providers[:j], providers[j+1:]...)
					found = true
					break
				}
			}
			break
		}
	}
	if !found {
		return fmt.Errorf("provider %q not found in project %q", providerName, projectName)
	}
	return saveConfig(cfg)
}

func saveConfig(cfg *Config) error {
	dir := filepath.Dir(ConfigPath)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpPath := tmp.Name()

	if err := toml.NewEncoder(tmp).Encode(cfg); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("encode config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, ConfigPath)
}

// SaveLanguage saves the language setting to the config file.
func SaveLanguage(lang string) error {
	configMu.Lock()
	defer configMu.Unlock()
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	cfg.Language = lang
	return saveConfig(cfg)
}

// ListProjects returns project names from the config file.
func ListProjects() ([]string, error) {
	if ConfigPath == "" {
		return nil, fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	var names []string
	for _, p := range cfg.Projects {
		names = append(names, p.Name)
	}
	return names, nil
}

// AddCommand adds a global custom command and persists to config.
func AddCommand(cmd CommandConfig) error {
	configMu.Lock()
	defer configMu.Unlock()
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	for _, c := range cfg.Commands {
		if c.Name == cmd.Name {
			return fmt.Errorf("command %q already exists", cmd.Name)
		}
	}
	cfg.Commands = append(cfg.Commands, cmd)
	return saveConfig(cfg)
}

// RemoveCommand removes a global custom command and persists to config.
func RemoveCommand(name string) error {
	configMu.Lock()
	defer configMu.Unlock()
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	found := false
	var remaining []CommandConfig
	for _, c := range cfg.Commands {
		if c.Name == name {
			found = true
		} else {
			remaining = append(remaining, c)
		}
	}
	if !found {
		return fmt.Errorf("command %q not found", name)
	}
	cfg.Commands = remaining
	return saveConfig(cfg)
}

// AddAlias adds a global alias and persists to config.
func AddAlias(alias AliasConfig) error {
	configMu.Lock()
	defer configMu.Unlock()
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	for i, a := range cfg.Aliases {
		if a.Name == alias.Name {
			cfg.Aliases[i] = alias
			return saveConfig(cfg)
		}
	}
	cfg.Aliases = append(cfg.Aliases, alias)
	return saveConfig(cfg)
}

// RemoveAlias removes a global alias and persists to config.
func RemoveAlias(name string) error {
	configMu.Lock()
	defer configMu.Unlock()
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	found := false
	var remaining []AliasConfig
	for _, a := range cfg.Aliases {
		if a.Name == name {
			found = true
		} else {
			remaining = append(remaining, a)
		}
	}
	if !found {
		return fmt.Errorf("alias %q not found", name)
	}
	cfg.Aliases = remaining
	return saveConfig(cfg)
}

// SaveDisplayConfig persists the display truncation settings to the config file.
func SaveDisplayConfig(thinkingMaxLen, toolMaxLen *int) error {
	configMu.Lock()
	defer configMu.Unlock()
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	if thinkingMaxLen != nil {
		cfg.Display.ThinkingMaxLen = thinkingMaxLen
	}
	if toolMaxLen != nil {
		cfg.Display.ToolMaxLen = toolMaxLen
	}
	return saveConfig(cfg)
}

// SaveTTSMode persists the TTS mode setting to the config file.
func SaveTTSMode(mode string) error {
	configMu.Lock()
	defer configMu.Unlock()
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	cfg.TTS.TTSMode = mode
	return saveConfig(cfg)
}

// GetProjectProviders returns providers for a given project.
func GetProjectProviders(projectName string) ([]ProviderConfig, string, error) {
	if ConfigPath == "" {
		return nil, "", fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, "", fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, "", fmt.Errorf("parse config: %w", err)
	}
	for _, p := range cfg.Projects {
		if p.Name == projectName {
			active, _ := p.Agent.Options["provider"].(string)
			return p.Agent.Providers, active, nil
		}
	}
	return nil, "", fmt.Errorf("project %q not found", projectName)
}
