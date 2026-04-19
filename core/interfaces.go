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

// CronReplyTargetResolver is an optional interface for platforms that need to
// map a logical cron session key to the actual reply target used at execution
// time. This is useful for platforms where proactive replies may need to create
// or switch to a thread before the cron run starts.
//
// Implementations that do not need special handling should return
// ErrNotSupported so callers can fall back to ReconstructReplyCtx(sessionKey).
type CronReplyTargetResolver interface {
	ResolveCronReplyTarget(sessionKey string, title string) (resolvedSessionKey string, replyCtx any, err error)
}

// SessionEnvInjector is an optional interface for agents that accept
// per-session environment variables (e.g. CC_PROJECT, CC_SESSION_KEY).
type SessionEnvInjector interface {
	SetSessionEnv(env []string)
}

// FormattingInstructionProvider is an optional interface for platforms that
// provide platform-specific formatting instructions for the agent system prompt
// (e.g., Slack mrkdwn vs standard Markdown).
type FormattingInstructionProvider interface {
	FormattingInstructions() string
}

// PlatformPromptInjector is an optional interface for agents that can receive
// platform-specific prompt fragments (e.g., formatting instructions).
// The engine calls this before StartSession when the platform provides formatting.
type PlatformPromptInjector interface {
	SetPlatformPrompt(prompt string)
}

// AgentSystemPrompt returns the system prompt fragment that informs agents about
// cc-connect capabilities (cron scheduling, etc.).
// The prompt is designed to be appended to the agent's existing system prompt.
func AgentSystemPrompt() string {
	return `You are running inside cc-connect, a bridge that connects you to messaging platforms.
Your normal text responses are automatically delivered to the user — just reply normally, do NOT use cc-connect send for ordinary text replies.

## Available tools

### Send generated images or files back to the user
When you generate a local image or file that should be sent to the user, use:

  cc-connect send --image /absolute/path/to/image.png
  cc-connect send --file /absolute/path/to/report.pdf
  cc-connect send --file /absolute/path/to/report.pdf --image /absolute/path/to/chart.png

You may repeat --image / --file multiple times. Use this only for generated attachments that need to be delivered to the user.
If you include --message, do not repeat the exact same sentence again in your normal reply, because your normal reply is also delivered automatically.

### Scheduled tasks (cron)
When the user asks you to do something on a schedule (e.g. "每天早上6点帮我总结GitHub trending"), use the Bash tool to run:

  cc-connect cron add --cron "<min> <hour> <day> <month> <weekday>" --prompt "<task description>" --desc "<short label>"

Environment variables CC_PROJECT and CC_SESSION_KEY are already set, so you do NOT need to specify --project or --session-key.

Optional flags:
  --session-mode <mode>     reuse (default) or new-per-run (fresh session each trigger)
  --timeout-mins <n>        max wait per run in minutes (default 30, 0 = unlimited)
  --exec <command>          run a shell command directly instead of --prompt

Examples:
  cc-connect cron add --cron "0 6 * * *" --prompt "Collect GitHub trending repos and send a summary" --desc "Daily GitHub Trending"
  cc-connect cron add --cron "0 9 * * 1" --prompt "Generate a weekly project status report" --desc "Weekly Report"
  cc-connect cron add --cron "*/2 * * * *" --exec "ipconfig" --session-mode new-per-run --desc "Every 2 min ipconfig"

You can also list or delete cron jobs:
  cc-connect cron list
  cc-connect cron del <job-id>

### Bot-to-bot relay
When you need to communicate with another bot (e.g. ask another AI agent a question), use:

  cc-connect relay send --to <target_project> "<message>"

IMPORTANT: <target_project> must be the EXACT project name from the /bind command output.
Do NOT guess or modify the name — use it exactly as shown (e.g. "gemini", not "gemini-bot").

This sends a message to the target bot and waits for its response (printed to stdout).
The conversation is visible in the group chat and each bot maintains its own relay session.

Environment variables CC_PROJECT and CC_SESSION_KEY are already set, so the relay knows which group chat to use.

### Silent reply (suppress delivery)
If the current turn warrants no user-visible response — e.g. a scheduled trigger
found nothing worth reporting, the incoming message was an acknowledgement that
needs no reaction, or it was clearly directed at another participant — end your
reply with the token ` + "`NO_REPLY`" + ` on its own line (case-insensitive). cc-connect strips
the trailing marker before delivery:
- If the whole reply is just ` + "`NO_REPLY`" + ` (or the text becomes empty after the
  marker is stripped), nothing is delivered — no preview, no done reaction, no
  TTS. Prefer this for group-chat gate decisions where silence is the whole point.
- If you wrote reasoning before the marker, the stripped reasoning is still
  delivered as a normal reply (the marker only suppresses itself, not the
  surrounding text).
Use this sparingly; when in doubt, send a brief reply instead.
`
}

