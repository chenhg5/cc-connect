package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// configMu serializes read-modify-write cycles to prevent lost updates.
var configMu sync.Mutex

// ConfigPath stores the path to the config file for saving
var ConfigPath string

type Config struct {
	DataDir         string              `toml:"data_dir"` // session store directory, default ~/.cc-connect
	AttachmentSend  string              `toml:"attachment_send"`
	Projects        []ProjectConfig     `toml:"projects"`
	Commands        []CommandConfig     `toml:"commands"`     // global custom slash commands
	Aliases         []AliasConfig       `toml:"aliases"`      // global command aliases
	BannedWords     []string            `toml:"banned_words"` // messages containing any of these words are blocked
	Log             LogConfig           `toml:"log"`
	Language        string              `toml:"language"` // "en" or "zh", default is "en"
	Speech          SpeechConfig        `toml:"speech"`
	TTS             TTSConfig           `toml:"tts"`
	Display         DisplayConfig       `toml:"display"`
	StreamPreview   StreamPreviewConfig `toml:"stream_preview"`  // real-time streaming preview
	RateLimit       RateLimitConfig          `toml:"rate_limit"`           // per-session rate limiting
	OutgoingRateLimit OutgoingRateLimitConfig `toml:"outgoing_rate_limit"`  // outgoing message throttling
	Relay           RelayConfig         `toml:"relay"`           // bot-to-bot relay behavior
	Quiet           *bool               `toml:"quiet,omitempty"` // global default for quiet mode; project-level overrides this
	Cron            CronConfig          `toml:"cron"`
	Webhook         WebhookConfig       `toml:"webhook"`
	Bridge          BridgeConfig        `toml:"bridge"`
	Management      ManagementConfig    `toml:"management"`
	IdleTimeoutMins *int                `toml:"idle_timeout_mins,omitempty"` // max minutes between agent events; 0 = no timeout; default 120
}

// CronConfig controls cron job behavior.
type CronConfig struct {
	Silent *bool `toml:"silent"` // suppress cron start notification; default false
}

// WebhookConfig controls the external HTTP webhook endpoint.
type WebhookConfig struct {
	Enabled *bool  `toml:"enabled"`         // default false
	Port    int    `toml:"port,omitempty"`  // listen port; default 9111
	Token   string `toml:"token,omitempty"` // shared secret for authentication; empty = no auth
	Path    string `toml:"path,omitempty"`  // URL path prefix; default "/hook"
}

// BridgeConfig controls the WebSocket bridge for external platform adapters.
type BridgeConfig struct {
	Enabled     *bool    `toml:"enabled"`                // default false
	Port        int      `toml:"port,omitempty"`         // listen port; default 9810
	Token       string   `toml:"token,omitempty"`        // shared secret for authentication; required
	Path        string   `toml:"path,omitempty"`         // URL path; default "/bridge/ws"
	CORSOrigins []string `toml:"cors_origins,omitempty"` // allowed CORS origins; empty = no CORS
}

// ManagementConfig controls the HTTP Management API for external tools.
type ManagementConfig struct {
	Enabled     *bool    `toml:"enabled"`                // default false
	Port        int      `toml:"port,omitempty"`         // listen port; default 9820
	Token       string   `toml:"token,omitempty"`        // shared secret for authentication; required
	CORSOrigins []string `toml:"cors_origins,omitempty"` // allowed CORS origins; empty = no CORS
}

// DisplayConfig controls how intermediate messages (thinking, tool output) are shown.
type DisplayConfig struct {
	ThinkingMaxLen *int `toml:"thinking_max_len"` // max chars for thinking messages; 0 = no truncation; default 300
	ToolMaxLen     *int `toml:"tool_max_len"`     // max chars for tool use messages; 0 = no truncation; default 500
}

