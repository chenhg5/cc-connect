package core

import (
	"context"
	"errors"
)

// Platform abstracts a messaging platform (Feishu, DingTalk, Slack, etc.).
type Platform interface {
	Name() string
	Start(handler MessageHandler) error
	Reply(ctx context.Context, replyCtx any, content string) error
	Send(ctx context.Context, replyCtx any, content string) error
	Stop() error
}

// ErrNotSupported indicates a platform doesn't support a particular operation.
var ErrNotSupported = errors.New("operation not supported by this platform")

// ReplyContextReconstructor is an optional interface for platforms that can
// recreate a reply context from a session key. This is needed for cron jobs
// to send messages to users without an incoming message.
type ReplyContextReconstructor interface {
	ReconstructReplyCtx(sessionKey string) (any, error)
}

// SessionEnvInjector is an optional interface for agents that accept
// per-session environment variables (e.g. CC_PROJECT, CC_SESSION_KEY).
type SessionEnvInjector interface {
	SetSessionEnv(env []string)
}

// AgentSystemPrompt returns the system prompt fragment that informs agents about
// cc-connect capabilities (cron scheduling, etc.).
// The prompt is designed to be appended to the agent's existing system prompt.
func AgentSystemPrompt() string {
	return `You are running inside cc-connect, a bridge that connects you to messaging platforms.
Your responses are automatically delivered to the user — just reply normally, do NOT use cc-connect send.

## Available tools

### Scheduled tasks (cron)
When the user asks you to do something on a schedule (e.g. "每天早上6点帮我总结GitHub trending"), use the Bash tool to run:

  cc-connect cron add --cron "<min> <hour> <day> <month> <weekday>" --prompt "<task description>" --desc "<short label>"

Environment variables CC_PROJECT and CC_SESSION_KEY are already set, so you do NOT need to specify --project or --session-key.

Examples:
  cc-connect cron add --cron "0 6 * * *" --prompt "Collect GitHub trending repos and send a summary" --desc "Daily GitHub Trending"
  cc-connect cron add --cron "0 9 * * 1" --prompt "Generate a weekly project status report" --desc "Weekly Report"

You can also list or delete cron jobs:
  cc-connect cron list
  cc-connect cron del <job-id>
`
}

// MessageUpdater is an optional interface for platforms that support updating messages.
type MessageUpdater interface {
	UpdateMessage(ctx context.Context, replyCtx any, content string) error
}

// ButtonOption represents a clickable inline button.
type ButtonOption struct {
	Text string // display text on the button
	Data string // callback data returned when clicked (≤64 bytes for Telegram)
}

// InlineButtonSender is an optional interface for platforms that support
// sending messages with clickable inline buttons (e.g. Telegram Inline Keyboard).
// Buttons is a 2D slice: each inner slice is one row of buttons.
type InlineButtonSender interface {
	SendWithButtons(ctx context.Context, replyCtx any, content string, buttons [][]ButtonOption) error
}

// MessageHandler is called by platforms when a new message arrives.
type MessageHandler func(p Platform, msg *Message)

// Agent abstracts an AI coding assistant (Claude Code, Cursor, Gemini CLI, etc.).
// All agents must support persistent bidirectional sessions via StartSession.
type Agent interface {
	Name() string
	// StartSession creates or resumes an interactive session with a persistent process.
	StartSession(ctx context.Context, sessionID string) (AgentSession, error)
	// ListSessions returns sessions known to the agent backend.
	ListSessions(ctx context.Context) ([]AgentSessionInfo, error)
	Stop() error
}

// AgentSession represents a running interactive agent session with a persistent process.
type AgentSession interface {
	// Send sends a user message (with optional images) to the running agent process.
	Send(prompt string, images []ImageAttachment) error
	// RespondPermission sends a permission decision back to the agent process.
	RespondPermission(requestID string, result PermissionResult) error
	// Events returns the channel that emits agent events (kept open across turns).
	Events() <-chan Event
	// CurrentSessionID returns the current agent-side session ID.
	CurrentSessionID() string
	// Alive returns true if the underlying process is still running.
	Alive() bool
	// Close terminates the session and its underlying process.
	Close() error
}