// SystemPromptSupporter is an optional marker interface for agents that
// natively inject AgentSystemPrompt() (e.g., via --append-system-prompt).
// Agents that do NOT implement this need the instructions written to their
// memory/instruction file for relay and cron to work.
type SystemPromptSupporter interface {
	HasSystemPromptSupport() bool
}

// TypingIndicator is an optional interface for platforms that can show a
// "processing" indicator (typing bubble, emoji reaction, etc.) while the
// agent is working. StartTyping is called when processing begins and returns
// a stop function that the caller must invoke when processing ends.
type TypingIndicator interface {
	StartTyping(ctx context.Context, replyCtx any) (stop func())
}

// TypingIndicatorDone is an optional interface for platforms that can show a
// "done" reaction after processing completes. The engine calls AddDoneReaction
// when the agent finishes a multi-round turn in quiet mode, so the user gets
// a push notification (e.g. Feishu card edits don't trigger pushes).
type TypingIndicatorDone interface {
	AddDoneReaction(replyCtx any)
}

// ImageSender is an optional interface for platforms that support sending images.
type ImageSender interface {
	SendImage(ctx context.Context, replyCtx any, img ImageAttachment) error
}

// FileSender is an optional interface for platforms that support sending files.
type FileSender interface {
	SendFile(ctx context.Context, replyCtx any, file FileAttachment) error
}

// MessageUpdater is an optional interface for platforms that support updating messages.
type MessageUpdater interface {
	UpdateMessage(ctx context.Context, replyCtx any, content string) error
}

// ProgressStyleProvider is an optional interface for platforms that expose
// a preferred style for intermediate progress rendering.
// Typical values: "legacy", "compact", "card".
type ProgressStyleProvider interface {
	ProgressStyle() string
}