// StreamPreviewConfig controls real-time streaming preview in IM.
type StreamPreviewConfig struct {
	Enabled           *bool    `toml:"enabled"`                      // default true
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

// OutgoingRateLimitConfig controls how fast messages are sent TO platforms.
// Prevents account bans on platforms with strict API rate limits (e.g. WeChat Work).
type OutgoingRateLimitConfig struct {
	MaxPerSecond *float64                               `toml:"max_per_second"` // messages per second; 0 = unlimited (default)
	Burst        *int                                   `toml:"burst"`          // max burst size; default = ceil(max_per_second)
	Platforms    map[string]OutgoingRateLimitPlatConfig  `toml:"platforms"`      // per-platform overrides keyed by platform type name
}

// OutgoingRateLimitPlatConfig is a per-platform override for outgoing rate limiting.
type OutgoingRateLimitPlatConfig struct {
	MaxPerSecond *float64 `toml:"max_per_second"`
	Burst        *int     `toml:"burst"`
}

// UsersConfig controls per-user role assignments and policies within a project.
type UsersConfig struct {
	DefaultRole string                `toml:"default_role,omitempty"` // role for unmatched users; default "member"
	Roles       map[string]RoleConfig `toml:"roles,omitempty"`
}

// RoleConfig defines policies for a user role.
type RoleConfig struct {
	UserIDs          []string         `toml:"user_ids"`
	DisabledCommands []string         `toml:"disabled_commands,omitempty"`
	RateLimit        *RateLimitConfig `toml:"rate_limit,omitempty"` // nil = inherit global
}

// RelayConfig controls bot-to-bot relay behavior.
type RelayConfig struct {
	TimeoutSecs *int `toml:"timeout_secs"` // max seconds to wait for relay response; 0 = disabled; default 120
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
	Enabled    bool   `toml:"enabled"`
	Provider   string `toml:"provider"`     // "qwen" | "openai" | "minimax" | "espeak" | "pico" | "edge"
	Voice      string `toml:"voice"`        // default voice name (for edge: "zh-CN-XiaoxiaoNeural"; for pico: "zh-CN"; for espeak: "zh")
	TTSMode    string `toml:"tts_mode"`     // "voice_only" (default) | "always"
	MaxTextLen int    `toml:"max_text_len"` // max rune count before skipping TTS; 0 = no limit
	OpenAI     struct {
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
	Enabled      *bool  `toml:"enabled"`                  // default false
	IntervalMins *int   `toml:"interval_mins,omitempty"`  // minutes between heartbeats; default 30
	OnlyWhenIdle *bool  `toml:"only_when_idle,omitempty"` // only fire when the session is not busy; default true
	SessionKey   string `toml:"session_key,omitempty"`    // target session key (e.g. "telegram:123:123"); required
	Prompt       string `toml:"prompt,omitempty"`         // explicit prompt; if empty, reads HEARTBEAT.md from work_dir
	Silent       *bool  `toml:"silent,omitempty"`         // suppress heartbeat notification; default true
	TimeoutMins  *int   `toml:"timeout_mins,omitempty"`   // max execution time; default 30
}

// AutoCompressConfig controls automatic context compression for a project.
type AutoCompressConfig struct {
	Enabled    *bool `toml:"enabled,omitempty"`      // default false
	MaxTokens  *int  `toml:"max_tokens,omitempty"`   // estimated token threshold to trigger /compress
	MinGapMins *int  `toml:"min_gap_mins,omitempty"` // minimum minutes between auto-compress runs (default 30)
}

// ProjectConfig binds one agent (with a specific work_dir) to one or more platforms.
type ProjectConfig struct {
	Name         string             `toml:"name"`
	Mode         string             `toml:"mode,omitempty"`     // "" or "multi-workspace"
	BaseDir      string             `toml:"base_dir,omitempty"` // parent dir for workspaces
	Agent        AgentConfig        `toml:"agent"`
	Platforms    []PlatformConfig   `toml:"platforms"`
	Heartbeat    HeartbeatConfig    `toml:"heartbeat"`
	AutoCompress AutoCompressConfig `toml:"auto_compress"`
	// ShowContextIndicator: nil/true = append [ctx: ~N%] to assistant replies; false = hide.
	ShowContextIndicator *bool        `toml:"show_context_indicator,omitempty"`
	Quiet                *bool        `toml:"quiet,omitempty"`             // project-level quiet mode; overrides global setting
	InjectSender         *bool        `toml:"inject_sender,omitempty"`     // prepend sender identity (platform + user ID) to each message sent to the agent
	DisabledCommands     []string     `toml:"disabled_commands,omitempty"` // commands to disable for this project (e.g. ["restart", "upgrade"])
	AdminFrom            string       `toml:"admin_from,omitempty"`        // comma-separated user IDs allowed to run privileged commands; "*" = all allowed users
	Users                *UsersConfig `toml:"users,omitempty"`             // per-user role config; nil = legacy behavior
}

type AgentConfig struct {
	Type      string           `toml:"type"`
	Options   map[string]any   `toml:"options"`
	Providers []ProviderConfig `toml:"providers"`
}

// ProviderModelConfig defines a selectable model entry for a provider,
// with an optional short alias used by the /model command.
type ProviderModelConfig struct {
	Model string `toml:"model"`
	Alias string `toml:"alias,omitempty"`
}

type ProviderConfig struct {
	Name     string                `toml:"name"`
	APIKey   string                `toml:"api_key"`
	BaseURL  string                `toml:"base_url,omitempty"`
	Model    string                `toml:"model,omitempty"`
	Models   []ProviderModelConfig `toml:"models,omitempty"`
	Thinking string                `toml:"thinking,omitempty"`
	Env      map[string]string     `toml:"env,omitempty"`
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
	Prompt      string `toml:"prompt"`   // prompt template (mutually exclusive with Exec)
	Exec        string `toml:"exec"`     // shell command to execute (mutually exclusive with Prompt)
	WorkDir     string `toml:"work_dir"` // optional: working directory for exec command
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
	cfg.AttachmentSend = strings.ToLower(strings.TrimSpace(cfg.AttachmentSend))
	if cfg.AttachmentSend == "" {
		cfg.AttachmentSend = "on"
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	switch strings.ToLower(strings.TrimSpace(c.AttachmentSend)) {
	case "", "on", "off":
	default:
		return fmt.Errorf("config: attachment_send must be \"on\" or \"off\"")
	}
	if c.Relay.TimeoutSecs != nil && *c.Relay.TimeoutSecs < 0 {
		return fmt.Errorf("config: relay.timeout_secs must be >= 0")
	}
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
		if err := validateUsersConfig(prefix, proj.Users); err != nil {
			return err
		}
	}
	return nil
}

// validateUsersConfig checks the [projects.users] section for consistency.
func validateUsersConfig(prefix string, u *UsersConfig) error {
	if u == nil {
		return nil
	}
	if len(u.Roles) == 0 {
		return fmt.Errorf("config: %s.users has no roles defined", prefix)
	}
	wildcardCount := 0
	seenUserIDs := make(map[string]string) // userID → role name
	for roleName, rc := range u.Roles {
		if len(rc.UserIDs) == 0 {
			return fmt.Errorf("config: %s.users.roles.%s has empty user_ids", prefix, roleName)
		}
		for _, uid := range rc.UserIDs {
			if uid == "*" {
				wildcardCount++
				continue
			}
			lower := strings.ToLower(uid)
			if prev, dup := seenUserIDs[lower]; dup {
				return fmt.Errorf("config: %s.users: user %q appears in both role %q and %q", prefix, uid, prev, roleName)
			}
			seenUserIDs[lower] = roleName
		}
	}
	if wildcardCount > 1 {
		return fmt.Errorf("config: %s.users: wildcard user_ids=[\"*\"] appears in multiple roles", prefix)
	}
	if u.DefaultRole != "" {
		if _, ok := u.Roles[u.DefaultRole]; !ok {
			return fmt.Errorf("config: %s.users.default_role %q does not match any defined role", prefix, u.DefaultRole)
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

// SaveProviderModel persists the selected model for a provider in a project.
func SaveProviderModel(projectName, providerName, model string) error {
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
		if cfg.Projects[i].Name != projectName {
			continue
		}
		for j := range cfg.Projects[i].Agent.Providers {
			if cfg.Projects[i].Agent.Providers[j].Name == providerName {
				cfg.Projects[i].Agent.Providers[j].Model = model
				return saveConfig(cfg)
			}
		}
		return fmt.Errorf("provider %q not found in project %q", providerName, projectName)
	}
	return fmt.Errorf("project %q not found in config", projectName)
}

// SaveAgentModel persists the selected default model for a project's agent.
func SaveAgentModel(projectName, model string) error {
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
		if cfg.Projects[i].Name != projectName {
			continue
		}
		if cfg.Projects[i].Agent.Options == nil {
			cfg.Projects[i].Agent.Options = make(map[string]any)
		}
		cfg.Projects[i].Agent.Options["model"] = model
		return saveConfig(cfg)
	}
	return fmt.Errorf("project %q not found in config", projectName)
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

	var buf strings.Builder
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("encode config: %w", err)
	}

	formatted := formatTOML(buf.String())
	if _, err := tmp.WriteString(formatted); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write config: %w", err)
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

// formatTOML post-processes raw TOML encoder output to improve readability:
//   - inserts blank lines before section/array-table headers
//   - removes key-value lines whose value is a zero/empty default
//   - removes section headers that have no remaining key-value pairs
func formatTOML(raw string) string {
	lines := strings.Split(raw, "\n")

	// Pass 1: mark zero-value lines for removal.
	// A "zero-value" line looks like:  key = "" | key = 0 | key = false | key = []
	keep := make([]bool, len(lines))
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed[0] == '[' || trimmed[0] == '#' {
			keep[i] = true
			continue
		}
		if eqIdx := strings.Index(trimmed, "="); eqIdx > 0 {
			val := strings.TrimSpace(trimmed[eqIdx+1:])
			switch val {
			case `""`, "0", "false", "[]":
				keep[i] = false
				continue
			}
		}
		keep[i] = true
	}

	// Pass 2: remove empty sections (header followed only by blank/removed lines
	// until the next header or EOF).
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] != '[' {
			continue
		}
		hasContent := false
		for j := i + 1; j < len(lines); j++ {
			t := strings.TrimSpace(lines[j])
			if len(t) > 0 && t[0] == '[' {
				break
			}
			if keep[j] && t != "" {
				hasContent = true
				break
			}
		}
		if !hasContent {
			keep[i] = false
		}
	}

	// Pass 3: assemble output with blank lines before section headers.
	var out []string
	for i, line := range lines {
		if !keep[i] {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 0 && trimmed[0] == '[' {
			if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
				out = append(out, "")
			}
		}
		out = append(out, line)
	}

	// Trim trailing blank lines, then ensure single trailing newline.
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n") + "\n"
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

// FeishuCredentialUpdateOptions controls how Feishu/Lark platform credentials
// are written back into config.toml for a specific project.
type FeishuCredentialUpdateOptions struct {
	ProjectName       string // required
	PlatformIndex     int    // 1-based index among feishu/lark platforms in the project; 0 = first
	PlatformType      string // optional target type: "feishu" or "lark"; empty keeps existing type
	AppID             string // required
	AppSecret         string // required
	OwnerOpenID       string // optional owner id from onboarding flow
	SetAllowFromEmpty bool   // when true, set allow_from=OwnerOpenID only if currently empty
}

// EnsureProjectWithFeishuOptions controls project auto-provisioning for Feishu/Lark setup.
type EnsureProjectWithFeishuOptions struct {
	ProjectName      string // required
	PlatformType     string // optional: "feishu" or "lark", default "feishu"
	CloneFromProject string // optional source project name to clone agent config from
	WorkDir          string // optional default work_dir when creating project
	AgentType        string // optional default agent type when no source project exists, default "codex"
}

// EnsureProjectWithFeishuResult describes whether project provisioning created a new project.
type EnsureProjectWithFeishuResult struct {
	Created          bool
	AddedPlatform    bool
	ProjectIndex     int
	PlatformAbsIndex int // first feishu/lark platform in project, -1 if absent
	PlatformType     string
}

// FeishuCredentialUpdateResult describes where credentials were written.
type FeishuCredentialUpdateResult struct {
	ProjectName      string
	ProjectIndex     int
	PlatformAbsIndex int // absolute index in projects[i].platforms
	PlatformType     string
	AllowFrom        string
}

// EnsureProjectWithFeishuPlatform ensures target project exists. If project does
// not exist, it creates one with a Feishu/Lark platform so credentials can be
// written immediately.
func EnsureProjectWithFeishuPlatform(opts EnsureProjectWithFeishuOptions) (*EnsureProjectWithFeishuResult, error) {
	configMu.Lock()
	defer configMu.Unlock()

	if ConfigPath == "" {
		return nil, fmt.Errorf("config path not set")
	}
	projectName := strings.TrimSpace(opts.ProjectName)
	if projectName == "" {
		return nil, fmt.Errorf("project name is required")
	}

	platformType := strings.ToLower(strings.TrimSpace(opts.PlatformType))
	if platformType == "" {
		platformType = "feishu"
	}
	if platformType != "feishu" && platformType != "lark" {
		return nil, fmt.Errorf("invalid platform type %q (want feishu or lark)", opts.PlatformType)
	}

	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	raw := string(data)
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	for i := range cfg.Projects {
		if cfg.Projects[i].Name != projectName {
			continue
		}
		platformIdx := firstFeishuPlatformIndex(cfg.Projects[i].Platforms)
		added := false
		if platformIdx < 0 {
			lines, hadTrailing := splitConfigLines(raw)
			spans := buildRawProjectSpans(lines)
			if i >= len(spans) {
				return nil, fmt.Errorf("project %q located in parsed config but not raw file", projectName)
			}
			insertAt := spans[i].end + 1
			block := make([]string, 0, 7)
			if insertAt > 0 && strings.TrimSpace(lines[insertAt-1]) != "" {
				block = append(block, "")
			}
			block = append(block, "[[projects.platforms]]")
			block = append(block, fmt.Sprintf("type = %s", quoteTomlString(platformType)))
			block = append(block, "")
			block = append(block, "[projects.platforms.options]")
			if insertAt < len(lines) && strings.TrimSpace(lines[insertAt]) != "" {
				block = append(block, "")
			}
			lines = insertLines(lines, insertAt, block)
			if err := writeRawConfig(joinConfigLines(lines, hadTrailing)); err != nil {
				return nil, err
			}
			platformIdx = len(cfg.Projects[i].Platforms)
			added = true
		}
		return &EnsureProjectWithFeishuResult{
			Created:          false,
			AddedPlatform:    added,
			ProjectIndex:     i,
			PlatformAbsIndex: platformIdx,
			PlatformType:     platformType,
		}, nil
	}

	proj := ProjectConfig{
		Name:      projectName,
		Agent:     pickAgentTemplateForNewProject(cfg, opts),
		Platforms: []PlatformConfig{{Type: platformType, Options: map[string]any{}}},
	}
	if proj.Agent.Type == "" {
		proj.Agent.Type = "codex"
	}
	if proj.Agent.Options == nil {
		proj.Agent.Options = map[string]any{}
	}
	workDir := strings.TrimSpace(opts.WorkDir)
	if workDir != "" {
		proj.Agent.Options["work_dir"] = workDir
	}

	lines, hadTrailing := splitConfigLines(raw)
	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
		lines = append(lines, "")
	}
	lines = append(lines, "[[projects]]")
	lines = append(lines, fmt.Sprintf("name = %s", quoteTomlString(proj.Name)))
	lines = append(lines, "")
	lines = append(lines, "[projects.agent]")
	lines = append(lines, fmt.Sprintf("type = %s", quoteTomlString(proj.Agent.Type)))
	lines = append(lines, "")
	lines = append(lines, "[projects.agent.options]")
	if wd, ok := proj.Agent.Options["work_dir"].(string); ok && strings.TrimSpace(wd) != "" {
		lines = append(lines, fmt.Sprintf("work_dir = %s", quoteTomlString(wd)))
	}
	if mode, ok := proj.Agent.Options["mode"].(string); ok && strings.TrimSpace(mode) != "" {
		lines = append(lines, fmt.Sprintf("mode = %s", quoteTomlString(mode)))
	}
	lines = append(lines, "")
	lines = append(lines, "[[projects.platforms]]")
	lines = append(lines, fmt.Sprintf("type = %s", quoteTomlString(platformType)))
	lines = append(lines, "")
	lines = append(lines, "[projects.platforms.options]")
	if err := writeRawConfig(joinConfigLines(lines, hadTrailing)); err != nil {
		return nil, err
	}

	return &EnsureProjectWithFeishuResult{
		Created:          true,
		AddedPlatform:    false,
		ProjectIndex:     len(cfg.Projects) - 1,
		PlatformAbsIndex: len(cfg.Projects[len(cfg.Projects)-1].Platforms) - 1,
		PlatformType:     platformType,
	}, nil
}

