package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// validRunAsUserName is the portable-username character set plus digits.
// POSIX does not require a specific pattern, but every mainstream Linux and
// macOS system accepts these characters for login names. Rejecting anything
// outside this set removes an injection vector into the sudo argv.
func isValidRunAsUserName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '_':
		case r >= '0' && r <= '9' && i > 0:
		case (r == '-' || r == '.') && i > 0:
		default:
			return false
		}
	}
	return true
}

var dangerousEnvVars = map[string]bool{
	"LD_PRELOAD":           true,
	"LD_LIBRARY_PATH":      true,
	"DYLD_INSERT_LIBRARIES": true,
	"DYLD_LIBRARY_PATH":    true,
	"PATH":                 true,
	"HOME":                 true,
	"USER":                 true,
	"SHELL":                true,
	"SUDO_USER":            true,
	"SUDO_COMMAND":         true,
}

func validateRunAsEnv(prefix string, envVars []string) error {
	for _, v := range envVars {
		name := strings.TrimSpace(v)
		if dangerousEnvVars[strings.ToUpper(name)] {
			return fmt.Errorf("config: %s.run_as_env must not include dangerous variable %q", prefix, name)
		}
	}
	return nil
}

func validateRunAsUser(prefix, name string) error {
	if name == "" {
		return nil
	}
	if runtime.GOOS == "windows" {
		return fmt.Errorf("config: %s.run_as_user is only supported on Linux/macOS", prefix)
	}
	if name == "root" || name == "0" {
		return fmt.Errorf("config: %s.run_as_user must not be root", prefix)
	}
	if !isValidRunAsUserName(name) {
		return fmt.Errorf("config: %s.run_as_user %q contains invalid characters (allowed: a-z, A-Z, 0-9, -, _, .; must start with a letter or underscore)", prefix, name)
	}
	return nil
}

// configMu serializes read-modify-write cycles to prevent lost updates.
var configMu sync.Mutex

// ConfigPath stores the path to the config file for saving
var ConfigPath string

type Config struct {
	DataDir        string `toml:"data_dir"` // session store directory, default ~/.cc-connect
	AttachmentSend string `toml:"attachment_send"`
	// Quiet is legacy: when true and [display] does not set thinking_messages / tool_messages,
	// engines behave as if those flags were false. Per-project quiet overrides when set.
	Quiet             *bool                   `toml:"quiet,omitempty"`
	Providers         []ProviderConfig        `toml:"providers"`          // global shared providers
	ProviderPresetsURL string                 `toml:"provider_presets_url,omitempty"` // remote JSON URL for provider presets
	Projects          []ProjectConfig         `toml:"projects"`
	Commands          []CommandConfig         `toml:"commands"`     // global custom slash commands
	Aliases           []AliasConfig           `toml:"aliases"`      // global command aliases
	BannedWords       []string                `toml:"banned_words"` // messages containing any of these words are blocked
	Log               LogConfig               `toml:"log"`
	Language          string                  `toml:"language"` // "en" or "zh", default is "en"
	Speech            SpeechConfig            `toml:"speech"`
	TTS               TTSConfig               `toml:"tts"`
	Display           DisplayConfig           `toml:"display"`
	StreamPreview     StreamPreviewConfig     `toml:"stream_preview"`      // real-time streaming preview
	RateLimit         RateLimitConfig         `toml:"rate_limit"`          // per-session rate limiting
	OutgoingRateLimit OutgoingRateLimitConfig `toml:"outgoing_rate_limit"` // outgoing message throttling
	Relay             RelayConfig             `toml:"relay"`               // bot-to-bot relay behavior
	Cron              CronConfig              `toml:"cron"`
	Queue             QueueConfig             `toml:"queue"`
	Webhook           WebhookConfig           `toml:"webhook"`
	Bridge            BridgeConfig            `toml:"bridge"`
	Management        ManagementConfig        `toml:"management"`
	Hooks             []HookConfig            `toml:"hooks"`
	IdleTimeoutMins   *int                    `toml:"idle_timeout_mins,omitempty"` // max minutes between agent events; 0 = no timeout; default 120
	// WorkspaceIdleTimeoutMins controls the workspace idle reaper timeout
	// (multi-workspace mode) for every engine in the process. 0 disables
	// reaping. Default: 15 minutes. Defined as a top-level (process-global)
	// setting so the reaper policy is consistent across projects; per-project
	// configuration is intentionally not supported.
	WorkspaceIdleTimeoutMins *int `toml:"workspace_idle_timeout_mins,omitempty"`
}

// CronConfig controls cron job behavior.
type CronConfig struct {
	Silent      *bool  `toml:"silent"`       // suppress cron start notification; default false
	SessionMode string `toml:"session_mode"` // default session mode: "" or "reuse" (default) or "new_per_run"
}