// ProgressCardPayloadSupport is an optional interface for platforms that can
// parse and render structured progress-card payloads.
type ProgressCardPayloadSupport interface {
	SupportsProgressCardPayload() bool
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

// CardSender is an optional interface for platforms that support sending
// structured rich cards (e.g. Feishu Interactive Card). Platforms that do not
// implement this interface will receive a plain-text fallback via Card.RenderText().
type CardSender interface {
	SendCard(ctx context.Context, replyCtx any, card *Card) error
	ReplyCard(ctx context.Context, replyCtx any, card *Card) error
}

// CardNavigationHandler is called by platforms to render a card for in-place
// card updates (e.g. Feishu card.action.trigger callback). The action string
// uses prefixes like "nav:/model" or "act:/model 3".
type CardNavigationHandler func(action string, sessionKey string) *Card

// CardNavigable is an optional interface for platforms that support in-place
// card navigation (updating the existing card instead of sending a new message).
type CardNavigable interface {
	SetCardNavigationHandler(h CardNavigationHandler)
}

// CardRefresher is an optional interface for platforms that can update a
// previously rendered card in-place after the original callback has returned.
// This is used when async operations (e.g. delete-mode deletion) need to
// refresh a "loading" card with the final result. Platforms that implement
// this interface should track the message ID from card action callbacks and
// use it to patch the card content.
type CardRefresher interface {
	RefreshCard(ctx context.Context, sessionKey string, card *Card) error
}

// PlatformLifecycleHandler receives readiness state transitions from async
// recoverable platforms.
type PlatformLifecycleHandler interface {
	OnPlatformReady(p Platform)
	OnPlatformUnavailable(p Platform, err error)
}

// AsyncRecoverablePlatform is an optional interface for platforms that start
// a background recovery loop and later report readiness or unavailability.
//
// Platforms implementing this interface may return from Start() before they are
// actually ready to receive traffic. Callers must treat OnPlatformReady as the
// signal that deferred platform capabilities may be initialized and the
// platform is usable. A nil Start() return therefore means the recovery loop
// was launched successfully, not necessarily that an initial connection was
// established.
type AsyncRecoverablePlatform interface {
	Platform
	SetLifecycleHandler(h PlatformLifecycleHandler)
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
	// Send sends a user message (with optional images and files) to the running agent process.
	Send(prompt string, images []ImageAttachment, files []FileAttachment) error
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
	Models   []ModelOption     // pre-configured list of available models for this provider
	Thinking string            // override thinking type sent to this provider ("disabled", "enabled", or "" for no rewrite)
	Env      map[string]string // arbitrary extra env vars (e.g. CLAUDE_CODE_USE_BEDROCK=1)
	// Codex-specific provider config (maps to Codex model_providers.<name>)
	CodexWireAPI     string            // wire API format (e.g. "responses")
	CodexHTTPHeaders map[string]string // custom HTTP headers
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

// ReasoningEffortSwitcher is an optional interface for agents that support
// runtime switching of reasoning effort.
type ReasoningEffortSwitcher interface {
	SetReasoningEffort(effort string)
	GetReasoningEffort() string
	AvailableReasoningEfforts() []string
}

// ModelOption describes a selectable model.
type ModelOption struct {
	Name  string // model identifier passed to CLI
	Desc  string // short description (display_name or empty)
	Alias string // optional short alias for the /model command (e.g. "codex" for "gpt-5.3-codex")
}

// UsageReporter is an optional interface for agents that can report account or
// model quota usage from their backing provider.
type UsageReporter interface {
	GetUsage(ctx context.Context) (*UsageReport, error)
}

// UsageReport is a provider-neutral quota snapshot returned by UsageReporter.
type UsageReport struct {
	Provider  string
	AccountID string
	UserID    string
	Email     string
	Plan      string
	Buckets   []UsageBucket
	Credits   *UsageCredits
}

// UsageBucket groups one logical quota, such as standard requests or code review.
type UsageBucket struct {
	Name         string
	Allowed      bool
	LimitReached bool
	Windows      []UsageWindow
}

// UsageWindow describes a single quota window.
type UsageWindow struct {
	Name              string
	UsedPercent       int
	WindowSeconds     int
	ResetAfterSeconds int
	ResetAtUnix       int64
}

// UsageCredits contains optional credit/balance metadata.
type UsageCredits struct {
	HasCredits bool
	Unlimited  bool
	Balance    string
}

// ContextUsageReporter is an optional interface for running agent sessions that
// can report real runtime context usage for the active conversation.
type ContextUsageReporter interface {
	GetContextUsage() *ContextUsage
}

// ContextUsage describes runtime context consumption for the active session.
type ContextUsage struct {
	// UsedTokens is the current token load to compare against ContextWindow when
	// computing remaining context capacity for the next turn.
	UsedTokens int
	// BaselineTokens is the portion of the context window always occupied by
	// fixed runtime/system instructions and therefore excluded from user-visible
	// "left" calculations when the agent provides it.
	BaselineTokens        int
	TotalTokens           int
	InputTokens           int
	CachedInputTokens     int
	OutputTokens          int
	ReasoningOutputTokens int
	ContextWindow         int
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

// SessionDeleter is an optional interface for agents that support deleting sessions.
type SessionDeleter interface {
	DeleteSession(ctx context.Context, sessionID string) error
}

// WorkDirSwitcher is an optional interface for agents that support runtime
// work directory switching. The change takes effect on the next session start;
// the current running session is terminated automatically by the engine.
type WorkDirSwitcher interface {
	SetWorkDir(dir string)
	GetWorkDir() string
}

// ModeSwitcher is an optional interface for agents that support runtime permission mode switching.
type ModeSwitcher interface {
	SetMode(mode string)
	GetMode() string
	PermissionModes() []PermissionModeInfo
}

// WorkspaceAgentOptionSnapshotter is an optional interface for agents that can
// export reusable constructor options needed to recreate an equivalent agent in
// a different workspace. Snapshot values should omit work_dir; the caller is
// responsible for setting the target workspace explicitly. Provider wiring and
// run_as propagation may still be handled separately by the engine.
type WorkspaceAgentOptionSnapshotter interface {
	WorkspaceAgentOptions() map[string]any
}

// LiveModeSwitcher is an optional interface for running agent sessions that can
// apply a mode change immediately without restarting the process.
type LiveModeSwitcher interface {
	SetLiveMode(mode string) bool
}

// PermissionModeInfo describes a permission mode for display.
type PermissionModeInfo struct {
	Key    string
	Name   string
	NameZh string
	Desc   string
	DescZh string
}

// BotCommandInfo represents a command for bot menu registration (e.g. Telegram setMyCommands).
type BotCommandInfo struct {
	Command     string // command name without leading "/"
	Description string // short description for the menu
	IsSkill     bool   // whether this entry comes from a skill
}

// CommandRegistrar is an optional interface for platforms that support
// registering commands to the platform's native menu (e.g. Telegram's setMyCommands).
type CommandRegistrar interface {
	RegisterCommands(commands []BotCommandInfo) error
}

// ChannelNameResolver is an optional interface for platforms that can resolve
// channel IDs to human-readable names.
type ChannelNameResolver interface {
	ResolveChannelName(channelID string) (string, error)
}