// SaveFeishuPlatformCredentials updates app_id/app_secret for a project's
// Feishu/Lark platform and persists the config atomically.
func SaveFeishuPlatformCredentials(opts FeishuCredentialUpdateOptions) (*FeishuCredentialUpdateResult, error) {
	configMu.Lock()
	defer configMu.Unlock()

	if ConfigPath == "" {
		return nil, fmt.Errorf("config path not set")
	}
	if strings.TrimSpace(opts.ProjectName) == "" {
		return nil, fmt.Errorf("project name is required")
	}
	if strings.TrimSpace(opts.AppID) == "" || strings.TrimSpace(opts.AppSecret) == "" {
		return nil, fmt.Errorf("app_id and app_secret are required")
	}
	if opts.PlatformIndex < 0 {
		return nil, fmt.Errorf("platform index must be >= 0")
	}
	if opts.PlatformType != "" && opts.PlatformType != "feishu" && opts.PlatformType != "lark" {
		return nil, fmt.Errorf("invalid platform type %q (want feishu or lark)", opts.PlatformType)
	}

	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	raw := string(data)
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	projectIdx := -1
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == opts.ProjectName {
			projectIdx = i
			break
		}
	}
	if projectIdx < 0 {
		return nil, fmt.Errorf("project %q not found", opts.ProjectName)
	}

	proj := &cfg.Projects[projectIdx]
	candidates := make([]int, 0, len(proj.Platforms))
	for i := range proj.Platforms {
		t := strings.ToLower(strings.TrimSpace(proj.Platforms[i].Type))
		if t == "feishu" || t == "lark" {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("project %q has no feishu/lark platform", opts.ProjectName)
	}

	targetPos := 0
	if opts.PlatformIndex > 0 {
		targetPos = opts.PlatformIndex - 1
	}
	if targetPos < 0 || targetPos >= len(candidates) {
		return nil, fmt.Errorf(
			"platform index %d out of range: project %q has %d feishu/lark platform(s)",
			opts.PlatformIndex, opts.ProjectName, len(candidates),
		)
	}

	absIdx := candidates[targetPos]
	platform := &proj.Platforms[absIdx]
	if opts.PlatformType != "" {
		platform.Type = opts.PlatformType
	}
	if platform.Options == nil {
		platform.Options = map[string]any{}
	}

	platform.Options["app_id"] = strings.TrimSpace(opts.AppID)
	platform.Options["app_secret"] = strings.TrimSpace(opts.AppSecret)

	allowFrom := strings.TrimSpace(stringOption(platform.Options["allow_from"]))
	if opts.SetAllowFromEmpty && allowFrom == "" && strings.TrimSpace(opts.OwnerOpenID) != "" {
		allowFrom = strings.TrimSpace(opts.OwnerOpenID)
	}

	lines, hadTrailing := splitConfigLines(raw)
	spans := buildRawProjectSpans(lines)
	if projectIdx >= len(spans) {
		return nil, fmt.Errorf("project %q located in parsed config but not raw file", opts.ProjectName)
	}
	if absIdx >= len(spans[projectIdx].platforms) {
		return nil, fmt.Errorf("feishu/lark platform located in parsed config but not raw file")
	}

	reloadSpan := func() rawPlatformSpan {
		spans = buildRawProjectSpans(lines)
		return spans[projectIdx].platforms[absIdx]
	}
	span := spans[projectIdx].platforms[absIdx]

	if opts.PlatformType != "" {
		if span.typeLine >= 0 {
			lines[span.typeLine] = replaceTomlStringKeyLine(lines[span.typeLine], "type", opts.PlatformType)
		} else {
			lines = insertLines(lines, span.start+1, []string{fmt.Sprintf("type = %s", quoteTomlString(opts.PlatformType))})
		}
		span = reloadSpan()
	}

	if span.optionsStart < 0 {
		insertAt := span.end + 1
		block := make([]string, 0, 4)
		if insertAt > 0 && strings.TrimSpace(lines[insertAt-1]) != "" {
			block = append(block, "")
		}
		block = append(block, "[projects.platforms.options]")
		if insertAt < len(lines) && strings.TrimSpace(lines[insertAt]) != "" {
			block = append(block, "")
		}
		lines = insertLines(lines, insertAt, block)
		span = reloadSpan()
	}

	lines = upsertTomlStringKey(lines, span.optionsStart+1, span.optionsEnd, "app_id", strings.TrimSpace(opts.AppID))
	span = reloadSpan()
	lines = upsertTomlStringKey(lines, span.optionsStart+1, span.optionsEnd, "app_secret", strings.TrimSpace(opts.AppSecret))
	span = reloadSpan()
	if opts.SetAllowFromEmpty && strings.TrimSpace(opts.OwnerOpenID) != "" {
		lines = upsertTomlStringKey(lines, span.optionsStart+1, span.optionsEnd, "allow_from", allowFrom)
		span = reloadSpan()
	}

	if err := writeRawConfig(joinConfigLines(lines, hadTrailing)); err != nil {
		return nil, err
	}

	return &FeishuCredentialUpdateResult{
		ProjectName:      opts.ProjectName,
		ProjectIndex:     projectIdx,
		PlatformAbsIndex: absIdx,
		PlatformType:     platform.Type,
		AllowFrom:        allowFrom,
	}, nil
}