// PermissionResult represents the user's decision on a permission request.
type PermissionResult struct {
	Behavior     string         `json:"behavior"`               // "allow" or "deny"
	UpdatedInput map[string]any `json:"updatedInput,omitempty"` // echoed back for allow
	Message      string         `json:"message,omitempty"`      // reason for deny
}

// ToolAuthorizer is an optional interface for agents that support dynamic tool authorization.
type ToolAuthorizer interface {
	AddAllowedTools(tools ...string) error
	GetAllowedTools() []string
}

// HistoryProvider is an optional interface for agents that can retrieve
// conversation history from their backend session files.
type HistoryProvider interface {
	GetSessionHistory(ctx context.Context, sessionID string, limit int) ([]HistoryEntry, error)
}

// ProviderConfig holds API provider settings for an agent.
type ProviderConfig struct {
	Name     string
	APIKey   string
	BaseURL  string
	Model    string
	Thinking string            // override thinking type sent to this provider ("disabled", "enabled", or "" for no rewrite)
	Env      map[string]string // arbitrary extra env vars (e.g. CLAUDE_CODE_USE_BEDROCK=1)
}

// ProviderSwitcher is an optional interface for agents that support multiple API providers.
type ProviderSwitcher interface {
	SetProviders(providers []ProviderConfig)
	SetActiveProvider(name string) bool
	GetActiveProvider() *ProviderConfig
	ListProviders() []ProviderConfig
}

// MemoryFileProvider is an optional interface for agents that support
// persistent instruction files (CLAUDE.md, AGENTS.md, GEMINI.md, etc.).
// The engine uses these paths for the /memory command.
type MemoryFileProvider interface {
	ProjectMemoryFile() string // project-level instruction file (e.g., <work_dir>/CLAUDE.md)
	GlobalMemoryFile() string  // user-level instruction file (e.g., ~/.claude/CLAUDE.md)
}

// ModelSwitcher is an optional interface for agents that support runtime model switching.
// Model changes take effect on the next session (existing sessions keep their model).
type ModelSwitcher interface {
	SetModel(model string)
	GetModel() string
	// AvailableModels tries to fetch models from the provider API.
	// Falls back to a built-in list on failure.
	AvailableModels(ctx context.Context) []ModelOption
}

// ModelOption describes a selectable model.
type ModelOption struct {
	Name string // model identifier passed to CLI
	Desc string // short description (display_name or empty)
}

// ContextCompressor is an optional interface for agents that support
// compressing/compacting the conversation context within a running session.
// CompressCommand returns the native slash command (e.g. "/compact", "/compress")
// that will be forwarded to the agent process. Return "" if not supported.
type ContextCompressor interface {
	CompressCommand() string
}

// CommandProvider is an optional interface for agents that expose custom slash
// commands via local files (e.g. .claude/commands/*.md). The engine scans the
// returned directories for *.md files and registers them as slash commands.
type CommandProvider interface {
	CommandDirs() []string
}

// SkillProvider is an optional interface for agents that expose skills via
// local directories (e.g. .claude/skills/<name>/SKILL.md). Each subdirectory
// containing a SKILL.md is treated as a skill. Skills are project-level and
// agent-specific — they are NOT shared across different agent types.
type SkillProvider interface {
	SkillDirs() []string
}

// ModeSwitcher is an optional interface for agents that support runtime permission mode switching.
type ModeSwitcher interface {
	SetMode(mode string)
	GetMode() string
	PermissionModes() []PermissionModeInfo
}

// PermissionModeInfo describes a permission mode for display.
type PermissionModeInfo struct {
	Key    string
	Name   string
	NameZh string
	Desc   string
	DescZh string
}