// QueueConfig controls the per-session message queue.
type QueueConfig struct {
	MaxDepth *int `toml:"max_depth"` // max queued messages per session; default 5
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

// HookConfig is a single event hook rule.
type HookConfig struct {
	Event   string `toml:"event"`             // event name or "*"
	Type    string `toml:"type"`              // "command" or "http"
	Command string `toml:"command,omitempty"` // shell command (type=command)
	URL     string `toml:"url,omitempty"`     // HTTP endpoint (type=http)
	Timeout int    `toml:"timeout,omitempty"` // seconds; 0 = default
	Async   *bool  `toml:"async,omitempty"`   // nil = true (async by default)
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
	ThinkingMessages *bool `toml:"thinking_messages"` // whether thinking messages are shown; default true
	ThinkingMaxLen   *int  `toml:"thinking_max_len"`  // max chars for thinking messages; 0 = no truncation; default 300
	ToolMaxLen       *int  `toml:"tool_max_len"`      // max chars for tool use messages; 0 = no truncation; default 500
	ToolMessages     *bool `toml:"tool_messages"`     // whether tool progress messages are shown; default true
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
	Platforms    map[string]OutgoingRateLimitPlatConfig `toml:"platforms"`      // per-platform overrides keyed by platform type name
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
	Provider string `toml:"provider"` // "openai" | "groq" | "qwen" | "gemini"
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
	Gemini struct {
		APIKey string `toml:"api_key"`
		Model  string `toml:"model"`
	} `toml:"gemini"`
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

// ObserveConfig controls forwarding of native terminal Claude Code sessions to a messaging platform.
type ObserveConfig struct {
	Enabled bool   `toml:"enabled"`
	Channel string `toml:"channel"`
}

// ReferenceConfig controls local file reference normalization and rendering.
type ReferenceConfig struct {
	NormalizeAgents []string `toml:"normalize_agents,omitempty"`
	RenderPlatforms []string `toml:"render_platforms,omitempty"`
	DisplayPath     string   `toml:"display_path,omitempty"`
	MarkerStyle     string   `toml:"marker_style,omitempty"`
	EnclosureStyle  string   `toml:"enclosure_style,omitempty"`
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
	// ResetOnIdleMins automatically rotates to a new cc-connect session after
	// the current session has been inactive for the specified number of minutes.
	// 0 or nil disables the behavior.
	ResetOnIdleMins *int `toml:"reset_on_idle_mins,omitempty"`
	// RunAsUser, when set, causes the agent command for this project to be
	// spawned under a different Unix user via `sudo -n -iu <user> --`. This
	// provides OS-level file-system isolation from the supervisor user who
	// runs cc-connect itself. Requires passwordless sudo to the target user
	// and is POSIX-only. See docs/usage.md "Running agents as a different
	// Unix user" for setup and migration.
	RunAsUser string `toml:"run_as_user,omitempty"`
	// RunAsEnv optionally extends the minimal environment variable allowlist
	// that crosses the sudo boundary when RunAsUser is set. The default
	// allowlist (LANG, LC_*, TERM) is always included; PATH is NOT preserved
	// by default — the target user's login PATH is used. Dangerous variables
	// (LD_PRELOAD, PATH, HOME, etc.) are rejected at config validation.
	// Use this only for variables the target user cannot set in their profile.
	RunAsEnv []string `toml:"run_as_env,omitempty"`
	// ShowContextIndicator: nil/true = append [ctx: ~N%] to assistant replies; false = hide.
	ShowContextIndicator *bool `toml:"show_context_indicator,omitempty"`
	// ReplyFooter: nil/true = append a Codex-style footer; false = disable.
	// (model/reasoning/usage/workdir, when available) to assistant replies.
	ReplyFooter      *bool        `toml:"reply_footer,omitempty"`
	InjectSender     *bool        `toml:"inject_sender,omitempty"`     // prepend sender identity (platform + user ID) to each message sent to the agent
	DisabledCommands []string     `toml:"disabled_commands,omitempty"` // commands to disable for this project (e.g. ["restart", "upgrade"])
	AdminFrom        string       `toml:"admin_from,omitempty"`        // comma-separated user IDs allowed to run privileged commands; "*" = all allowed users
	Users            *UsersConfig `toml:"users,omitempty"`             // per-user role config; nil = legacy behavior
	// WorkspaceIdleTimeoutMinsLegacy is the deprecated per-project form of
	// the workspace idle reaper timeout. New configs should set the top-level
	// Config.WorkspaceIdleTimeoutMins instead. When the top-level field is
	// unset, this legacy value is still honored (with a deprecation warning)
	// to keep existing configs working. Will be removed in a future release.
	WorkspaceIdleTimeoutMinsLegacy *int `toml:"workspace_idle_timeout_mins,omitempty"`
	// Quiet is legacy per-project override; see Config.Quiet. When true and global [display]
	// omits thinking_messages / tool_messages, those default to off for this project.
	Quiet      *bool           `toml:"quiet,omitempty"`
	// Display, when non-nil, overrides individual fields of the global [display]
	// block for this project. Each sub-field is independently optional; unset
	// fields fall back to the global [display] value, then to the built-in
	// defaults. Example: enable verbose display globally but force quiet on a
	// specific noisy project, or vice versa.
	//
	//   [display]
	//   thinking_messages = true
	//   tool_messages = true
	//
	//   [[projects]]
	//   name = "noisy-project"
	//   [projects.display]
	//   thinking_messages = false
	//   tool_messages = false
	Display    *DisplayConfig  `toml:"display,omitempty"`
	Observe    *ObserveConfig  `toml:"observe,omitempty"`
	References ReferenceConfig `toml:"references,omitempty"`
	// FilterExternalSessions: when true, /list only shows sessions created by
	// cc-connect, hiding sessions created by direct CLI usage in the same work_dir.
	// Default is false (show all sessions).
	FilterExternalSessions *bool `toml:"filter_external_sessions,omitempty"`
}

type AgentConfig struct {
	Type         string           `toml:"type"`
	Options      map[string]any   `toml:"options"`
	ProviderRefs []string         `toml:"provider_refs,omitempty"` // references to global [[providers]] by name
	Providers    []ProviderConfig `toml:"providers"`
}

// ProviderModelConfig defines a selectable model entry for a provider,
// with an optional short alias used by the /model command.
type ProviderModelConfig struct {
	Model string `toml:"model"`
	Alias string `toml:"alias,omitempty"`
}

type ProviderConfig struct {
	Name        string                `toml:"name"`
	APIKey      string                `toml:"api_key"`
	BaseURL     string                `toml:"base_url,omitempty"`
	Model       string                `toml:"model,omitempty"`
	Models      []ProviderModelConfig `toml:"models,omitempty"`
	Thinking    string                `toml:"thinking,omitempty"`
	Env         map[string]string     `toml:"env,omitempty"`
	AgentTypes      []string                          `toml:"agent_types,omitempty"`       // optional: restrict to specific agent types (e.g. ["claudecode", "codex"])
	Endpoints       map[string]string                 `toml:"endpoints,omitempty"`         // per-agent-type base URL overrides (e.g. codex = "https://x/v1")
	AgentModels     map[string]string                 `toml:"agent_models,omitempty"`      // per-agent-type default model (e.g. codex = "openai/gpt-5.3-codex")
	AgentModelLists map[string][]ProviderModelConfig  `toml:"agent_model_lists,omitempty"` // per-agent-type model lists (overrides Models when matched)
	Codex           *CodexProviderConfig              `toml:"codex,omitempty"`             // Codex-specific provider settings
}

// CodexProviderConfig holds Codex CLI-specific provider fields
// that map to [model_providers.<name>] in Codex's own config.toml.
type CodexProviderConfig struct {
	EnvKey      string            `toml:"env_key,omitempty" json:"env_key,omitempty"`
	WireAPI     string            `toml:"wire_api,omitempty" json:"wire_api,omitempty"`
	HTTPHeaders map[string]string `toml:"http_headers,omitempty" json:"http_headers,omitempty"`
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
	resolveEnvInConfig(cfg)

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

	cfg.ResolveProviderRefs()

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

var envPlaceholderPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func resolveEnvInConfig(cfg *Config) {
	resolveEnvValue(reflect.ValueOf(cfg))
}

func resolveEnvValue(v reflect.Value) {
	if !v.IsValid() {
		return
	}

	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			resolveEnvValue(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			resolveEnvValue(v.Field(i))
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString(resolveEnvPlaceholders(v.String()))
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			elem := v.Index(i)
			if elem.CanSet() {
				elem.Set(resolveEnvClone(elem))
				continue
			}
			resolveEnvValue(elem)
		}
	case reflect.Map:
		if v.IsNil() {
			return
		}
		iter := v.MapRange()
		for iter.Next() {
			v.SetMapIndex(iter.Key(), resolveEnvClone(iter.Value()))
		}
	case reflect.Interface:
		if v.IsNil() || !v.CanSet() {
			return
		}
		v.Set(resolveEnvClone(v.Elem()))
	}
}

func resolveEnvClone(v reflect.Value) reflect.Value {
	if !v.IsValid() {
		return v
	}

	switch v.Kind() {
	case reflect.String:
		out := reflect.New(v.Type()).Elem()
		out.SetString(resolveEnvPlaceholders(v.String()))
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(v.Elem())
		resolveEnvValue(out.Elem())
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		resolveEnvValue(out)
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(resolveEnvClone(v.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(resolveEnvClone(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), resolveEnvClone(iter.Value()))
		}
		return out
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(resolveEnvClone(v.Elem()))
		return out
	default:
		return v
	}
}

func resolveEnvPlaceholders(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return envPlaceholderPattern.ReplaceAllStringFunc(s, func(match string) string {
		parts := envPlaceholderPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		val, ok := os.LookupEnv(parts[1])
		if !ok {
			slog.Warn("config: env var placeholder references unset variable",
				"var", parts[1], "placeholder", match)
		}
		return val
	})
}

// projectQuietEffective returns whether legacy quiet applies to this project: an explicit
// per-project quiet overrides; otherwise the global root quiet applies.
func projectQuietEffective(cfg *Config, proj *ProjectConfig) bool {
	if proj.Quiet != nil {
		return *proj.Quiet
	}
	if cfg.Quiet != nil {
		return *cfg.Quiet
	}
	return false
}

// EffectiveDisplay resolves the per-project [projects.display] override on top
// of the global [display] block, falling back to built-in defaults. Resolution
// order for each field:
//  1. project-level [projects.display].<field>  (highest precedence)
//  2. global [display].<field>
//  3. built-in default
//
// Legacy quiet (root or per-project) is preserved: if quiet is in effect AND
// neither layer explicitly set thinking_messages / tool_messages, they default
// to false (backward-compatible with pre-display quiet = true).
func EffectiveDisplay(cfg *Config, proj *ProjectConfig) (thinkingMessages, toolMessages bool, thinkingMaxLen, toolMaxLen int) {
	pickBool := func(projVal, globalVal *bool, dflt bool) bool {
		if projVal != nil {
			return *projVal
		}
		if globalVal != nil {
			return *globalVal
		}
		return dflt
	}
	pickInt := func(projVal, globalVal *int, dflt int) int {
		if projVal != nil {
			return *projVal
		}
		if globalVal != nil {
			return *globalVal
		}
		return dflt
	}

	var projDisp *DisplayConfig
	if proj != nil {
		projDisp = proj.Display
	}
	getProjBool := func(f func(*DisplayConfig) *bool) *bool {
		if projDisp == nil {
			return nil
		}
		return f(projDisp)
	}
	getProjInt := func(f func(*DisplayConfig) *int) *int {
		if projDisp == nil {
			return nil
		}
		return f(projDisp)
	}

	thinkingMessages = pickBool(
		getProjBool(func(d *DisplayConfig) *bool { return d.ThinkingMessages }),
		cfg.Display.ThinkingMessages,
		true,
	)
	toolMessages = pickBool(
		getProjBool(func(d *DisplayConfig) *bool { return d.ToolMessages }),
		cfg.Display.ToolMessages,
		true,
	)
	thinkingMaxLen = pickInt(
		getProjInt(func(d *DisplayConfig) *int { return d.ThinkingMaxLen }),
		cfg.Display.ThinkingMaxLen,
		300,
	)
	toolMaxLen = pickInt(
		getProjInt(func(d *DisplayConfig) *int { return d.ToolMaxLen }),
		cfg.Display.ToolMaxLen,
		500,
	)

	// Legacy quiet behavior preserved: when project-level quiet is on AND
	// neither layer explicitly set thinking_messages / tool_messages, they
	// default to off. Per-project [projects.display] takes precedence over
	// both global display and quiet.
	if projectQuietEffective(cfg, proj) {
		projThink := getProjBool(func(d *DisplayConfig) *bool { return d.ThinkingMessages })
		projTool := getProjBool(func(d *DisplayConfig) *bool { return d.ToolMessages })
		if cfg.Display.ThinkingMessages == nil && projThink == nil {
			thinkingMessages = false
		}
		if cfg.Display.ToolMessages == nil && projTool == nil {
			toolMessages = false
		}
	}
	return thinkingMessages, toolMessages, thinkingMaxLen, toolMaxLen
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
		if proj.ResetOnIdleMins != nil && *proj.ResetOnIdleMins < 0 {
			return fmt.Errorf("config: %s.reset_on_idle_mins must be >= 0", prefix)
		}
		if err := validateRunAsUser(prefix, proj.RunAsUser); err != nil {
			return err
		}
		if err := validateRunAsEnv(prefix, proj.RunAsEnv); err != nil {
			return err
		}
		if err := validateReferenceConfig(prefix, proj.References); err != nil {
			return err
		}
		if err := validateUsersConfig(prefix, proj.Users); err != nil {
			return err
		}
	}
	return nil
}

var supportedReferenceAgents = map[string]struct{}{
	"all":        {},
	"codex":      {},
	"claudecode": {},
}

var supportedReferencePlatforms = map[string]struct{}{
	"all":    {},
	"feishu": {},
	"weixin": {},
}

var supportedReferenceDisplayPaths = map[string]struct{}{
	"":                 {},
	"absolute":         {},
	"relative":         {},
	"basename":         {},
	"dirname_basename": {},
	"smart":            {},
}

var supportedReferenceMarkerStyles = map[string]struct{}{
	"":      {},
	"none":  {},
	"ascii": {},
	"emoji": {},
}

var supportedReferenceEnclosureStyles = map[string]struct{}{
	"":          {},
	"none":      {},
	"bracket":   {},
	"angle":     {},
	"fullwidth": {},
	"code":      {},
}

func validateReferenceConfig(prefix string, rc ReferenceConfig) error {
	for _, v := range rc.NormalizeAgents {
		key := strings.ToLower(strings.TrimSpace(v))
		if _, ok := supportedReferenceAgents[key]; !ok {
			return fmt.Errorf("config: %s.references.normalize_agents has unsupported value %q", prefix, v)
		}
	}
	for _, v := range rc.RenderPlatforms {
		key := strings.ToLower(strings.TrimSpace(v))
		if _, ok := supportedReferencePlatforms[key]; !ok {
			return fmt.Errorf("config: %s.references.render_platforms has unsupported value %q", prefix, v)
		}
	}
	if _, ok := supportedReferenceDisplayPaths[strings.ToLower(strings.TrimSpace(rc.DisplayPath))]; !ok {
		return fmt.Errorf("config: %s.references.display_path has unsupported value %q", prefix, rc.DisplayPath)
	}
	if _, ok := supportedReferenceMarkerStyles[strings.ToLower(strings.TrimSpace(rc.MarkerStyle))]; !ok {
		return fmt.Errorf("config: %s.references.marker_style has unsupported value %q", prefix, rc.MarkerStyle)
	}
	if _, ok := supportedReferenceEnclosureStyles[strings.ToLower(strings.TrimSpace(rc.EnclosureStyle))]; !ok {
		return fmt.Errorf("config: %s.references.enclosure_style has unsupported value %q", prefix, rc.EnclosureStyle)
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
// It uses surgical text editing to preserve comments and unknown fields.
func SaveActiveProvider(projectName, providerName string) error {
	configMu.Lock()
	defer configMu.Unlock()
	return patchProjectAgentOption(projectName, "provider", providerName)
}

// SaveProviderModel persists the selected model for a provider in a project.
// It first looks in the project's inline providers, then falls back to
// global [[providers]] if the provider is referenced via provider_refs.
// Uses surgical text editing to preserve comments and unknown fields.
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
	raw := string(data)
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	projectIdx := -1
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == projectName {
			projectIdx = i
			break
		}
	}
	if projectIdx < 0 {
		return fmt.Errorf("project %q not found in config", projectName)
	}

	lines, hadTrailing := splitConfigLines(raw)
	spans := buildRawProjectSpans(lines)
	if projectIdx >= len(spans) {
		return fmt.Errorf("project %q located in parsed config but not raw file", projectName)
	}
	projSpan := spans[projectIdx]

	for j, prov := range cfg.Projects[projectIdx].Agent.Providers {
		if prov.Name == providerName {
			if j < len(projSpan.agentProviders) {
				ps := projSpan.agentProviders[j]
				lines = upsertTomlStringKey(lines, ps.start+1, ps.end, "model", model)
				return writeRawConfig(joinConfigLines(lines, hadTrailing))
			}
			break
		}
	}

	for _, ref := range cfg.Projects[projectIdx].Agent.ProviderRefs {
		if ref == providerName {
			return patchGlobalProviderField(lines, hadTrailing, cfg, providerName, "model", model)
		}
	}
	return fmt.Errorf("provider %q not found in project %q", providerName, projectName)
}

func patchGlobalProviderField(lines []string, hadTrailing bool, cfg *Config, providerName, key, value string) error {
	globalStarts := make([]int, 0, 4)
	for i := range lines {
		if matchTableHeader(lines[i], "[[providers]]") {
			globalStarts = append(globalStarts, i)
		}
	}
	for k, gp := range cfg.Providers {
		if gp.Name != providerName || k >= len(globalStarts) {
			continue
		}
		gstart := globalStarts[k]
		gend := len(lines) - 1
		if k+1 < len(globalStarts) {
			gend = globalStarts[k+1] - 1
		}
		for j := gstart + 1; j <= gend; j++ {
			if isAnyTableHeader(lines[j]) {
				gend = j - 1
				break
			}
		}
		lines = upsertTomlStringKey(lines, gstart+1, gend, key, value)
		return writeRawConfig(joinConfigLines(lines, hadTrailing))
	}
	return fmt.Errorf("global provider %q not found", providerName)
}

// SaveAgentModel persists the selected default model for a project's agent.
// It uses surgical text editing to preserve comments and unknown fields.
func SaveAgentModel(projectName, model string) error {
	configMu.Lock()
	defer configMu.Unlock()
	return patchProjectAgentOption(projectName, "model", model)
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
// For global providers referenced via provider_refs, it removes the reference
// instead of deleting the global definition.
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
		if cfg.Projects[i].Name != projectName {
			continue
		}
		// Check inline providers
		providers := cfg.Projects[i].Agent.Providers
		for j := range providers {
			if providers[j].Name == providerName {
				cfg.Projects[i].Agent.Providers = append(providers[:j], providers[j+1:]...)
				found = true
				break
			}
		}
		// Also remove from provider_refs if present
		refs := cfg.Projects[i].Agent.ProviderRefs
		for j := range refs {
			if refs[j] == providerName {
				cfg.Projects[i].Agent.ProviderRefs = append(refs[:j], refs[j+1:]...)
				found = true
				break
			}
		}
		break
	}
	if !found {
		return fmt.Errorf("provider %q not found in project %q", providerName, projectName)
	}
	return saveConfig(cfg)
}

// ResolveProviderRefs merges global [[providers]] into each project that uses
// provider_refs. Inline [[projects.agent.providers]] entries are appended after
// resolved refs; if an inline entry has the same name as a global one, the
// inline entry wins (override).
func (cfg *Config) ResolveProviderRefs() {
	if len(cfg.Providers) == 0 {
		return
	}
	globalByName := make(map[string]ProviderConfig, len(cfg.Providers))
	for _, p := range cfg.Providers {
		globalByName[p.Name] = p
	}
	for i := range cfg.Projects {
		refs := cfg.Projects[i].Agent.ProviderRefs
		if len(refs) == 0 {
			continue
		}
		agentType := cfg.Projects[i].Agent.Type
		inlineNames := make(map[string]bool, len(cfg.Projects[i].Agent.Providers))
		for _, p := range cfg.Projects[i].Agent.Providers {
			inlineNames[p.Name] = true
		}
		var resolved []ProviderConfig
		for _, name := range refs {
			if inlineNames[name] {
				continue // inline override takes precedence
			}
			gp, ok := globalByName[name]
			if !ok {
				slog.Warn("provider ref not found in global [[providers]]", "project", cfg.Projects[i].Name, "ref", name)
				continue
			}
			if len(gp.AgentTypes) > 0 && !containsString(gp.AgentTypes, agentType) {
				slog.Debug("skipping provider: agent type mismatch", "provider", name, "project", cfg.Projects[i].Name,
					"provider_agents", gp.AgentTypes, "project_agent", agentType)
				continue
			}
		resolved = append(resolved, gp.ResolveForAgent(agentType))
		}
		cfg.Projects[i].Agent.Providers = append(resolved, cfg.Projects[i].Agent.Providers...)
	}
}

// ResolveForAgent applies per-agent-type overrides (Endpoints, AgentModels,
// AgentModelLists) to a copy of the provider and returns it.
func (p ProviderConfig) ResolveForAgent(agentType string) ProviderConfig {
	if ep, ok := p.Endpoints[agentType]; ok && ep != "" {
		p.BaseURL = ep
	}
	if am, ok := p.AgentModels[agentType]; ok && am != "" {
		p.Model = am
	}
	if aml, ok := p.AgentModelLists[agentType]; ok && len(aml) > 0 {
		p.Models = aml
	}
	return p
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// ── Global provider CRUD ───────────────────────────────────────

// ListGlobalProviders returns the top-level [[providers]] list.
func ListGlobalProviders() ([]ProviderConfig, error) {
	configMu.Lock()
	defer configMu.Unlock()
	cfg, err := loadLocked()
	if err != nil {
		return nil, err
	}
	return cfg.Providers, nil
}

// AddGlobalProvider appends a provider to the top-level [[providers]] and saves.
func AddGlobalProvider(provider ProviderConfig) error {
	configMu.Lock()
	defer configMu.Unlock()
	cfg, err := loadLocked()
	if err != nil {
		return err
	}
	for _, existing := range cfg.Providers {
		if existing.Name == provider.Name {
			return fmt.Errorf("global provider %q already exists", provider.Name)
		}
	}
	cfg.Providers = append(cfg.Providers, provider)
	return saveConfig(cfg)
}

// UpdateGlobalProvider replaces an existing global provider by name.
func UpdateGlobalProvider(name string, provider ProviderConfig) error {
	configMu.Lock()
	defer configMu.Unlock()
	cfg, err := loadLocked()
	if err != nil {
		return err
	}
	for i := range cfg.Providers {
		if cfg.Providers[i].Name == name {
			provider.Name = name // name is immutable in update
			cfg.Providers[i] = provider
			return saveConfig(cfg)
		}
	}
	return fmt.Errorf("global provider %q not found", name)
}

// RemoveGlobalProvider removes a provider from top-level [[providers]] and
// also strips the name from every project's provider_refs, then saves.
func RemoveGlobalProvider(name string) error {
	configMu.Lock()
	defer configMu.Unlock()
	cfg, err := loadLocked()
	if err != nil {
		return err
	}
	found := false
	for i := range cfg.Providers {
		if cfg.Providers[i].Name == name {
			cfg.Providers = append(cfg.Providers[:i], cfg.Providers[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("global provider %q not found", name)
	}
	for i := range cfg.Projects {
		refs := cfg.Projects[i].Agent.ProviderRefs
		for j := 0; j < len(refs); j++ {
			if refs[j] == name {
				cfg.Projects[i].Agent.ProviderRefs = append(refs[:j], refs[j+1:]...)
				break
			}
		}
	}
	return saveConfig(cfg)
}

func loadLocked() (*Config, error) {
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
	return cfg, nil
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
//   - removes empty section headers (no key-value pairs between this header and the next)
//
// It deliberately keeps all key-value lines intact, including zero-value ones
// (e.g. `thinking_messages = false`, `port = 0`), because those may be explicitly set by the user.
func formatTOML(raw string) string {
	lines := strings.Split(raw, "\n")

	// Pass 1: identify empty sections (header followed only by blank lines
	// until the next header or EOF).
	skipSection := make(map[int]bool)
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
			if t != "" {
				hasContent = true
				break
			}
		}
		if !hasContent {
			skipSection[i] = true
		}
	}

	// Pass 2: strip trailing whitespace from each line, skip empty sections,
	// ensure a blank line before section headers, and collapse consecutive
	// blank lines into one.
	var out []string
	prevBlank := false
	for i, line := range lines {
		if skipSection[i] {
			continue
		}
		line = strings.TrimRight(line, " \t")
		trimmed := strings.TrimSpace(line)
		isBlank := trimmed == ""

		if isBlank {
			if prevBlank {
				continue
			}
			prevBlank = true
			out = append(out, "")
			continue
		}
		prevBlank = false

		if trimmed[0] == '[' {
			if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
				out = append(out, "")
			}
		}
		out = append(out, line)
	}

	// Trim leading and trailing blank lines, then ensure single trailing newline.
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n") + "\n"
}

// SaveLanguage saves the language setting to the config file.
// Uses surgical text editing to preserve comments and unknown fields.
func SaveLanguage(lang string) error {
	configMu.Lock()
	defer configMu.Unlock()
	return patchTopLevelField("language", lang)
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

// SaveDisplayConfig persists the display settings to the config file.
// Uses surgical text editing to preserve comments and unknown fields.
func SaveDisplayConfig(thinkingMessages *bool, thinkingMaxLen, toolMaxLen *int, toolMessages *bool) error {
	configMu.Lock()
	defer configMu.Unlock()
	if thinkingMessages != nil {
		if err := patchSectionField("display", "thinking_messages", fmt.Sprintf("%t", *thinkingMessages)); err != nil {
			return err
		}
	}
	if thinkingMaxLen != nil {
		if err := patchSectionField("display", "thinking_max_len", fmt.Sprintf("%d", *thinkingMaxLen)); err != nil {
			return err
		}
	}
	if toolMaxLen != nil {
		if err := patchSectionField("display", "tool_max_len", fmt.Sprintf("%d", *toolMaxLen)); err != nil {
			return err
		}
	}
	if toolMessages != nil {
		if err := patchSectionField("display", "tool_messages", fmt.Sprintf("%t", *toolMessages)); err != nil {
			return err
		}
	}
	return nil
}

// SaveTTSMode persists the TTS mode setting to the config file.
// Uses surgical text editing to preserve comments and unknown fields.
func SaveTTSMode(mode string) error {
	configMu.Lock()
	defer configMu.Unlock()
	return patchSectionField("tts", "tts_mode", quoteTomlString(mode))
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
	SetAllowFromEmpty bool   // when true, seed/append allow_from with OwnerOpenID while preserving "*"
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
	if opts.SetAllowFromEmpty && strings.TrimSpace(opts.OwnerOpenID) != "" {
		allowFrom = mergeAllowFromValue(allowFrom, strings.TrimSpace(opts.OwnerOpenID))
		if allowFrom != "" {
			platform.Options["allow_from"] = allowFrom
		}
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

func mergeAllowFromValue(current, userID string) string {
	current = strings.TrimSpace(current)
	userID = strings.TrimSpace(userID)

	if current == "*" || userID == "" {
		return current
	}
	if current == "" {
		return userID
	}

	parts := strings.Split(current, ",")
	merged := make([]string, 0, len(parts)+1)
	seen := make(map[string]struct{}, len(parts)+1)

	appendPart := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if v == "*" {
			merged = []string{"*"}
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		merged = append(merged, v)
	}

	for _, part := range parts {
		if len(merged) == 1 && merged[0] == "*" {
			return "*"
		}
		appendPart(part)
	}
	if len(merged) == 1 && merged[0] == "*" {
		return "*"
	}
	appendPart(userID)
	if len(merged) == 1 && merged[0] == "*" {
		return "*"
	}
	return strings.Join(merged, ",")
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
	ScannedUserID     string // optional ilink_user_id for allow_from merge when SetAllowFromEmpty
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
	if opts.SetAllowFromEmpty && strings.TrimSpace(opts.ScannedUserID) != "" {
		allowFrom = mergeAllowFromValue(allowFrom, strings.TrimSpace(opts.ScannedUserID))
		if allowFrom != "" {
			platform.Options["allow_from"] = allowFrom
		}
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
	if agentType := strings.TrimSpace(opts.AgentType); agentType != "" {
		realType, preset, _ := strings.Cut(agentType, ":")
		agentOpts := map[string]any{}
		if realType == "acp" && preset != "" {
			agentOpts["command"] = preset
			agentOpts["display_name"] = preset
		}
		return AgentConfig{
			Type:    realType,
			Options: agentOpts,
		}
	}
	if len(cfg.Projects) > 0 {
		return cloneAgentConfig(cfg.Projects[0].Agent)
	}
	return AgentConfig{
		Type:    "codex",
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
			p := ProviderConfig{
				Name:        in.Providers[i].Name,
				APIKey:      in.Providers[i].APIKey,
				BaseURL:     in.Providers[i].BaseURL,
				Model:       in.Providers[i].Model,
				Models:      append([]ProviderModelConfig(nil), in.Providers[i].Models...),
				Thinking:    in.Providers[i].Thinking,
				Env:         cloneStringMap(in.Providers[i].Env),
				Endpoints:   cloneStringMap(in.Providers[i].Endpoints),
				AgentModels: cloneStringMap(in.Providers[i].AgentModels),
			}
			if len(in.Providers[i].AgentModelLists) > 0 {
				p.AgentModelLists = make(map[string][]ProviderModelConfig, len(in.Providers[i].AgentModelLists))
				for k, v := range in.Providers[i].AgentModelLists {
					p.AgentModelLists[k] = append([]ProviderModelConfig(nil), v...)
				}
			}
			if in.Providers[i].Codex != nil {
				p.Codex = &CodexProviderConfig{
					EnvKey:      in.Providers[i].Codex.EnvKey,
					WireAPI:     in.Providers[i].Codex.WireAPI,
					HTTPHeaders: cloneStringMap(in.Providers[i].Codex.HTTPHeaders),
				}
			}
			out.Providers[i] = p
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

// patchProjectAgentOption does a surgical text-level update of a single key
// under [projects.agent.options] for the given project. It preserves all
// comments, unknown fields, and formatting in the config file.
// The caller must hold configMu.
func patchProjectAgentOption(projectName, key, value string) error {
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	raw := string(data)
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	projectIdx := -1
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == projectName {
			projectIdx = i
			break
		}
	}
	if projectIdx < 0 {
		return fmt.Errorf("project %q not found in config", projectName)
	}

	lines, hadTrailing := splitConfigLines(raw)
	spans := buildRawProjectSpans(lines)
	if projectIdx >= len(spans) {
		return fmt.Errorf("project %q located in parsed config but not raw file", projectName)
	}
	projSpan := spans[projectIdx]

	if projSpan.agentOptionsStart < 0 {
		// [projects.agent.options] doesn't exist; create it.
		insertAt := projSpan.agentEnd + 1
		if projSpan.agentStart < 0 {
			// [projects.agent] also doesn't exist; insert after [[projects]] header + name line
			insertAt = projSpan.start + 1
			for ln := projSpan.start + 1; ln <= projSpan.end; ln++ {
				if isAnyTableHeader(lines[ln]) {
					insertAt = ln
					break
				}
				insertAt = ln + 1
			}
			block := []string{"", "[projects.agent]", "type = \"claudecode\"", "", "[projects.agent.options]"}
			lines = insertLines(lines, insertAt, block)
		} else {
			block := []string{"", "[projects.agent.options]"}
			lines = insertLines(lines, insertAt, block)
		}
		spans = buildRawProjectSpans(lines)
		projSpan = spans[projectIdx]
	}

	lines = upsertTomlStringKey(lines, projSpan.agentOptionsStart+1, projSpan.agentOptionsEnd, key, value)
	return writeRawConfig(joinConfigLines(lines, hadTrailing))
}

// patchTopLevelField does a surgical text-level update of a single top-level
// key in the config file. The caller must hold configMu.
func patchTopLevelField(key, value string) error {
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	raw := string(data)
	lines, hadTrailing := splitConfigLines(raw)

	// Top-level keys appear before the first section header.
	topEnd := len(lines) - 1
	for i := range lines {
		if isAnyTableHeader(lines[i]) {
			topEnd = i - 1
			break
		}
	}

	for i := 0; i <= topEnd && i < len(lines); i++ {
		if matchTomlStringKey(lines[i], key) {
			lines[i] = replaceTomlStringKeyLine(lines[i], key, value)
			return writeRawConfig(joinConfigLines(lines, hadTrailing))
		}
	}
	// Key not found; insert before the first section header.
	insertAt := topEnd + 1
	if insertAt < 0 {
		insertAt = 0
	}
	lines = insertLines(lines, insertAt, []string{fmt.Sprintf("%s = %s", key, quoteTomlString(value))})
	return writeRawConfig(joinConfigLines(lines, hadTrailing))
}

// patchSectionField does a surgical text-level update of a single key
// under a given [section] in the config file. The caller must hold configMu.
func patchSectionField(section, key, tomlValue string) error {
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	raw := string(data)
	lines, hadTrailing := splitConfigLines(raw)

	sectionStart := -1
	sectionEnd := len(lines) - 1
	header := "[" + section + "]"
	for i := range lines {
		if sectionStart < 0 && matchTableHeader(lines[i], header) {
			sectionStart = i
			continue
		}
		if sectionStart >= 0 && isAnyTableHeader(lines[i]) {
			sectionEnd = i - 1
			break
		}
	}

	if sectionStart < 0 {
		topEnd := len(lines) - 1
		for i := range lines {
			if isAnyTableHeader(lines[i]) {
				topEnd = i - 1
				break
			}
		}
		insertAt := topEnd + 1
		if insertAt < 0 {
			insertAt = 0
		}
		block := []string{"", header, fmt.Sprintf("%s = %s", key, tomlValue)}
		lines = insertLines(lines, insertAt, block)
		return writeRawConfig(joinConfigLines(lines, hadTrailing))
	}

	lines = upsertTomlRawKey(lines, sectionStart+1, sectionEnd, key, tomlValue)
	return writeRawConfig(joinConfigLines(lines, hadTrailing))
}

type rawProjectSpan struct {
	start     int
	end       int
	platforms []rawPlatformSpan

	agentStart        int // [projects.agent] header; -1 if absent
	agentEnd          int // last line before the next header or project end
	agentOptionsStart int // [projects.agent.options] header; -1 if absent
	agentOptionsEnd   int // last line of agent options section
	agentProviders    []rawProviderSpan
}

type rawProviderSpan struct {
	start    int // [[projects.agent.providers]] header
	end      int
	nameLine int // line with name = "..."
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
		span := rawProjectSpan{
			start:             start,
			end:               end,
			agentStart:        -1,
			agentEnd:          -1,
			agentOptionsStart: -1,
			agentOptionsEnd:   -1,
		}

		for ln := start + 1; ln <= end; ln++ {
			if matchTableHeader(lines[ln], "[projects.agent]") && !matchTableHeader(lines[ln], "[projects.agent.options]") && !matchTableHeader(lines[ln], "[[projects.agent.providers]]") {
				span.agentStart = ln
				span.agentEnd = end
				for j := ln + 1; j <= end; j++ {
					if isAnyTableHeader(lines[j]) {
						span.agentEnd = j - 1
						break
					}
				}
			}
			if matchTableHeader(lines[ln], "[projects.agent.options]") {
				span.agentOptionsStart = ln
				span.agentOptionsEnd = end
				for j := ln + 1; j <= end; j++ {
					if isAnyTableHeader(lines[j]) {
						span.agentOptionsEnd = j - 1
						break
					}
				}
			}
			if matchTableHeader(lines[ln], "[[projects.agent.providers]]") {
				provSpan := rawProviderSpan{start: ln, end: end, nameLine: -1}
				for j := ln + 1; j <= end; j++ {
					if isAnyTableHeader(lines[j]) {
						provSpan.end = j - 1
						break
					}
				}
				for j := ln + 1; j <= provSpan.end; j++ {
					if matchTomlStringKey(lines[j], "name") {
						provSpan.nameLine = j
						break
					}
				}
				span.agentProviders = append(span.agentProviders, provSpan)
			}
		}

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

// upsertTomlRawKey is like upsertTomlStringKey but writes the value literally
// (no quoting). Use for booleans, integers, and pre-formatted values.
func upsertTomlRawKey(lines []string, start, end int, key, rawValue string) []string {
	if start < 0 {
		start = 0
	}
	if end >= len(lines) {
		end = len(lines) - 1
	}
	for i := start; i <= end && i < len(lines); i++ {
		if matchTomlStringKey(lines[i], key) {
			indent := leadingWhitespace(lines[i])
			comment := extractLineComment(lines[i])
			lines[i] = fmt.Sprintf("%s%s = %s", indent, key, rawValue)
			if comment != "" {
				lines[i] += " " + comment
			}
			return lines
		}
	}
	insertAt := end + 1
	if insertAt < start {
		insertAt = start
	}
	return insertLines(lines, insertAt, []string{fmt.Sprintf("%s = %s", key, rawValue)})
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
	Language             *string
	AdminFrom            *string
	DisabledCommands     []string
	WorkDir              *string
	Mode                 *string
	AgentType            *string
	ShowContextIndicator *bool
	ReplyFooter          *bool
	InjectSender         *bool
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
		if update.AgentType != nil && *update.AgentType != proj.Agent.Type {
			newType := *update.AgentType
			proj.Agent.Type = newType
			// Filter out provider_refs incompatible with the new agent type.
			globalByName := make(map[string]ProviderConfig, len(cfg.Providers))
			for _, p := range cfg.Providers {
				globalByName[p.Name] = p
			}
			var compatible []string
			for _, ref := range proj.Agent.ProviderRefs {
				gp, ok := globalByName[ref]
				if !ok {
					continue
				}
				if len(gp.AgentTypes) > 0 && !containsString(gp.AgentTypes, newType) {
					slog.Info("removing incompatible provider ref on agent type change",
						"project", projectName, "provider", ref,
						"provider_agents", gp.AgentTypes, "new_agent", newType)
					continue
				}
				compatible = append(compatible, ref)
			}
			proj.Agent.ProviderRefs = compatible
			// Clear active provider if it was removed.
			if opts := proj.Agent.Options; opts != nil {
				if prov, ok := opts["provider"].(string); ok && prov != "" {
					found := false
					for _, ref := range compatible {
						if ref == prov {
							found = true
							break
						}
					}
					if !found {
						delete(opts, "provider")
					}
				}
			}
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
		if update.ReplyFooter != nil {
			v := *update.ReplyFooter
			proj.ReplyFooter = &v
		}
		if update.InjectSender != nil {
			v := *update.InjectSender
			proj.InjectSender = &v
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
		if p.ReplyFooter != nil {
			result["reply_footer"] = *p.ReplyFooter
		}
		if p.InjectSender != nil {
			result["inject_sender"] = *p.InjectSender
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
		if len(p.Agent.ProviderRefs) > 0 {
			result["provider_refs"] = p.Agent.ProviderRefs
		}
		return result
	}
	return nil
}

// SaveProviderRefs updates provider_refs for a project.
func SaveProviderRefs(projectName string, refs []string) error {
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
			cfg.Projects[i].Agent.ProviderRefs = refs
			return saveConfig(cfg)
		}
	}
	return fmt.Errorf("project %q not found", projectName)
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
	content = formatTOML(content)
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

// FormatConfigFile reads the config file at the given path, formats it, and
// writes it back. It validates the TOML syntax before writing.
func FormatConfigFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("invalid TOML: %w", err)
	}
	formatted := formatTOML(string(data))
	if formatted == string(data) {
		return nil
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(formatted); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write formatted config: %w", err)
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
	return os.Rename(tmpPath, path)
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
	if cfg.IdleTimeoutMins != nil {
		result["idle_timeout_mins"] = *cfg.IdleTimeoutMins
	} else {
		result["idle_timeout_mins"] = 120
	}
	// Display
	if cfg.Display.ThinkingMessages != nil {
		result["thinking_messages"] = *cfg.Display.ThinkingMessages
	} else {
		result["thinking_messages"] = true
	}
	if cfg.Display.ThinkingMaxLen != nil {
		result["thinking_max_len"] = *cfg.Display.ThinkingMaxLen
	} else {
		result["thinking_max_len"] = 300
	}
	if cfg.Display.ToolMessages != nil {
		result["tool_messages"] = *cfg.Display.ToolMessages
	} else {
		result["tool_messages"] = true
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
	// Queue
	queueMax := 5
	if cfg.Queue.MaxDepth != nil {
		queueMax = *cfg.Queue.MaxDepth
	}
	result["queue_max_depth"] = queueMax
	return result
}

// GlobalSettingsUpdate holds fields to update in global config.
type GlobalSettingsUpdate struct {
	Language           *string `json:"language"`
	AttachmentSend     *string `json:"attachment_send"`
	LogLevel           *string `json:"log_level"`
	IdleTimeoutMins    *int    `json:"idle_timeout_mins"`
	ThinkingMessages   *bool   `json:"thinking_messages"`
	ThinkingMaxLen     *int    `json:"thinking_max_len"`
	ToolMessages       *bool   `json:"tool_messages"`
	ToolMaxLen         *int    `json:"tool_max_len"`
	StreamPreviewOn    *bool   `json:"stream_preview_enabled"`
	StreamPreviewIntMs *int    `json:"stream_preview_interval_ms"`
	RateLimitMax       *int    `json:"rate_limit_max_messages"`
	RateLimitWindow    *int    `json:"rate_limit_window_secs"`
	QueueMaxDepth      *int    `json:"queue_max_depth"`
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
	if u.AttachmentSend != nil {
		cfg.AttachmentSend = *u.AttachmentSend
	}
	if u.LogLevel != nil {
		cfg.Log.Level = *u.LogLevel
	}
	if u.IdleTimeoutMins != nil {
		cfg.IdleTimeoutMins = u.IdleTimeoutMins
	}
	if u.ThinkingMessages != nil {
		cfg.Display.ThinkingMessages = u.ThinkingMessages
	}
	if u.ThinkingMaxLen != nil {
		cfg.Display.ThinkingMaxLen = u.ThinkingMaxLen
	}
	if u.ToolMessages != nil {
		cfg.Display.ToolMessages = u.ToolMessages
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
	if u.QueueMaxDepth != nil {
		cfg.Queue.MaxDepth = u.QueueMaxDepth
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