func stringOption(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func firstFeishuPlatformIndex(platforms []PlatformConfig) int {
	for i := range platforms {
		t := strings.ToLower(strings.TrimSpace(platforms[i].Type))
		if t == "feishu" || t == "lark" {
			return i
		}
	}
	return -1
}

func firstWeixinPlatformIndex(platforms []PlatformConfig) int {
	for i := range platforms {
		t := strings.ToLower(strings.TrimSpace(platforms[i].Type))
		if t == "weixin" {
			return i
		}
	}
	return -1
}

// EnsureProjectWithWeixinOptions controls project auto-provisioning for Weixin (ilink) setup.
type EnsureProjectWithWeixinOptions struct {
	ProjectName      string
	CloneFromProject string
	WorkDir          string
	AgentType        string
}

// EnsureProjectWithWeixinResult describes whether project provisioning created a new project or platform block.
type EnsureProjectWithWeixinResult struct {
	Created          bool
	AddedPlatform    bool
	ProjectIndex     int
	PlatformAbsIndex int
}

// WeixinCredentialUpdateOptions updates token (and optional URLs) for a project's Weixin platform.
type WeixinCredentialUpdateOptions struct {
	ProjectName       string
	PlatformIndex     int // 1-based index among weixin platforms; 0 = first
	Token             string
	BaseURL           string // optional; empty = do not change in TOML
	CDNBaseURL        string // optional; empty = do not change
	AccountID         string // optional ilink_bot_id → options.account_id
	ScannedUserID     string // optional ilink_user_id for allow_from when SetAllowFromEmpty
	SetAllowFromEmpty bool
}

// WeixinCredentialUpdateResult describes where credentials were written.
type WeixinCredentialUpdateResult struct {
	ProjectName      string
	ProjectIndex     int
	PlatformAbsIndex int
	AllowFrom        string
}

// EnsureProjectWithWeixinPlatform ensures the target project exists and has a weixin platform entry.
func EnsureProjectWithWeixinPlatform(opts EnsureProjectWithWeixinOptions) (*EnsureProjectWithWeixinResult, error) {
	configMu.Lock()
	defer configMu.Unlock()

	if ConfigPath == "" {
		return nil, fmt.Errorf("config path not set")
	}
	projectName := strings.TrimSpace(opts.ProjectName)
	if projectName == "" {
		return nil, fmt.Errorf("project name is required")
	}

	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	raw := string(data)
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	for i := range cfg.Projects {
		if cfg.Projects[i].Name != projectName {
			continue
		}
		platformIdx := firstWeixinPlatformIndex(cfg.Projects[i].Platforms)
		added := false
		if platformIdx < 0 {
			lines, hadTrailing := splitConfigLines(raw)
			spans := buildRawProjectSpans(lines)
			if i >= len(spans) {
				return nil, fmt.Errorf("project %q located in parsed config but not raw file", projectName)
			}
			insertAt := spans[i].end + 1
			block := make([]string, 0, 7)
			if insertAt > 0 && strings.TrimSpace(lines[insertAt-1]) != "" {
				block = append(block, "")
			}
			block = append(block, "[[projects.platforms]]")
			block = append(block, `type = "weixin"`)
			block = append(block, "")
			block = append(block, "[projects.platforms.options]")
			if insertAt < len(lines) && strings.TrimSpace(lines[insertAt]) != "" {
				block = append(block, "")
			}
			lines = insertLines(lines, insertAt, block)
			if err := writeRawConfig(joinConfigLines(lines, hadTrailing)); err != nil {
				return nil, err
			}
			platformIdx = len(cfg.Projects[i].Platforms)
			added = true
		}
		return &EnsureProjectWithWeixinResult{
			Created:          false,
			AddedPlatform:    added,
			ProjectIndex:     i,
			PlatformAbsIndex: platformIdx,
		}, nil
	}

	proj := ProjectConfig{
		Name:      projectName,
		Agent:     pickAgentTemplateForNewProject(cfg, EnsureProjectWithFeishuOptions{CloneFromProject: opts.CloneFromProject, WorkDir: opts.WorkDir, AgentType: opts.AgentType}),
		Platforms: []PlatformConfig{{Type: "weixin", Options: map[string]any{}}},
	}
	if proj.Agent.Type == "" {
		proj.Agent.Type = "codex"
	}
	if proj.Agent.Options == nil {
		proj.Agent.Options = map[string]any{}
	}
	workDir := strings.TrimSpace(opts.WorkDir)
	if workDir != "" {
		proj.Agent.Options["work_dir"] = workDir
	}

	lines, hadTrailing := splitConfigLines(raw)
	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
		lines = append(lines, "")
	}
	lines = append(lines, "[[projects]]")
	lines = append(lines, fmt.Sprintf("name = %s", quoteTomlString(proj.Name)))
	lines = append(lines, "")
	lines = append(lines, "[projects.agent]")
	lines = append(lines, fmt.Sprintf("type = %s", quoteTomlString(proj.Agent.Type)))
	lines = append(lines, "")
	lines = append(lines, "[projects.agent.options]")
	if wd, ok := proj.Agent.Options["work_dir"].(string); ok && strings.TrimSpace(wd) != "" {
		lines = append(lines, fmt.Sprintf("work_dir = %s", quoteTomlString(wd)))
	}
	if mode, ok := proj.Agent.Options["mode"].(string); ok && strings.TrimSpace(mode) != "" {
		lines = append(lines, fmt.Sprintf("mode = %s", quoteTomlString(mode)))
	}
	lines = append(lines, "")
	lines = append(lines, "[[projects.platforms]]")
	lines = append(lines, `type = "weixin"`)
	lines = append(lines, "")
	lines = append(lines, "[projects.platforms.options]")
	if err := writeRawConfig(joinConfigLines(lines, hadTrailing)); err != nil {
		return nil, err
	}

	return &EnsureProjectWithWeixinResult{
		Created:          true,
		AddedPlatform:    false,
		ProjectIndex:     len(cfg.Projects),
		PlatformAbsIndex: 0,
	}, nil
}

// SaveWeixinPlatformCredentials updates token (and optional fields) for a project's Weixin platform.
func SaveWeixinPlatformCredentials(opts WeixinCredentialUpdateOptions) (*WeixinCredentialUpdateResult, error) {
	configMu.Lock()
	defer configMu.Unlock()

	if ConfigPath == "" {
		return nil, fmt.Errorf("config path not set")
	}
	if strings.TrimSpace(opts.ProjectName) == "" {
		return nil, fmt.Errorf("project name is required")
	}
	if strings.TrimSpace(opts.Token) == "" {
		return nil, fmt.Errorf("token is required")
	}
	if opts.PlatformIndex < 0 {
		return nil, fmt.Errorf("platform index must be >= 0")
	}

	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	raw := string(data)
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	projectIdx := -1
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == opts.ProjectName {
			projectIdx = i
			break
		}
	}
	if projectIdx < 0 {
		return nil, fmt.Errorf("project %q not found", opts.ProjectName)
	}

	proj := &cfg.Projects[projectIdx]
	candidates := make([]int, 0, len(proj.Platforms))
	for i := range proj.Platforms {
		t := strings.ToLower(strings.TrimSpace(proj.Platforms[i].Type))
		if t == "weixin" {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("project %q has no weixin platform", opts.ProjectName)
	}

	targetPos := 0
	if opts.PlatformIndex > 0 {
		targetPos = opts.PlatformIndex - 1
	}
	if targetPos < 0 || targetPos >= len(candidates) {
		return nil, fmt.Errorf(
			"platform index %d out of range: project %q has %d weixin platform(s)",
			opts.PlatformIndex, opts.ProjectName, len(candidates),
		)
	}

	absIdx := candidates[targetPos]
	platform := &proj.Platforms[absIdx]
	if platform.Options == nil {
		platform.Options = map[string]any{}
	}

	token := strings.TrimSpace(opts.Token)
	platform.Options["token"] = token

	if u := strings.TrimSpace(opts.BaseURL); u != "" {
		platform.Options["base_url"] = u
	}
	if u := strings.TrimSpace(opts.CDNBaseURL); u != "" {
		platform.Options["cdn_base_url"] = u
	}
	if id := strings.TrimSpace(opts.AccountID); id != "" {
		platform.Options["account_id"] = id
	}

	allowFrom := strings.TrimSpace(stringOption(platform.Options["allow_from"]))
	if opts.SetAllowFromEmpty && allowFrom == "" && strings.TrimSpace(opts.ScannedUserID) != "" {
		allowFrom = strings.TrimSpace(opts.ScannedUserID)
		platform.Options["allow_from"] = allowFrom
	}

	lines, hadTrailing := splitConfigLines(raw)
	spans := buildRawProjectSpans(lines)
	if projectIdx >= len(spans) {
		return nil, fmt.Errorf("project %q located in parsed config but not raw file", opts.ProjectName)
	}
	if absIdx >= len(spans[projectIdx].platforms) {
		return nil, fmt.Errorf("weixin platform located in parsed config but not raw file")
	}

	reloadSpan := func() rawPlatformSpan {
		spans = buildRawProjectSpans(lines)
		return spans[projectIdx].platforms[absIdx]
	}
	span := spans[projectIdx].platforms[absIdx]

	if span.optionsStart < 0 {
		insertAt := span.end + 1
		block := make([]string, 0, 4)
		if insertAt > 0 && strings.TrimSpace(lines[insertAt-1]) != "" {
			block = append(block, "")
		}
		block = append(block, "[projects.platforms.options]")
		if insertAt < len(lines) && strings.TrimSpace(lines[insertAt]) != "" {
			block = append(block, "")
		}
		lines = insertLines(lines, insertAt, block)
		span = reloadSpan()
	}

	lines = upsertTomlStringKey(lines, span.optionsStart+1, span.optionsEnd, "token", token)
	span = reloadSpan()

	if u := strings.TrimSpace(opts.BaseURL); u != "" {
		lines = upsertTomlStringKey(lines, span.optionsStart+1, span.optionsEnd, "base_url", u)
		span = reloadSpan()
	}
	if u := strings.TrimSpace(opts.CDNBaseURL); u != "" {
		lines = upsertTomlStringKey(lines, span.optionsStart+1, span.optionsEnd, "cdn_base_url", u)
		span = reloadSpan()
	}
	if id := strings.TrimSpace(opts.AccountID); id != "" {
		lines = upsertTomlStringKey(lines, span.optionsStart+1, span.optionsEnd, "account_id", id)
		span = reloadSpan()
	}
	if opts.SetAllowFromEmpty && strings.TrimSpace(opts.ScannedUserID) != "" {
		lines = upsertTomlStringKey(lines, span.optionsStart+1, span.optionsEnd, "allow_from", allowFrom)
		span = reloadSpan()
	}

	if err := writeRawConfig(joinConfigLines(lines, hadTrailing)); err != nil {
		return nil, err
	}

	return &WeixinCredentialUpdateResult{
		ProjectName:      opts.ProjectName,
		ProjectIndex:     projectIdx,
		PlatformAbsIndex: absIdx,
		AllowFrom:        allowFrom,
	}, nil
}

func pickAgentTemplateForNewProject(cfg *Config, opts EnsureProjectWithFeishuOptions) AgentConfig {
	cloneName := strings.TrimSpace(opts.CloneFromProject)
	if cloneName != "" {
		for i := range cfg.Projects {
			if cfg.Projects[i].Name == cloneName {
				return cloneAgentConfig(cfg.Projects[i].Agent)
			}
		}
	}
	if len(cfg.Projects) > 0 {
		return cloneAgentConfig(cfg.Projects[0].Agent)
	}

	agentType := strings.TrimSpace(opts.AgentType)
	if agentType == "" {
		agentType = "codex"
	}
	return AgentConfig{
		Type:    agentType,
		Options: map[string]any{},
	}
}

func cloneAgentConfig(in AgentConfig) AgentConfig {
	out := AgentConfig{
		Type:    in.Type,
		Options: cloneAnyMap(in.Options),
	}
	if len(in.Providers) > 0 {
		out.Providers = make([]ProviderConfig, len(in.Providers))
		for i := range in.Providers {
			out.Providers[i] = ProviderConfig{
				Name:     in.Providers[i].Name,
				APIKey:   in.Providers[i].APIKey,
				BaseURL:  in.Providers[i].BaseURL,
				Model:    in.Providers[i].Model,
				Models:   append([]ProviderModelConfig(nil), in.Providers[i].Models...),
				Thinking: in.Providers[i].Thinking,
				Env:      cloneStringMap(in.Providers[i].Env),
			}
		}
	}
	return out
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type rawProjectSpan struct {
	start     int
	end       int
	platforms []rawPlatformSpan
}

type rawPlatformSpan struct {
	start        int
	end          int
	typeLine     int
	optionsStart int
	optionsEnd   int
}

func splitConfigLines(raw string) ([]string, bool) {
	if raw == "" {
		return []string{}, false
	}
	hadTrailing := strings.HasSuffix(raw, "\n")
	raw = strings.TrimSuffix(raw, "\n")
	if raw == "" {
		return []string{}, hadTrailing
	}
	return strings.Split(raw, "\n"), hadTrailing
}

func joinConfigLines(lines []string, hadTrailing bool) string {
	out := strings.Join(lines, "\n")
	if hadTrailing || len(lines) > 0 {
		out += "\n"
	}
	return out
}

func buildRawProjectSpans(lines []string) []rawProjectSpan {
	projectStarts := make([]int, 0, 4)
	for i := range lines {
		if matchTableHeader(lines[i], "[[projects]]") {
			projectStarts = append(projectStarts, i)
		}
	}
	if len(projectStarts) == 0 {
		return nil
	}

	spans := make([]rawProjectSpan, 0, len(projectStarts))
	for i, start := range projectStarts {
		end := len(lines) - 1
		if i+1 < len(projectStarts) {
			end = projectStarts[i+1] - 1
		}
		span := rawProjectSpan{start: start, end: end}

		platformStarts := make([]int, 0, 2)
		for ln := start + 1; ln <= end; ln++ {
			if matchTableHeader(lines[ln], "[[projects.platforms]]") {
				platformStarts = append(platformStarts, ln)
			}
		}
		for p, pstart := range platformStarts {
			pend := end
			if p+1 < len(platformStarts) {
				pend = platformStarts[p+1] - 1
			}
			ps := rawPlatformSpan{
				start:        pstart,
				end:          pend,
				typeLine:     -1,
				optionsStart: -1,
				optionsEnd:   -1,
			}
			inMainPlatformTable := true
			for ln := pstart + 1; ln <= pend; ln++ {
				if isAnyTableHeader(lines[ln]) {
					inMainPlatformTable = false
				}
				if inMainPlatformTable && ps.typeLine < 0 && matchTomlStringKey(lines[ln], "type") {
					ps.typeLine = ln
				}
				if ps.optionsStart < 0 && matchTableHeader(lines[ln], "[projects.platforms.options]") {
					ps.optionsStart = ln
					ps.optionsEnd = pend
					for j := ln + 1; j <= pend; j++ {
						if isAnyTableHeader(lines[j]) {
							ps.optionsEnd = j - 1
							break
						}
					}
				}
			}
			span.platforms = append(span.platforms, ps)
		}

		spans = append(spans, span)
	}
	return spans
}

func matchTableHeader(line, header string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, header) {
		return false
	}
	if len(t) == len(header) {
		return true
	}
	next := t[len(header)]
	return next == ' ' || next == '\t' || next == '#'
}

func isAnyTableHeader(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "[")
}

func matchTomlStringKey(line, key string) bool {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "[") {
		return false
	}
	if !strings.HasPrefix(t, key) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(t, key))
	return strings.HasPrefix(rest, "=")
}

func insertLines(lines []string, at int, block []string) []string {
	if at < 0 {
		at = 0
	}
	if at > len(lines) {
		at = len(lines)
	}
	out := make([]string, 0, len(lines)+len(block))
	out = append(out, lines[:at]...)
	out = append(out, block...)
	out = append(out, lines[at:]...)
	return out
}

func upsertTomlStringKey(lines []string, start, end int, key, value string) []string {
	if start < 0 {
		start = 0
	}
	if end >= len(lines) {
		end = len(lines) - 1
	}
	for i := start; i <= end && i < len(lines); i++ {
		if matchTomlStringKey(lines[i], key) {
			lines[i] = replaceTomlStringKeyLine(lines[i], key, value)
			return lines
		}
	}
	insertAt := end + 1
	if insertAt < start {
		insertAt = start
	}
	return insertLines(lines, insertAt, []string{fmt.Sprintf("%s = %s", key, quoteTomlString(value))})
}

func replaceTomlStringKeyLine(line, key, value string) string {
	indent := leadingWhitespace(line)
	comment := extractLineComment(line)
	updated := fmt.Sprintf("%s%s = %s", indent, key, quoteTomlString(value))
	if comment != "" {
		updated += " " + comment
	}
	return updated
}

func quoteTomlString(value string) string {
	return strconv.Quote(value)
}

func leadingWhitespace(s string) string {
	i := 0
	for i < len(s) {
		if s[i] != ' ' && s[i] != '\t' {
			break
		}
		i++
	}
	return s[:i]
}

func extractLineComment(line string) string {
	inQuote := false
	escaped := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inQuote {
			escaped = true
			continue
		}
		if ch == '"' {
			inQuote = !inQuote
			continue
		}
		if ch == '#' && !inQuote {
			return strings.TrimSpace(line[i:])
		}
	}
	return ""
}

// ProjectSettingsUpdate carries optional field updates for SaveProjectSettings.
type ProjectSettingsUpdate struct {
	Quiet                *bool
	Language             *string
	AdminFrom            *string
	DisabledCommands     []string
	WorkDir              *string
	Mode                 *string
	ShowContextIndicator *bool
	PlatformAllowFrom    map[string]string
}

// SaveProjectSettings persists project-level settings and the global language to config.toml.
func SaveProjectSettings(projectName string, update ProjectSettingsUpdate) error {
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

	if update.Language != nil {
		cfg.Language = *update.Language
	}

	for i := range cfg.Projects {
		if cfg.Projects[i].Name != projectName {
			continue
		}
		proj := &cfg.Projects[i]
		if update.Quiet != nil {
			proj.Quiet = update.Quiet
		}
		if update.AdminFrom != nil {
			proj.AdminFrom = *update.AdminFrom
		}
		if update.DisabledCommands != nil {
			proj.DisabledCommands = update.DisabledCommands
		}
		if update.ShowContextIndicator != nil {
			v := *update.ShowContextIndicator
			proj.ShowContextIndicator = &v
		}
		if update.WorkDir != nil || update.Mode != nil {
			if proj.Agent.Options == nil {
				proj.Agent.Options = map[string]any{}
			}
		}
		if update.WorkDir != nil {
			wd := strings.TrimSpace(*update.WorkDir)
			if wd == "" {
				delete(proj.Agent.Options, "work_dir")
			} else {
				proj.Agent.Options["work_dir"] = wd
			}
		}
		if update.Mode != nil {
			mode := strings.TrimSpace(*update.Mode)
			if mode == "" {
				delete(proj.Agent.Options, "mode")
			} else {
				proj.Agent.Options["mode"] = mode
			}
		}
		if update.PlatformAllowFrom != nil {
			for j := range proj.Platforms {
				typ := strings.TrimSpace(proj.Platforms[j].Type)
				if typ == "" {
					continue
				}
				var af string
				var found bool
				for k, v := range update.PlatformAllowFrom {
					if strings.EqualFold(strings.TrimSpace(k), typ) {
						af, found = v, true
						break
					}
				}
				if !found {
					continue
				}
				if proj.Platforms[j].Options == nil {
					proj.Platforms[j].Options = map[string]any{}
				}
				proj.Platforms[j].Options["allow_from"] = strings.TrimSpace(af)
			}
		}
		return saveConfig(cfg)
	}
	return fmt.Errorf("project %q not found", projectName)
}

// GetProjectConfigDetails returns persisted project fields from the config file for the management API.
func GetProjectConfigDetails(projectName string) map[string]any {
	if ConfigPath == "" {
		return nil
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil
	}
	for _, p := range cfg.Projects {
		if p.Name != projectName {
			continue
		}
		result := map[string]any{}
		if p.Agent.Options != nil {
			if wd, ok := p.Agent.Options["work_dir"].(string); ok && strings.TrimSpace(wd) != "" {
				result["work_dir"] = wd
			}
			if mode, ok := p.Agent.Options["mode"].(string); ok && strings.TrimSpace(mode) != "" {
				result["mode"] = mode
			}
		}
		if p.ShowContextIndicator != nil {
			result["show_context_indicator"] = *p.ShowContextIndicator
		}
		platConfigs := make([]map[string]any, len(p.Platforms))
		for j, plat := range p.Platforms {
			pc := map[string]any{"type": plat.Type}
			if plat.Options != nil {
				if af, ok := plat.Options["allow_from"].(string); ok {
					pc["allow_from"] = af
				}
			}
			platConfigs[j] = pc
		}
		result["platform_configs"] = platConfigs
		return result
	}
	return nil
}

// RemoveProject removes a project from the config file.
func RemoveProject(projectName string) error {
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
			cfg.Projects = append(cfg.Projects[:i], cfg.Projects[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("project %q not found", projectName)
	}
	return saveConfig(cfg)
}

// AddPlatformToProject appends a platform config to a project.
// If the project doesn't exist, it is created using agentType and workDir when provided,
// otherwise agent config is cloned from the first existing project when present.
func AddPlatformToProject(projectName string, platform PlatformConfig, workDir, agentType string) error {
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
	if platform.Options == nil {
		platform.Options = map[string]any{}
	}
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == projectName {
			cfg.Projects[i].Platforms = append(cfg.Projects[i].Platforms, platform)
			return saveConfig(cfg)
		}
	}
	agentCfg := AgentConfig{Type: "codex", Options: map[string]any{}}
	at := strings.TrimSpace(agentType)
	if at != "" {
		agentCfg.Type = at
	}
	if len(cfg.Projects) > 0 && at == "" {
		agentCfg = cloneAgentConfig(cfg.Projects[0].Agent)
	}
	wd := strings.TrimSpace(workDir)
	if wd != "" {
		if agentCfg.Options == nil {
			agentCfg.Options = map[string]any{}
		}
		agentCfg.Options["work_dir"] = wd
	}
	cfg.Projects = append(cfg.Projects, ProjectConfig{
		Name:      projectName,
		Agent:     agentCfg,
		Platforms: []PlatformConfig{platform},
	})
	return saveConfig(cfg)
}

func writeRawConfig(content string) error {
	dir := filepath.Dir(ConfigPath)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write config: %w", err)
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

// GetGlobalSettings reads global settings from config.toml.
func GetGlobalSettings() map[string]any {
	if ConfigPath == "" {
		return nil
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil
	}
	result := map[string]any{
		"language":        cfg.Language,
		"attachment_send": cfg.AttachmentSend,
		"log_level":       cfg.Log.Level,
	}
	if cfg.Quiet != nil {
		result["quiet"] = *cfg.Quiet
	} else {
		result["quiet"] = false
	}
	if cfg.IdleTimeoutMins != nil {
		result["idle_timeout_mins"] = *cfg.IdleTimeoutMins
	} else {
		result["idle_timeout_mins"] = 120
	}
	// Display
	if cfg.Display.ThinkingMaxLen != nil {
		result["thinking_max_len"] = *cfg.Display.ThinkingMaxLen
	} else {
		result["thinking_max_len"] = 300
	}
	if cfg.Display.ToolMaxLen != nil {
		result["tool_max_len"] = *cfg.Display.ToolMaxLen
	} else {
		result["tool_max_len"] = 500
	}
	// Stream preview
	spEnabled := true
	if cfg.StreamPreview.Enabled != nil {
		spEnabled = *cfg.StreamPreview.Enabled
	}
	result["stream_preview_enabled"] = spEnabled
	spInterval := 1500
	if cfg.StreamPreview.IntervalMs != nil {
		spInterval = *cfg.StreamPreview.IntervalMs
	}
	result["stream_preview_interval_ms"] = spInterval
	// Rate limit
	rlMax := 20
	if cfg.RateLimit.MaxMessages != nil {
		rlMax = *cfg.RateLimit.MaxMessages
	}
	result["rate_limit_max_messages"] = rlMax
	rlWindow := 60
	if cfg.RateLimit.WindowSecs != nil {
		rlWindow = *cfg.RateLimit.WindowSecs
	}
	result["rate_limit_window_secs"] = rlWindow
	return result
}

// GlobalSettingsUpdate holds fields to update in global config.
type GlobalSettingsUpdate struct {
	Language           *string `json:"language"`
	Quiet              *bool   `json:"quiet"`
	AttachmentSend     *string `json:"attachment_send"`
	LogLevel           *string `json:"log_level"`
	IdleTimeoutMins    *int    `json:"idle_timeout_mins"`
	ThinkingMaxLen     *int    `json:"thinking_max_len"`
	ToolMaxLen         *int    `json:"tool_max_len"`
	StreamPreviewOn    *bool   `json:"stream_preview_enabled"`
	StreamPreviewIntMs *int    `json:"stream_preview_interval_ms"`
	RateLimitMax       *int    `json:"rate_limit_max_messages"`
	RateLimitWindow    *int    `json:"rate_limit_window_secs"`
}

// SaveGlobalSettings persists global settings to config.toml.
func SaveGlobalSettings(u GlobalSettingsUpdate) error {
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
	if u.Language != nil {
		cfg.Language = *u.Language
	}
	if u.Quiet != nil {
		cfg.Quiet = u.Quiet
	}
	if u.AttachmentSend != nil {
		cfg.AttachmentSend = *u.AttachmentSend
	}
	if u.LogLevel != nil {
		cfg.Log.Level = *u.LogLevel
	}
	if u.IdleTimeoutMins != nil {
		cfg.IdleTimeoutMins = u.IdleTimeoutMins
	}
	if u.ThinkingMaxLen != nil {
		cfg.Display.ThinkingMaxLen = u.ThinkingMaxLen
	}
	if u.ToolMaxLen != nil {
		cfg.Display.ToolMaxLen = u.ToolMaxLen
	}
	if u.StreamPreviewOn != nil {
		cfg.StreamPreview.Enabled = u.StreamPreviewOn
	}
	if u.StreamPreviewIntMs != nil {
		cfg.StreamPreview.IntervalMs = u.StreamPreviewIntMs
	}
	if u.RateLimitMax != nil {
		cfg.RateLimit.MaxMessages = u.RateLimitMax
	}
	if u.RateLimitWindow != nil {
		cfg.RateLimit.WindowSecs = u.RateLimitWindow
	}
	return saveConfig(cfg)
}

// WebSetupResult holds the config values after enabling web admin.
type WebSetupResult struct {
	ManagementPort  int
	ManagementToken string
	BridgePort      int
	BridgeToken     string
	AlreadyEnabled  bool
}

// EnableWebAdmin enables the bridge and management sections in config.toml.
// If already enabled, returns the existing config values without changes.
func EnableWebAdmin(mgmtToken, bridgeToken string) (*WebSetupResult, error) {
	configMu.Lock()
	defer configMu.Unlock()
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

	mgmtEnabled := cfg.Management.Enabled != nil && *cfg.Management.Enabled
	bridgeEnabled := cfg.Bridge.Enabled != nil && *cfg.Bridge.Enabled

	if mgmtEnabled && bridgeEnabled {
		return &WebSetupResult{
			ManagementPort:  orDefault(cfg.Management.Port, 9820),
			ManagementToken: cfg.Management.Token,
			BridgePort:      orDefault(cfg.Bridge.Port, 9810),
			BridgeToken:     cfg.Bridge.Token,
			AlreadyEnabled:  true,
		}, nil
	}

	t := true
	changed := false
	if !mgmtEnabled {
		cfg.Management.Enabled = &t
		if cfg.Management.Port == 0 {
			cfg.Management.Port = 9820
		}
		if cfg.Management.Token == "" {
			cfg.Management.Token = mgmtToken
		}
		if len(cfg.Management.CORSOrigins) == 0 {
			cfg.Management.CORSOrigins = []string{"*"}
		}
		changed = true
	}
	if !bridgeEnabled {
		cfg.Bridge.Enabled = &t
		if cfg.Bridge.Port == 0 {
			cfg.Bridge.Port = 9810
		}
		if cfg.Bridge.Token == "" {
			cfg.Bridge.Token = bridgeToken
		}
		if len(cfg.Bridge.CORSOrigins) == 0 {
			cfg.Bridge.CORSOrigins = []string{"*"}
		}
		changed = true
	}

	if changed {
		if err := saveConfig(cfg); err != nil {
			return nil, fmt.Errorf("save config: %w", err)
		}
	}

	return &WebSetupResult{
		ManagementPort:  orDefault(cfg.Management.Port, 9820),
		ManagementToken: cfg.Management.Token,
		BridgePort:      orDefault(cfg.Bridge.Port, 9810),
		BridgeToken:     cfg.Bridge.Token,
		AlreadyEnabled:  false,
	}, nil
}

func orDefault(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}
