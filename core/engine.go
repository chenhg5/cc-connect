package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxPlatformMessageLen = 4000
const telegramBotCommandLimit = 100
const maxQueuedMessages = 5 // cap queued messages to bound memory usage

const (
	defaultThinkingMaxLen = 300
	defaultToolMaxLen     = 500
)

// Slow-operation thresholds. Operations exceeding these durations produce a
// slog.Warn so operators can quickly pinpoint bottlenecks.
const (
	slowPlatformSend    = 2 * time.Second  // platform Reply / Send
	slowAgentStart      = 5 * time.Second  // agent.StartSession
	slowAgentClose      = 3 * time.Second  // agentSession.Close
	slowAgentSend       = 2 * time.Second  // agentSession.Send
	slowAgentFirstEvent = 15 * time.Second // time from send to first agent event
)

const (
	replyFooterUsageTimeout  = 1500 * time.Millisecond
	replyFooterUsageCacheTTL = 30 * time.Second
)

// VersionInfo is set by main at startup so that /version works.
var VersionInfo string

// CurrentVersion is the semver tag (e.g. "v1.2.0-beta.1"), set by main.
var CurrentVersion string

// ErrAttachmentSendDisabled indicates that side-channel image/file delivery is disabled by config.
var ErrAttachmentSendDisabled = errors.New("attachment send is disabled by config")

// RestartRequest carries info needed to send a post-restart notification.
type RestartRequest struct {
	SessionKey string `json:"session_key"`
	Platform   string `json:"platform"`
}

type replyFooterUsageCache struct {
	text      string
	fetchedAt time.Time
}

// SaveRestartNotify persists restart info so the new process can send
// a "restart successful" message after startup.
func SaveRestartNotify(dataDir string, req RestartRequest) error {
	dir := filepath.Join(dataDir, "run")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("SaveRestartNotify: mkdir failed", "dir", dir, "error", err)
	}
	data, _ := json.Marshal(req)
	return os.WriteFile(filepath.Join(dir, "restart_notify"), data, 0o644)
}

// ConsumeRestartNotify reads and deletes the restart notification file.
// Returns nil if no notification is pending.
func ConsumeRestartNotify(dataDir string) *RestartRequest {
	p := filepath.Join(dataDir, "run", "restart_notify")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	os.Remove(p)
	var req RestartRequest
	if json.Unmarshal(data, &req) != nil {
		return nil
	}
	return &req
}

// SendRestartNotification sends a "restart successful" message to the
// platform/session that initiated the restart.
func (e *Engine) SendRestartNotification(platformName, sessionKey string) {
	for _, p := range e.platforms {
		if p.Name() != platformName {
			continue
		}
		rc, ok := p.(ReplyContextReconstructor)
		if !ok {
			slog.Debug("restart notify: platform does not support ReconstructReplyCtx", "platform", platformName)
			return
		}
		rctx, err := rc.ReconstructReplyCtx(sessionKey)
		if err != nil {
			slog.Debug("restart notify: reconstruct failed", "error", err)
			return
		}
		text := e.i18n.T(MsgRestartSuccess)
		if CurrentVersion != "" {
			text += fmt.Sprintf(" (%s)", CurrentVersion)
		}
		if err := e.waitOutgoing(p); err != nil {
			slog.Debug("restart notify: outgoing wait cancelled or limited", "platform", platformName, "error", err)
			return
		}
		if err := p.Send(e.ctx, rctx, text); err != nil {
			slog.Debug("restart notify: send failed", "error", err)
		}
		return
	}
}

// RestartCh is signaled when /restart is invoked. main listens on it
// to perform a graceful shutdown followed by syscall.Exec.
var RestartCh = make(chan RestartRequest, 1)

// DisplayCfg controls how intermediate messages are surfaced.
// A value of -1 means "use default", 0 means "no truncation".
type DisplayCfg struct {
	Mode             string // "full" (default), "compact", or "quiet"
	ThinkingMessages bool
	ThinkingMaxLen   int // max runes for thinking preview; 0 = no truncation
	ToolMaxLen       int // max runes for tool use preview; 0 = no truncation
	ToolMessages     bool
}

// RateLimitCfg controls per-session message rate limiting.
type RateLimitCfg struct {
	MaxMessages int           // max messages per window; 0 = disabled
	Window      time.Duration // sliding window size
}

// Engine routes messages between platforms and the agent for a single project.
type Engine struct {
	name                  string
	agent                 Agent
	platforms             []Platform
	sessions              *SessionManager
	ctx                   context.Context
	cancel                context.CancelFunc
	i18n                  *I18n
	speech                SpeechCfg
	tts                   *TTSCfg
	display               DisplayCfg
	injectSender          bool
	attachmentSendEnabled bool
	startedAt             time.Time

	providerSaveFunc       func(providerName string) error
	providerAddSaveFunc    func(p ProviderConfig) error
	providerRemoveSaveFunc func(name string) error
	providerModelSaveFunc  func(providerName, model string) error
	modelSaveFunc          func(model string) error

	ttsSaveFunc func(mode string) error

	commandSaveAddFunc func(name, description, prompt, exec, workDir string) error
	commandSaveDelFunc func(name string) error

	displaySaveFunc  func(mode *string, thinkingMessages *bool, thinkingMaxLen, toolMaxLen *int, toolMessages *bool) error
	configReloadFunc func() (*ConfigReloadResult, error)

	cronScheduler      *CronScheduler
	heartbeatScheduler *HeartbeatScheduler

	commands *CommandRegistry
	skills   *SkillRegistry
	aliases  map[string]string // trigger → command (e.g. "帮助" → "/help")
	aliasMu  sync.RWMutex

	aliasSaveAddFunc func(name, command string) error
	aliasSaveDelFunc func(name string) error

	bannedWords []string
	bannedMu    sync.RWMutex

	disabledCmds map[string]bool
	adminFrom    string           // comma-separated user IDs for privileged commands; "*" = all allowed users; "" = deny
	userRoles    *UserRoleManager // nil = legacy mode (no per-user policies)
	userRolesMu  sync.RWMutex     // protects userRoles, disabledCmds, and adminFrom

	rateLimiter      *RateLimiter
	outgoingRL       *OutgoingRateLimiter
	streamPreview    StreamPreviewCfg
	references       ReferenceRenderCfg
	relayManager     *RelayManager
	eventIdleTimeout time.Duration
	dirHistory       *DirHistory
	baseWorkDir      string
	projectState     *ProjectStateStore

	// Auto-compress settings
	autoCompressEnabled   bool
	autoCompressMaxTokens int
	autoCompressMinGap    time.Duration
	resetOnIdle           time.Duration

	// When true, append [ctx: ~N%] (or model self-report) to assistant replies shown on platforms.
	showContextIndicator bool
	replyFooterEnabled   bool

	// Multi-workspace mode
	multiWorkspace    bool
	baseDir           string
	workspaceBindings *WorkspaceBindingManager
	workspacePool     *workspacePool
	initFlows         map[string]*workspaceInitFlow // workspace channel key → init state
	initFlowsMu       sync.Mutex

	// Terminal observation (--observe)
	observeEnabled    bool
	observeProjectDir string // ~/.claude/projects/{projectKey}
	observeSessionKey string // e.g. "slack:C123:U456" — target for forwarding
	observeCancel     context.CancelFunc

	// Interactive agent session management
	interactiveMu     sync.Mutex
	interactiveStates map[string]*interactiveState // key = sessionKey

	platformLifecycleMu sync.Mutex
	platformReady       map[Platform]bool
	stopping            bool
	replyFooterMu       sync.Mutex
	replyFooterUsage    replyFooterUsageCache

	// /web command callbacks
	webSetupFunc  func() (port int, token string, needRestart bool, err error)
	webStatusFunc func() (url string)
}

// workspaceInitFlow tracks a channel that is being onboarded to a workspace.
type workspaceInitFlow struct {
	state       string // "awaiting_url", "awaiting_confirm"
	repoURL     string
	cloneTo     string
	channelName string
}

// queuedMessage holds a message that arrived while the session was busy.
// The message is NOT sent to agent stdin at queue time; the event loop
// sends it after the current turn completes to avoid mid-turn interference.
type queuedMessage struct {
	platform      Platform
	replyCtx      any
	content       string
	images        []ImageAttachment
	files         []FileAttachment
	fromVoice     bool
	userID        string
	userName      string // sender's display name for sender injection
	msgPlatform   string // platform name for sender injection
	msgSessionKey string // session key for extracting chat ID
}

// interactiveState tracks a running interactive agent session and its permission state.
type interactiveState struct {
	agentSession           AgentSession
	platform               Platform
	replyCtx               any
	workspaceDir           string
	agent                  Agent
	mu                     sync.Mutex
	stopCh                 chan struct{}
	stopped                bool
	pending                *pendingPermission
	pendingMessages        []queuedMessage // messages queued while session was busy
	approveAll             bool            // when true, auto-approve all permission requests for this session
	fromVoice              bool            // true if current turn originated from voice transcription
	sideText               string
	deleteMode             *deleteModeState
	modelSwitch            *modelSwitchState
	lastAutoCompressAt     time.Time
	lastAutoCompressTokens int
}

type deleteModeState struct {
	page        int
	selectedIDs map[string]struct{}
	phase       string
	hint        string
	result      string
}

type modelSwitchState struct {
	phase  string
	target string
	result string
}

// pendingPermission represents a permission request waiting for user response.
type pendingPermission struct {
	RequestID       string
	ToolName        string
	ToolInput       map[string]any
	InputPreview    string
	Questions       []UserQuestion // non-nil for AskUserQuestion
	Answers         map[int]string // collected answers keyed by question index
	CurrentQuestion int            // index of the question currently being asked
	Resolved        chan struct{}  // closed when user responds
	resolveOnce     sync.Once
}

func (s *interactiveState) stopSignal() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopCh == nil {
		s.stopCh = make(chan struct{})
		if s.stopped {
			close(s.stopCh)
		}
	}
	return s.stopCh
}

func (s *interactiveState) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

func (s *interactiveState) markStopped() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	if s.stopCh == nil {
		s.stopCh = make(chan struct{})
	}
	close(s.stopCh)
}

// resolve safely closes the Resolved channel exactly once.
func (pp *pendingPermission) resolve() {
	pp.resolveOnce.Do(func() { close(pp.Resolved) })
}

func NewEngine(name string, ag Agent, platforms []Platform, sessionStorePath string, lang Language) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		name:                  name,
		agent:                 ag,
		platforms:             platforms,
		sessions:              NewSessionManager(sessionStorePath),
		ctx:                   ctx,
		cancel:                cancel,
		i18n:                  NewI18n(lang),
		attachmentSendEnabled: true,
		display:               DisplayCfg{Mode: "full", ThinkingMessages: true, ThinkingMaxLen: defaultThinkingMaxLen, ToolMaxLen: defaultToolMaxLen, ToolMessages: true},
		commands:              NewCommandRegistry(),
		skills:                NewSkillRegistry(),
		aliases:               make(map[string]string),
		interactiveStates:     make(map[string]*interactiveState),
		platformReady:         make(map[Platform]bool),
		startedAt:             time.Now(),
		streamPreview:         DefaultStreamPreviewCfg(),
		references:            DefaultReferenceRenderCfg(),
		eventIdleTimeout:      defaultEventIdleTimeout,
		showContextIndicator:  true,
	}

	if ag != nil {
		e.sessions.InvalidateForAgent(ag.Name())
	}

	if cp, ok := ag.(CommandProvider); ok {
		e.commands.SetAgentDirs(cp.CommandDirs())
	}
	if sp, ok := ag.(SkillProvider); ok {
		e.skills.SetDirs(sp.SkillDirs())
	}

	return e
}

// SetMultiWorkspace enables multi-workspace mode for the engine.
func (e *Engine) SetMultiWorkspace(baseDir, bindingStorePath string) {
	e.multiWorkspace = true
	e.baseDir = baseDir
	e.workspaceBindings = NewWorkspaceBindingManager(bindingStorePath)
	e.workspacePool = newWorkspacePool(15 * time.Minute)
	e.initFlows = make(map[string]*workspaceInitFlow)
	go e.runIdleReaper()
}

func (e *Engine) runIdleReaper() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.reapIdleWorkspaces()
		}
	}
}

func (e *Engine) reapIdleWorkspaces() {
	if e.workspacePool == nil {
		return
	}

	reaped := e.workspacePool.ReapIdle()
	if len(reaped) == 0 {
		return
	}

	reapedSet := make(map[string]struct{}, len(reaped))
	for _, ws := range reaped {
		reapedSet[ws] = struct{}{}
	}

	type cleanupTarget struct {
		key   string
		state *interactiveState
	}

	var targets []cleanupTarget
	e.interactiveMu.Lock()
	for key, state := range e.interactiveStates {
		if _, ok := reapedSet[state.workspaceDir]; ok {
			targets = append(targets, cleanupTarget{key: key, state: state})
		}
	}
	e.interactiveMu.Unlock()

	for _, target := range targets {
		e.cleanupInteractiveState(target.key, target.state)
	}
	for _, ws := range reaped {
		slog.Info("workspace idle-reaped", "workspace", ws)
	}
}

// SetSpeechConfig configures the speech-to-text subsystem.
func (e *Engine) SetSpeechConfig(cfg SpeechCfg) {
	e.speech = cfg
}

// SetTTSConfig configures the text-to-speech subsystem.
func (e *Engine) SetTTSConfig(cfg *TTSCfg) {
	e.tts = cfg
}

// SetTTSSaveFunc registers a callback that persists TTS mode changes.
func (e *Engine) SetTTSSaveFunc(fn func(mode string) error) {
	e.ttsSaveFunc = fn
}

// SetDisplayConfig overrides the default truncation settings.
func (e *Engine) SetDisplayConfig(cfg DisplayCfg) {
	e.display = cfg
}

// SetReferenceConfig configures local reference normalization/rendering.
func (e *Engine) SetReferenceConfig(cfg ReferenceRenderCfg) {
	e.references = normalizeReferenceRenderCfg(cfg)
}

// estimateTokens provides a rough token estimate for a set of history entries.
func estimateTokens(entries []HistoryEntry) int {
	return estimateTokensWithPendingAssistant(entries, "")
}

// estimateTokensWithPendingAssistant is like estimateTokens but includes an assistant
// message not yet written to history (used at EventResult before AddHistory).
func estimateTokensWithPendingAssistant(entries []HistoryEntry, pendingAssistant string) int {
	// Heuristic: ~1 token per 4 characters in mixed English/Chinese.
	count := 0
	for _, h := range entries {
		count += len([]rune(h.Content))
	}
	if pendingAssistant != "" {
		count += len([]rune(pendingAssistant))
	}
	if count == 0 {
		return 0
	}
	return (count + 3) / 4
}

// SetAutoCompressConfig configures automatic context compression.
func (e *Engine) SetAutoCompressConfig(enabled bool, maxTokens int, minGap time.Duration) {
	e.autoCompressEnabled = enabled
	e.autoCompressMaxTokens = maxTokens
	if minGap <= 0 {
		minGap = 30 * time.Minute
	}
	e.autoCompressMinGap = minGap
}

// SetResetOnIdle configures automatic session rotation after prolonged inactivity.
// A zero or negative duration disables the behavior.
func (e *Engine) SetResetOnIdle(d time.Duration) {
	if d <= 0 {
		e.resetOnIdle = 0
		return
	}
	e.resetOnIdle = d
}

// SetShowContextIndicator controls whether assistant replies include the [ctx: ~N%] suffix.
func (e *Engine) SetShowContextIndicator(show bool) {
	e.showContextIndicator = show
}

// SetReplyFooterEnabled controls whether assistant replies include a Codex-like
// footer line with model / reasoning / usage / workdir metadata when available.
func (e *Engine) SetReplyFooterEnabled(show bool) {
	e.replyFooterEnabled = show
}

func (e *Engine) SetWebSetupFunc(fn func() (int, string, bool, error)) { e.webSetupFunc = fn }
func (e *Engine) SetWebStatusFunc(fn func() string)                    { e.webStatusFunc = fn }

// SetInjectSender controls whether sender identity (platform and user ID) is
// prepended to each message before forwarding it to the agent. When enabled,
// the agent receives a preamble line like:
//
//	[cc-connect sender_id=ou_abc123 platform=feishu]
//
// This allows the agent to identify who sent the message and adjust behavior
// accordingly (e.g. personal task views, role-based access control).
func (e *Engine) SetInjectSender(v bool) {
	e.injectSender = v
}

// SetAttachmentSendEnabled controls whether side-channel image/file delivery is allowed.
func (e *Engine) SetAttachmentSendEnabled(v bool) {
	e.attachmentSendEnabled = v
}

// SetObserveConfig enables terminal session observation.
// projectDir is the Claude Code project directory containing session JSONL files.
// sessionKey identifies the Slack channel to forward messages to.
func (e *Engine) SetObserveConfig(projectDir, sessionKey string) {
	e.observeEnabled = true
	e.observeProjectDir = projectDir
	e.observeSessionKey = sessionKey
}

func (e *Engine) SetLanguageSaveFunc(fn func(Language) error) {
	e.i18n.SetSaveFunc(fn)
}

// findObserverTarget returns the first platform that implements ObserverTarget,
// or nil if none do.
func (e *Engine) findObserverTarget() ObserverTarget {
	for _, p := range e.platforms {
		if ot, ok := p.(ObserverTarget); ok {
			return ot
		}
	}
	return nil
}

func (e *Engine) SetProviderSaveFunc(fn func(providerName string) error) {
	e.providerSaveFunc = fn
}

func (e *Engine) SetProviderAddSaveFunc(fn func(ProviderConfig) error) {
	e.providerAddSaveFunc = fn
}

func (e *Engine) SetProviderRemoveSaveFunc(fn func(string) error) {
	e.providerRemoveSaveFunc = fn
}

func (e *Engine) SetProviderModelSaveFunc(fn func(providerName, model string) error) {
	e.providerModelSaveFunc = fn
}

func (e *Engine) SetModelSaveFunc(fn func(model string) error) {
	e.modelSaveFunc = fn
}

// AddPlatform appends a platform to the engine after construction.
// The platform is started and wired during the next Engine.Start call,
// or if the engine is already running, it is started immediately.
func (e *Engine) AddPlatform(p Platform) {
	e.platforms = append(e.platforms, p)
}

func (e *Engine) SetCronScheduler(cs *CronScheduler) {
	e.cronScheduler = cs
}

func (e *Engine) SetHeartbeatScheduler(hs *HeartbeatScheduler) {
	e.heartbeatScheduler = hs
}

func (e *Engine) SetCommandSaveAddFunc(fn func(name, description, prompt, exec, workDir string) error) {
	e.commandSaveAddFunc = fn
}

func (e *Engine) SetCommandSaveDelFunc(fn func(name string) error) {
	e.commandSaveDelFunc = fn
}

func (e *Engine) SetDisplaySaveFunc(fn func(mode *string, thinkingMessages *bool, thinkingMaxLen, toolMaxLen *int, toolMessages *bool) error) {
	e.displaySaveFunc = fn
}

// ConfigReloadResult describes what was updated by a config reload.
type ConfigReloadResult struct {
	DisplayUpdated   bool
	ProvidersUpdated int
	CommandsUpdated  int
}

func (e *Engine) SetConfigReloadFunc(fn func() (*ConfigReloadResult, error)) {
	e.configReloadFunc = fn
}

// GetAgent returns the engine's agent (for type assertions like ProviderSwitcher).
func (e *Engine) GetAgent() Agent {
	return e.agent
}

// AddCommand registers a custom slash command.
func (e *Engine) AddCommand(name, description, prompt, exec, workDir, source string) {
	e.commands.Add(name, description, prompt, exec, workDir, source)
}

// ClearCommands removes all commands from the given source.
func (e *Engine) ClearCommands(source string) {
	e.commands.ClearSource(source)
}

// AddAlias registers a command alias.
func (e *Engine) AddAlias(name, command string) {
	e.aliasMu.Lock()
	defer e.aliasMu.Unlock()
	e.aliases[name] = command
}

func (e *Engine) SetAliasSaveAddFunc(fn func(name, command string) error) {
	e.aliasSaveAddFunc = fn
}

func (e *Engine) SetAliasSaveDelFunc(fn func(name string) error) {
	e.aliasSaveDelFunc = fn
}

// ClearAliases removes all aliases (for config reload).
func (e *Engine) ClearAliases() {
	e.aliasMu.Lock()
	defer e.aliasMu.Unlock()
	e.aliases = make(map[string]string)
}

// resolveDisabledCmds resolves a list of command names (including "*" wildcard)
// to a set of canonical command IDs.
func resolveDisabledCmds(cmds []string) map[string]bool {
	m := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		c = strings.ToLower(strings.TrimPrefix(c, "/"))
		if c == "*" {
			for _, bc := range builtinCommands {
				m[bc.id] = true
			}
			return m
		}
		if id := matchPrefix(c, builtinCommands); id != "" {
			m[id] = true
		} else {
			m[c] = true
		}
	}
	return m
}

// GetDisabledCommands returns the list of disabled command IDs for this project.
func (e *Engine) GetDisabledCommands() []string {
	e.userRolesMu.RLock()
	defer e.userRolesMu.RUnlock()
	out := make([]string, 0, len(e.disabledCmds))
	for k := range e.disabledCmds {
		out = append(out, k)
	}
	return out
}

// SetDisabledCommands sets the list of command IDs that are disabled for this project.
func (e *Engine) SetDisabledCommands(cmds []string) {
	e.userRolesMu.Lock()
	defer e.userRolesMu.Unlock()
	e.disabledCmds = resolveDisabledCmds(cmds)
}

// SetUserRoles configures per-user role-based policies. Pass nil to disable.
func (e *Engine) SetUserRoles(urm *UserRoleManager) {
	e.userRolesMu.Lock()
	defer e.userRolesMu.Unlock()
	if e.userRoles != nil {
		e.userRoles.Stop()
	}
	e.userRoles = urm
}

// SetAdminFrom sets the admin allowlist for privileged commands.
// "*" means all users who pass allow_from are admins.
// Empty string means privileged commands are denied for everyone.
func (e *Engine) SetAdminFrom(adminFrom string) {
	e.userRolesMu.Lock()
	e.adminFrom = strings.TrimSpace(adminFrom)
	af := e.adminFrom
	shellDisabled := e.disabledCmds["shell"]
	e.userRolesMu.Unlock()
	if af == "" && !shellDisabled {
		slog.Warn("admin_from is not set — privileged commands (/shell, /show, /dir, /restart, /upgrade) are blocked. "+
			"Set admin_from in config to enable them, or use disabled_commands to hide them.",
			"project", e.name)
	}
}

// privilegedCommands are commands that require admin_from authorization.
var privilegedCommands = map[string]bool{
	"shell":   true,
	"show":    true,
	"dir":     true,
	"restart": true,
	"upgrade": true,
	"web":     true,
	"diff":    true,
}

// isAdmin checks whether the given user ID is authorized for privileged commands.
// Unlike AllowList, empty adminFrom means deny-all (fail-closed).
func (e *Engine) isAdmin(userID string) bool {
	e.userRolesMu.RLock()
	af := e.adminFrom
	e.userRolesMu.RUnlock()
	if af == "" {
		return false
	}
	if af == "*" {
		return true
	}
	for _, id := range strings.Split(af, ",") {
		if strings.EqualFold(strings.TrimSpace(id), userID) {
			return true
		}
	}
	return false
}

// SetBannedWords replaces the banned words list.
func (e *Engine) SetBannedWords(words []string) {
	e.bannedMu.Lock()
	defer e.bannedMu.Unlock()
	lower := make([]string, len(words))
	for i, w := range words {
		lower[i] = strings.ToLower(w)
	}
	e.bannedWords = lower
}

// SetRateLimitCfg configures per-session message rate limiting.
// It stops the previous rate limiter's background goroutine before replacing it.
func (e *Engine) SetRateLimitCfg(cfg RateLimitCfg) {
	if e.rateLimiter != nil {
		e.rateLimiter.Stop()
	}
	e.rateLimiter = NewRateLimiter(cfg.MaxMessages, cfg.Window)
}

// SetOutgoingRateLimitCfg configures per-platform outgoing message throttling.
func (e *Engine) SetOutgoingRateLimitCfg(defaults OutgoingRateLimitCfg, overrides map[string]OutgoingRateLimitCfg) {
	e.outgoingRL = NewOutgoingRateLimiter(defaults, overrides)
}

// checkRateLimit returns true if the message is allowed, false if rate-limited.
// It checks per-user role-based limits first, then falls back to the global limiter.
func (e *Engine) checkRateLimit(msg *Message) bool {
	e.userRolesMu.RLock()
	urm := e.userRoles
	e.userRolesMu.RUnlock()

	// Try role-specific rate limit first
	if urm != nil {
		// Use userID if available, else fall back to sessionKey for unidentified users.
		// NOTE: sessionKey fallback means anonymous users get separate buckets per
		// session, which is less strict than per-user limiting. Platforms should
		// provide UserID for effective rate limiting.
		rateKey := msg.UserID
		if rateKey == "" {
			rateKey = msg.SessionKey
			slog.Debug("rate limit: no UserID, falling back to sessionKey", "session_key", msg.SessionKey)
		}
		allowed, handled := urm.AllowRate(rateKey)
		if handled {
			return allowed
		}
		// Role has no rate_limit config — fall through to global, keyed by user
	}
	// Global rate limiter
	if e.rateLimiter == nil {
		return true
	}
	// When users config active: key by userID (per-user); otherwise sessionKey (legacy)
	key := msg.SessionKey
	if urm != nil && msg.UserID != "" {
		key = msg.UserID
	}
	return e.rateLimiter.Allow(key)
}

// SetStreamPreviewCfg configures the streaming preview behavior.
func (e *Engine) SetStreamPreviewCfg(cfg StreamPreviewCfg) {
	e.streamPreview = cfg
}

// SetEventIdleTimeout sets the maximum time to wait between consecutive agent events.
// 0 disables the timeout entirely.
func (e *Engine) SetEventIdleTimeout(d time.Duration) {
	e.eventIdleTimeout = d
}

func (e *Engine) SetRelayManager(rm *RelayManager) {
	e.relayManager = rm
}

func (e *Engine) RelayManager() *RelayManager {
	return e.relayManager
}

func (e *Engine) SetDirHistory(dh *DirHistory) {
	e.dirHistory = dh
}

func (e *Engine) SetBaseWorkDir(dir string) {
	e.baseWorkDir = dir
}

func (e *Engine) SetProjectStateStore(store *ProjectStateStore) {
	e.projectState = store
}

// RemoveCommand removes a custom command by name. Returns false if not found.
func (e *Engine) RemoveCommand(name string) bool {
	return e.commands.Remove(name)
}

func (e *Engine) ProjectName() string {
	return e.name
}

// ActiveSessionKeys returns the session keys of all active interactive sessions.
func (e *Engine) ActiveSessionKeys() []string {
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()
	var keys []string
	for key, state := range e.interactiveStates {
		if state.platform != nil {
			keys = append(keys, key)
		}
	}
	return keys
}

// ExecuteCronJob runs a cron job by injecting a synthetic message into the engine.
// It finds the platform that owns the session key, reconstructs a reply context,
// and processes the message as if the user sent it.
func (e *Engine) ExecuteCronJob(job *CronJob) error {
	sessionKey := job.SessionKey
	platformName := ""
	if idx := strings.Index(sessionKey, ":"); idx > 0 {
		platformName = sessionKey[:idx]
	}

	var targetPlatform Platform
	for _, p := range e.platforms {
		if p.Name() == platformName {
			targetPlatform = p
			break
		}
	}
	// Fallback: in multi-workspace mode the stored session key may be prefixed
	// with the workspace path (e.g. "/home/user/project:slack:C123:U456").
	// Search for a known platform name within the key and strip the prefix.
	if targetPlatform == nil {
		for _, p := range e.platforms {
			needle := ":" + p.Name() + ":"
			if idx := strings.Index(sessionKey, needle); idx >= 0 {
				targetPlatform = p
				platformName = p.Name()
				sessionKey = sessionKey[idx+1:] // strip workspace prefix
				break
			}
		}
	}
	if targetPlatform == nil {
		return fmt.Errorf("platform %q not found for session %q", platformName, sessionKey)
	}

	rc, ok := targetPlatform.(ReplyContextReconstructor)
	if !ok {
		return fmt.Errorf("platform %q does not support proactive messaging (cron)", platformName)
	}

	runSessionKey := sessionKey
	var replyCtx any
	var err error
	if !job.Mute {
		if resolver, ok := targetPlatform.(CronReplyTargetResolver); ok {
			resolvedSessionKey, resolvedReplyCtx, err := resolver.ResolveCronReplyTarget(sessionKey, cronRunTitle(job))
			if err != nil {
				if !errors.Is(err, ErrNotSupported) {
					return fmt.Errorf("resolve cron reply target: %w", err)
				}
			} else {
				if resolvedSessionKey != "" {
					runSessionKey = resolvedSessionKey
				}
				if resolvedReplyCtx != nil {
					replyCtx = resolvedReplyCtx
				}
			}
		}
	}
	if replyCtx == nil {
		replyCtx, err = rc.ReconstructReplyCtx(runSessionKey)
		if err != nil {
			return fmt.Errorf("reconstruct reply context: %w", err)
		}
	}

	// Wrap platform to discard all outgoing messages when muted
	effectivePlatform := targetPlatform
	if job.Mute {
		effectivePlatform = &mutePlatform{targetPlatform}
	}

	// Notify user that a cron job is executing (unless silent/muted)
	if !job.Mute {
		silent := false
		if e.cronScheduler != nil {
			silent = e.cronScheduler.IsSilent(job)
		}
		if !silent {
			desc := job.Description
			if desc == "" {
				if job.IsShellJob() {
					desc = truncateStr(job.Exec, 40)
				} else {
					desc = truncateStr(job.Prompt, 40)
				}
			}
			e.send(targetPlatform, replyCtx, fmt.Sprintf("⏰ %s", desc))
		}
	}

	if job.IsShellJob() {
		return e.executeCronShell(effectivePlatform, replyCtx, job)
	}

	msg := &Message{
		SessionKey:   sessionKey,
		Platform:     platformName,
		UserID:       "cron",
		UserName:     "cron",
		Content:      job.Prompt,
		ReplyCtx:     replyCtx,
		ModeOverride: job.Mode,
	}

	// Resolve workspace-specific agent and sessions for multi-workspace mode.
	// Priority: job.WorkDir (explicit) > workspace binding > global agent fallback.
	agent := e.agent
	sessions := e.sessions
	workspaceDir := ""

	if e.multiWorkspace {
		channelID := extractChannelID(sessionKey)
		if channelID != "" {
			workspace, _, err := e.resolveWorkspace(targetPlatform, channelID)
			if err == nil && workspace != "" {
				wsAgent, wsSessions, _, effectiveDir, err := e.workspaceContext(workspace, sessionKey)
				if err == nil {
					agent = wsAgent
					sessions = wsSessions
					workspaceDir = effectiveDir
				}
			}
		}
	}

	if job.WorkDir != "" {
		wsAgent, wsSessions, err := e.getOrCreateWorkspaceAgent(job.WorkDir)
		if err == nil {
			agent = wsAgent
			sessions = wsSessions
			workspaceDir = job.WorkDir
		} else {
			slog.Warn("cron: workspace agent creation failed, using global",
				"work_dir", job.WorkDir, "session_key", sessionKey, "error", err)
		}
	}

	useNewSession := false
	if e.cronScheduler != nil {
		useNewSession = e.cronScheduler.UsesNewSession(job)
	} else {
		useNewSession = job.UsesNewSessionPerRun()
	}

	if useNewSession {
		msg.SessionKey = runSessionKey
		session := sessions.NewSideSession(runSessionKey, "cron-"+job.ID)
		if !session.TryLock() {
			return fmt.Errorf("session %q is busy", runSessionKey)
		}
		iKey := fmt.Sprintf("%s#cron:%s", runSessionKey, session.ID)
		if workspaceDir != "" {
			iKey = workspaceDir + ":" + iKey
		}
		e.processInteractiveMessageWith(effectivePlatform, msg, session, agent, sessions, iKey, workspaceDir, runSessionKey)
		e.cleanupInteractiveState(iKey)
		return nil
	}

	session := sessions.GetOrCreateActive(sessionKey)
	if !session.TryLock() {
		return fmt.Errorf("session %q is busy", sessionKey)
	}

	iKey := sessionKey
	if workspaceDir != "" {
		iKey = workspaceDir + ":" + sessionKey
	}
	e.processInteractiveMessageWith(effectivePlatform, msg, session, agent, sessions, iKey, workspaceDir, sessionKey)
	return nil
}

func cronRunTitle(job *CronJob) string {
	if job == nil {
		return "cron"
	}
	if desc := strings.TrimSpace(job.Description); desc != "" {
		return truncateStr(desc, 60)
	}
	if job.IsShellJob() {
		if cmd := strings.TrimSpace(job.Exec); cmd != "" {
			return truncateStr(cmd, 60)
		}
		return "cron"
	}
	if prompt := strings.TrimSpace(job.Prompt); prompt != "" {
		return truncateStr(prompt, 60)
	}
	return "cron"
}

// executeCronShell runs a shell command for a cron job and sends the output.
func (e *Engine) executeCronShell(p Platform, replyCtx any, job *CronJob) error {
	workDir := job.WorkDir
	if workDir == "" {
		if wd, ok := e.agent.(interface{ GetWorkDir() string }); ok {
			workDir = wd.GetWorkDir()
		}
	}
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	timeout := job.ExecutionTimeout()
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(e.ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(e.ctx)
	}
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", job.Exec)
	cmd.Dir = workDir
	output, err := cmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		e.send(p, replyCtx, fmt.Sprintf("⏰ ⚠️ timeout: `%s`", truncateStr(job.Exec, 60)))
		return fmt.Errorf("shell command timed out")
	}

	result := strings.TrimSpace(string(output))
	if err != nil {
		if result != "" {
			e.send(p, replyCtx, fmt.Sprintf("⏰ ❌ `%s`\n\n%s\n\nerror: %v", truncateStr(job.Exec, 60), truncateStr(result, 3000), err))
		} else {
			e.send(p, replyCtx, fmt.Sprintf("⏰ ❌ `%s`\nerror: %v", truncateStr(job.Exec, 60), err))
		}
		return fmt.Errorf("shell: %w", err)
	}

	if result == "" {
		result = "(no output)"
	}
	e.send(p, replyCtx, fmt.Sprintf("⏰ ✅ `%s`\n\n%s", truncateStr(job.Exec, 60), truncateStr(result, 3000)))
	return nil
}

// ExecuteHeartbeat runs a heartbeat check by injecting a synthetic message
// into the main session, similar to cron but designed for periodic awareness.
func (e *Engine) ExecuteHeartbeat(sessionKey, prompt string, silent bool) error {
	platformName := ""
	if idx := strings.Index(sessionKey, ":"); idx > 0 {
		platformName = sessionKey[:idx]
	}

	var targetPlatform Platform
	for _, p := range e.platforms {
		if p.Name() == platformName {
			targetPlatform = p
			break
		}
	}
	// Fallback: in multi-workspace mode the stored session key may be prefixed
	// with the workspace path (e.g. "/home/user/project:slack:C123:U456").
	// Search for a known platform name within the key and strip the prefix.
	if targetPlatform == nil {
		for _, p := range e.platforms {
			needle := ":" + p.Name() + ":"
			if idx := strings.Index(sessionKey, needle); idx >= 0 {
				targetPlatform = p
				platformName = p.Name()
				sessionKey = sessionKey[idx+1:] // strip workspace prefix
				break
			}
		}
	}
	if targetPlatform == nil {
		return fmt.Errorf("platform %q not found for session %q", platformName, sessionKey)
	}

	rc, ok := targetPlatform.(ReplyContextReconstructor)
	if !ok {
		return fmt.Errorf("platform %q does not support proactive messaging (heartbeat)", platformName)
	}

	replyCtx, err := rc.ReconstructReplyCtx(sessionKey)
	if err != nil {
		return fmt.Errorf("reconstruct reply context: %w", err)
	}

	if !silent {
		e.send(targetPlatform, replyCtx, "💓 heartbeat")
	}

	msg := &Message{
		SessionKey: sessionKey,
		Platform:   platformName,
		UserID:     "heartbeat",
		UserName:   "heartbeat",
		Content:    prompt,
		ReplyCtx:   replyCtx,
	}

	session := e.sessions.GetOrCreateActive(sessionKey)
	if !session.TryLock() {
		return fmt.Errorf("session %q is busy", sessionKey)
	}

	e.processInteractiveMessage(targetPlatform, msg, session)
	return nil
}

func (e *Engine) Start() error {
	var startErrs []error
	readyCount := 0
	pendingCount := 0
	for _, p := range e.platforms {
		_, isAsync := p.(AsyncRecoverablePlatform)
		if async, ok := p.(AsyncRecoverablePlatform); ok {
			async.SetLifecycleHandler(e)
		}
		if err := p.Start(e.handleMessage); err != nil {
			slog.Warn("platform start failed", "project", e.name, "platform", p.Name(), "error", err)
			startErrs = append(startErrs, fmt.Errorf("[%s] start platform %s: %w", e.name, p.Name(), err))
			continue
		}
		if isAsync {
			pendingCount++
			slog.Info("platform recovery loop started", "project", e.name, "platform", p.Name())
			continue
		}
		e.onPlatformReady(p)
		readyCount++
	}

	// Log summary
	if len(startErrs) > 0 || pendingCount > 0 {
		slog.Warn("engine started with partial readiness",
			"project", e.name,
			"agent", e.agent.Name(),
			"ready", readyCount,
			"pending", pendingCount,
			"failed", len(startErrs))
	} else {
		slog.Info("engine started", "project", e.name, "agent", e.agent.Name(), "platforms", len(e.platforms))
	}

	// Only return error if ALL platforms failed
	if len(startErrs) == len(e.platforms) && len(e.platforms) > 0 {
		return startErrs[0] // Return first error
	}

	e.startObserver()
	return nil
}

func (e *Engine) Stop() error {
	e.platformLifecycleMu.Lock()
	e.stopping = true
	e.platformLifecycleMu.Unlock()

	// Cancel first so late lifecycle callbacks observe shutdown immediately.
	e.cancel()

	if e.observeCancel != nil {
		e.observeCancel()
	}

	// Stop platforms after cancellation so they can unwind against the closed context.
	var errs []error
	for _, p := range e.platforms {
		if err := p.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("stop platform %s: %w", p.Name(), err))
		}
	}

	e.interactiveMu.Lock()
	states := make(map[string]*interactiveState, len(e.interactiveStates))
	for k, v := range e.interactiveStates {
		states[k] = v
		delete(e.interactiveStates, k)
	}
	e.interactiveMu.Unlock()

	for key, state := range states {
		if state.agentSession != nil {
			slog.Debug("engine.Stop: closing agent session", "session", key)
			state.agentSession.Close()
		}
	}

	if e.rateLimiter != nil {
		e.rateLimiter.Stop()
	}
	e.userRolesMu.Lock()
	if e.userRoles != nil {
		e.userRoles.Stop()
	}
	e.userRolesMu.Unlock()

	if err := e.agent.Stop(); err != nil {
		errs = append(errs, fmt.Errorf("stop agent %s: %w", e.agent.Name(), err))
	}
	if len(errs) > 0 {
		return fmt.Errorf("engine stop errors: %v", errs)
	}
	return nil
}

// OnPlatformReady marks an async platform as ready and initializes platform-level
// capabilities once per ready cycle.
func (e *Engine) OnPlatformReady(p Platform) {
	e.onPlatformReady(p)
}

// OnPlatformUnavailable marks an async platform as unavailable.
func (e *Engine) OnPlatformUnavailable(p Platform, err error) {
	if !e.markPlatformUnavailable(p) {
		return
	}
	slog.Warn("platform unavailable", "project", e.name, "platform", p.Name(), "error", err)
}

// ReceiveMessage delivers a message from a platform to the engine.
// This is a public wrapper for use in integration tests and external callers.
func (e *Engine) ReceiveMessage(p Platform, msg *Message) {
	e.handleMessage(p, msg)
}

func (e *Engine) onPlatformReady(p Platform) {
	if !e.markPlatformReady(p) {
		return
	}
	slog.Info("platform ready", "project", e.name, "platform", p.Name())
	e.initPlatformCapabilities(p)
}

func (e *Engine) markPlatformReady(p Platform) bool {
	e.platformLifecycleMu.Lock()
	defer e.platformLifecycleMu.Unlock()

	if e.stopping || e.ctx.Err() != nil {
		return false
	}
	if e.platformReady[p] {
		return false
	}
	e.platformReady[p] = true
	return true
}

func (e *Engine) markPlatformUnavailable(p Platform) bool {
	e.platformLifecycleMu.Lock()
	defer e.platformLifecycleMu.Unlock()

	if e.stopping || e.ctx.Err() != nil {
		return false
	}
	if !e.platformReady[p] {
		return false
	}
	e.platformReady[p] = false
	return true
}

func (e *Engine) initPlatformCapabilities(p Platform) {
	if registrar, ok := p.(CommandRegistrar); ok {
		commands, skillsOmitted := e.menuCommandsForPlatform(p.Name())
		if skillsOmitted && strings.EqualFold(p.Name(), "telegram") {
			slog.Info("telegram: omitting skill commands from menu due to command limit", "project", e.name)
		}
		if err := registrar.RegisterCommands(commands); err != nil {
			slog.Error("platform command registration failed", "project", e.name, "platform", p.Name(), "error", err)
		} else {
			slog.Debug("platform commands registered", "project", e.name, "platform", p.Name(), "count", len(commands))
		}
	}

	if nav, ok := p.(CardNavigable); ok {
		nav.SetCardNavigationHandler(e.handleCardNav)
	}
}

// matchBannedWord returns the first banned word found in content, or "".
func (e *Engine) matchBannedWord(content string) string {
	e.bannedMu.RLock()
	defer e.bannedMu.RUnlock()
	if len(e.bannedWords) == 0 {
		return ""
	}
	lower := strings.ToLower(content)
	for _, w := range e.bannedWords {
		if strings.Contains(lower, w) {
			return w
		}
	}
	return ""
}

// resolveAlias checks if the content (or its first word) matches an alias and replaces it.
func (e *Engine) resolveAlias(content string) string {
	e.aliasMu.RLock()
	defer e.aliasMu.RUnlock()

	if len(e.aliases) == 0 {
		return content
	}

	// Exact match on full content
	if cmd, ok := e.aliases[content]; ok {
		return cmd
	}

	// Match first word, append remaining args
	parts := strings.SplitN(content, " ", 2)
	if cmd, ok := e.aliases[parts[0]]; ok {
		if len(parts) > 1 {
			return cmd + " " + parts[1]
		}
		return cmd
	}
	return content
}

func (e *Engine) handleMessage(p Platform, msg *Message) {
	slog.Info("message received",
		"platform", msg.Platform, "msg_id", msg.MessageID,
		"session", msg.SessionKey, "user", msg.UserName,
		"content_len", len(msg.Content),
		"has_images", len(msg.Images) > 0, "has_audio", msg.Audio != nil, "has_files", len(msg.Files) > 0,
	)

	// Voice message: transcribe to text first
	if msg.Audio != nil {
		// If STT is configured, use it for transcription (more accurate)
		if e.speech.Enabled && e.speech.STT != nil {
			e.handleVoiceMessage(p, msg)
			return
		}
		// Fallback: use platform-provided recognition text if available
		if msg.Content == "" {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgVoiceNotEnabled))
			return
		}
		// Use platform recognition with a hint, then continue processing
		slog.Info("using platform-provided voice recognition",
			"platform", msg.Platform, "content_len", len(msg.Content))
		if msg.FromVoice {
			// Use platform name as parameter for the message
			// Capitalize first letter for better presentation
			if platformName := msg.Platform; len(platformName) > 0 {
				// Safe capitalization that handles multi-word names
				r := []rune(platformName)
				if len(r) > 0 {
					r[0] = []rune(strings.ToUpper(string(r[0])))[0]
				}
				platformName = string(r)
				e.send(p, msg.ReplyCtx, e.i18n.Tf(MsgVoiceUsingPlatformRecognition, platformName))
			}
		}
		// Continue processing with the platform-provided text content
	}

	content := strings.TrimSpace(msg.Content)
	if content == "" && len(msg.Images) == 0 && len(msg.Files) == 0 && msg.Location == nil {
		return
	}

	// Resolve aliases on user text BEFORE merging ExtraContent, so reply
	// quotes and platform context survive alias resolution (PR #420 fix).
	content = e.resolveAlias(content)
	if msg.ExtraContent != "" {
		if content == "" {
			msg.Content = msg.ExtraContent
		} else {
			msg.Content = msg.ExtraContent + "\n" + content
		}
	} else {
		msg.Content = content
	}

	// Rate limit check (per-user role-based, then global fallback)
	if !e.checkRateLimit(msg) {
		slog.Info("message rate limited",
			"session", msg.SessionKey, "user_id", msg.UserID, "user", msg.UserName)
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgRateLimited))
		return
	}

	// Banned words check (skip for slash commands)
	if !strings.HasPrefix(content, "/") {
		if word := e.matchBannedWord(content); word != "" {
			slog.Info("message blocked by banned word", "word", word, "user", msg.UserName)
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBannedWordBlocked))
			return
		}
	}

	// Multi-workspace resolution
	var wsAgent Agent
	var wsSessions *SessionManager
	var resolvedWorkspace string
	if e.multiWorkspace {
		channelID := effectiveChannelID(msg)
		channelKey := effectiveWorkspaceChannelKey(msg)
		workspace, channelName, err := e.resolveWorkspace(p, channelID)
		if err != nil {
			slog.Error("workspace resolution failed", "err", err)
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
			return
		}
		if workspace == "" {
			// No workspace — handle init flow (unless it's a /workspace command)
			if !strings.HasPrefix(content, "/workspace") && !strings.HasPrefix(content, "/ws ") {
				if e.handleWorkspaceInitFlow(p, msg, channelName) {
					return
				}
			} else {
				// Workspace command bypassed the init flow; clean up any stale flow
				// so it doesn't interfere if the channel becomes unbound again later.
				e.initFlowsMu.Lock()
				delete(e.initFlows, channelKey)
				e.initFlowsMu.Unlock()
			}
			// If init flow didn't consume, only workspace commands work
			if !strings.HasPrefix(content, "/") {
				return
			}
		} else {
			// Touch for idle tracking
			if ws := e.workspacePool.Get(workspace); ws != nil {
				ws.Touch()
			}

			var effectiveWorkspace string
			wsAgent, wsSessions, _, effectiveWorkspace, err = e.workspaceContext(workspace, msg.SessionKey)
			if err != nil {
				slog.Error("failed to create workspace agent", "workspace", workspace, "err", err)
				e.reply(p, msg.ReplyCtx, fmt.Sprintf("Failed to initialize workspace: %v", err))
				return
			}
			resolvedWorkspace = effectiveWorkspace
		}
	}

	if len(msg.Images) == 0 && strings.HasPrefix(content, "/") {
		if e.handleCommand(p, msg, content) {
			return
		}
		// Unrecognized slash command — fall through to agent as normal message
	}

	// Permission responses bypass the session lock
	if e.handlePendingPermission(p, msg, content) {
		return
	}

	// Select session manager and agent based on workspace mode
	sessions := e.sessions
	agent := e.agent
	interactiveKey := msg.SessionKey
	if e.multiWorkspace && wsSessions != nil {
		sessions = wsSessions
		agent = wsAgent
		interactiveKey = resolvedWorkspace + ":" + msg.SessionKey
	}

	session := sessions.GetOrCreateActive(msg.SessionKey)
	sessions.UpdateUserMeta(msg.SessionKey, msg.UserName, msg.ChatName)
	if !session.TryLock() {
		// Check for /btw — inject into the running session mid-turn
		trimmed := strings.TrimSpace(content)
		if isBtwCommand(trimmed) {
			btw := strings.TrimSpace(trimmed[len(matchBtwPrefix(trimmed)):])
			if btw != "" {
				e.interactiveMu.Lock()
				state, ok := e.interactiveStates[interactiveKey]
				e.interactiveMu.Unlock()
				if ok && state.agentSession != nil && state.agentSession.Alive() {
					if err := state.agentSession.Send(btw, nil, nil); err != nil {
						slog.Error("btw: send failed", "error", err)
						e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBtwSendFailed))
					} else {
						e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBtwSent))
					}
					return
				}
			}
		}
		// Session is busy — try to queue the message for the running turn
		// so the agent processes it immediately after the current turn ends.
		if e.queueMessageForBusySession(p, msg, interactiveKey) {
			// Race guard: the drain loop in processInteractiveMessageWith may
			// have just finished (session unlocked) between our TryLock failure
			// and the queue append. Re-try TryLock — if it succeeds, no one is
			// draining the queue so we must start a processor ourselves.
			if session.TryLock() {
				go e.drainOrphanedQueue(session, sessions, interactiveKey, agent, resolvedWorkspace)
			}
			return
		}
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPreviousProcessing))
		return
	}

	if rotated := e.maybeAutoResetSessionOnIdle(p, msg, sessions, interactiveKey, session); rotated != nil {
		session = rotated
	}

	// Ensure an interactiveState entry exists before launching the async
	// processor so messages arriving during session startup can be queued
	// instead of dropped (issue #565).
	e.ensureInteractiveStateForQueueing(interactiveKey, p, msg.ReplyCtx)

	slog.Info("processing message",
		"platform", msg.Platform,
		"user", msg.UserName,
		"session", session.ID,
	)

	go e.processInteractiveMessageWith(p, msg, session, agent, sessions, interactiveKey, resolvedWorkspace, msg.SessionKey)
}

func (e *Engine) maybeAutoResetSessionOnIdle(p Platform, msg *Message, sessions *SessionManager, interactiveKey string, session *Session) *Session {
	if e.resetOnIdle <= 0 || session == nil {
		return nil
	}

	hasBackend := session.GetAgentSessionID() != ""
	hasHistory := len(session.GetHistory(1)) > 0
	if !hasBackend && !hasHistory {
		return nil
	}

	lastActive := session.GetUpdatedAt()
	if lastActive.IsZero() || time.Since(lastActive) < e.resetOnIdle {
		return nil
	}

	slog.Info("auto-resetting idle session",
		"session_key", msg.SessionKey,
		"session_id", session.ID,
		"idle_for", time.Since(lastActive),
		"threshold", e.resetOnIdle,
	)

	// Check if the old session has an agent process that needs graceful
	// shutdown. If so, tell the user we're wrapping up before blocking.
	e.interactiveMu.Lock()
	state, hasState := e.interactiveStates[interactiveKey]
	hasAgent := hasState && state != nil && state.agentSession != nil && state.agentSession.Alive()
	e.interactiveMu.Unlock()

	if hasAgent {
		// Notify the user before the potentially long close. The close
		// returns as soon as the process exits (usually seconds), but
		// Stop hooks can take up to 120s.
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSessionClosingGraceful))
	}

	e.cleanupInteractiveState(interactiveKey)
	session.UnlockWithoutUpdate()

	newSession := sessions.NewSession(msg.SessionKey, "")
	if !newSession.TryLock() {
		slog.Error("failed to lock new session after idle auto-reset", "session_key", msg.SessionKey, "new_session", newSession.ID)
		return nil
	}

	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgSessionAutoResetIdle, int(e.resetOnIdle/time.Minute)))
	return newSession
}

// queueMessageForBusySession queues a message for later delivery when the
// session is busy. The message is NOT sent to agent stdin at queue time;
// the event loop sends it after the current turn's EventResult is received.
// Returns true if the message was successfully queued, false otherwise.
func (e *Engine) queueMessageForBusySession(p Platform, msg *Message, interactiveKey string) bool {
	e.interactiveMu.Lock()
	state, hasState := e.interactiveStates[interactiveKey]
	e.interactiveMu.Unlock()

	if !hasState || state == nil {
		return false
	}
	// Allow queueing when agentSession is nil (session is starting up,
	// issue #565). Only reject if the session was established and died.
	if state.agentSession != nil && !state.agentSession.Alive() {
		return false
	}

	// Only queue metadata — do NOT send to agent stdin yet.
	// The agent CLI may treat a mid-turn stdin message as part of the
	// current turn, causing the event loop to hang waiting for a second
	// EventResult that never arrives. Instead, the event loop sends the
	// message after the current turn's EventResult is received.
	state.mu.Lock()
	if len(state.pendingMessages) >= maxQueuedMessages {
		state.mu.Unlock()
		return false // fall back to "previous processing" reply
	}
	state.pendingMessages = append(state.pendingMessages, queuedMessage{
		platform:      p,
		replyCtx:      msg.ReplyCtx,
		content:       msg.Content,
		images:        msg.Images,
		files:         msg.Files,
		fromVoice:     msg.FromVoice,
		userID:        msg.UserID,
		userName:      msg.UserName,
		msgPlatform:   msg.Platform,
		msgSessionKey: msg.SessionKey,
	})
	queueDepth := len(state.pendingMessages)
	state.mu.Unlock()

	slog.Info("message queued for busy session",
		"session", msg.SessionKey,
		"user", msg.UserName,
		"queue_depth", queueDepth,
	)
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMessageQueued))
	return true
}

// ensureInteractiveStateForQueueing creates a placeholder interactiveState
// entry if none exists. This allows messages arriving while the agent session
// is still starting up to be queued instead of dropped (issue #565).
// The placeholder has agentSession==nil; getOrCreateInteractiveStateWith will
// replace it with a fully initialized state once the agent process is spawned.
func (e *Engine) ensureInteractiveStateForQueueing(key string, p Platform, replyCtx any) {
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()
	if _, ok := e.interactiveStates[key]; !ok {
		e.interactiveStates[key] = &interactiveState{
			platform: p,
			replyCtx: replyCtx,
		}
	}
}

// drainOrphanedQueue is called when a message was queued but the drain loop
// has already exited. It processes all pending messages in the state, similar
// to the drain loop in processInteractiveMessageWith but as a standalone
// goroutine.
func (e *Engine) drainOrphanedQueue(session *Session, sessions *SessionManager, interactiveKey string, agent Agent, workspaceDir string) {
	unlocked := false
	defer func() {
		if !unlocked {
			session.Unlock()
		}
	}()

	e.interactiveMu.Lock()
	state, hasState := e.interactiveStates[interactiveKey]
	e.interactiveMu.Unlock()

	if !hasState || state == nil || state.agentSession == nil || !state.agentSession.Alive() {
		if hasState && state != nil {
			e.notifyDroppedQueuedMessages(state, fmt.Errorf("agent session ended"))
		}
		return
	}

	unlocked = e.drainPendingMessages(state, session, sessions, interactiveKey)
}

// ──────────────────────────────────────────────────────────────
// Voice message handling
// ──────────────────────────────────────────────────────────────

func (e *Engine) handleVoiceMessage(p Platform, msg *Message) {
	if !e.speech.Enabled || e.speech.STT == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgVoiceNotEnabled))
		return
	}

	audio := msg.Audio
	if NeedsConversion(audio.Format) && !HasFFmpeg() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgVoiceNoFFmpeg))
		return
	}

	slog.Info("transcribing voice message",
		"platform", msg.Platform, "user", msg.UserName,
		"format", audio.Format, "size", len(audio.Data),
	)
	e.send(p, msg.ReplyCtx, e.i18n.T(MsgVoiceTranscribing))

	text, err := TranscribeAudio(e.ctx, e.speech.STT, audio, e.speech.Language)
	if err != nil {
		slog.Error("speech transcription failed", "error", err)
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgVoiceTranscribeFailed), err))
		return
	}

	text = strings.TrimSpace(text)
	if text == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgVoiceEmpty))
		return
	}

	slog.Info("voice transcribed", "text_len", len(text))
	e.send(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgVoiceTranscribed), text))

	// Replace audio with transcribed text and re-dispatch
	msg.Audio = nil
	msg.Content = text
	msg.FromVoice = true
	e.handleMessage(p, msg)
}

// ──────────────────────────────────────────────────────────────
// Permission handling
// ──────────────────────────────────────────────────────────────

func (e *Engine) handlePendingPermission(p Platform, msg *Message, content string) bool {
	iKey := e.interactiveKeyForSessionKey(msg.SessionKey)
	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[iKey]
	e.interactiveMu.Unlock()
	if !ok || state == nil {
		return false
	}

	state.mu.Lock()
	pending := state.pending
	state.mu.Unlock()
	if pending == nil {
		return false
	}

	// AskUserQuestion: interpret user response as an answer, not a permission decision
	if len(pending.Questions) > 0 {
		curIdx := pending.CurrentQuestion
		q := pending.Questions[curIdx]
		answer := e.resolveAskQuestionAnswer(q, content)

		if pending.Answers == nil {
			pending.Answers = make(map[int]string)
		}
		pending.Answers[curIdx] = answer

		// More questions remaining — advance to next and send new card
		if curIdx+1 < len(pending.Questions) {
			pending.CurrentQuestion = curIdx + 1
			e.reply(p, msg.ReplyCtx, fmt.Sprintf("✅ %s: **%s**", q.Question, answer))
			e.sendAskQuestionPrompt(p, msg.ReplyCtx, pending.Questions, curIdx+1)
			return true
		}

		// All questions answered — build response and resolve
		updatedInput := buildAskQuestionResponse(pending.ToolInput, pending.Questions, pending.Answers)

		if err := state.agentSession.RespondPermission(pending.RequestID, PermissionResult{
			Behavior:     "allow",
			UpdatedInput: updatedInput,
		}); err != nil {
			slog.Error("failed to send AskUserQuestion response", "error", err)
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), err))
		} else {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf("✅ %s: **%s**", q.Question, answer))
		}

		state.mu.Lock()
		state.pending = nil
		state.mu.Unlock()
		pending.resolve()
		return true
	}

	lower := strings.ToLower(strings.TrimSpace(content))

	if isApproveAllResponse(lower) {
		state.mu.Lock()
		state.approveAll = true
		state.mu.Unlock()

		if err := state.agentSession.RespondPermission(pending.RequestID, PermissionResult{
			Behavior:     "allow",
			UpdatedInput: pending.ToolInput,
		}); err != nil {
			slog.Error("failed to send permission response", "error", err)
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), err))
		} else {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPermissionApproveAll))
		}
	} else if isAllowResponse(lower) {
		if err := state.agentSession.RespondPermission(pending.RequestID, PermissionResult{
			Behavior:     "allow",
			UpdatedInput: pending.ToolInput,
		}); err != nil {
			slog.Error("failed to send permission response", "error", err)
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), err))
		} else {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPermissionAllowed))
		}
	} else if isDenyResponse(lower) {
		if err := state.agentSession.RespondPermission(pending.RequestID, PermissionResult{
			Behavior: "deny",
			Message:  "User denied this tool use.",
		}); err != nil {
			slog.Error("failed to send deny response", "error", err)
		}
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPermissionDenied))
	} else {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPermissionHint))
		return true
	}

	state.mu.Lock()
	state.pending = nil
	state.mu.Unlock()
	pending.resolve()

	return true
}

// resolveAskQuestionAnswer converts user input into answer text.
// It handles button callbacks ("askq:qIdx:optIdx"), numeric selections ("1", "1,3"), and free text.
func (e *Engine) resolveAskQuestionAnswer(q UserQuestion, input string) string {
	input = strings.TrimSpace(input)

	// Handle card button callback: "askq:qIdx:optIdx"
	if strings.HasPrefix(input, "askq:") {
		parts := strings.SplitN(input, ":", 3)
		if len(parts) == 3 {
			if idx, err := strconv.Atoi(parts[2]); err == nil && idx >= 1 && idx <= len(q.Options) {
				return q.Options[idx-1].Label
			}
		}
		// Legacy format "askq:N"
		if len(parts) == 2 {
			if idx, err := strconv.Atoi(parts[1]); err == nil && idx >= 1 && idx <= len(q.Options) {
				return q.Options[idx-1].Label
			}
		}
	}

	// Try numeric index(es)
	if q.MultiSelect {
		parts := strings.FieldsFunc(input, func(r rune) bool { return r == ',' || r == '，' || r == ' ' })
		var labels []string
		allNumeric := true
		for _, p := range parts {
			p = strings.TrimSpace(p)
			idx, err := strconv.Atoi(p)
			if err != nil || idx < 1 || idx > len(q.Options) {
				allNumeric = false
				break
			}
			labels = append(labels, q.Options[idx-1].Label)
		}
		if allNumeric && len(labels) > 0 {
			return strings.Join(labels, ", ")
		}
	} else {
		if idx, err := strconv.Atoi(input); err == nil && idx >= 1 && idx <= len(q.Options) {
			return q.Options[idx-1].Label
		}
	}

	return input
}

// buildAskQuestionResponse constructs the updatedInput for AskUserQuestion control_response.
func buildAskQuestionResponse(originalInput map[string]any, questions []UserQuestion, collected map[int]string) map[string]any {
	result := make(map[string]any)
	for k, v := range originalInput {
		result[k] = v
	}
	answers := make(map[string]any)
	for idx, ans := range collected {
		answers[strconv.Itoa(idx)] = ans
	}
	result["answers"] = answers
	return result
}

func isApproveAllResponse(s string) bool {
	for _, w := range []string{
		"allow all", "allowall", "approve all", "yes all",
		"允许所有", "允许全部", "全部允许", "所有允许", "都允许", "全部同意",
	} {
		if s == w {
			return true
		}
	}
	return false
}

func isAllowResponse(s string) bool {
	for _, w := range []string{"allow", "yes", "y", "ok", "允许", "同意", "可以", "好", "好的", "是", "确认", "approve"} {
		if s == w {
			return true
		}
	}
	return false
}

func isDenyResponse(s string) bool {
	for _, w := range []string{"deny", "no", "n", "reject", "拒绝", "不允许", "不行", "不", "否", "取消", "cancel"} {
		if s == w {
			return true
		}
	}
	return false
}

// ──────────────────────────────────────────────────────────────
// Interactive agent processing
// ──────────────────────────────────────────────────────────────

func (e *Engine) processInteractiveMessage(p Platform, msg *Message, session *Session) {
	e.processInteractiveMessageWith(p, msg, session, e.agent, e.sessions, msg.SessionKey, "", "")
}

// processInteractiveMessageWith is the core interactive processing loop.
// It accepts an explicit agent, interactiveKey (for the interactiveStates map),
// and workspaceDir so that multi-workspace mode can route to per-workspace agents.
// ccSessionKey, when non-empty, is used for CC_SESSION_KEY in the agent env; otherwise interactiveKey is used.
func (e *Engine) processInteractiveMessageWith(p Platform, msg *Message, session *Session, agent Agent, sessions *SessionManager, interactiveKey string, workspaceDir string, ccSessionKey string) {
	// session.Unlock() is NOT deferred here — it is called explicitly in
	// the drain loop below while holding state.mu to close the race window
	// between "queue is empty" and "session unlocked". A deferred fallback
	// ensures the lock is released on early-return paths.
	unlocked := false
	defer func() {
		if !unlocked {
			session.Unlock()
		}
	}()

	if e.ctx.Err() != nil {
		return
	}

	turnStart := time.Now()

	e.i18n.DetectAndSet(msg.Content)
	session.AddHistory("user", msg.Content)

	// Use the agent override when available (multi-workspace mode)
	var agentOverride Agent
	if agent != e.agent {
		agentOverride = agent
	}
	state := e.getOrCreateInteractiveStateWith(interactiveKey, p, msg.ReplyCtx, session, sessions, agentOverride, ccSessionKey)

	// Set workspaceDir on the state for idle reaper identification
	if workspaceDir != "" {
		state.mu.Lock()
		state.workspaceDir = workspaceDir
		state.mu.Unlock()
	}

	// Update reply context for this turn
	state.mu.Lock()
	state.platform = p
	state.replyCtx = msg.ReplyCtx
	state.mu.Unlock()

	if state.agentSession == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgFailedToStartAgentSession))
		return
	}

	if workspaceDir != "" && e.workspacePool != nil {
		ws := e.workspacePool.GetOrCreate(workspaceDir)
		ws.BeginTurn()
		defer ws.EndTurn()
	}

	// Apply per-message permission mode override (e.g. cron jobs with mode = "bypassPermissions").
	// Defer restores only when SetLiveMode succeeds for the override.
	if msg.ModeOverride != "" {
		if switcher, ok := state.agentSession.(LiveModeSwitcher); ok {
			if switcher.SetLiveMode(msg.ModeOverride) {
				defer func() {
					defaultMode := "default"
					if ma, ok := e.agent.(interface{ GetMode() string }); ok {
						if m := ma.GetMode(); m != "" {
							defaultMode = m
						}
					}
					switcher.SetLiveMode(defaultMode)
				}()
			}
		}
	}

	// Start typing indicator if platform supports it.
	// Ownership is transferred to processInteractiveEvents which manages
	// stopping/restarting it across queued message turns.
	var stopTyping func()
	if ti, ok := p.(TypingIndicator); ok {
		stopTyping = ti.StartTyping(e.ctx, msg.ReplyCtx)
	}
	defer func() {
		// Stop typing if ownership was NOT transferred to processInteractiveEvents
		// (i.e. an early return before that call).
		if stopTyping != nil {
			stopTyping()
		}
	}()

	// Drain any stale events left in the channel from a previous turn.
	// This prevents the next processInteractiveEvents from reading an old
	// EventResult that was pushed after the previous turn already returned.
	drainEvents(state.agentSession.Events())

	promptContent := e.buildSenderPrompt(msg.Content, msg.UserID, msg.UserName, msg.Platform, msg.SessionKey)

	sendStart := time.Now()
	state.mu.Lock()
	state.fromVoice = msg.FromVoice
	state.sideText = ""
	state.mu.Unlock()

	// Run Send concurrently with processInteractiveEvents. Some agents block inside
	// Send until the prompt turn finishes (e.g. ACP session/prompt); they may emit
	// EventPermissionRequest while blocked — the event loop must run in parallel.
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- state.agentSession.Send(promptContent, msg.Images, msg.Files)
	}()

	e.processInteractiveEvents(state, session, sessions, interactiveKey, msg.MessageID, turnStart, stopTyping, sendDone, msg.ReplyCtx)
	if elapsed := time.Since(sendStart); elapsed >= slowAgentSend {
		slog.Warn("slow agent send", "elapsed", elapsed, "session", msg.SessionKey, "content_len", len(msg.Content))
	}
	stopTyping = nil // ownership transferred; prevent defer from double-stopping

	// Guard against a narrow race: a message may have been queued between
	// processInteractiveEvents observing an empty queue and returning here
	// (session is still locked, so handleMessage's TryLock fails and routes
	// the message to queueMessageForBusySession). Drain any such orphans.
	if e.drainPendingMessages(state, session, sessions, interactiveKey) {
		unlocked = true
	}
}

// getOrCreateWorkspaceAgent returns (or creates) a per-workspace agent and session manager.
// workspace must be a normalized path (from resolveWorkspace or normalizeWorkspacePath).
func (e *Engine) getOrCreateWorkspaceAgent(workspace string) (Agent, *SessionManager, error) {
	ws := e.workspacePool.GetOrCreate(workspace)
	ws.mu.Lock()
	defer ws.mu.Unlock()

	if ws.agent != nil {
		return ws.agent, ws.sessions, nil
	}

	// Create a new agent instance with this workspace's work_dir
	opts := make(map[string]any)
	if snapshotter, ok := e.agent.(WorkspaceAgentOptionSnapshotter); ok {
		for k, v := range snapshotter.WorkspaceAgentOptions() {
			opts[k] = v
		}
	}
	opts["work_dir"] = workspace

	// Copy model from original agent if possible
	if _, ok := opts["model"]; !ok {
		if ma, ok := e.agent.(interface{ GetModel() string }); ok {
			if m := ma.GetModel(); m != "" {
				opts["model"] = m
			}
		}
	}
	// Copy permission mode
	if _, ok := opts["mode"]; !ok {
		if ma, ok := e.agent.(interface{ GetMode() string }); ok {
			if m := ma.GetMode(); m != "" {
				opts["mode"] = m
			}
		}
	}
	// Copy run_as_user (and run_as_env) for OS-level isolation. Without
	// this, per-workspace agents silently bypass the project-level
	// run_as_user config because their opts map is freshly constructed
	// above, not inherited from the project-level opts that main.go
	// already decorated. See cc-connect#496 and the cc-connect/core/runas.go
	// preamble for why run_as_user has to survive this copy.
	if _, ok := opts["run_as_user"]; !ok {
		if ma, ok := e.agent.(interface{ GetRunAsUser() string }); ok {
			if u := ma.GetRunAsUser(); u != "" {
				opts["run_as_user"] = u
			}
		}
	}
	if _, ok := opts["run_as_env"]; !ok {
		if ma, ok := e.agent.(interface{ GetRunAsEnv() []string }); ok {
			if env := ma.GetRunAsEnv(); len(env) > 0 {
				opts["run_as_env"] = env
			}
		}
	}

	agent, err := CreateAgent(e.agent.Name(), opts)
	if err != nil {
		return nil, nil, fmt.Errorf("create workspace agent for %s: %w", workspace, err)
	}

	// Wire providers if original agent has them
	if ps, ok := e.agent.(ProviderSwitcher); ok {
		if ps2, ok2 := agent.(ProviderSwitcher); ok2 {
			ps2.SetProviders(ps.ListProviders())
			if active := ps.GetActiveProvider(); active != nil && active.Name != "" {
				ps2.SetActiveProvider(active.Name)
			}
		}
	}

	// Create per-workspace session manager
	h := sha256.Sum256([]byte(workspace))
	sessionFile := filepath.Join(filepath.Dir(e.sessions.StorePath()),
		fmt.Sprintf("%s_ws_%s.json", e.name, hex.EncodeToString(h[:4])))
	sessions := NewSessionManager(sessionFile)

	ws.agent = agent
	ws.sessions = sessions
	return agent, sessions, nil
}

func (e *Engine) resolveChannelWorkDir(workspace, interactiveKey string) string {
	if e.projectState == nil {
		return workspace
	}
	override := e.projectState.WorkspaceDirOverride(interactiveKey)
	if override == "" {
		return workspace
	}
	if info, err := os.Stat(override); err == nil && info.IsDir() {
		return override
	}
	e.projectState.ClearWorkspaceDirOverride(interactiveKey)
	e.projectState.Save()
	return workspace
}

func (e *Engine) workspaceContext(workspace, sessionKey string) (Agent, *SessionManager, string, string, error) {
	interactiveKey := workspace + ":" + sessionKey
	effectiveDir := e.resolveChannelWorkDir(workspace, interactiveKey)
	wsAgent, wsSessions, err := e.getOrCreateWorkspaceAgent(effectiveDir)
	if err != nil {
		return nil, nil, "", "", err
	}
	return wsAgent, wsSessions, interactiveKey, effectiveDir, nil
}

// getOrCreateInteractiveStateWith accepts an optional agent override for multi-workspace mode.
// adoptPendingFromPlaceholder copies pendingMessages from an existing placeholder
// state to newState so queued messages are not lost when the map entry is replaced.
// Must be called under interactiveMu.
func adoptPendingFromPlaceholder(existing, newState *interactiveState) {
	if existing == nil || existing == newState {
		return
	}
	existing.mu.Lock()
	if len(existing.pendingMessages) > 0 {
		newState.pendingMessages = existing.pendingMessages
		existing.pendingMessages = nil
	}
	existing.mu.Unlock()
}

// When agentOverride is non-nil it is used instead of e.agent to start the session.
// ccSessionKey, when non-empty, is used for CC_SESSION_KEY env injection; otherwise sessionKey is used.
func (e *Engine) getOrCreateInteractiveStateWith(sessionKey string, p Platform, replyCtx any, session *Session, sessions *SessionManager, agentOverride Agent, ccSessionKey string) *interactiveState {
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()

	state, ok := e.interactiveStates[sessionKey]
	if ok && state.agentSession != nil && state.agentSession.Alive() {
		// Verify the running agent session matches the current active session.
		// After /new or /switch the active session changes, but the old agent
		// process may still be alive. Reusing it would send messages to the
		// wrong conversation context.
		wantID := session.GetAgentSessionID()
		currentID := state.agentSession.CurrentSessionID()
		// Reuse only when the live process matches what the Session expects:
		// - IDs match (same Claude session), or
		// - the process has not reported an ID yet (startup; empty want is OK).
		// If wantID is empty (/new, cleared session) but the process already has
		// a concrete ID, reusing would keep --resume context — recycle (#238).
		needRecycle := currentID != "" && (wantID == "" || wantID != currentID)
		if !needRecycle {
			return state
		}
		// Tear down the stale agent so we start one that matches the Session below.
		slog.Info("interactive session mismatch, recycling",
			"session_key", sessionKey,
			"want_agent_session", wantID,
			"have_agent_session", currentID,
		)
		state.markStopped()
		// Close synchronously to prevent race condition where old agent
		// continues outputting while new agent starts (issue #327).
		e.closeAgentSessionWithTimeout(sessionKey, state.agentSession)
		delete(e.interactiveStates, sessionKey)
		ok = false // prevent reading stale settings below
	}

	// Select the agent to use for this session
	agent := e.agent
	if agentOverride != nil {
		agent = agentOverride
	}

	ccKey := sessionKey
	if ccSessionKey != "" {
		ccKey = ccSessionKey
	}

	// Inject per-session env vars so the agent subprocess can call `cc-connect cron add` etc.
	if inj, ok := agent.(SessionEnvInjector); ok {
		envVars := []string{
			"CC_PROJECT=" + e.name,
			"CC_SESSION_KEY=" + ccKey,
		}
		if exePath, err := os.Executable(); err == nil {
			binDir := filepath.Dir(exePath)
			if curPath := os.Getenv("PATH"); curPath != "" {
				envVars = append(envVars, "PATH="+binDir+string(filepath.ListSeparator)+curPath)
			} else {
				envVars = append(envVars, "PATH="+binDir)
			}
		}
		inj.SetSessionEnv(envVars)
	}

	// Inject platform-specific formatting instructions into the agent's system prompt.
	// Clear the prompt first so instructions from a previous platform don't leak
	// into sessions for platforms that don't provide their own instructions.
	if ppi, ok := agent.(PlatformPromptInjector); ok {
		prompt := ""
		if fip, ok := p.(FormattingInstructionProvider); ok {
			prompt = fip.FormattingInstructions()
		}
		ppi.SetPlatformPrompt(prompt)
	}

	// Check if context is already canceled (e.g. during shutdown/restart)
	if e.ctx.Err() != nil {
		slog.Debug("skipping session start: context canceled", "session_key", sessionKey)
		newState := &interactiveState{platform: p, replyCtx: replyCtx, agent: agent}
		adoptPendingFromPlaceholder(e.interactiveStates[sessionKey], newState)
		state = newState
		e.interactiveStates[sessionKey] = state
		return state
	}

	// Resume only when we have a concrete saved agent session ID. If the session
	// is unbound, force a fresh start instead of attaching to whichever CLI
	// conversation happens to be "latest" in this workspace.
	startSessionID := session.GetAgentSessionID()
	isResume := startSessionID != ""
	startAt := time.Now()
	agentSession, err := agent.StartSession(e.ctx, startSessionID)
	startElapsed := time.Since(startAt)
	if err != nil {
		// If resume/continue failed, try a fresh session as fallback.
		if startSessionID != "" {
			slog.Error("session resume failed, falling back to fresh session",
				"session_key", sessionKey, "failed_session_id", startSessionID,
				"error", err, "elapsed", startElapsed)
			startAt = time.Now()
			agentSession, err = agent.StartSession(e.ctx, "")
			startElapsed = time.Since(startAt)
			if err == nil {
				slog.Info("fresh session started after resume failure",
					"session_key", sessionKey, "elapsed", startElapsed)
			}
		}
		if err != nil {
			slog.Error("failed to start interactive session", "error", err, "elapsed", startElapsed)
			newState := &interactiveState{platform: p, replyCtx: replyCtx, agent: agent}
			adoptPendingFromPlaceholder(e.interactiveStates[sessionKey], newState)
			state = newState
			e.interactiveStates[sessionKey] = state
			return state
		}
	}
	if startElapsed >= slowAgentStart {
		slog.Warn("slow agent session start", "elapsed", startElapsed, "agent", agent.Name(), "session_id", startSessionID)
	}

	if newID := agentSession.CurrentSessionID(); newID != "" {
		if session.CompareAndSetAgentSessionID(newID, agent.Name()) {
			sessions.Save()
		}
	}

	newState := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     replyCtx,
		agent:        agent,
	}
	adoptPendingFromPlaceholder(e.interactiveStates[sessionKey], newState)
	state = newState
	e.interactiveStates[sessionKey] = state

	slog.Info("session spawned", "session_key", sessionKey, "agent_session", session.GetAgentSessionID(), "is_resume", isResume, "elapsed", startElapsed)
	return state
}

// cleanupInteractiveState removes the interactive state for the given session key
// and closes its agent session. When an expected state is provided, cleanup is
// skipped if the map entry has been replaced by a different state — this prevents
// a stale goroutine (still running after /new created a fresh Session object and
// a new turn started on it) from accidentally destroying the replacement state.
//
// IMPORTANT: The state is deleted from the map AFTER the agent session is closed
// to avoid race conditions where concurrent requests see an empty map while the
// agent session is still being shut down (which can take up to 130s for Stop hooks).
func (e *Engine) cleanupInteractiveState(sessionKey string, expected ...*interactiveState) {
	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[sessionKey]
	if len(expected) > 0 && expected[0] != nil && state != expected[0] {
		// Another turn has already replaced the state — skip cleanup.
		e.interactiveMu.Unlock()
		return
	}
	// Capture the agent session and nil it out atomically to prevent a
	// concurrent cleanup (without expected) from closing the same session.
	var agentSession AgentSession
	if ok && state != nil {
		agentSession = state.agentSession
		state.agentSession = nil
	}
	e.interactiveMu.Unlock()

	// Notify senders of any queued messages that will never be processed.
	if ok && state != nil {
		state.markStopped()
		e.notifyDroppedQueuedMessages(state, fmt.Errorf("session reset"))
	}

	// Close the agent session BEFORE deleting from the map.
	// This prevents race conditions where /stop during cleanup sees
	// an empty map and reports "No execution in progress" while
	// the agent session Close() is still blocking (up to 130s).
	if agentSession != nil {
		e.closeAgentSessionWithTimeout(sessionKey, agentSession)
	}

	// Now delete the state from the map after the session is closed.
	e.interactiveMu.Lock()
	// Re-check that the state hasn't been replaced during the close
	currentState, currentOk := e.interactiveStates[sessionKey]
	if currentOk && len(expected) > 0 && expected[0] != nil && currentState != expected[0] {
		// Another turn has replaced the state during our close — don't delete it.
		e.interactiveMu.Unlock()
		return
	}
	delete(e.interactiveStates, sessionKey)
	e.interactiveMu.Unlock()
}

func (e *Engine) closeAgentSessionAsync(sessionKey string, agentSession AgentSession) {
	if agentSession == nil {
		return
	}
	go e.closeAgentSessionWithTimeout(sessionKey, agentSession)
}

func (e *Engine) closeAgentSessionWithTimeout(sessionKey string, agentSession AgentSession) {
	if agentSession == nil {
		return
	}

	// Allow enough time for the agent's own graceful shutdown sequence:
	// stdin close → Stop hooks (claude-mem summary etc.) → SIGTERM → SIGKILL.
	// Claude Code's Stop hooks can take up to 120s (claude-mem uses a
	// sonnet summarizer). The 130s budget covers the default 120s graceful
	// phase + 5s SIGTERM + 5s buffer. The wait ends early if the process
	// exits sooner — this is the ceiling, not the typical duration.
	const closeTimeout = 130 * time.Second

	slog.Debug("cleanupInteractiveState: closing agent session", "session", sessionKey)
	closeStart := time.Now()

	done := make(chan struct{})
	go func() {
		agentSession.Close()
		close(done)
	}()

	select {
	case <-done:
		if elapsed := time.Since(closeStart); elapsed >= slowAgentClose {
			slog.Warn("slow agent session close", "elapsed", elapsed, "session", sessionKey)
		}
	case <-time.After(closeTimeout):
		slog.Error("agent session close timed out, abandoning",
			"timeout", closeTimeout, "session", sessionKey)
	}
}

const defaultEventIdleTimeout = 2 * time.Hour

func (e *Engine) processInteractiveEvents(state *interactiveState, session *Session, sessions *SessionManager, sessionKey string, msgID string, turnStart time.Time, stopTypingFn func(), sendDone <-chan error, replyCtx any) {
	var textParts []string
	var segmentStart int // index into textParts: text before this has been sent/displayed
	toolCount := 0
	waitStart := time.Now()
	firstEventLogged := false
	triggerAutoCompress := false
	pendingSend := sendDone

	// stopTyping tracks the current turn's typing indicator so it can be
	// stopped when a queued message starts a new turn.
	stopTyping := stopTypingFn
	// doneReaction stores a function to add a "done" emoji after stopTyping.
	// Set during EventResult handling for multi-round quiet turns.
	var doneReaction func()
	defer func() {
		if stopTyping != nil {
			stopTyping()
		}
		if doneReaction != nil {
			doneReaction()
		}
	}()

	state.mu.Lock()
	workspaceDir := state.workspaceDir
	replyAgent := state.agent
	if replyAgent == nil {
		replyAgent = e.agent
	}
	workspaceRenderer := func(content string) string {
		return e.renderOutgoingContentForWorkspace(state.platform, content, workspaceDir)
	}
	sendWorkspace := func(p Platform, replyCtx any, content string) {
		e.sendForWorkspace(p, replyCtx, content, workspaceDir)
	}
	sendWorkspaceWithError := func(p Platform, replyCtx any, content string) error {
		return e.sendWithErrorForWorkspace(p, replyCtx, content, workspaceDir)
	}
	sp := newStreamPreview(e.streamPreview, state.platform, state.replyCtx, e.ctx, workspaceRenderer)
	cp := newCompactProgressWriter(e.ctx, state.platform, state.replyCtx, e.agent.Name(), e.i18n.CurrentLang(), workspaceRenderer)
	state.mu.Unlock()

	// Idle timeout: 0 = disabled
	var idleTimer *time.Timer
	var idleCh <-chan time.Time
	if e.eventIdleTimeout > 0 {
		idleTimer = time.NewTimer(e.eventIdleTimeout)
		defer idleTimer.Stop()
		idleCh = idleTimer.C
	}

	events := state.agentSession.Events()
	stopCh := state.stopSignal()
	for {
		var event Event
		var ok bool

		select {
		case <-stopCh:
			sp.discard()
			return
		case event, ok = <-events:
			if !ok {
				goto channelClosed
			}
		case err := <-pendingSend:
			pendingSend = nil
			if err != nil {
				slog.Error("failed to send prompt", "error", err, "session_key", sessionKey)
				sp.discard()
				if stopTyping != nil {
					stopTyping()
					stopTyping = nil
				}
				e.notifyDroppedQueuedMessages(state, err)
				if state.agentSession == nil || !state.agentSession.Alive() {
					e.cleanupInteractiveState(sessionKey, state)
				}
				state.mu.Lock()
				p := state.platform
				state.mu.Unlock()
				e.send(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgError), err))
				return
			}
			continue
		case <-idleCh:
			slog.Error("agent session idle timeout: no events for too long, killing session",
				"session_key", sessionKey, "timeout", e.eventIdleTimeout, "elapsed", time.Since(turnStart))
			cp.Finalize(ProgressCardStateFailed)
			sp.discard()
			state.mu.Lock()
			p := state.platform
			state.mu.Unlock()
			e.send(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgError), "agent session timed out (no response)"))
			e.cleanupInteractiveState(sessionKey, state)
			return
		case <-e.ctx.Done():
			return
		}

		if state.isStopped() {
			sp.discard()
			return
		}

		// Reset idle timer after receiving an event
		if idleTimer != nil {
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(e.eventIdleTimeout)
		}

		if !firstEventLogged {
			firstEventLogged = true
			if elapsed := time.Since(waitStart); elapsed >= slowAgentFirstEvent {
				slog.Warn("slow agent first event", "elapsed", elapsed, "session", sessionKey, "event_type", event.Type)
			}
		}

		state.mu.Lock()
		p := state.platform
		state.mu.Unlock()

		switch event.Type {
		case EventThinking:
			// When thinking messages are hidden, behavior depends on display mode:
			//   quiet:   append separator to keep all text in one card
			//   compact: freeze+detach to split text into separate cards
			if !e.display.ThinkingMessages && len(textParts) > segmentStart {
				if e.display.Mode == "quiet" {
					if sp.canPreview() && sp.appendSeparator("\n\n") {
						textParts = append(textParts, "\n\n")
					}
				} else {
					if sp.canPreview() {
						sp.freeze()
						sp.detachPreview()
					} else {
						segment := strings.Join(textParts[segmentStart:], "")
						if segment != "" {
							for _, chunk := range splitMessage(segment, maxPlatformMessageLen) {
								sendWorkspace(p, replyCtx, chunk)
							}
						}
					}
					segmentStart = len(textParts)
				}
			}
			if e.display.ThinkingMessages && event.Content != "" {
				// Flush accumulated text segment before thinking display
				previewActive := sp.canPreview()
				if len(textParts) > segmentStart {
					if !previewActive {
						segment := strings.Join(textParts[segmentStart:], "")
						if segment != "" {
							for _, chunk := range splitMessage(segment, maxPlatformMessageLen) {
								sendWorkspace(p, replyCtx, chunk)
							}
						}
					}
					segmentStart = len(textParts)
				}
				sp.freeze()
				if previewActive {
					sp.detachPreview() // keep frozen preview visible as permanent message
				}
				preview := truncateIf(event.Content, e.display.ThinkingMaxLen)
				thinkingMsg := fmt.Sprintf(e.i18n.T(MsgThinking), preview)
				if !cp.AppendEvent(ProgressEntryThinking, preview, "", thinkingMsg) {
					sendWorkspace(p, replyCtx, thinkingMsg)
				}
			}

		case EventToolUse:
			toolCount++
			// When tool messages are hidden, behavior depends on display mode:
			//   quiet:   append separator to keep all text in one card
			//   compact: freeze+detach to split text into separate cards
			if !e.display.ToolMessages && len(textParts) > segmentStart {
				if e.display.Mode == "quiet" {
					if sp.canPreview() && sp.appendSeparator("\n\n") {
						textParts = append(textParts, "\n\n")
					}
				} else {
					if sp.canPreview() {
						sp.freeze()
						sp.detachPreview()
					} else {
						segment := strings.Join(textParts[segmentStart:], "")
						if segment != "" {
							for _, chunk := range splitMessage(segment, maxPlatformMessageLen) {
								sendWorkspace(p, replyCtx, chunk)
							}
						}
					}
					segmentStart = len(textParts)
				}
			}
			if e.display.ToolMessages {
				// Flush accumulated text segment before tool display
				previewActive := sp.canPreview()
				if len(textParts) > segmentStart {
					if !previewActive {
						segment := strings.Join(textParts[segmentStart:], "")
						if segment != "" {
							for _, chunk := range splitMessage(segment, maxPlatformMessageLen) {
								sendWorkspace(p, replyCtx, chunk)
							}
						}
					}
					segmentStart = len(textParts)
				}
				sp.freeze()
				if previewActive {
					sp.detachPreview() // keep frozen preview visible as permanent message
				}
				toolInput := event.ToolInput
				var formattedInput string
				if toolInput == "" {
					formattedInput = ""
				} else if strings.Contains(toolInput, "```") {
					// Already contains code blocks (pre-formatted by agent) — use as-is
					formattedInput = toolInput
				} else if strings.Contains(toolInput, "\n") || utf8.RuneCountInString(toolInput) > 200 {
					lang := toolCodeLang(event.ToolName, toolInput)
					formattedInput = fmt.Sprintf("```%s\n%s\n```", lang, toolInput)
				} else {
					switch event.ToolName {
					case "shell", "run_shell_command", "Bash":
						formattedInput = fmt.Sprintf("```bash\n%s\n```", toolInput)
					default:
						formattedInput = fmt.Sprintf("`%s`", toolInput)
					}
				}
				toolMsg := fmt.Sprintf(e.i18n.T(MsgTool), toolCount, event.ToolName, formattedInput)
				if !cp.AppendEvent(ProgressEntryToolUse, toolInput, event.ToolName, toolMsg) {
					for _, chunk := range SplitMessageCodeFenceAware(toolMsg, maxPlatformMessageLen) {
						sendWorkspace(p, replyCtx, chunk)
					}
				}
			}

		case EventToolResult:
			if e.display.ToolMessages {
				result := strings.TrimSpace(event.ToolResult)
				if result == "" {
					result = strings.TrimSpace(event.Content)
				}
				if result != "" {
					result = truncateIf(result, e.display.ToolMaxLen)
				}
				if result != "" || event.ToolStatus != "" || event.ToolExitCode != nil || event.ToolSuccess != nil {
					resultMsg := e.formatToolResultEventFallback(event.ToolName, result, event.ToolStatus, event.ToolExitCode, event.ToolSuccess)
					entry := ProgressCardEntry{
						Kind:     ProgressEntryToolResult,
						Tool:     event.ToolName,
						Text:     result,
						Status:   event.ToolStatus,
						ExitCode: event.ToolExitCode,
						Success:  event.ToolSuccess,
					}
					if !cp.AppendStructured(entry, resultMsg) {
						if !SuppressStandaloneToolResultEvent(p) {
							e.sendRaw(p, replyCtx, resultMsg)
						}
					}
				}
			}

		case EventText:
			if event.Content != "" {
				textParts = append(textParts, event.Content)
				if sp.canPreview() {
					sp.appendText(event.Content)
				}
			}
			if event.SessionID != "" {
				if session.CompareAndSetAgentSessionID(event.SessionID, e.agent.Name()) {
					pendingName := session.GetName()
					if pendingName != "" && pendingName != "session" && pendingName != "default" {
						sessions.SetSessionName(event.SessionID, pendingName)
					}
					sessions.Save()
				}
			}

		case EventPermissionRequest:
			isAskQuestion := event.ToolName == "AskUserQuestion" && len(event.Questions) > 0

			state.mu.Lock()
			autoApprove := state.approveAll
			state.mu.Unlock()

			if autoApprove && !isAskQuestion {
				slog.Debug("auto-approving (approve-all)", "request_id", event.RequestID, "tool", event.ToolName)
				_ = state.agentSession.RespondPermission(event.RequestID, PermissionResult{
					Behavior:     "allow",
					UpdatedInput: event.ToolInputRaw,
				})
				continue
			}

			// Flush accumulated text segment before permission prompt
			previewActive := sp.canPreview()
			if len(textParts) > segmentStart {
				if !previewActive {
					segment := strings.Join(textParts[segmentStart:], "")
					if segment != "" {
						for _, chunk := range splitMessage(segment, maxPlatformMessageLen) {
							sendWorkspace(p, replyCtx, chunk)
						}
					}
				}
				segmentStart = len(textParts)
			}
			sp.freeze()
			if previewActive {
				sp.detachPreview() // keep frozen preview visible as permanent message
			}

			slog.Info("permission request",
				"request_id", event.RequestID,
				"tool", event.ToolName,
			)

			pending := &pendingPermission{
				RequestID:    event.RequestID,
				ToolName:     event.ToolName,
				ToolInput:    event.ToolInputRaw,
				InputPreview: event.ToolInput,
				Questions:    event.Questions,
				Resolved:     make(chan struct{}),
			}
			state.mu.Lock()
			state.pending = pending
			state.mu.Unlock()

			if isAskQuestion {
				e.sendAskQuestionPrompt(p, replyCtx, event.Questions, 0)
			} else {
				permLimit := e.display.ToolMaxLen
				if permLimit > 0 {
					permLimit = permLimit * 8 / 5
				}
				toolInput := truncateIf(event.ToolInput, permLimit)
				prompt := fmt.Sprintf(e.i18n.T(MsgPermissionPrompt), event.ToolName, toolInput)
				e.sendPermissionPrompt(p, replyCtx, prompt, event.ToolName, toolInput)
			}

			// Stop idle timer while waiting for user permission response;
			// the user may take a long time to decide, and we don't want
			// the idle timeout to kill the session during that wait.
			if idleTimer != nil {
				idleTimer.Stop()
			}

			<-pending.Resolved
			slog.Info("permission resolved", "request_id", event.RequestID)

			// Restart idle timer after permission is resolved
			if idleTimer != nil {
				idleTimer.Reset(e.eventIdleTimeout)
			}

		case EventResult:
			cp.Finalize(ProgressCardStateCompleted)
			// Use state.agentSession.CurrentSessionID() instead of event.SessionID.
			// event.SessionID may be empty in some cases, causing the agent_session_id
			// to not be persisted to disk, breaking session resume on next startup.
			if state != nil && state.agentSession != nil {
				if currentID := state.agentSession.CurrentSessionID(); currentID != "" {
					session.SetAgentSessionID(currentID, e.agent.Name())
					sessions.Save()
				}
			}

			fullResponse := event.Content
			// When tool progress is hidden, segmentStart stays 0 and textParts
			// contains ALL text across tool boundaries. Prefer the full accumulated
			// text over event.Content which only contains the last assistant segment.
			if len(textParts) > 0 && segmentStart == 0 && !e.display.ToolMessages {
				fullResponse = strings.Join(textParts, "")
			} else if fullResponse == "" && len(textParts) > 0 {
				fullResponse = strings.Join(textParts, "")
			}
			if fullResponse == "" {
				fullResponse = e.i18n.T(MsgEmptyResponse)
			}

			// Context usage indicator: prefer SDK tokens, fall back to self-reported.
			sdkPlausible := event.InputTokens >= 100
			selfPct := parseSelfReportedCtx(fullResponse)
			cleanResponse := ctxSelfReportRe.ReplaceAllString(fullResponse, "")
			cleanResponse = strings.TrimRight(cleanResponse, "\n ")
			baseResponse := cleanResponse

			contextEstimate := estimateTokensWithPendingAssistant(session.GetHistory(0), baseResponse)

			// Evaluate auto-compress trigger (token estimate on user+assistant text,
			// including this turn's assistant reply before it is appended to history).
			if e.autoCompressEnabled && e.autoCompressMaxTokens > 0 {
				estimate := contextEstimate
				now := time.Now()
				state.mu.Lock()
				last := state.lastAutoCompressAt
				state.mu.Unlock()
				if estimate >= e.autoCompressMaxTokens && (last.IsZero() || now.Sub(last) >= e.autoCompressMinGap) {
					triggerAutoCompress = true
					state.mu.Lock()
					state.lastAutoCompressTokens = estimate
					state.mu.Unlock()
				}
			}

			session.AddHistory("assistant", baseResponse)
			sessions.Save()

			if e.showContextIndicator {
				if sdkPlausible {
					cleanResponse += contextIndicator(event.InputTokens)
				} else if selfPct > 0 {
					cleanResponse += fmt.Sprintf("\n[ctx: ~%d%%]", selfPct)
				}
			}
			if footer := e.buildReplyFooter(replyAgent, state.agentSession, workspaceDir, replyFooterContextText(replyFooterSessionContextUsage(state.agentSession), e.i18n)); footer != "" {
				cleanResponse = appendReplyFooter(cleanResponse, footer)
			}
			fullResponse = cleanResponse

			turnDuration := time.Since(turnStart)
			slog.Info("turn complete",
				"session", session.ID,
				"agent_session", session.GetAgentSessionID(),
				"msg_id", msgID,
				"tools", toolCount,
				"response_len", len(fullResponse),
				"turn_duration", turnDuration,
				"input_tokens", event.InputTokens,
				"output_tokens", event.OutputTokens,
			)

			replyStart := time.Now()
			normalizedBaseResponse := strings.TrimSpace(baseResponse)
			state.mu.Lock()
			suppressDuplicate := normalizedBaseResponse != "" && normalizedBaseResponse == state.sideText
			state.sideText = ""
			state.mu.Unlock()

			// When tool calls happened and prior text was already surfaced in segments,
			// only send the unsent remainder. When tool progress is hidden, tool events don't surface
			// side-channel messages and segmentStart stays 0, so keep normal finalize flow.
			if toolCount > 0 && segmentStart > 0 {
				sp.discard()
				if segmentStart < len(textParts) {
					unsent := strings.Join(textParts[segmentStart:], "")
					if unsent != "" {
						for _, chunk := range splitMessage(unsent, maxPlatformMessageLen) {
							if err := sendWorkspaceWithError(p, replyCtx, chunk); err != nil {
								return
							}
						}
					}
				}
			} else if suppressDuplicate {
				sp.discard()
				if metaOnly := strings.TrimSpace(strings.TrimPrefix(fullResponse, baseResponse)); metaOnly != "" {
					for _, chunk := range splitMessage(metaOnly, maxPlatformMessageLen) {
						if err := sendWorkspaceWithError(p, replyCtx, chunk); err != nil {
							return
						}
					}
				}
				slog.Debug("EventResult: suppressed duplicate side-channel text", "response_len", len(fullResponse))
			} else if sp.finish(fullResponse) {
				slog.Debug("EventResult: finalized via stream preview", "response_len", len(fullResponse))
			} else {
				slog.Debug("EventResult: sending via p.Send (preview inactive or failed)", "response_len", len(fullResponse), "chunks", len(splitMessage(fullResponse, maxPlatformMessageLen)))
				for _, chunk := range splitMessage(fullResponse, maxPlatformMessageLen) {
					if err := sendWorkspaceWithError(p, replyCtx, chunk); err != nil {
						return
					}
				}
			}

			if elapsed := time.Since(replyStart); elapsed >= slowPlatformSend {
				slog.Warn("slow final reply send", "platform", p.Name(), "elapsed", elapsed, "response_len", len(fullResponse))
			}

			// TTS: async voice reply if enabled
			if e.tts != nil && e.tts.Enabled && e.tts.TTS != nil {
				state.mu.Lock()
				fromVoice := state.fromVoice
				state.mu.Unlock()
				mode := e.tts.GetTTSMode()
				slog.Debug("tts: checking conditions", "mode", mode, "fromVoice", fromVoice, "will_send", mode == "always" || (mode == "voice_only" && fromVoice))
				if mode == "always" || (mode == "voice_only" && fromVoice) {
					go e.sendTTSReply(p, replyCtx, fullResponse)
				}
			} else {
				slog.Debug("tts: not enabled", "tts_nil", e.tts == nil, "enabled", e.tts != nil && e.tts.Enabled, "tts_obj_nil", e.tts == nil || e.tts.TTS == nil)
			}

			// Auto-compress after finishing a turn, before sending any queued messages.
			if triggerAutoCompress {
				compressor, ok := e.agent.(ContextCompressor)
				if ok && compressor.CompressCommand() != "" {
					if pendingSend != nil {
						if err := <-pendingSend; err != nil {
							slog.Debug("async send error before compress", "error", err)
						}
					}
					state.mu.Lock()
					state.lastAutoCompressAt = time.Now()
					tokenEst := state.lastAutoCompressTokens
					state.mu.Unlock()
					slog.Info("auto-compress: triggering", "session", sessionKey)

					// Notify user before compressing so they know the context is about to change.
					compressNotice := e.i18n.T(MsgCompressing)
					if tokenEst > 0 {
						compressNotice = fmt.Sprintf("%s (~%dk tokens)", compressNotice, tokenEst/1000)
					}
					e.send(state.platform, state.replyCtx, compressNotice)

					// Run compress inline while the session is still locked.
					e.runCompress(state, session, sessions, sessionKey, state.platform, state.replyCtx, true)
					return
				}
			}

			// Check for queued messages — if present, continue the event loop
			// for the next turn instead of returning.
			state.mu.Lock()
			if len(state.pendingMessages) > 0 {
				queued := state.pendingMessages[0]
				state.pendingMessages = state.pendingMessages[1:]
				remainingQueue := len(state.pendingMessages)
				state.platform = queued.platform
				state.replyCtx = queued.replyCtx
				state.fromVoice = queued.fromVoice
				state.mu.Unlock()

				// Stop the previous turn's typing indicator
				if stopTyping != nil {
					stopTyping()
					stopTyping = nil
				}
				// Start a new typing indicator for the queued message's context
				if ti, ok := queued.platform.(TypingIndicator); ok {
					stopTyping = ti.StartTyping(e.ctx, queued.replyCtx)
				}
				// Agent continues working — don't add done reaction for this turn.
				doneReaction = nil

				// Drain stale events before starting the next turn. Between
				// EventResult and Send(), the only buffered events would be
				// stale leftovers (e.g. a deferred EventError from cmd.Wait()).
				drainEvents(state.agentSession.Events())

				if pendingSend != nil {
					if err := <-pendingSend; err != nil {
						slog.Debug("async send error before queued turn", "error", err)
					}
				}

				queuedPrompt := e.buildSenderPrompt(queued.content, queued.userID, queued.userName, queued.msgPlatform, queued.msgSessionKey)

				nextSend := make(chan error, 1)
				go func() {
					nextSend <- state.agentSession.Send(queuedPrompt, queued.images, queued.files)
				}()
				pendingSend = nextSend

				// Detect language now (deferred from queue time to avoid
				// flipping locale while the previous turn is still running).
				e.i18n.DetectAndSet(queued.content)

				// Reset per-turn state for the next turn
				textParts = nil
				segmentStart = 0
				toolCount = 0
				turnStart = time.Now()
				firstEventLogged = false
				waitStart = time.Now()
				queuedRenderer := func(content string) string {
					return e.renderOutgoingContentForWorkspace(queued.platform, content, workspaceDir)
				}
				sp = newStreamPreview(e.streamPreview, queued.platform, queued.replyCtx, e.ctx, queuedRenderer)
				cp = newCompactProgressWriter(e.ctx, queued.platform, queued.replyCtx, e.agent.Name(), e.i18n.CurrentLang(), queuedRenderer)

				session.AddHistory("user", queued.content)

				if idleTimer != nil {
					if !idleTimer.Stop() {
						select {
						case <-idleTimer.C:
						default:
						}
					}
					idleTimer.Reset(e.eventIdleTimeout)
				}

				slog.Info("processing queued message",
					"session", sessionKey,
					"remaining_queue", remainingQueue,
				)
				continue
			}
			state.mu.Unlock()

			if pendingSend != nil {
				if err := <-pendingSend; err != nil {
					slog.Debug("async send error after EventResult", "error", err)
				}
			}

			// Add a "done" reaction when the preview was updated in-place
			// (user only got a push for the initial send). The reaction is
			// added after stopTyping (deferred) so the "doing" emoji is
			// removed first.
			if sp.needsDoneReaction() {
				if doneTI, ok := p.(TypingIndicatorDone); ok {
					doneReaction = func() { doneTI.AddDoneReaction(replyCtx) }
				}
			}

			return

		case EventError:
			cp.Finalize(ProgressCardStateFailed)
			sp.discard()
			if event.Error != nil {
				slog.Error("agent error", "error", event.Error)
				e.send(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgError), event.Error))
			}
			// Only drop queued messages if the agent session is dead.
			// Some agents (e.g. Codex) emit EventError for per-turn failures
			// while keeping the session alive for subsequent turns.
			if state.agentSession == nil || !state.agentSession.Alive() {
				e.notifyDroppedQueuedMessages(state, event.Error)
			}
			return
		}
	}

channelClosed:
	// Channel closed - process exited unexpectedly
	slog.Warn("agent process exited", "session_key", sessionKey)
	e.notifyDroppedQueuedMessages(state, fmt.Errorf("agent process exited"))
	e.cleanupInteractiveState(sessionKey, state)

	if len(textParts) > 0 {
		state.mu.Lock()
		p := state.platform
		state.mu.Unlock()

		fullResponse := strings.Join(textParts, "")
		session.AddHistory("assistant", fullResponse)

		if toolCount > 0 && segmentStart > 0 {
			sp.discard()
			if segmentStart < len(textParts) {
				unsent := strings.Join(textParts[segmentStart:], "")
				if unsent != "" {
					for _, chunk := range splitMessage(unsent, maxPlatformMessageLen) {
						if err := sendWorkspaceWithError(p, replyCtx, chunk); err != nil {
							return
						}
					}
				}
			}
		} else if sp.finish(fullResponse) {
			slog.Debug("stream preview: finalized in-place (process exited)")
		} else {
			for _, chunk := range splitMessage(fullResponse, maxPlatformMessageLen) {
				if err := sendWorkspaceWithError(p, replyCtx, chunk); err != nil {
					return
				}
			}
		}
	}
}

// notifyDroppedQueuedMessages drains pendingMessages from the state and
// sends an error notification to each queued message's sender. Called when
// the event loop exits abnormally (EventError, channel closed) and queued
// messages can no longer be delivered to the agent.
func (e *Engine) notifyDroppedQueuedMessages(state *interactiveState, reason error) {
	state.mu.Lock()
	remaining := state.pendingMessages
	state.pendingMessages = nil
	state.mu.Unlock()
	for _, q := range remaining {
		e.send(q.platform, q.replyCtx, fmt.Sprintf(e.i18n.T(MsgError), reason))
	}
}

// drainPendingMessages processes all queued messages in the state's pendingMessages
// queue. It atomically unlocks the session when the queue is empty (while holding
// state.mu) to close the race window between "queue empty" and "session unlocked".
// Returns true if the session was unlocked by this call.
func (e *Engine) drainPendingMessages(state *interactiveState, session *Session, sessions *SessionManager, sessionKey string) bool {
	for {
		state.mu.Lock()
		if len(state.pendingMessages) == 0 {
			session.Unlock()
			state.mu.Unlock()
			return true
		}
		queued := state.pendingMessages[0]
		state.pendingMessages = state.pendingMessages[1:]
		state.platform = queued.platform
		state.replyCtx = queued.replyCtx
		state.fromVoice = queued.fromVoice
		state.mu.Unlock()

		e.i18n.DetectAndSet(queued.content)
		prompt := e.buildSenderPrompt(queued.content, queued.userID, queued.userName, queued.msgPlatform, queued.msgSessionKey)

		if state.agentSession == nil || !state.agentSession.Alive() {
			e.send(queued.platform, queued.replyCtx, fmt.Sprintf(e.i18n.T(MsgError), "agent session ended"))
			e.notifyDroppedQueuedMessages(state, fmt.Errorf("agent session ended"))
			return false
		}

		drainEvents(state.agentSession.Events())

		session.AddHistory("user", queued.content)

		sendDone := make(chan error, 1)
		go func() {
			sendDone <- state.agentSession.Send(prompt, queued.images, queued.files)
		}()

		var stopTyping func()
		if ti, ok := queued.platform.(TypingIndicator); ok {
			stopTyping = ti.StartTyping(e.ctx, queued.replyCtx)
		}

		slog.Info("processing queued message", "session", sessionKey)
		e.processInteractiveEvents(state, session, sessions, sessionKey, "", time.Now(), stopTyping, sendDone, queued.replyCtx)
	}
}

// ──────────────────────────────────────────────────────────────
// Command handling
// ──────────────────────────────────────────────────────────────

// builtinCommands maps canonical command names to their aliases/full names.
// The first entry is the canonical name used for prefix matching.
var builtinCommands = []struct {
	names []string
	id    string
}{
	{[]string{"new"}, "new"},
	{[]string{"list", "sessions"}, "list"},
	{[]string{"switch"}, "switch"},
	{[]string{"name", "rename"}, "name"},
	{[]string{"current"}, "current"},
	{[]string{"status"}, "status"},
	{[]string{"usage", "quota"}, "usage"},
	{[]string{"history"}, "history"},
	{[]string{"allow"}, "allow"},
	{[]string{"model"}, "model"},
	{[]string{"reasoning", "effort"}, "reasoning"},
	{[]string{"mode"}, "mode"},
	{[]string{"lang"}, "lang"},
	{[]string{"quiet"}, "quiet"},
	{[]string{"provider"}, "provider"},
	{[]string{"memory"}, "memory"},
	{[]string{"cron"}, "cron"},
	{[]string{"heartbeat", "hb"}, "heartbeat"},
	{[]string{"compress", "compact"}, "compress"},
	{[]string{"stop"}, "stop"},
	{[]string{"help"}, "help"},
	{[]string{"version"}, "version"},
	{[]string{"commands", "command", "cmd"}, "commands"},
	{[]string{"skills", "skill"}, "skills"},
	{[]string{"config"}, "config"},
	{[]string{"doctor"}, "doctor"},
	{[]string{"upgrade", "update"}, "upgrade"},
	{[]string{"restart"}, "restart"},
	{[]string{"alias"}, "alias"},
	{[]string{"delete", "del", "rm"}, "delete"},
	{[]string{"bind"}, "bind"},
	{[]string{"search", "find"}, "search"},
	{[]string{"shell", "sh", "exec", "run"}, "shell"},
	{[]string{"show"}, "show"},
	{[]string{"dir", "cd", "chdir", "workdir"}, "dir"},
	{[]string{"tts"}, "tts"},
	{[]string{"workspace", "ws"}, "workspace"},
	{[]string{"whoami", "myid"}, "whoami"},
	{[]string{"web"}, "web"},
	{[]string{"diff"}, "diff"},
}

// isBtwCommand checks if a trimmed message starts with a /btw command.
func isBtwCommand(trimmed string) bool {
	return matchBtwPrefix(trimmed) != ""
}

// matchBtwPrefix returns the prefix portion (e.g. "/btw ") if the
// message starts with a btw command, or "" if it doesn't match.
func matchBtwPrefix(trimmed string) string {
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"/btw"} {
		if strings.HasPrefix(lower, prefix) {
			rest := trimmed[len(prefix):]
			if rest == "" || rest[0] == ' ' {
				return trimmed[:len(prefix)]
			}
		}
	}
	return ""
}

// matchPrefix finds a unique command matching the given prefix.
// Returns the command id or "" if no match / ambiguous.
func matchPrefix(prefix string, candidates []struct {
	names []string
	id    string
}) string {
	// Exact match first
	for _, c := range candidates {
		for _, n := range c.names {
			if prefix == n {
				return c.id
			}
		}
	}
	// Prefix match
	var matched string
	for _, c := range candidates {
		for _, n := range c.names {
			if strings.HasPrefix(n, prefix) {
				if matched != "" && matched != c.id {
					return "" // ambiguous
				}
				matched = c.id
				break
			}
		}
	}
	return matched
}

// matchSubCommand does prefix matching against a flat list of subcommand names.
func matchSubCommand(input string, candidates []string) string {
	for _, c := range candidates {
		if input == c {
			return c
		}
	}
	var matched string
	for _, c := range candidates {
		if strings.HasPrefix(c, input) {
			if matched != "" {
				return input // ambiguous → return raw input (will hit default)
			}
			matched = c
		}
	}
	if matched != "" {
		return matched
	}
	return input
}

func (e *Engine) handleCommand(p Platform, msg *Message, raw string) bool {
	parts := strings.Fields(raw)
	cmd := strings.ToLower(strings.TrimPrefix(parts[0], "/"))
	args := parts[1:]

	cmdID := matchPrefix(cmd, builtinCommands)

	// Resolve effective disabled commands: role-based if available, else project-level
	e.userRolesMu.RLock()
	disabledCmds := e.disabledCmds
	urm := e.userRoles
	e.userRolesMu.RUnlock()
	if urm != nil {
		if role := urm.ResolveRole(msg.UserID); role != nil {
			disabledCmds = role.DisabledCmds
		}
	}

	if cmdID != "" && disabledCmds[cmdID] {
		slog.Info("audit: command_blocked",
			"user_id", msg.UserID, "platform", msg.Platform,
			"project", e.name, "command", cmdID, "reason", "disabled")
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandDisabled), "/"+cmdID))
		return true
	}

	if cmdID != "" && privilegedCommands[cmdID] && !e.isAdmin(msg.UserID) {
		slog.Info("audit: command_blocked",
			"user_id", msg.UserID, "platform", msg.Platform,
			"project", e.name, "command", cmdID, "reason", "unauthorized")
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgAdminRequired), "/"+cmdID))
		return true
	}

	if cmdID != "" {
		slog.Info("audit: command_executed",
			"user_id", msg.UserID, "platform", msg.Platform,
			"project", e.name, "command", cmdID)
	}

	switch cmdID {
	case "new":
		e.cmdNew(p, msg, args)
	case "list":
		e.cmdList(p, msg, args)
	case "switch":
		e.cmdSwitch(p, msg, args)
	case "name":
		e.cmdName(p, msg, args)
	case "current":
		e.cmdCurrent(p, msg)
	case "status":
		e.cmdStatus(p, msg)
	case "usage":
		e.cmdUsage(p, msg)
	case "history":
		e.cmdHistory(p, msg, args)
	case "allow":
		e.cmdAllow(p, msg, args)
	case "model":
		e.cmdModel(p, msg, args)
	case "reasoning":
		e.cmdReasoning(p, msg, args)
	case "mode":
		e.cmdMode(p, msg, args)
	case "lang":
		e.cmdLang(p, msg, args)
	case "quiet":
		e.cmdQuiet(p, msg, args)
	case "provider":
		e.cmdProvider(p, msg, args)
	case "memory":
		e.cmdMemory(p, msg, args)
	case "cron":
		e.cmdCron(p, msg, args)
	case "heartbeat":
		e.cmdHeartbeat(p, msg, args)
	case "compress":
		e.cmdCompress(p, msg)
	case "stop":
		e.cmdStop(p, msg)
	case "help":
		e.cmdHelp(p, msg)
	case "version":
		e.reply(p, msg.ReplyCtx, VersionInfo)
	case "commands":
		e.cmdCommands(p, msg, args)
	case "skills":
		e.cmdSkills(p, msg)
	case "config":
		e.cmdConfig(p, msg, args)
	case "doctor":
		e.cmdDoctor(p, msg)
	case "upgrade":
		e.cmdUpgrade(p, msg, args)
	case "restart":
		e.cmdRestart(p, msg)
	case "alias":
		e.cmdAlias(p, msg, args)
	case "delete":
		e.cmdDelete(p, msg, args)
	case "bind":
		e.cmdBind(p, msg, args)
	case "search":
		e.cmdSearch(p, msg, args)
	case "shell":
		e.cmdShell(p, msg, raw)
	case "diff":
		e.cmdDiff(p, msg, raw)
	case "show":
		e.cmdShow(p, msg, args)
	case "dir":
		e.cmdDir(p, msg, args)
	case "tts":
		e.cmdTTS(p, msg, args)
	case "workspace":
		if !e.multiWorkspace {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsNotEnabled))
			return true
		}
		e.handleWorkspaceCommand(p, msg, args)
		return true
	case "whoami":
		e.cmdWhoami(p, msg)
	case "web":
		e.cmdWeb(p, msg, args)
	default:
		if custom, ok := e.commands.Resolve(cmd); ok {
			if disabledCmds[strings.ToLower(custom.Name)] {
				slog.Info("audit: command_blocked",
					"user_id", msg.UserID, "platform", msg.Platform,
					"project", e.name, "command", custom.Name, "reason", "disabled")
				e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandDisabled), "/"+custom.Name))
				return true
			}
			slog.Info("audit: command_executed",
				"user_id", msg.UserID, "platform", msg.Platform,
				"project", e.name, "command", custom.Name, "type", "custom")
			e.executeCustomCommand(p, msg, custom, args)
			return true
		}
		if skill := e.skills.Resolve(cmd); skill != nil {
			if disabledCmds[strings.ToLower(skill.Name)] {
				slog.Info("audit: command_blocked",
					"user_id", msg.UserID, "platform", msg.Platform,
					"project", e.name, "command", skill.Name, "reason", "disabled")
				e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandDisabled), "/"+skill.Name))
				return true
			}
			slog.Info("audit: command_executed",
				"user_id", msg.UserID, "platform", msg.Platform,
				"project", e.name, "command", skill.Name, "type", "skill")
			e.executeSkill(p, msg, skill, args)
			return true
		}
		// Not a cc-connect command — notify user, then fall through to agent
		e.send(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgUnknownCommand), "/"+cmd))
		return false
	}
	return true
}

func (e *Engine) handleWorkspaceCommand(p Platform, msg *Message, args []string) {
	channelID := effectiveChannelID(msg)
	channelKey := effectiveWorkspaceChannelKey(msg)
	projectKey := "project:" + e.name
	resolveChannelName := func() func() string {
		resolved := false
		channelName := ""
		return func() string {
			if resolved {
				return channelName
			}
			resolved = true
			if resolver, ok := p.(ChannelNameResolver); ok {
				channelName, _ = resolver.ResolveChannelName(channelID)
			}
			return channelName
		}
	}()
	replyWorkspaceInfo := func(b *WorkspaceBinding, bindingKey string) {
		if bindingKey == sharedWorkspaceBindingsKey {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsInfoShared, b.Workspace, b.BoundAt.Format(time.RFC3339)))
			return
		}
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsInfo, b.Workspace, b.BoundAt.Format(time.RFC3339)))
	}
	routeWorkspace := func(bindingKey string, pathParts []string, usageKey, successKey MsgKey) bool {
		routePath := strings.TrimSpace(strings.Join(pathParts, " "))
		if routePath == "" {
			e.reply(p, msg.ReplyCtx, e.i18n.T(usageKey))
			return false
		}
		if !filepath.IsAbs(routePath) {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsRouteAbsoluteRequired, routePath))
			return false
		}

		info, err := os.Stat(routePath)
		if err != nil {
			if os.IsNotExist(err) {
				e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsRouteNotFound, routePath))
			} else {
				e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
			}
			return false
		}
		if !info.IsDir() {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsRouteNotDirectory, routePath))
			return false
		}

		normalizedPath := normalizeWorkspacePath(routePath)
		e.workspaceBindings.Bind(bindingKey, channelKey, resolveChannelName(), normalizedPath)
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(successKey, normalizedPath))
		return true
	}
	bindWorkspace := func(bindingKey, wsName string, successKey MsgKey) bool {
		wsPath := filepath.Join(e.baseDir, wsName)

		// Check if workspace directory exists
		if _, err := os.Stat(wsPath); os.IsNotExist(err) {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsBindNotFound, wsName))
			return false
		}

		e.workspaceBindings.Bind(bindingKey, channelKey, resolveChannelName(), normalizeWorkspacePath(wsPath))
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(successKey, wsName))
		return true
	}
	initWorkspace := func(bindingKey, target string, successKey MsgKey) bool {
		// Support local directory paths (absolute or relative to baseDir).
		if looksLikeLocalDir(target) {
			dirPath, err := resolveLocalDirPath(target, e.baseDir)
			if err != nil {
				e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsInitDirNotFound, target))
				return false
			}
			info, statErr := os.Stat(dirPath)
			if statErr != nil || !info.IsDir() {
				e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsInitDirNotFound, target))
				return false
			}
			e.workspaceBindings.Bind(bindingKey, channelKey, resolveChannelName(), normalizeWorkspacePath(dirPath))
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(successKey, dirPath))
			return true
		}

		if !looksLikeGitURL(target) {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsInitInvalidTarget))
			return false
		}

		repoName := extractRepoName(target)
		cloneTo := filepath.Join(e.baseDir, repoName)

		if _, err := os.Stat(cloneTo); err == nil {
			e.workspaceBindings.Bind(bindingKey, channelKey, resolveChannelName(), normalizeWorkspacePath(cloneTo))
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(successKey, cloneTo))
			return true
		}

		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsCloneProgress, target))

		if err := gitClone(target, cloneTo); err != nil {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsCloneFailed, err))
			return false
		}

		e.workspaceBindings.Bind(bindingKey, channelKey, resolveChannelName(), normalizeWorkspacePath(cloneTo))
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(successKey, cloneTo))
		return true
	}
	listBindings := func(bindingKey string, emptyKey, titleKey MsgKey) {
		bindings := e.workspaceBindings.ListByProject(bindingKey)
		if len(bindings) == 0 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(emptyKey))
			return
		}
		var sb strings.Builder
		sb.WriteString(e.i18n.T(titleKey) + "\n")
		for chID, b := range bindings {
			name := b.ChannelName
			if name == "" {
				name = chID
			}
			sb.WriteString(fmt.Sprintf("• #%s → `%s`\n", name, b.Workspace))
		}
		e.reply(p, msg.ReplyCtx, sb.String())
	}

	subCmd := ""
	if len(args) > 0 {
		subCmd = matchSubCommand(args[0], []string{"init", "bind", "route", "unbind", "list", "shared"})
	}

	switch subCmd {
	case "":
		b, bindingKey, usable := e.lookupEffectiveWorkspaceBinding(channelKey)
		if !usable {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsNoBinding))
		} else {
			replyWorkspaceInfo(b, bindingKey)
		}

	case "bind":
		if len(args) < 2 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsBindUsage))
			return
		}
		bindWorkspace(projectKey, args[1], MsgWsBindSuccess)

	case "route":
		if len(args) < 2 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsRouteUsage))
			return
		}
		routeWorkspace(projectKey, args[1:], MsgWsRouteUsage, MsgWsRouteSuccess)

	case "init":
		if len(args) < 2 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsInitUsage))
			return
		}
		initWorkspace(projectKey, args[1], MsgWsCloneSuccess)

	case "shared":
		sharedSubCmd := ""
		if len(args) > 1 {
			sharedSubCmd = matchSubCommand(args[1], []string{"init", "bind", "route", "unbind", "list"})
		}
		switch sharedSubCmd {
		case "":
			b := e.workspaceBindings.Lookup(sharedWorkspaceBindingsKey, channelKey)
			if b == nil {
				e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsSharedNoBinding))
			} else {
				replyWorkspaceInfo(b, sharedWorkspaceBindingsKey)
			}
			return
		case "bind":
			if len(args) < 3 {
				e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsSharedUsage))
				return
			}
			bindWorkspace(sharedWorkspaceBindingsKey, args[2], MsgWsSharedBindSuccess)
			return
		case "route":
			if len(args) < 3 {
				e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsSharedUsage))
				return
			}
			routeWorkspace(sharedWorkspaceBindingsKey, args[2:], MsgWsSharedUsage, MsgWsSharedRouteSuccess)
			return
		case "init":
			if len(args) < 3 {
				e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsSharedUsage))
				return
			}
			initWorkspace(sharedWorkspaceBindingsKey, args[2], MsgWsSharedBindSuccess)
			return
		case "unbind":
			if e.workspaceBindings.Lookup(sharedWorkspaceBindingsKey, channelKey) == nil {
				e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsSharedNoBinding))
				return
			}
			e.workspaceBindings.Unbind(sharedWorkspaceBindingsKey, channelKey)
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsSharedUnbindSuccess))
			return
		case "list":
			listBindings(sharedWorkspaceBindingsKey, MsgWsSharedListEmpty, MsgWsSharedListTitle)
			return
		default:
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsSharedUsage))
			return
		}

	case "unbind":
		if e.workspaceBindings.Lookup(projectKey, channelKey) == nil {
			if e.workspaceBindings.Lookup(sharedWorkspaceBindingsKey, channelKey) != nil {
				e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsSharedOnlyHint))
			} else {
				e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsNoBinding))
			}
			return
		}
		e.workspaceBindings.Unbind(projectKey, channelKey)
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsUnbindSuccess))

	case "list":
		listBindings(projectKey, MsgWsListEmpty, MsgWsListTitle)

	default:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsUsage))
	}
}

func (e *Engine) cmdNew(p Platform, msg *Message, args []string) {
	_, sessions, interactiveKey, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}

	slog.Info("cmdNew: cleaning up old session", "session_key", msg.SessionKey)
	e.cleanupInteractiveState(interactiveKey)
	slog.Info("cmdNew: cleanup done, creating new session", "session_key", msg.SessionKey)

	// Clear old session's agent session ID so it cannot be resumed
	old := sessions.GetOrCreateActive(msg.SessionKey)
	old.SetAgentSessionID("", "")
	old.ClearHistory()
	sessions.Save()

	name := ""
	if len(args) > 0 {
		name = strings.Join(args, " ")
	}
	sessions.NewSession(msg.SessionKey, name)
	if name != "" {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgNewSessionCreatedName), name))
	} else {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgNewSessionCreated))
	}
}

// filterOwnedSessions removes agent sessions that are not tracked by cc-connect's
// session manager. This prevents external CLI sessions in the same work_dir from
// appearing in /list, /switch, /delete, etc. If the session manager has no tracked
// agent sessions at all (e.g. first run), all sessions are returned unfiltered.
func filterOwnedSessions(sessions []AgentSessionInfo, known map[string]struct{}) []AgentSessionInfo {
	if len(known) == 0 {
		return sessions
	}
	filtered := make([]AgentSessionInfo, 0, len(sessions))
	for _, s := range sessions {
		if _, ok := known[s.ID]; ok {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

const listPageSize = 20

// dirCardPageSize is the max directory history rows per card page (Feishu / other card UIs).
const dirCardPageSize = 20

func (e *Engine) cmdList(p Platform, msg *Message, args []string) {
	agent, sessions, _, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}

	if !supportsCards(p) {
		agentSessions, err := agent.ListSessions(e.ctx)
		if err != nil {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgListError), err))
			return
		}
		agentSessions = filterOwnedSessions(agentSessions, sessions.KnownAgentSessionIDs())
		if len(agentSessions) == 0 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgListEmpty))
			return
		}

		total := len(agentSessions)
		totalPages := (total + listPageSize - 1) / listPageSize

		page := 1
		if len(args) > 0 {
			if n, err := strconv.Atoi(args[0]); err == nil && n > 0 {
				page = n
			}
		}
		if page > totalPages {
			page = totalPages
		}

		start := (page - 1) * listPageSize
		end := start + listPageSize
		if end > total {
			end = total
		}

		agentName := agent.Name()
		activeSession := sessions.GetOrCreateActive(msg.SessionKey)
		activeAgentID := activeSession.GetAgentSessionID()

		var sb strings.Builder
		if totalPages > 1 {
			sb.WriteString(fmt.Sprintf(e.i18n.T(MsgListTitlePaged), agentName, total, page, totalPages))
		} else {
			sb.WriteString(fmt.Sprintf(e.i18n.T(MsgListTitle), agentName, total))
		}
		for i := start; i < end; i++ {
			s := agentSessions[i]
			marker := "◻"
			if s.ID == activeAgentID {
				marker = "▶"
			}
			displayName := sessions.GetSessionName(s.ID)
			if displayName != "" {
				displayName = "📌 " + displayName
			} else {
				displayName = strings.ReplaceAll(s.Summary, "\n", " ")
				displayName = strings.Join(strings.Fields(displayName), " ")
				if displayName == "" {
					displayName = "(empty)"
				}
				if len([]rune(displayName)) > 40 {
					displayName = string([]rune(displayName)[:40]) + "…"
				}
			}
			sb.WriteString(fmt.Sprintf("%s **%d.** %s · **%d** msgs · %s\n",
				marker, i+1, displayName, s.MessageCount, s.ModifiedAt.Format("01-02 15:04")))
		}
		if totalPages > 1 {
			sb.WriteString(fmt.Sprintf(e.i18n.T(MsgListPageHint), page, totalPages))
		}
		sb.WriteString(e.i18n.T(MsgListSwitchHint))
		e.reply(p, msg.ReplyCtx, sb.String())
		return
	}

	page := 1
	if len(args) > 0 {
		if n, err := strconv.Atoi(args[0]); err == nil && n > 0 {
			page = n
		}
	}
	card, err := e.renderListCard(msg.SessionKey, page)
	if err != nil {
		e.reply(p, msg.ReplyCtx, err.Error())
		return
	}
	e.replyWithCard(p, msg.ReplyCtx, card)
}

func (e *Engine) cmdSwitch(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		e.reply(p, msg.ReplyCtx, "Usage: /switch <number | id_prefix | name>")
		return
	}
	query := strings.TrimSpace(strings.Join(args, " "))

	slog.Info("cmdSwitch: listing agent sessions", "session_key", msg.SessionKey)
	agent, sessions, interactiveKey, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}
	agentSessions, err := agent.ListSessions(e.ctx)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	agentSessions = filterOwnedSessions(agentSessions, sessions.KnownAgentSessionIDs())

	matched := e.matchSession(agentSessions, sessions, query)
	if matched == nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgSwitchNoMatch), query))
		return
	}

	slog.Info("cmdSwitch: cleaning up old session", "session_key", msg.SessionKey)
	e.cleanupInteractiveState(interactiveKey)
	slog.Info("cmdSwitch: cleanup done", "session_key", msg.SessionKey)

	session := sessions.SwitchToAgentSession(msg.SessionKey, matched.ID, agent.Name(), matched.Summary)
	session.ClearHistory()

	shortID := matched.ID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	displayName := sessions.GetSessionName(matched.ID)
	if displayName == "" {
		displayName = matched.Summary
	}
	e.reply(p, msg.ReplyCtx,
		e.i18n.Tf(MsgSwitchSuccess, displayName, shortID, matched.MessageCount))
}

// matchSession resolves a user query to an agent session. Priority:
//  1. Numeric index (1-based, matching /list output)
//  2. Exact custom name match (case-insensitive)
//  3. Session ID prefix match
//  4. Custom name prefix match (case-insensitive)
//  5. Summary substring match (case-insensitive)
func (e *Engine) matchSession(sessions []AgentSessionInfo, manager *SessionManager, query string) *AgentSessionInfo {
	if len(sessions) == 0 {
		return nil
	}

	// 1. Numeric index
	if idx, err := strconv.Atoi(query); err == nil && idx >= 1 && idx <= len(sessions) {
		return &sessions[idx-1]
	}

	queryLower := strings.ToLower(query)

	// 2. Exact custom name match
	for i := range sessions {
		name := manager.GetSessionName(sessions[i].ID)
		if name != "" && strings.ToLower(name) == queryLower {
			return &sessions[i]
		}
	}

	// 3. Session ID prefix match
	for i := range sessions {
		if strings.HasPrefix(sessions[i].ID, query) {
			return &sessions[i]
		}
	}

	// 4. Custom name prefix match
	for i := range sessions {
		name := manager.GetSessionName(sessions[i].ID)
		if name != "" && strings.HasPrefix(strings.ToLower(name), queryLower) {
			return &sessions[i]
		}
	}

	// 5. Summary substring match
	for i := range sessions {
		if sessions[i].Summary != "" && strings.Contains(strings.ToLower(sessions[i].Summary), queryLower) {
			return &sessions[i]
		}
	}

	return nil
}

func (e *Engine) commandWorkDir(agent Agent, msg *Message) string {
	if switcher, ok := agent.(WorkDirSwitcher); ok {
		if wd := strings.TrimSpace(switcher.GetWorkDir()); wd != "" {
			return normalizeWorkspacePath(wd)
		}
	}
	if e.multiWorkspace {
		channelKey := effectiveWorkspaceChannelKey(msg)
		if b, _, usable := e.lookupEffectiveWorkspaceBinding(channelKey); usable {
			return normalizeWorkspacePath(b.Workspace)
		}
	}
	if wd, ok := agent.(interface{ GetWorkDir() string }); ok {
		if dir := strings.TrimSpace(wd.GetWorkDir()); dir != "" {
			return normalizeWorkspacePath(dir)
		}
	}
	if wd, ok := e.agent.(interface{ GetWorkDir() string }); ok {
		if dir := strings.TrimSpace(wd.GetWorkDir()); dir != "" {
			return normalizeWorkspacePath(dir)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		return normalizeWorkspacePath(cwd)
	}
	return ""
}

func (e *Engine) buildReplyFooter(agent Agent, session AgentSession, workspaceDir string, contextLeft string) string {
	if !e.replyFooterEnabled || agent == nil {
		return ""
	}

	var parts []string
	if model := replyFooterModel(session, agent); model != "" {
		parts = append(parts, model)
	}
	if effort := replyFooterReasoningEffort(session, agent); effort != "" {
		parts = append(parts, effort)
	}
	if left := strings.TrimSpace(contextLeft); left != "" {
		parts = append(parts, left)
	} else if usage := e.replyFooterUsageText(session, agent); usage != "" {
		parts = append(parts, usage)
	}
	if dir := replyFooterWorkDir(session, agent, workspaceDir); dir != "" {
		parts = append(parts, dir)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

func replyFooterModel(session AgentSession, agent Agent) string {
	if session != nil {
		if getter, ok := session.(interface{ GetModel() string }); ok {
			if model := strings.TrimSpace(getter.GetModel()); model != "" {
				return model
			}
		}
	}
	if getter, ok := agent.(interface{ GetModel() string }); ok {
		return strings.TrimSpace(getter.GetModel())
	}
	return ""
}

func replyFooterReasoningEffort(session AgentSession, agent Agent) string {
	if session != nil {
		if getter, ok := session.(interface{ GetReasoningEffort() string }); ok {
			if effort := strings.TrimSpace(getter.GetReasoningEffort()); effort != "" {
				return effort
			}
		}
	}
	if getter, ok := agent.(interface{ GetReasoningEffort() string }); ok {
		return strings.TrimSpace(getter.GetReasoningEffort())
	}
	return ""
}

func (e *Engine) replyFooterUsageText(session AgentSession, agent Agent) string {
	ctx, cancel := context.WithTimeout(e.ctx, replyFooterUsageTimeout)
	defer cancel()

	if session != nil {
		if reporter, ok := session.(UsageReporter); ok {
			if report, err := reporter.GetUsage(ctx); err == nil {
				return formatReplyFooterUsage(report, e.i18n)
			}
		}
	}

	reporter, ok := agent.(UsageReporter)
	if !ok {
		return ""
	}

	e.replyFooterMu.Lock()
	cached := e.replyFooterUsage
	e.replyFooterMu.Unlock()
	if !cached.fetchedAt.IsZero() && time.Since(cached.fetchedAt) < replyFooterUsageCacheTTL {
		return cached.text
	}

	text := ""
	if report, err := reporter.GetUsage(ctx); err == nil {
		text = formatReplyFooterUsage(report, e.i18n)
	} else if !cached.fetchedAt.IsZero() {
		text = cached.text
	}

	e.replyFooterMu.Lock()
	e.replyFooterUsage = replyFooterUsageCache{text: text, fetchedAt: time.Now()}
	e.replyFooterMu.Unlock()
	return text
}

func formatReplyFooterUsage(report *UsageReport, i18n *I18n) string {
	if report == nil || i18n == nil {
		return ""
	}
	window, _ := selectUsageWindows(report)
	if window == nil {
		return ""
	}
	remaining := 100 - window.UsedPercent
	if remaining < 0 {
		remaining = 0
	}
	if remaining > 100 {
		remaining = 100
	}
	return i18n.Tf(MsgReplyFooterRemaining, remaining)
}

func replyFooterSessionContextUsage(session AgentSession) *ContextUsage {
	if session == nil {
		return nil
	}
	reporter, ok := session.(ContextUsageReporter)
	if !ok {
		return nil
	}
	return reporter.GetContextUsage()
}

func replyFooterContextText(usage *ContextUsage, i18n *I18n) string {
	if usage == nil || i18n == nil {
		return ""
	}
	if usage.ContextWindow <= 0 {
		return ""
	}

	usedTokens := usage.UsedTokens
	if usedTokens <= 0 {
		switch {
		case usage.TotalTokens > 0:
			usedTokens = usage.TotalTokens
		case usage.InputTokens > 0 || usage.OutputTokens > 0:
			usedTokens = usage.InputTokens + usage.OutputTokens
		default:
			return ""
		}
	}

	baseline := usage.BaselineTokens
	if baseline < 0 {
		baseline = 0
	}
	if usage.ContextWindow <= baseline {
		return i18n.Tf(MsgReplyFooterRemaining, 0)
	}

	effectiveWindow := usage.ContextWindow - baseline
	effectiveUsed := usedTokens - baseline
	if effectiveUsed < 0 {
		effectiveUsed = 0
	}
	remaining := effectiveWindow - effectiveUsed
	if remaining < 0 {
		remaining = 0
	}

	left := int(math.Round(float64(remaining) / float64(effectiveWindow) * 100))
	if left < 0 {
		left = 0
	}
	if left > 100 {
		left = 100
	}
	return i18n.Tf(MsgReplyFooterRemaining, left)
}

func replyFooterWorkDir(session AgentSession, agent Agent, workspaceDir string) string {
	dir := strings.TrimSpace(workspaceDir)
	if dir == "" {
		if session != nil {
			if wd, ok := session.(interface{ GetWorkDir() string }); ok {
				dir = strings.TrimSpace(wd.GetWorkDir())
			}
		}
	}
	if dir == "" {
		if switcher, ok := agent.(WorkDirSwitcher); ok {
			dir = strings.TrimSpace(switcher.GetWorkDir())
		}
	}
	if dir == "" {
		if wd, ok := agent.(interface{ GetWorkDir() string }); ok {
			dir = strings.TrimSpace(wd.GetWorkDir())
		}
	}
	if dir == "" {
		return ""
	}
	return compactReplyFooterPath(dir)
}

func compactReplyFooterPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = normalizeWorkspacePath(path)
	if home, err := os.UserHomeDir(); err == nil {
		home = normalizeWorkspacePath(home)
		if path == home {
			return "~"
		}
		prefix := home + string(os.PathSeparator)
		if strings.HasPrefix(path, prefix) {
			return "~" + filepath.ToSlash(strings.TrimPrefix(path, home))
		}
	}

	slash := filepath.ToSlash(path)
	if filepath.IsAbs(path) {
		trimmed := strings.Trim(slash, "/")
		if trimmed == "" {
			return "/"
		}
		parts := strings.Split(trimmed, "/")
		if len(parts) == 1 {
			return parts[0]
		}
		start := len(parts) - 2
		if start < 0 {
			start = 0
		}
		return "…/" + strings.Join(parts[start:], "/")
	}
	return slash
}

func appendReplyFooter(content, footer string) string {
	if footer == "" {
		return content
	}
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return "*" + footer + "*"
	}
	return content + "\n\n*" + footer + "*"
}

func (e *Engine) cmdShow(p Platform, msg *Message, args []string) {
	rawRef := strings.TrimSpace(strings.Join(args, " "))
	if rawRef == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgShowUsage))
		return
	}

	agent, _, _, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}
	workDir := e.commandWorkDir(agent, msg)
	req, err := buildReferenceViewRequest(rawRef, workDir)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgShowParseError, rawRef))
		return
	}
	content, err := renderReferenceView(req)
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "path does not exist"):
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgShowNotFound, rawRef))
		case strings.Contains(err.Error(), "directory reference cannot carry a location"):
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgShowDirWithLocation, rawRef))
		default:
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgShowReadFailed, err))
		}
		return
	}
	e.reply(p, msg.ReplyCtx, content)
}

func (e *Engine) cmdShell(p Platform, msg *Message, raw string) {
	// Strip the command prefix ("/shell ", "/sh ", "/exec ", "/run ")
	shellCmd := raw
	for _, prefix := range []string{"/shell ", "/sh ", "/exec ", "/run "} {
		if strings.HasPrefix(strings.ToLower(raw), prefix) {
			shellCmd = raw[len(prefix):]
			break
		}
	}
	shellCmd = strings.TrimSpace(shellCmd)

	if shellCmd == "" {
		e.reply(p, msg.ReplyCtx, "Usage: /shell <command>\nExample: /shell ls -la")
		return
	}

	agent, _, _, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}
	workDir := e.commandWorkDir(agent, msg)
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	go func() {
		ctx, cancel := context.WithTimeout(e.ctx, 60*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "sh", "-c", shellCmd)
		cmd.Dir = workDir
		output, err := cmd.CombinedOutput()

		if ctx.Err() == context.DeadlineExceeded {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandTimeout), shellCmd))
			return
		}

		result := strings.TrimSpace(string(output))
		if err != nil && result == "" {
			result = err.Error()
		}
		if result == "" {
			result = "(no output)"
		}
		if runes := []rune(result); len(runes) > 4000 {
			result = string(runes[:3997]) + "..."
		}

		e.reply(p, msg.ReplyCtx, fmt.Sprintf("$ %s\n```\n%s\n```", shellCmd, result))
	}()
}

func (e *Engine) cmdDiff(p Platform, msg *Message, raw string) {
	// Parse optional target: /diff [target]
	diffTarget := ""
	if strings.HasPrefix(strings.ToLower(raw), "/diff ") {
		diffTarget = strings.TrimSpace(raw[6:])
	}

	if strings.HasPrefix(diffTarget, "-") {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), "diff target must not start with '-'"))
		return
	}

	// Resolve working directory (same pattern as cmdShell)
	var workDir string
	if e.multiWorkspace {
		channelKey := effectiveWorkspaceChannelKey(msg)
		if b, _, usable := e.lookupEffectiveWorkspaceBinding(channelKey); usable {
			workDir = normalizeWorkspacePath(b.Workspace)
		}
	}
	if workDir == "" {
		if wd, ok := e.agent.(interface{ GetWorkDir() string }); ok {
			workDir = wd.GetWorkDir()
		}
	}
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	go func() {
		ctx, cancel := context.WithTimeout(e.ctx, 60*time.Second)
		defer cancel()

		// Get current branch name and short commit ID
		branchCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
		branchCmd.Dir = workDir
		branchOut, _ := branchCmd.Output()
		currentBranch := strings.TrimSpace(string(branchOut))
		if currentBranch == "" {
			currentBranch = "unknown"
		}

		commitCmd := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD")
		commitCmd.Dir = workDir
		commitOut, _ := commitCmd.Output()
		commitID := strings.TrimSpace(string(commitOut))
		if commitID == "" {
			commitID = "0000000"
		}

		gitArgs := []string{"diff"}
		if diffTarget != "" {
			gitArgs = append(gitArgs, "--", diffTarget)
		}
		gitCmd := exec.CommandContext(ctx, "git", gitArgs...)
		gitCmd.Dir = workDir
		diffOutput, err := gitCmd.Output()

		if ctx.Err() == context.DeadlineExceeded {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandTimeout), "git diff"))
			return
		}
		if err != nil && len(diffOutput) == 0 {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
			return
		}

		target := diffTarget
		if target == "" {
			target = "HEAD"
		}
		if len(strings.TrimSpace(string(diffOutput))) == 0 {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgDiffEmpty), target))
			return
		}

		// Try diff2html + FileSender
		if fileSender, ok := p.(FileSender); ok {
			title := fmt.Sprintf("%s vs %s", currentBranch, target)
			htmlData, err := e.diff2html(ctx, diffOutput, workDir, title)
			if err == nil {
				fileName := fmt.Sprintf("%s-%s.html", currentBranch, commitID)
				_ = e.waitOutgoing(p)
				if err := fileSender.SendFile(e.ctx, msg.ReplyCtx, FileAttachment{
					MimeType: "text/html", Data: htmlData, FileName: fileName,
				}); err == nil {
					return
				}
			}
			if errors.Is(err, exec.ErrNotFound) {
				e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDiffNoDiff2HTML))
			}
		}

		// Fallback: plain text diff
		result := strings.TrimSpace(string(diffOutput))
		if runes := []rune(result); len(runes) > 4000 {
			result = string(runes[:3997]) + "..."
		}
		e.reply(p, msg.ReplyCtx, "```diff\n"+result+"\n```")
	}()
}

func (e *Engine) diff2html(ctx context.Context, diff []byte, workDir, title string) ([]byte, error) {
	if _, err := exec.LookPath("diff2html"); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "diff2html", "-i", "stdin", "-o", "stdout", "--title", title)
	cmd.Dir = workDir
	cmd.Stdin = bytes.NewReader(diff)
	return cmd.Output()
}

// dirApply applies /dir mutations (same semantics as cmdDir). sessionKey is used for GetOrCreateActive.
// On failure returns a non-empty errMsg; on success returns ("", successMsg) for plain-text replies.
func (e *Engine) dirApply(agent Agent, sessions *SessionManager, interactiveKey, sessionKey string, args []string) (errMsg, successMsg string) {
	switcher, ok := agent.(WorkDirSwitcher)
	if !ok {
		return e.i18n.T(MsgDirNotSupported), ""
	}
	currentDir := switcher.GetWorkDir()

	if len(args) == 1 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "reset":
			baseDir := strings.TrimSpace(e.baseWorkDir)
			if baseDir == "" {
				baseDir = currentDir
			}
			if baseDir == "" {
				baseDir, _ = os.Getwd()
			}
			if absDir, err := filepath.Abs(baseDir); err == nil {
				baseDir = absDir
			}

			if !e.multiWorkspace {
				switcher.SetWorkDir(baseDir)
			}
			e.cleanupInteractiveState(interactiveKey)

			s := sessions.GetOrCreateActive(sessionKey)
			s.SetAgentSessionID("", "")
			s.ClearHistory()
			sessions.Save()

			if e.projectState != nil {
				if e.multiWorkspace {
					e.projectState.ClearWorkspaceDirOverride(interactiveKey)
				} else {
					e.projectState.ClearWorkDirOverride()
				}
				e.projectState.Save()
			}
			if e.dirHistory != nil {
				e.dirHistory.Add(e.name, baseDir)
			}

			return "", e.i18n.Tf(MsgDirReset, baseDir)
		}
	}

	arg := strings.Join(args, " ")
	var newDir string

	if idx, err := strconv.Atoi(strings.TrimSpace(arg)); err == nil && idx > 0 {
		if e.dirHistory != nil {
			newDir = e.dirHistory.Get(e.name, idx)
			if newDir == "" {
				return e.i18n.Tf(MsgDirInvalidIndex, idx), ""
			}
		} else {
			return e.i18n.T(MsgDirNoHistory), ""
		}
	} else if arg == "-" {
		if e.dirHistory != nil {
			newDir = e.dirHistory.Previous(e.name)
			if newDir == "" {
				return e.i18n.T(MsgDirNoPrevious), ""
			}
		} else {
			return e.i18n.T(MsgDirNoHistory), ""
		}
	} else {
		newDir = filepath.Clean(arg)
		if strings.HasPrefix(newDir, "~") {
			if homeDir, err := os.UserHomeDir(); err == nil {
				newDir = filepath.Join(homeDir, strings.TrimPrefix(newDir, "~"))
			}
		} else if !filepath.IsAbs(newDir) {
			baseDir := currentDir
			if baseDir == "" {
				baseDir, _ = os.Getwd()
			}
			newDir = filepath.Join(baseDir, newDir)
		}
	}
	if absDir, err := filepath.Abs(newDir); err == nil {
		newDir = absDir
	}

	info, err := os.Stat(newDir)
	if err != nil || !info.IsDir() {
		return e.i18n.Tf(MsgDirInvalidPath, newDir), ""
	}

	if !e.multiWorkspace {
		switcher.SetWorkDir(newDir)
	}
	e.cleanupInteractiveState(interactiveKey)

	s := sessions.GetOrCreateActive(sessionKey)
	s.SetAgentSessionID("", "")
	s.ClearHistory()
	sessions.Save()

	if e.dirHistory != nil {
		e.dirHistory.Add(e.name, newDir)
	}
	if e.projectState != nil {
		if e.multiWorkspace {
			e.projectState.SetWorkspaceDirOverride(interactiveKey, newDir)
		} else {
			e.projectState.SetWorkDirOverride(newDir)
		}
		e.projectState.Save()
	}

	return "", e.i18n.Tf(MsgDirChanged, newDir)
}

func (e *Engine) cmdDir(p Platform, msg *Message, args []string) {
	agent, sessions, interactiveKey, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}
	switcher, ok := agent.(WorkDirSwitcher)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDirNotSupported))
		return
	}

	currentDir := switcher.GetWorkDir()

	if len(args) == 0 {
		if supportsCards(p) {
			e.replyWithCard(p, msg.ReplyCtx, e.renderDirCardSafe(msg.SessionKey, 1))
			return
		}
		var sb strings.Builder
		sb.WriteString(e.i18n.Tf(MsgDirCurrent, currentDir))

		if e.dirHistory != nil {
			history := e.dirHistory.List(e.name)
			if len(history) > 0 {
				sb.WriteString("\n\n")
				sb.WriteString(e.i18n.T(MsgDirHistoryTitle))
				for i, dir := range history {
					marker := "◻"
					if dir == currentDir {
						marker = "▶"
					}
					sb.WriteString(fmt.Sprintf("\n  %s %d. %s", marker, i+1, dir))
				}
				sb.WriteString("\n\n")
				sb.WriteString(e.i18n.T(MsgDirHistoryHint))
			}
		}
		e.reply(p, msg.ReplyCtx, sb.String())
		return
	}

	if len(args) == 1 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "help", "-h", "--help":
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDirUsage))
			return
		}
	}

	errMsg, successMsg := e.dirApply(agent, sessions, interactiveKey, msg.SessionKey, args)
	if errMsg != "" {
		e.reply(p, msg.ReplyCtx, errMsg)
		return
	}
	if supportsCards(p) {
		e.replyWithCard(p, msg.ReplyCtx, e.renderDirCardSafe(msg.SessionKey, 1))
		return
	}
	e.reply(p, msg.ReplyCtx, successMsg)
}

// cmdSearch searches sessions by name or message content.
// Usage: /search <keyword>
func (e *Engine) cmdSearch(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSearchUsage))
		return
	}

	keyword := strings.ToLower(strings.Join(args, " "))

	// Get all agent sessions
	agent, sessions, _, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}
	agentSessions, err := agent.ListSessions(e.ctx)
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgSearchError), err))
		return
	}
	agentSessions = filterOwnedSessions(agentSessions, sessions.KnownAgentSessionIDs())

	type searchResult struct {
		id           string
		name         string
		summary      string
		matchType    string // "name" or "message"
		messageCount int
	}

	var results []searchResult

	for _, s := range agentSessions {
		// Check session name (custom name or summary)
		customName := sessions.GetSessionName(s.ID)
		displayName := customName
		if displayName == "" {
			displayName = s.Summary
		}

		// Match by name/summary
		if strings.Contains(strings.ToLower(displayName), keyword) {
			results = append(results, searchResult{
				id:           s.ID,
				name:         displayName,
				summary:      s.Summary,
				matchType:    "name",
				messageCount: s.MessageCount,
			})
			continue
		}

		// Match by session ID prefix
		if strings.HasPrefix(strings.ToLower(s.ID), keyword) {
			results = append(results, searchResult{
				id:           s.ID,
				name:         displayName,
				summary:      s.Summary,
				matchType:    "id",
				messageCount: s.MessageCount,
			})
			continue
		}
	}

	if len(results) == 0 {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgSearchNoResult), keyword))
		return
	}

	// Build result message
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(e.i18n.T(MsgSearchResult), len(results), keyword))

	for i, r := range results {
		shortID := r.id
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}
		sb.WriteString(fmt.Sprintf("\n%d. [%s] %s", i+1, shortID, r.name))
	}

	sb.WriteString("\n\n" + e.i18n.T(MsgSearchHint))

	e.reply(p, msg.ReplyCtx, sb.String())
}

func (e *Engine) cmdName(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgNameUsage))
		return
	}

	agent, sessions, _, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}

	// Check if first arg is a number → naming a specific session by list index
	var targetID string
	var name string

	if idx, err := strconv.Atoi(args[0]); err == nil && idx >= 1 {
		// /name <number> <name...>
		if len(args) < 2 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgNameUsage))
			return
		}
		agentSessions, err := agent.ListSessions(e.ctx)
		if err != nil {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
			return
		}
		agentSessions = filterOwnedSessions(agentSessions, sessions.KnownAgentSessionIDs())
		if idx > len(agentSessions) {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgSwitchNoSession), idx))
			return
		}
		targetID = agentSessions[idx-1].ID
		name = strings.Join(args[1:], " ")
	} else {
		// /name <name...> → current session
		session := sessions.GetOrCreateActive(msg.SessionKey)
		targetID = session.GetAgentSessionID()
		if targetID == "" {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgNameNoSession))
			return
		}
		name = strings.Join(args, " ")
	}

	name = strings.TrimSpace(name)
	if name == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgNameUsage))
		return
	}

	sessions.SetSessionName(targetID, name)

	shortID := targetID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgNameSet), name, shortID))
}

func (e *Engine) cmdCurrent(p Platform, msg *Message) {
	if !supportsCards(p) {
		_, sessions, _, err := e.commandContext(p, msg)
		if err != nil {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
			return
		}
		s := sessions.GetOrCreateActive(msg.SessionKey)
		agentID := s.GetAgentSessionID()
		if agentID == "" {
			agentID = e.i18n.T(MsgSessionNotStarted)
		}
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCurrentSession), s.Name, agentID, len(s.History)))
		return
	}

	e.replyWithCard(p, msg.ReplyCtx, e.renderCurrentCard(msg.SessionKey))
}

func (e *Engine) cmdStatus(p Platform, msg *Message) {
	if !supportsCards(p) {
		agent, sessions, _, err := e.commandContext(p, msg)
		if err != nil {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
			return
		}
		platNames := make([]string, len(e.platforms))
		for i, pl := range e.platforms {
			platNames[i] = pl.Name()
		}
		platformStr := strings.Join(platNames, ", ")
		if len(platNames) == 0 {
			platformStr = "-"
		}

		workDirStr := e.commandWorkDir(agent, msg)

		uptimeStr := formatDurationI18n(time.Since(e.startedAt), e.i18n.CurrentLang())

		cur := e.i18n.CurrentLang()
		langStr := fmt.Sprintf("%s (%s)", string(cur), langDisplayName(cur))

		var modeStr string
		if ms, ok := agent.(ModeSwitcher); ok {
			mode := ms.GetMode()
			if mode != "" {
				modeStr = e.i18n.Tf(MsgStatusMode, mode)
			}
		}
		thinkingStr := e.i18n.T(MsgDisabledShort)
		if e.display.ThinkingMessages {
			thinkingStr = e.i18n.T(MsgEnabledShort)
		}
		toolStr := e.i18n.T(MsgDisabledShort)
		if e.display.ToolMessages {
			toolStr = e.i18n.T(MsgEnabledShort)
		}
		modeStr += e.i18n.Tf(MsgStatusThinkingMessages, thinkingStr)
		modeStr += e.i18n.Tf(MsgStatusToolMessages, toolStr)

		s := sessions.GetOrCreateActive(msg.SessionKey)
		sessionDisplayName := sessions.GetSessionName(s.GetAgentSessionID())
		if sessionDisplayName == "" {
			sessionDisplayName = s.Name
		}
		sessionStr := e.i18n.Tf(MsgStatusSession, sessionDisplayName, len(s.History))

		var cronStr string
		if e.cronScheduler != nil {
			if jobs := e.cronScheduler.Store().ListBySessionKey(msg.SessionKey); len(jobs) > 0 {
				enabledCount := 0
				for _, j := range jobs {
					if j.Enabled {
						enabledCount++
					}
				}
				cronStr = e.i18n.Tf(MsgStatusCron, len(jobs), enabledCount)
			}
		}

		sessionKeyStr := e.i18n.Tf(MsgStatusSessionKey, msg.SessionKey)

		agentSIDStr := ""
		if agentSID := s.GetAgentSessionID(); agentSID != "" {
			agentSIDStr = e.i18n.Tf(MsgStatusAgentSID, agentSID)
		}

		userIDStr := ""
		if msg.UserID != "" {
			userIDStr = e.i18n.Tf(MsgStatusUserID, msg.UserID)
		}

		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgStatusTitle,
			e.name,
			agent.Name(),
			workDirStr,
			platformStr,
			uptimeStr,
			langStr,
			modeStr,
			sessionStr,
			cronStr,
			sessionKeyStr,
			agentSIDStr,
			userIDStr,
		))
		return
	}

	e.replyWithCard(p, msg.ReplyCtx, e.renderStatusCard(msg.SessionKey, msg.UserID))
}

func (e *Engine) cmdUsage(p Platform, msg *Message) {
	reporter, ok := e.agent.(UsageReporter)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgUsageNotSupported))
		return
	}

	fetchCtx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()

	report, err := reporter.GetUsage(fetchCtx)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgUsageFetchFailed, err))
		return
	}

	if supportsCards(p) {
		e.replyWithCard(p, msg.ReplyCtx, e.renderUsageCard(report))
		return
	}

	e.reply(p, msg.ReplyCtx, formatUsageReport(report, e.i18n.CurrentLang()))
}

func formatUsageReport(report *UsageReport, lang Language) string {
	if report == nil {
		return usageUnavailableText(lang)
	}

	var sb strings.Builder
	sb.WriteString(usageAccountLabel(lang))
	sb.WriteString(accountDisplay(report))
	sb.WriteString(formatUsageBlocks(report, lang))

	return strings.TrimSpace(sb.String())
}

func formatUsageBlocks(report *UsageReport, lang Language) string {
	primary, secondary := selectUsageWindows(report)
	var sections []string
	if primary != nil {
		sections = append(sections, formatUsageBlock(lang, primary))
	}
	if secondary != nil {
		sections = append(sections, formatUsageBlock(lang, secondary))
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}

func accountDisplay(report *UsageReport) string {
	var base string
	if report.Email != "" {
		base = report.Email
	} else if report.AccountID != "" {
		base = report.AccountID
	} else if report.UserID != "" {
		base = report.UserID
	} else {
		base = "-"
	}
	if report.Plan != "" {
		return fmt.Sprintf("%s (%s)", base, report.Plan)
	}
	return base
}

func selectUsageWindows(report *UsageReport) (*UsageWindow, *UsageWindow) {
	for _, bucket := range report.Buckets {
		if len(bucket.Windows) == 0 {
			continue
		}
		var primary, secondary *UsageWindow
		for i := range bucket.Windows {
			window := &bucket.Windows[i]
			switch window.WindowSeconds {
			case 18000:
				primary = window
			case 604800:
				if secondary == nil {
					secondary = window
				}
			}
		}
		if primary == nil && len(bucket.Windows) > 0 {
			primary = &bucket.Windows[0]
		}
		if secondary == nil && len(bucket.Windows) > 1 {
			secondary = &bucket.Windows[1]
		}
		if primary != nil || secondary != nil {
			return primary, secondary
		}
	}
	return nil, nil
}

func formatUsageBlock(lang Language, window *UsageWindow) string {
	remaining := 100 - window.UsedPercent
	if remaining < 0 {
		remaining = 0
	}
	var sb strings.Builder
	sb.WriteString(usageWindowLabel(lang, window.WindowSeconds))
	sb.WriteString("\n")
	sb.WriteString(usageRemainingLabel(lang))
	sb.WriteString(usageColon(lang))
	sb.WriteString(fmt.Sprintf("%d%%", remaining))
	sb.WriteString("\n")
	sb.WriteString(usageResetLabel(lang))
	sb.WriteString(usageColon(lang))
	sb.WriteString(formatUsageResetTime(lang, window.ResetAfterSeconds))
	return sb.String()
}

func (e *Engine) renderUsageCard(report *UsageReport) *Card {
	lang := e.i18n.CurrentLang()
	return NewCard().
		Title(usageCardTitle(lang), "indigo").
		Markdown(strings.TrimSpace(formatUsageReport(report, lang))).
		Buttons(e.cardBackButton()).
		Build()
}

func formatUsageResetTime(lang Language, resetAfterSeconds int) string {
	if resetAfterSeconds <= 0 {
		switch lang {
		case LangChinese, LangTraditionalChinese:
			return "-"
		case LangJapanese:
			return "-"
		case LangSpanish:
			return "-"
		default:
			return "-"
		}
	}
	return formatDurationI18n(time.Duration(resetAfterSeconds)*time.Second, lang)
}

func usageAccountLabel(lang Language) string {
	switch lang {
	case LangChinese:
		return "账号："
	case LangTraditionalChinese:
		return "帳號："
	case LangJapanese:
		return "アカウント: "
	case LangSpanish:
		return "Cuenta: "
	default:
		return "Account: "
	}
}

func usageWindowLabel(lang Language, seconds int) string {
	switch seconds {
	case 18000:
		switch lang {
		case LangChinese:
			return "5小时限额"
		case LangTraditionalChinese:
			return "5小時限額"
		case LangJapanese:
			return "5時間枠"
		case LangSpanish:
			return "Límite 5h"
		default:
			return "5h limit"
		}
	case 604800:
		switch lang {
		case LangChinese:
			return "7日限额"
		case LangTraditionalChinese:
			return "7日限額"
		case LangJapanese:
			return "7日枠"
		case LangSpanish:
			return "Límite 7d"
		default:
			return "7d limit"
		}
	default:
		switch lang {
		case LangChinese, LangTraditionalChinese:
			return formatDurationI18n(time.Duration(seconds)*time.Second, lang) + "限额"
		case LangJapanese:
			return formatDurationI18n(time.Duration(seconds)*time.Second, lang) + "枠"
		case LangSpanish:
			return "Límite " + formatDurationI18n(time.Duration(seconds)*time.Second, lang)
		default:
			return formatDurationI18n(time.Duration(seconds)*time.Second, lang) + " limit"
		}
	}
}

func usageRemainingLabel(lang Language) string {
	switch lang {
	case LangChinese:
		return "剩余"
	case LangTraditionalChinese:
		return "剩餘"
	case LangJapanese:
		return "残り"
	case LangSpanish:
		return "restante"
	default:
		return "Remaining"
	}
}

func usageResetLabel(lang Language) string {
	switch lang {
	case LangChinese:
		return "重置"
	case LangTraditionalChinese:
		return "重置"
	case LangJapanese:
		return "リセット"
	case LangSpanish:
		return "Reinicio"
	default:
		return "Resets"
	}
}

func usageColon(lang Language) string {
	switch lang {
	case LangChinese, LangTraditionalChinese:
		return "："
	default:
		return ": "
	}
}

func usageCardTitle(lang Language) string {
	switch lang {
	case LangChinese:
		return "Usage"
	case LangTraditionalChinese:
		return "Usage"
	case LangJapanese:
		return "Usage"
	case LangSpanish:
		return "Usage"
	default:
		return "Usage"
	}
}

func usageUnavailableText(lang Language) string {
	switch lang {
	case LangChinese:
		return "暂无 usage 信息。"
	case LangTraditionalChinese:
		return "暫無 usage 資訊。"
	case LangJapanese:
		return "usage 情報はありません。"
	case LangSpanish:
		return "No hay datos de usage."
	default:
		return "Usage unavailable."
	}
}

func splitCardTitleBody(content string) (string, string) {
	content = strings.TrimSpace(content)
	parts := strings.SplitN(content, "\n\n", 2)
	title := strings.TrimSpace(parts[0])
	if len(parts) == 1 {
		return title, ""
	}
	return title, strings.TrimSpace(parts[1])
}

func (e *Engine) cardBackButton() CardButton {
	return DefaultBtn(e.i18n.T(MsgCardBack), "nav:/help")
}

func (e *Engine) modelCardBackButton() CardButton {
	return DefaultBtn(e.i18n.T(MsgCardBack), "nav:/model")
}

func (e *Engine) cardPrevButton(action string) CardButton {
	return DefaultBtn(e.i18n.T(MsgCardPrev), action)
}

func (e *Engine) cardNextButton(action string) CardButton {
	return DefaultBtn(e.i18n.T(MsgCardNext), action)
}

// simpleCard builds a card with a title, markdown body and a single Back button.
// Used to reduce repetition across render functions that share this pattern.
func (e *Engine) simpleCard(title, color, content string) *Card {
	return NewCard().Title(title, color).Markdown(content).Buttons(e.cardBackButton()).Build()
}

// renderListCardSafe wraps renderListCard and returns an error card on failure.
func (e *Engine) renderListCardSafe(sessionKey string, page int) *Card {
	card, err := e.renderListCard(sessionKey, page)
	if err != nil {
		agent, _ := e.sessionContextForKey(sessionKey)
		return e.simpleCard(e.i18n.Tf(MsgCardTitleSessions, agent.Name(), 0), "red", err.Error())
	}
	return card
}

// renderDirCardSafe wraps renderDirCard and returns an error card on failure.
func (e *Engine) renderDirCardSafe(sessionKey string, page int) *Card {
	card, err := e.renderDirCard(sessionKey, page)
	if err != nil {
		return e.simpleCard(e.i18n.T(MsgDirCardTitle), "red", err.Error())
	}
	return card
}

func (e *Engine) renderStatusCard(sessionKey string, userID string) *Card {
	agent, sessions := e.sessionContextForKey(sessionKey)
	platNames := make([]string, len(e.platforms))
	for i, pl := range e.platforms {
		platNames[i] = pl.Name()
	}
	platformStr := strings.Join(platNames, ", ")
	if len(platNames) == 0 {
		platformStr = "-"
	}

	workDirStr := ""
	if wd, ok := agent.(interface{ GetWorkDir() string }); ok {
		workDirStr = strings.TrimSpace(wd.GetWorkDir())
	}
	if workDirStr == "" {
		workDirStr, _ = os.Getwd()
	}

	uptimeStr := formatDurationI18n(time.Since(e.startedAt), e.i18n.CurrentLang())

	cur := e.i18n.CurrentLang()
	langStr := fmt.Sprintf("%s (%s)", string(cur), langDisplayName(cur))

	var modeStr string
	if ms, ok := agent.(ModeSwitcher); ok {
		mode := ms.GetMode()
		if mode != "" {
			modeStr = e.i18n.Tf(MsgStatusMode, mode)
		}
	}
	thinkingStr := e.i18n.T(MsgDisabledShort)
	if e.display.ThinkingMessages {
		thinkingStr = e.i18n.T(MsgEnabledShort)
	}
	toolStr := e.i18n.T(MsgDisabledShort)
	if e.display.ToolMessages {
		toolStr = e.i18n.T(MsgEnabledShort)
	}
	modeStr += e.i18n.Tf(MsgStatusThinkingMessages, thinkingStr)
	modeStr += e.i18n.Tf(MsgStatusToolMessages, toolStr)

	s := sessions.GetOrCreateActive(sessionKey)
	sessionDisplayName := sessions.GetSessionName(s.GetAgentSessionID())
	if sessionDisplayName == "" {
		sessionDisplayName = s.GetName()
	}
	sessionStr := e.i18n.Tf(MsgStatusSession, sessionDisplayName, len(s.History))

	var cronStr string
	if e.cronScheduler != nil {
		if jobs := e.cronScheduler.Store().ListBySessionKey(sessionKey); len(jobs) > 0 {
			enabledCount := 0
			for _, j := range jobs {
				if j.Enabled {
					enabledCount++
				}
			}
			cronStr = e.i18n.Tf(MsgStatusCron, len(jobs), enabledCount)
		}
	}

	sessionKeyStr := e.i18n.Tf(MsgStatusSessionKey, sessionKey)

	agentSIDStr := ""
	if agentSID := s.GetAgentSessionID(); agentSID != "" {
		agentSIDStr = e.i18n.Tf(MsgStatusAgentSID, agentSID)
	}

	userIDStr := ""
	if userID != "" {
		userIDStr = e.i18n.Tf(MsgStatusUserID, userID)
	}

	statusText := e.i18n.Tf(MsgStatusTitle,
		e.name,
		agent.Name(),
		workDirStr,
		platformStr,
		uptimeStr,
		langStr,
		modeStr,
		sessionStr,
		cronStr,
		sessionKeyStr,
		agentSIDStr,
		userIDStr,
	)
	title, body := splitCardTitleBody(statusText)

	return NewCard().
		Title(title, "green").
		Markdown(body).
		Buttons(e.cardBackButton()).
		Build()
}

func cronTimeFormat(t, now time.Time) string {
	if t.Year() != now.Year() {
		return "2006-01-02 15:04"
	}
	return "01-02 15:04"
}

func formatDurationI18n(d time.Duration, lang Language) string {
	d = d.Round(time.Second)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	switch lang {
	case LangChinese, LangTraditionalChinese:
		if days > 0 {
			return fmt.Sprintf("%d天 %d小时 %d分钟", days, hours, minutes)
		}
		if hours > 0 {
			return fmt.Sprintf("%d小时 %d分钟", hours, minutes)
		}
		return fmt.Sprintf("%d分钟", minutes)
	case LangJapanese:
		if days > 0 {
			return fmt.Sprintf("%d日 %d時間 %d分", days, hours, minutes)
		}
		if hours > 0 {
			return fmt.Sprintf("%d時間 %d分", hours, minutes)
		}
		return fmt.Sprintf("%d分", minutes)
	case LangSpanish:
		if days > 0 {
			return fmt.Sprintf("%d días %dh %dm", days, hours, minutes)
		}
		if hours > 0 {
			return fmt.Sprintf("%dh %dm", hours, minutes)
		}
		return fmt.Sprintf("%dm", minutes)
	default:
		if days > 0 {
			return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
		}
		if hours > 0 {
			return fmt.Sprintf("%dh %dm", hours, minutes)
		}
		return fmt.Sprintf("%dm", minutes)
	}
}

func (e *Engine) cmdHistory(p Platform, msg *Message, args []string) {
	if len(args) == 0 && supportsCards(p) {
		e.replyWithCard(p, msg.ReplyCtx, e.renderHistoryCard(msg.SessionKey))
		return
	}
	if len(args) == 0 {
		args = []string{"10"}
	}

	agent, sessions, _, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}
	s := sessions.GetOrCreateActive(msg.SessionKey)
	n := 10
	if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
		n = v
	}

	entries := s.GetHistory(n)
	agentSID := s.GetAgentSessionID()
	if len(entries) == 0 && agentSID != "" {
		if hp, ok := agent.(HistoryProvider); ok {
			if agentEntries, err := hp.GetSessionHistory(e.ctx, agentSID, n); err == nil {
				entries = agentEntries
			}
		}
	}

	if len(entries) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgHistoryEmpty))
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📜 History (last %d):\n\n", len(entries)))
	for _, h := range entries {
		icon := "👤"
		if h.Role == "assistant" {
			icon = "🤖"
		}
		content := h.Content
		if len([]rune(content)) > 200 {
			content = string([]rune(content)[:200]) + "..."
		}
		sb.WriteString(fmt.Sprintf("%s [%s]\n%s\n\n", icon, h.Timestamp.Format("15:04:05"), content))
	}
	e.reply(p, msg.ReplyCtx, sb.String())
}

func (e *Engine) cmdLang(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		cur := e.i18n.CurrentLang()
		name := langDisplayName(cur)
		text := e.i18n.Tf(MsgLangCurrent, name)
		buttons := [][]ButtonOption{
			{
				{Text: "English", Data: "cmd:/lang en"},
				{Text: "中文", Data: "cmd:/lang zh"},
				{Text: "繁體中文", Data: "cmd:/lang zh-TW"},
			},
			{
				{Text: "日本語", Data: "cmd:/lang ja"},
				{Text: "Español", Data: "cmd:/lang es"},
				{Text: "Auto", Data: "cmd:/lang auto"},
			},
		}
		if supportsCards(p) {
			e.replyWithCard(p, msg.ReplyCtx, e.renderLangCard())
			return
		}
		if _, ok := p.(InlineButtonSender); ok {
			e.replyWithButtons(p, msg.ReplyCtx, text, buttons)
			return
		}
		var sb strings.Builder
		sb.WriteString(text)
		sb.WriteString("\n\n")
		sb.WriteString("- English: `/lang en`\n")
		sb.WriteString("- 中文: `/lang zh`\n")
		sb.WriteString("- 繁體中文: `/lang zh-TW`\n")
		sb.WriteString("- 日本語: `/lang ja`\n")
		sb.WriteString("- Español: `/lang es`\n")
		sb.WriteString("- Auto: `/lang auto`")
		e.reply(p, msg.ReplyCtx, sb.String())
		return
	}

	target := strings.ToLower(strings.TrimSpace(args[0]))
	var lang Language
	switch target {
	case "en", "english":
		lang = LangEnglish
	case "zh", "cn", "chinese", "中文":
		lang = LangChinese
	case "zh-tw", "zh_tw", "zhtw", "繁體", "繁体":
		lang = LangTraditionalChinese
	case "ja", "jp", "japanese", "日本語":
		lang = LangJapanese
	case "es", "spanish", "español":
		lang = LangSpanish
	case "auto":
		lang = LangAuto
	default:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgLangInvalid))
		return
	}

	e.i18n.SetLang(lang)
	name := langDisplayName(lang)
	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgLangChanged, name))
}

func langDisplayName(lang Language) string {
	switch lang {
	case LangEnglish:
		return "English"
	case LangChinese:
		return "中文"
	case LangTraditionalChinese:
		return "繁體中文"
	case LangJapanese:
		return "日本語"
	case LangSpanish:
		return "Español"
	default:
		return "Auto"
	}
}

func (e *Engine) cmdHelp(p Platform, msg *Message) {
	if !supportsCards(p) {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgHelp))
		return
	}
	e.replyWithCard(p, msg.ReplyCtx, e.renderHelpCard())
}

const defaultHelpGroup = "session"

type helpCardItem struct {
	command string
	action  string
}

type helpCardGroup struct {
	key      string
	titleKey MsgKey
	items    []helpCardItem
}

func helpCardGroups() []helpCardGroup {
	return []helpCardGroup{
		{
			key:      "session",
			titleKey: MsgHelpSessionSection,
			items: []helpCardItem{
				{command: "/new", action: "act:/new"},
				{command: "/list", action: "nav:/list"},
				{command: "/current", action: "nav:/current"},
				{command: "/switch", action: "nav:/list"},
				{command: "/search", action: "cmd:/search"},
				{command: "/history", action: "nav:/history"},
				{command: "/delete", action: "cmd:/delete"},
				{command: "/name", action: "cmd:/name"},
			},
		},
		{
			key:      "agent",
			titleKey: MsgHelpAgentSection,
			items: []helpCardItem{
				{command: "/model", action: "nav:/model"},
				{command: "/reasoning", action: "nav:/reasoning"},
				{command: "/mode", action: "nav:/mode"},
				{command: "/lang", action: "nav:/lang"},
				{command: "/provider", action: "nav:/provider"},
				{command: "/memory", action: "cmd:/memory"},
				{command: "/allow", action: "cmd:/allow"},
				{command: "/quiet", action: "cmd:/quiet"},
				{command: "/tts", action: "cmd:/tts"},
			},
		},
		{
			key:      "tools",
			titleKey: MsgHelpToolsSection,
			items: []helpCardItem{
				{command: "/shell", action: "cmd:/shell"},
				{command: "/show", action: "cmd:/show"},
				{command: "/cron", action: "nav:/cron"},
				{command: "/heartbeat", action: "nav:/heartbeat"},
				{command: "/commands", action: "nav:/commands"},
				{command: "/alias", action: "nav:/alias"},
				{command: "/skills", action: "nav:/skills"},
				{command: "/compress", action: "cmd:/compress"},
				{command: "/stop", action: "act:/stop"},
			},
		},
		{
			key:      "system",
			titleKey: MsgHelpSystemSection,
			items: []helpCardItem{
				{command: "/status", action: "nav:/status"},
				{command: "/doctor", action: "nav:/doctor"},
				{command: "/usage", action: "cmd:/usage"},
				{command: "/config", action: "nav:/config"},
				{command: "/bind", action: "cmd:/bind"},
				{command: "/workspace", action: "cmd:/workspace"},
				{command: "/dir", action: "nav:/dir"},
				{command: "/version", action: "nav:/version"},
				{command: "/upgrade", action: "nav:/upgrade"},
				{command: "/restart", action: "cmd:/restart"},
			},
		},
	}
}

func (e *Engine) renderHelpCard() *Card {
	return e.renderHelpGroupCard(defaultHelpGroup)
}

// splitHelpTabRows splits tab buttons into rows. Card-based platforms
// get 2 buttons per row for better layout; others get all in one row.
func splitHelpTabRows(useMultiRow bool, tabs []CardButton) [][]CardButton {
	if useMultiRow {
		rows := make([][]CardButton, 0, (len(tabs)+1)/2)
		for i := 0; i < len(tabs); i += 2 {
			end := i + 2
			if end > len(tabs) {
				end = len(tabs)
			}
			rows = append(rows, tabs[i:end])
		}
		return rows
	}
	return [][]CardButton{tabs}
}

func (e *Engine) renderHelpGroupCard(groupKey string) *Card {
	sectionTitle := func(key MsgKey) string {
		section := e.i18n.T(key)
		if idx := strings.IndexByte(section, '\n'); idx >= 0 {
			return section[:idx]
		}
		return section
	}
	tabLabel := func(key MsgKey) string {
		return strings.Trim(sectionTitle(key), "* ")
	}
	commandText := func(command string) string {
		return "**" + command + "**  " + e.i18n.T(MsgKey(strings.TrimPrefix(command, "/")))
	}

	groups := helpCardGroups()
	current := groups[0]
	normalizedGroup := strings.ToLower(strings.TrimSpace(groupKey))
	for _, group := range groups {
		if group.key == normalizedGroup {
			current = group
			break
		}
	}

	cb := NewCard().Title(e.i18n.T(MsgHelpTitle), "blue")
	var tabs []CardButton
	for _, group := range groups {
		btnType := "default"
		if group.key == current.key {
			btnType = "primary"
		}
		tabs = append(tabs, Btn(tabLabel(group.titleKey), btnType, "nav:/help "+group.key))
	}
	for _, row := range splitHelpTabRows(true, tabs) {
		cb.ButtonsEqual(row...)
	}
	for _, item := range current.items {
		cb.ListItem(commandText(item.command), "▶", item.action)
	}
	cb.Note(e.i18n.T(MsgHelpTip))
	return cb.Build()
}

// GetAllCommands returns all available commands for bot menu registration.
// It includes built-in commands (with localized descriptions) and custom commands.
func (e *Engine) GetAllCommands() []BotCommandInfo {
	var commands []BotCommandInfo

	e.userRolesMu.RLock()
	disabledCmds := e.disabledCmds
	e.userRolesMu.RUnlock()

	// Collect built-in  commands (use primary name, first in names list)
	seenCmds := make(map[string]bool)
	for _, c := range builtinCommands {
		if len(c.names) == 0 {
			continue
		}
		// Use id as primary
		primaryName := c.id
		if seenCmds[primaryName] {
			continue
		}
		seenCmds[primaryName] = true

		// Skip disabled commands
		if disabledCmds[c.id] {
			continue
		}

		commands = append(commands, BotCommandInfo{
			Command:     primaryName,
			Description: e.i18n.T(MsgKey(primaryName)),
		})
	}

	// Collect custom commands from CommandRegistry
	for _, c := range e.commands.ListAll() {
		if seenCmds[strings.ToLower(c.Name)] {
			continue
		}
		seenCmds[strings.ToLower(c.Name)] = true

		desc := c.Description
		if desc == "" {
			desc = "Custom command"
		}

		commands = append(commands, BotCommandInfo{
			Command:     c.Name,
			Description: desc,
		})
	}

	// Collect skills
	for _, s := range e.skills.ListAll() {
		lowerName := strings.ToLower(s.Name)
		if seenCmds[lowerName] {
			continue
		}
		if disabledCmds[lowerName] {
			continue
		}
		seenCmds[lowerName] = true

		desc := s.Description
		if desc == "" {
			desc = "Skill"
		}

		commands = append(commands, BotCommandInfo{
			Command:     s.Name,
			Description: desc,
			IsSkill:     true,
		})
	}

	return commands
}

func (e *Engine) menuCommandsForPlatform(platformName string) ([]BotCommandInfo, bool) {
	commands := e.GetAllCommands()
	if !strings.EqualFold(platformName, "telegram") {
		return commands, false
	}
	return telegramMenuCommandsAllOrNone(commands)
}

func telegramMenuCommandsAllOrNone(commands []BotCommandInfo) ([]BotCommandInfo, bool) {
	var nonSkill []BotCommandInfo
	var skill []BotCommandInfo
	for _, command := range commands {
		if command.IsSkill {
			skill = append(skill, command)
			continue
		}
		nonSkill = append(nonSkill, command)
	}

	if len(telegramMenuEntryNames(append(append([]BotCommandInfo{}, nonSkill...), skill...))) <= telegramBotCommandLimit {
		return commands, false
	}
	return nonSkill, len(skill) > 0
}

func telegramMenuEntryNames(commands []BotCommandInfo) []string {
	var names []string
	seen := make(map[string]bool)
	for _, command := range commands {
		name := sanitizeTelegramMenuCommand(command.Command)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

func sanitizeTelegramMenuCommand(cmd string) string {
	cmd = strings.ToLower(cmd)
	var b strings.Builder
	for _, c := range cmd {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	result := b.String()
	for strings.Contains(result, "__") {
		result = strings.ReplaceAll(result, "__", "_")
	}
	result = strings.Trim(result, "_")
	if len(result) == 0 || result[0] < 'a' || result[0] > 'z' {
		return ""
	}
	if len(result) > 32 {
		result = result[:32]
	}
	return result
}

func (e *Engine) cmdModel(p Platform, msg *Message, args []string) {
	agent, sessions, interactiveKey, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}

	switcher, ok := agent.(ModelSwitcher)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgModelNotSupported))
		return
	}

	if len(args) == 0 {
		if !supportsCards(p) {
			fetchCtx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
			defer cancel()
			models := switcher.AvailableModels(fetchCtx)

			var sb strings.Builder
			current := switcher.GetModel()
			if current == "" {
				sb.WriteString(e.i18n.T(MsgModelDefault))
			} else {
				sb.WriteString(e.i18n.Tf(MsgModelCurrent, current))
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
			sb.WriteString(e.i18n.T(MsgModelListTitle))
			var buttons [][]ButtonOption
			var row []ButtonOption
			for i, m := range models {
				marker := "  "
				if m.Name == current {
					marker = "> "
				}
				var line string
				if m.Alias != "" {
					line = fmt.Sprintf("%s%d. %s - %s\n", marker, i+1, m.Alias, m.Name)
				} else {
					desc := m.Desc
					if desc != "" {
						desc = " — " + desc
					}
					line = fmt.Sprintf("%s%d. %s%s\n", marker, i+1, m.Name, desc)
				}
				sb.WriteString(line)

				label := m.Name
				if m.Alias != "" {
					label = m.Alias
				}
				if m.Name == current {
					label = "▶ " + label
				}
				row = append(row, ButtonOption{Text: label, Data: fmt.Sprintf("cmd:/model switch %d", i+1)})
				if len(row) >= 3 {
					buttons = append(buttons, row)
					row = nil
				}
			}
			if len(row) > 0 {
				buttons = append(buttons, row)
			}
			sb.WriteString("\n")
			sb.WriteString(e.i18n.T(MsgModelUsage))
			e.replyWithButtons(p, msg.ReplyCtx, sb.String(), buttons)
			return
		}
		e.replyWithCard(p, msg.ReplyCtx, e.renderModelCard(msg.SessionKey))
		return
	}

	targetInput, ok := parseModelSwitchArgs(args)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgModelUsage))
		return
	}

	target := strings.TrimSpace(targetInput)
	if modelSwitchNeedsLookup(target) {
		fetchCtx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
		defer cancel()
		models := switcher.AvailableModels(fetchCtx)
		target = resolveModelSwitchTarget(target, models)
	}

	target, err = e.switchModelOnAgent(agent, target, agent == e.agent)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgModelChangeFailed, err))
		return
	}
	e.cleanupInteractiveState(interactiveKey)

	s := sessions.GetOrCreateActive(msg.SessionKey)
	s.SetAgentSessionID("", "")
	s.ClearHistory()
	sessions.Save()

	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgModelChanged, target))
}

// resolveModelAlias resolves a user-supplied string to a model name.
// It first checks for an exact alias match, then falls back to the original value
// (which may be a direct model name).
func resolveModelAlias(models []ModelOption, input string) string {
	for _, m := range models {
		if m.Alias != "" && strings.EqualFold(m.Alias, input) {
			return m.Name
		}
	}
	return input
}

func resolveModelSwitchTarget(input string, models []ModelOption) string {
	input = strings.TrimSpace(input)
	if idx, err := strconv.Atoi(input); err == nil && idx >= 1 && idx <= len(models) {
		return models[idx-1].Name
	}
	if resolved := resolveModelAlias(models, input); resolved != input {
		return resolved
	}
	for _, m := range models {
		if strings.EqualFold(m.Name, input) {
			return m.Name
		}
	}
	return input
}

func modelSwitchNeedsLookup(input string) bool {
	input = strings.TrimSpace(input)
	if input == "" {
		return false
	}
	if _, err := strconv.Atoi(input); err == nil {
		return true
	}
	return !strings.Contains(input, "/")
}

func parseModelSwitchArgs(args []string) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	if len(args) == 1 {
		if strings.EqualFold(strings.TrimSpace(args[0]), "switch") {
			return "", false
		}
		return args[0], true
	}
	if strings.EqualFold(strings.TrimSpace(args[0]), "switch") && len(args) >= 2 {
		return strings.TrimSpace(args[1]), true
	}
	return "", false
}

// switchModel applies a runtime model selection to the global engine agent and
// persists the change so reloads keep the selected default.
func (e *Engine) switchModel(target string) (string, error) {
	return e.switchModelOnAgent(e.agent, target, true)
}

// switchModelOnAgent applies a runtime model selection to the provided agent.
// When persistConfig is true, config-backed model/provider changes are saved so
// reloads keep the new default. Workspace-scoped runtime switches pass false.
func (e *Engine) switchModelOnAgent(agent Agent, target string, persistConfig bool) (string, error) {
	switcher, ok := agent.(ModelSwitcher)
	if !ok {
		return target, nil
	}

	providerSwitcher, ok := agent.(ProviderSwitcher)
	if !ok {
		if e.modelSaveFunc != nil {
			if err := e.modelSaveFunc(target); err != nil {
				return "", fmt.Errorf("save model: %w", err)
			}
		}
		switcher.SetModel(target)
		return target, nil
	}
	active := providerSwitcher.GetActiveProvider()
	if active == nil {
		if e.modelSaveFunc != nil {
			if err := e.modelSaveFunc(target); err != nil {
				return "", fmt.Errorf("save model: %w", err)
			}
		}
		switcher.SetModel(target)
		return target, nil
	}

	providers := providerSwitcher.ListProviders()
	updated, found := SetProviderModel(providers, active.Name, target)
	if !found {
		switcher.SetModel(target)
		return target, nil
	}
	if !persistConfig {
		switcher.SetModel(target)
		return target, nil
	}
	if persistConfig && e.providerModelSaveFunc != nil {
		if err := e.providerModelSaveFunc(active.Name, target); err != nil {
			return "", fmt.Errorf("save provider model %q: %w", active.Name, err)
		}
	}
	providerSwitcher.SetProviders(updated)
	switcher.SetModel(target)
	providerSwitcher.SetActiveProvider(active.Name)
	return target, nil
}

func (e *Engine) cmdReasoning(p Platform, msg *Message, args []string) {
	switcher, ok := e.agent.(ReasoningEffortSwitcher)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgReasoningNotSupported))
		return
	}

	if len(args) == 0 {
		if !supportsCards(p) {
			efforts := switcher.AvailableReasoningEfforts()

			var sb strings.Builder
			current := switcher.GetReasoningEffort()
			if current == "" {
				sb.WriteString(e.i18n.T(MsgReasoningDefault))
			} else {
				sb.WriteString(e.i18n.Tf(MsgReasoningCurrent, current))
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
			sb.WriteString(e.i18n.T(MsgReasoningListTitle))
			var buttons [][]ButtonOption
			var row []ButtonOption
			for i, effort := range efforts {
				marker := "  "
				if effort == current {
					marker = "> "
				}
				sb.WriteString(fmt.Sprintf("%s%d. %s\n", marker, i+1, effort))

				label := effort
				if effort == current {
					label = "▶ " + label
				}
				row = append(row, ButtonOption{Text: label, Data: fmt.Sprintf("cmd:/reasoning %d", i+1)})
				if len(row) >= 3 {
					buttons = append(buttons, row)
					row = nil
				}
			}
			if len(row) > 0 {
				buttons = append(buttons, row)
			}
			sb.WriteString("\n")
			sb.WriteString(e.i18n.T(MsgReasoningUsage))
			e.replyWithButtons(p, msg.ReplyCtx, sb.String(), buttons)
			return
		}
		e.replyWithCard(p, msg.ReplyCtx, e.renderReasoningCard())
		return
	}

	efforts := switcher.AvailableReasoningEfforts()
	target := strings.ToLower(strings.TrimSpace(args[0]))
	if idx, err := strconv.Atoi(target); err == nil && idx >= 1 && idx <= len(efforts) {
		target = efforts[idx-1]
	}

	valid := false
	for _, effort := range efforts {
		if effort == target {
			valid = true
			break
		}
	}
	if !valid {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgReasoningUsage))
		return
	}

	switcher.SetReasoningEffort(target)
	e.cleanupInteractiveState(e.interactiveKeyForSessionKey(msg.SessionKey))

	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	s.SetAgentSessionID("", "")
	s.ClearHistory()
	e.sessions.Save()

	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgReasoningChanged, target))
}

func (e *Engine) cmdMode(p Platform, msg *Message, args []string) {
	switcher, ok := e.agent.(ModeSwitcher)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgModeNotSupported))
		return
	}

	if len(args) == 0 {
		if !supportsCards(p) {
			current := switcher.GetMode()
			modes := switcher.PermissionModes()
			var sb strings.Builder
			zhLike := e.i18n.IsZhLike()
			for _, m := range modes {
				suffix := ""
				if m.Key == current {
					if zhLike {
						suffix = "（当前）"
					} else {
						suffix = " (current)"
					}
				}
				if zhLike {
					sb.WriteString(fmt.Sprintf("**%s**%s — %s\n", m.NameZh, suffix, m.DescZh))
				} else {
					sb.WriteString(fmt.Sprintf("**%s**%s — %s\n", m.Name, suffix, m.Desc))
				}
			}
			sb.WriteString(e.modeUsageText(modes))

			var buttons [][]ButtonOption
			var row []ButtonOption
			for _, m := range modes {
				label := m.Name
				if zhLike {
					label = m.NameZh
				}
				row = append(row, ButtonOption{Text: label, Data: "cmd:/mode " + m.Key})
				if len(row) >= 2 {
					buttons = append(buttons, row)
					row = nil
				}
			}
			if len(row) > 0 {
				buttons = append(buttons, row)
			}
			e.replyWithButtons(p, msg.ReplyCtx, sb.String(), buttons)
			return
		}
		e.replyWithCard(p, msg.ReplyCtx, e.renderModeCard())
		return
	}

	target := strings.ToLower(args[0])
	switcher.SetMode(target)
	newMode := switcher.GetMode()
	appliedLive := e.applyLiveModeChange(msg.SessionKey, newMode)

	if !appliedLive {
		e.cleanupInteractiveState(e.interactiveKeyForSessionKey(msg.SessionKey))
	}

	modes := switcher.PermissionModes()
	displayName := newMode
	zhLike := e.i18n.IsZhLike()
	for _, m := range modes {
		if m.Key == newMode {
			if zhLike {
				displayName = m.NameZh
			} else {
				displayName = m.Name
			}
			break
		}
	}
	reply := fmt.Sprintf(e.i18n.T(MsgModeChanged), displayName)
	if appliedLive {
		reply += "\n\n(Current session updated immediately.)"
	}
	e.reply(p, msg.ReplyCtx, reply)
}

func (e *Engine) modeUsageText(modes []PermissionModeInfo) string {
	keys := make([]string, 0, len(modes))
	for _, mode := range modes {
		keys = append(keys, "`"+mode.Key+"`")
	}
	return e.i18n.Tf(MsgModeUsage, strings.Join(keys, " / "))
}

func (e *Engine) applyLiveModeChange(sessionKey, mode string) bool {
	iKey := e.interactiveKeyForSessionKey(sessionKey)
	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[iKey]
	e.interactiveMu.Unlock()
	if !ok || state == nil || state.agentSession == nil || !state.agentSession.Alive() {
		return false
	}
	switcher, ok := state.agentSession.(LiveModeSwitcher)
	if !ok {
		return false
	}
	return switcher.SetLiveMode(mode)
}

func (e *Engine) cmdQuiet(p Platform, msg *Message, args []string) {
	// /quiet [full|compact|quiet]
	// Without argument: cycle full → quiet → compact → full.
	// With argument: set mode directly.
	var newMode string
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "full", "compact", "quiet":
			newMode = strings.ToLower(args[0])
		default:
			e.reply(p, msg.ReplyCtx, "Usage: /quiet [full|compact|quiet]")
			return
		}
	} else {
		switch e.display.Mode {
		case "full", "":
			newMode = "quiet"
		case "quiet":
			newMode = "compact"
		default: // "compact" or unknown
			newMode = "full"
		}
	}

	e.display.Mode = newMode
	switch newMode {
	case "compact", "quiet":
		e.display.ThinkingMessages = false
		e.display.ToolMessages = false
	default:
		e.display.ThinkingMessages = true
		e.display.ToolMessages = true
	}

	if e.displaySaveFunc != nil {
		tm := e.display.ThinkingMessages
		tool := e.display.ToolMessages
		if err := e.displaySaveFunc(&newMode, &tm, nil, nil, &tool); err != nil {
			slog.Error("failed to persist display config after /quiet", "error", err)
		}
	}

	switch newMode {
	case "quiet":
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgQuietOn))
	case "compact":
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDisplayModeCompact))
	default:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgQuietOff))
	}
}

func (e *Engine) cmdTTS(p Platform, msg *Message, args []string) {
	if e.tts == nil || !e.tts.Enabled || e.tts.TTS == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgTTSNotEnabled))
		return
	}
	if len(args) == 0 {
		providerStr := e.tts.Provider
		if providerStr == "" {
			providerStr = "unknown"
		}
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgTTSStatus), e.tts.GetTTSMode(), providerStr))
		return
	}
	switch args[0] {
	case "always", "voice_only":
		mode := args[0]
		e.tts.SetTTSMode(mode)
		if e.ttsSaveFunc != nil {
			if err := e.ttsSaveFunc(mode); err != nil {
				slog.Warn("tts: failed to persist mode", "error", err)
			}
		}
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgTTSSwitched), mode))
	default:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgTTSUsage))
	}
}

func (e *Engine) cmdStop(p Platform, msg *Message) {
	iKey := e.interactiveKeyForSessionKey(msg.SessionKey)
	if !e.stopInteractiveSession(iKey, p, msg.ReplyCtx) {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgNoExecution))
		return
	}
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgExecutionStopped))
}

func (e *Engine) stopInteractiveSession(sessionKey string, quietPlatform Platform, quietReplyCtx any) bool {
	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[sessionKey]
	if !ok || state == nil {
		e.interactiveMu.Unlock()
		return false
	}

	state.mu.Lock()
	pending := state.pending
	state.pending = nil
	agentSession := state.agentSession
	state.mu.Unlock()

	state.markStopped()
	delete(e.interactiveStates, sessionKey)
	e.interactiveMu.Unlock()

	if pending != nil {
		pending.resolve()
	}
	e.notifyDroppedQueuedMessages(state, fmt.Errorf("session reset"))
	e.closeAgentSessionAsync(sessionKey, agentSession)
	return true
}

func (e *Engine) cmdCompress(p Platform, msg *Message) {
	compressor, ok := e.agent.(ContextCompressor)
	if !ok || compressor.CompressCommand() == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCompressNotSupported))
		return
	}

	iKey := e.interactiveKeyForSessionKey(msg.SessionKey)
	e.interactiveMu.Lock()
	state, hasState := e.interactiveStates[iKey]
	e.interactiveMu.Unlock()

	if !hasState || state == nil || state.agentSession == nil || !state.agentSession.Alive() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCompressNoSession))
		return
	}

	_, sessions := e.sessionContextForKey(msg.SessionKey)
	session := sessions.GetOrCreateActive(msg.SessionKey)
	if !session.TryLock() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPreviousProcessing))
		return
	}

	e.send(p, msg.ReplyCtx, e.i18n.T(MsgCompressing))

	go e.runCompress(state, session, sessions, iKey, p, msg.ReplyCtx, false)
}

// runCompress sends the agent's compress command and handles results.
// If autoTriggered is true, suppress user-visible "compressing" and completion messages.
func (e *Engine) runCompress(state *interactiveState, session *Session, sessions *SessionManager, iKey string, p Platform, replyCtx any, auto bool) {
	// session.Unlock() is called inside drainQueuedMessagesAfterCompress
	// while holding state.mu to close the race window. Deferred fallback
	// ensures the lock is released on early-return paths.
	compressUnlocked := false
	defer func() {
		if !compressUnlocked {
			session.Unlock()
		}
	}()

	state.mu.Lock()
	state.platform = p
	state.replyCtx = replyCtx
	state.mu.Unlock()

	drainEvents(state.agentSession.Events())

	compressor, ok := e.agent.(ContextCompressor)
	if !ok || compressor.CompressCommand() == "" {
		if !auto {
			e.reply(p, replyCtx, e.i18n.T(MsgCompressNotSupported))
		}
		return
	}

	cmd := compressor.CompressCommand()
	if err := state.agentSession.Send(cmd, nil, nil); err != nil {
		if !auto {
			e.reply(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgError), err))
		}
		if !state.agentSession.Alive() {
			e.cleanupInteractiveState(iKey)
		}
		return
	}

	e.processCompressEvents(state, session, sessions, iKey, p, replyCtx, &compressUnlocked, auto)
}

// processCompressEvents drains agent events after a compress command.
// Unlike processInteractiveEvents it does NOT record history and treats
// an empty result as success rather than "(empty response)".
func (e *Engine) processCompressEvents(state *interactiveState, session *Session, sessions *SessionManager, sessionKey string, p Platform, replyCtx any, unlocked *bool, auto bool) {

	var textParts []string
	events := state.agentSession.Events()
	stopCh := state.stopSignal()

	var idleTimer *time.Timer
	var idleCh <-chan time.Time
	if e.eventIdleTimeout > 0 {
		idleTimer = time.NewTimer(e.eventIdleTimeout)
		defer idleTimer.Stop()
		idleCh = idleTimer.C
	}

	for {
		var event Event
		var ok bool

		select {
		case <-stopCh:
			return
		case event, ok = <-events:
			if !ok {
				e.cleanupInteractiveState(sessionKey, state)
				if !auto {
					if len(textParts) > 0 {
						e.send(p, replyCtx, strings.Join(textParts, ""))
					} else {
						e.reply(p, replyCtx, e.i18n.T(MsgCompressDone))
					}
				}
				e.notifyDroppedQueuedMessages(state, fmt.Errorf("agent process exited during compress"))
				return
			}
		case <-idleCh:
			if !auto {
				e.send(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgError), "compress timed out"))
			}
			e.cleanupInteractiveState(sessionKey, state)
			e.notifyDroppedQueuedMessages(state, fmt.Errorf("compress timed out"))
			return
		case <-e.ctx.Done():
			return
		}

		if state.isStopped() {
			return
		}

		if idleTimer != nil {
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(e.eventIdleTimeout)
		}

		switch event.Type {
		case EventText:
			if !auto && event.Content != "" {
				textParts = append(textParts, event.Content)
			}
		case EventToolResult:
			if !auto {
				out := strings.TrimSpace(event.Content)
				if out == "" {
					out = strings.TrimSpace(event.ToolResult)
				}
				if out == "" {
					break
				}
				tn := strings.TrimSpace(event.ToolName)
				if tn == "" {
					tn = "tool"
				}
				textParts = append(textParts, fmt.Sprintf(e.i18n.T(MsgToolResult), tn, out)+"\n")
			}
		case EventResult:
			result := event.Content
			if result == "" && len(textParts) > 0 {
				result = strings.Join(textParts, "")
			}
			if !auto {
				if result != "" {
					e.send(p, replyCtx, result)
				} else {
					e.reply(p, replyCtx, e.i18n.T(MsgCompressDone))
				}
			}

			// After compress succeeds, process any queued messages instead of dropping them.
			e.drainQueuedMessagesAfterCompress(state, session, sessions, sessionKey, unlocked)
			return
		case EventError:
			if !auto && event.Error != nil {
				e.reply(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgError), event.Error))
			}
			// Only drop queued messages if the agent is dead; some agents
			// emit per-turn EventError while staying alive.
			if !state.agentSession.Alive() {
				e.notifyDroppedQueuedMessages(state, event.Error)
			} else {
				// Agent survived — try to process queued messages.
				e.drainQueuedMessagesAfterCompress(state, session, sessions, sessionKey, unlocked)
			}
			return
		case EventPermissionRequest:
			_ = state.agentSession.RespondPermission(event.RequestID, PermissionResult{
				Behavior:     "allow",
				UpdatedInput: event.ToolInputRaw,
			})
		}
	}
}

// drainQueuedMessagesAfterCompress processes any messages that were queued
// during a /compress operation. It sends each one to the agent and runs the
// full interactive event loop for it.
func (e *Engine) drainQueuedMessagesAfterCompress(state *interactiveState, session *Session, sessions *SessionManager, sessionKey string, unlocked *bool) {
	if e.drainPendingMessages(state, session, sessions, sessionKey) {
		*unlocked = true
	}
}

func (e *Engine) cmdAllow(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		if auth, ok := e.agent.(ToolAuthorizer); ok {
			tools := auth.GetAllowedTools()
			if len(tools) == 0 {
				e.reply(p, msg.ReplyCtx, e.i18n.T(MsgNoToolsAllowed))
			} else {
				e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCurrentTools), strings.Join(tools, ", ")))
			}
		} else {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgToolAuthNotSupported))
		}
		return
	}

	toolName := strings.TrimSpace(args[0])
	if auth, ok := e.agent.(ToolAuthorizer); ok {
		if err := auth.AddAllowedTools(toolName); err != nil {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgToolAllowFailed), err))
			return
		}
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgToolAllowedNew), toolName))
	} else {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgToolAuthNotSupported))
	}
}

func (e *Engine) cmdProvider(p Platform, msg *Message, args []string) {
	switcher, ok := e.agent.(ProviderSwitcher)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProviderNotSupported))
		return
	}

	if len(args) == 0 {
		if supportsCards(p) {
			e.replyWithCard(p, msg.ReplyCtx, e.renderProviderCard())
			return
		}

		current := switcher.GetActiveProvider()
		providers := switcher.ListProviders()
		if current == nil && len(providers) == 0 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProviderNone))
			return
		}

		var sb strings.Builder
		if current != nil {
			sb.WriteString(fmt.Sprintf(e.i18n.T(MsgProviderCurrent), current.Name))
			sb.WriteString("\n\n")
		}
		sb.WriteString(e.i18n.T(MsgProviderListTitle))
		for _, prov := range providers {
			marker := "  "
			if current != nil && prov.Name == current.Name {
				marker = "▶ "
			}
			detail := prov.Name
			if prov.BaseURL != "" {
				detail += " (" + prov.BaseURL + ")"
			}
			if prov.Model != "" {
				detail += " [" + prov.Model + "]"
			}
			sb.WriteString(fmt.Sprintf("%s%s\n", marker, detail))
		}
		sb.WriteString("\n" + e.i18n.T(MsgProviderSwitchHint))
		e.reply(p, msg.ReplyCtx, sb.String())
		return
	}

	sub := matchSubCommand(strings.ToLower(args[0]), []string{
		"list", "add", "remove", "switch", "current", "clear", "reset", "none",
	})
	switch sub {
	case "list":
		providers := switcher.ListProviders()
		if len(providers) == 0 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProviderListEmpty))
			return
		}
		current := switcher.GetActiveProvider()
		var sb strings.Builder
		sb.WriteString(e.i18n.T(MsgProviderListTitle))
		for _, prov := range providers {
			marker := "  "
			if current != nil && prov.Name == current.Name {
				marker = "▶ "
			}
			detail := prov.Name
			if prov.BaseURL != "" {
				detail += " (" + prov.BaseURL + ")"
			}
			if prov.Model != "" {
				detail += " [" + prov.Model + "]"
			}
			sb.WriteString(fmt.Sprintf("%s%s\n", marker, detail))
		}
		sb.WriteString("\n" + e.i18n.T(MsgProviderSwitchHint))
		e.reply(p, msg.ReplyCtx, sb.String())

	case "add":
		e.cmdProviderAdd(p, msg, switcher, args[1:])

	case "remove", "rm", "delete":
		e.cmdProviderRemove(p, msg, switcher, args[1:])

	case "switch":
		if len(args) < 2 {
			e.reply(p, msg.ReplyCtx, "Usage: /provider switch <name>")
			return
		}
		e.switchProvider(p, msg, switcher, args[1])

	case "current":
		current := switcher.GetActiveProvider()
		if current == nil {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProviderNone))
			return
		}
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderCurrent), current.Name))

	case "clear", "reset", "none":
		switcher.SetActiveProvider("")
		e.cleanupInteractiveState(e.interactiveKeyForSessionKey(msg.SessionKey))
		if e.providerSaveFunc != nil {
			if err := e.providerSaveFunc(""); err != nil {
				slog.Error("failed to save provider", "error", err)
			}
		}
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProviderCleared))

	default:
		e.switchProvider(p, msg, switcher, args[0])
	}
}

func (e *Engine) cmdProviderAdd(p Platform, msg *Message, switcher ProviderSwitcher, args []string) {
	if len(args) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProviderAddUsage))
		return
	}

	var prov ProviderConfig

	// Join args back; detect JSON (starts with '{') vs positional
	raw := strings.Join(args, " ")
	raw = strings.TrimSpace(raw)

	if strings.HasPrefix(raw, "{") {
		// JSON format: /provider add {"name":"relay","api_key":"sk-xxx",...}
		var jp struct {
			Name    string            `json:"name"`
			APIKey  string            `json:"api_key"`
			BaseURL string            `json:"base_url"`
			Model   string            `json:"model"`
			Env     map[string]string `json:"env"`
		}
		if err := json.Unmarshal([]byte(raw), &jp); err != nil {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderAddFailed), "invalid JSON: "+err.Error()))
			return
		}
		if jp.Name == "" {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderAddFailed), "\"name\" is required"))
			return
		}
		prov = ProviderConfig{Name: jp.Name, APIKey: jp.APIKey, BaseURL: jp.BaseURL, Model: jp.Model, Env: jp.Env}
	} else {
		// Positional: /provider add <name> <api_key> [base_url] [model]
		if len(args) < 2 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProviderAddUsage))
			return
		}
		prov.Name = args[0]
		prov.APIKey = args[1]
		if len(args) > 2 {
			prov.BaseURL = args[2]
		}
		if len(args) > 3 {
			prov.Model = args[3]
		}
	}

	// Check for duplicates
	for _, existing := range switcher.ListProviders() {
		if existing.Name == prov.Name {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderAddFailed), fmt.Sprintf("provider %q already exists", prov.Name)))
			return
		}
	}

	// Add to runtime
	updated := append(switcher.ListProviders(), prov)
	switcher.SetProviders(updated)

	// Persist to config
	if e.providerAddSaveFunc != nil {
		if err := e.providerAddSaveFunc(prov); err != nil {
			slog.Error("failed to persist provider", "error", err)
		}
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderAdded), prov.Name, prov.Name))
}

func (e *Engine) cmdProviderRemove(p Platform, msg *Message, switcher ProviderSwitcher, args []string) {
	if len(args) == 0 {
		e.reply(p, msg.ReplyCtx, "Usage: /provider remove <name>")
		return
	}
	name := args[0]

	providers := switcher.ListProviders()
	found := false
	var remaining []ProviderConfig
	for _, prov := range providers {
		if prov.Name == name {
			found = true
		} else {
			remaining = append(remaining, prov)
		}
	}

	if !found {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderNotFound), name))
		return
	}

	// If removing the active provider, clear it
	active := switcher.GetActiveProvider()
	switcher.SetProviders(remaining)
	if active != nil && active.Name == name {
		// No active provider after removal
		slog.Info("removed active provider, clearing selection", "name", name)
	}

	// Persist
	if e.providerRemoveSaveFunc != nil {
		if err := e.providerRemoveSaveFunc(name); err != nil {
			slog.Error("failed to persist provider removal", "error", err)
		}
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderRemoved), name))
}

func (e *Engine) switchProvider(p Platform, msg *Message, switcher ProviderSwitcher, name string) {
	if !switcher.SetActiveProvider(name) {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderNotFound), name))
		return
	}
	e.cleanupInteractiveState(e.interactiveKeyForSessionKey(msg.SessionKey))

	if e.providerSaveFunc != nil {
		if err := e.providerSaveFunc(name); err != nil {
			slog.Error("failed to save provider", "error", err)
		}
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgProviderSwitched), name))
}

// ──────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────

// SendToSession sends a message to an active session from an external caller (API/CLI).
// If sessionKey is empty, it picks the first active session.
func (e *Engine) SendToSession(sessionKey, message string) error {
	return e.SendToSessionWithAttachments(sessionKey, message, nil, nil)
}

func (e *Engine) SendToSessionWithAttachments(sessionKey, message string, images []ImageAttachment, files []FileAttachment) error {
	e.interactiveMu.Lock()

	var state *interactiveState
	if sessionKey != "" {
		state = e.interactiveStates[sessionKey]
		if state == nil && e.multiWorkspace {
			if iKey := e.interactiveKeyForSessionKey(sessionKey); iKey != sessionKey {
				state = e.interactiveStates[iKey]
			}
		}
	} else if len(e.interactiveStates) == 1 {
		// Single session: use it when no sessionKey is provided (backward compatible)
		for _, s := range e.interactiveStates {
			state = s
			break
		}
	} else if len(e.interactiveStates) > 1 && (len(images) > 0 || len(files) > 0) {
		// Multiple sessions with attachments but no explicit sessionKey: ambiguous
		e.interactiveMu.Unlock()
		return fmt.Errorf("multiple active sessions; must specify --session to send attachments")
	} else {
		// Multiple sessions but text-only: pick the first (legacy behavior)
		for _, s := range e.interactiveStates {
			state = s
			break
		}
	}
	e.interactiveMu.Unlock()

	var p Platform
	var replyCtx any
	if state != nil {
		state.mu.Lock()
		p = state.platform
		replyCtx = state.replyCtx
		state.mu.Unlock()
	}

	if p == nil && sessionKey != "" {
		strippedKey := sessionKey
		platformName := ""
		if idx := strings.Index(strippedKey, ":"); idx > 0 {
			platformName = strippedKey[:idx]
		}
		var targetPlatform Platform
		for _, candidate := range e.platforms {
			if candidate.Name() == platformName {
				targetPlatform = candidate
				break
			}
		}
		// Fallback: multi-workspace mode may prefix the session key with the
		// workspace path (same heuristic as ExecuteCronJob / ExecuteHeartbeat).
		if targetPlatform == nil {
			for _, candidate := range e.platforms {
				needle := ":" + candidate.Name() + ":"
				if idx := strings.Index(strippedKey, needle); idx >= 0 {
					targetPlatform = candidate
					strippedKey = strippedKey[idx+1:]
					break
				}
			}
		}
		if targetPlatform != nil {
			rc, ok := targetPlatform.(ReplyContextReconstructor)
			if !ok {
				return fmt.Errorf("platform %q does not support proactive messaging", targetPlatform.Name())
			}
			reconstructed, err := rc.ReconstructReplyCtx(strippedKey)
			if err != nil {
				return fmt.Errorf("reconstruct reply context: %w", err)
			}
			p = targetPlatform
			replyCtx = reconstructed
		}
	}

	if p == nil {
		return fmt.Errorf("no active session found (key=%q)", sessionKey)
	}

	if message == "" && len(images) == 0 && len(files) == 0 {
		return fmt.Errorf("message or attachment is required")
	}
	if (len(images) > 0 || len(files) > 0) && !e.attachmentSendEnabled {
		return ErrAttachmentSendDisabled
	}

	var imageSender ImageSender
	if len(images) > 0 {
		var ok bool
		imageSender, ok = p.(ImageSender)
		if !ok {
			return fmt.Errorf("platform %s: %w", p.Name(), ErrNotSupported)
		}
	}

	var fileSender FileSender
	if len(files) > 0 {
		var ok bool
		fileSender, ok = p.(FileSender)
		if !ok {
			return fmt.Errorf("platform %s: %w", p.Name(), ErrNotSupported)
		}
	}

	if message != "" {
		if err := e.waitOutgoing(p); err != nil {
			return err
		}
		if err := p.Send(e.ctx, replyCtx, message); err != nil {
			return err
		}
		if state != nil && (len(images) > 0 || len(files) > 0) {
			state.mu.Lock()
			state.sideText = strings.TrimSpace(message)
			state.mu.Unlock()
		}
	}
	for _, img := range images {
		if err := e.waitOutgoing(p); err != nil {
			return err
		}
		if err := imageSender.SendImage(e.ctx, replyCtx, img); err != nil {
			return err
		}
	}
	for _, file := range files {
		if err := e.waitOutgoing(p); err != nil {
			return err
		}
		if err := fileSender.SendFile(e.ctx, replyCtx, file); err != nil {
			return err
		}
	}
	return nil
}

// sendPermissionPrompt sends a permission prompt with interactive buttons when
// the platform supports them. Fallback chain: InlineButtonSender → CardSender → plain text.
func (e *Engine) sendPermissionPrompt(p Platform, replyCtx any, prompt, toolName, toolInput string) {
	// Try inline buttons first (Telegram)
	if bs, ok := p.(InlineButtonSender); ok {
		buttons := [][]ButtonOption{
			{
				{Text: e.i18n.T(MsgPermBtnAllow), Data: "perm:allow"},
				{Text: e.i18n.T(MsgPermBtnDeny), Data: "perm:deny"},
			},
			{
				{Text: e.i18n.T(MsgPermBtnAllowAll), Data: "perm:allow_all"},
			},
		}
		if err := e.waitOutgoing(p); err != nil {
			slog.Warn("sendPermissionPrompt: outgoing wait cancelled", "platform", p.Name(), "error", err)
			return
		}
		if err := bs.SendWithButtons(e.ctx, replyCtx, prompt, buttons); err == nil {
			return
		} else {
			slog.Warn("sendPermissionPrompt: inline buttons failed, falling back", "error", err)
		}
	}

	// Try card with buttons (Feishu/Lark)
	if supportsCards(p) {
		body := fmt.Sprintf(e.i18n.T(MsgPermCardBody), toolName, toolInput)
		extra := func(label, color string) map[string]string {
			return map[string]string{
				"perm_label": label,
				"perm_color": color,
				"perm_body":  body,
			}
		}
		allowBtn := CardButton{Text: e.i18n.T(MsgPermBtnAllow), Type: "primary", Value: "perm:allow",
			Extra: extra("✅ "+e.i18n.T(MsgPermBtnAllow), "green")}
		denyBtn := CardButton{Text: e.i18n.T(MsgPermBtnDeny), Type: "danger", Value: "perm:deny",
			Extra: extra("❌ "+e.i18n.T(MsgPermBtnDeny), "red")}
		allowAllBtn := CardButton{Text: e.i18n.T(MsgPermBtnAllowAll), Type: "default", Value: "perm:allow_all",
			Extra: extra("✅ "+e.i18n.T(MsgPermBtnAllowAll), "green")}

		card := NewCard().
			Title(e.i18n.T(MsgPermCardTitle), "orange").
			Markdown(body).
			ButtonsEqual(allowBtn, denyBtn).
			Buttons(allowAllBtn).
			Note(e.i18n.T(MsgPermCardNote)).
			Build()
		e.sendWithCard(p, replyCtx, card)
		return
	}

	e.send(p, replyCtx, prompt)
}

// sendAskQuestionPrompt renders one question (by index) from the AskUserQuestion list.
// qIdx is the 0-based index of the question to display.
func (e *Engine) sendAskQuestionPrompt(p Platform, replyCtx any, questions []UserQuestion, qIdx int) {
	if qIdx >= len(questions) {
		return
	}
	q := questions[qIdx]
	total := len(questions)

	titleSuffix := ""
	if total > 1 {
		titleSuffix = fmt.Sprintf(" (%d/%d)", qIdx+1, total)
	}

	// Try card (Feishu/Lark)
	if supportsCards(p) {
		cb := NewCard().Title(e.i18n.T(MsgAskQuestionTitle)+titleSuffix, "blue")
		body := "**" + q.Question + "**"
		if q.MultiSelect {
			body += e.i18n.T(MsgAskQuestionMulti)
		}
		cb.Markdown(body)
		for i, opt := range q.Options {
			desc := opt.Label
			if opt.Description != "" {
				desc += " — " + opt.Description
			}
			answerData := fmt.Sprintf("askq:%d:%d", qIdx, i+1)
			cb.ListItemBtnExtra(desc, opt.Label, "default", answerData, map[string]string{
				"askq_label":    opt.Label,
				"askq_question": q.Question,
			})
		}
		cb.Note(e.i18n.T(MsgAskQuestionNote))
		e.sendWithCard(p, replyCtx, cb.Build())
		return
	}

	// Try inline buttons (Telegram)
	if bs, ok := p.(InlineButtonSender); ok {
		var textBuf strings.Builder
		textBuf.WriteString("❓ *")
		textBuf.WriteString(q.Question)
		textBuf.WriteString("*")
		textBuf.WriteString(titleSuffix)
		if q.MultiSelect {
			textBuf.WriteString(e.i18n.T(MsgAskQuestionMulti))
		}
		hasDesc := false
		for _, opt := range q.Options {
			if opt.Description != "" {
				hasDesc = true
				break
			}
		}
		if hasDesc {
			textBuf.WriteString("\n")
			for i, opt := range q.Options {
				textBuf.WriteString(fmt.Sprintf("\n*%d. %s*", i+1, opt.Label))
				if opt.Description != "" {
					textBuf.WriteString(" — ")
					textBuf.WriteString(opt.Description)
				}
			}
			textBuf.WriteString("\n")
		}
		var rows [][]ButtonOption
		for i, opt := range q.Options {
			rows = append(rows, []ButtonOption{{Text: opt.Label, Data: fmt.Sprintf("askq:%d:%d", qIdx, i+1)}})
		}
		if err := e.waitOutgoing(p); err != nil {
			slog.Warn("sendAskQuestionPrompt: outgoing wait cancelled", "platform", p.Name(), "error", err)
			return
		}
		if err := bs.SendWithButtons(e.ctx, replyCtx, textBuf.String(), rows); err == nil {
			return
		}
	}

	// Plain text fallback
	var sb strings.Builder
	sb.WriteString("❓ **")
	sb.WriteString(q.Question)
	sb.WriteString("**")
	sb.WriteString(titleSuffix)
	if q.MultiSelect {
		sb.WriteString(e.i18n.T(MsgAskQuestionMulti))
	}
	sb.WriteString("\n\n")
	for i, opt := range q.Options {
		sb.WriteString(fmt.Sprintf("%d. **%s**", i+1, opt.Label))
		if opt.Description != "" {
			sb.WriteString(" — ")
			sb.WriteString(opt.Description)
		}
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("\n%s", e.i18n.T(MsgAskQuestionNote)))
	e.send(p, replyCtx, sb.String())
}

// waitOutgoing blocks on the per-platform outgoing rate limiter when enabled.
func (e *Engine) waitOutgoing(p Platform) error {
	if e.outgoingRL == nil {
		return nil
	}
	return e.outgoingRL.Wait(e.ctx, p.Name())
}

func (e *Engine) renderOutgoingContentForWorkspace(p Platform, content, workspaceDir string) string {
	if strings.TrimSpace(content) == "" {
		return content
	}
	return TransformLocalReferences(content, e.references, e.agent.Name(), p.Name(), workspaceDir)
}

func (e *Engine) sendWithErrorForWorkspace(p Platform, replyCtx any, content, workspaceDir string) error {
	if err := e.waitOutgoing(p); err != nil {
		slog.Warn("outgoing rate limit: context cancelled", "platform", p.Name(), "error", err)
		return err
	}
	content = e.renderOutgoingContentForWorkspace(p, content, workspaceDir)
	return e.sendAlreadyRenderedWithError(p, replyCtx, content)
}

func (e *Engine) sendForWorkspace(p Platform, replyCtx any, content, workspaceDir string) {
	_ = e.sendWithErrorForWorkspace(p, replyCtx, content, workspaceDir)
}

func (e *Engine) renderCardForPlatform(p Platform, card *Card) *Card {
	return e.renderCardForPlatformWorkspace(p, card, "")
}

func (e *Engine) renderCardForPlatformWorkspace(p Platform, card *Card, workspaceDir string) *Card {
	if card == nil {
		return nil
	}
	out := &Card{}
	if card.Header != nil {
		h := *card.Header
		out.Header = &h
	}
	out.Elements = make([]CardElement, 0, len(card.Elements))
	for _, elem := range card.Elements {
		switch v := elem.(type) {
		case CardMarkdown:
			content := v.Content
			if workspaceDir != "" {
				content = e.renderOutgoingContentForWorkspace(p, v.Content, workspaceDir)
			}
			out.Elements = append(out.Elements, CardMarkdown{Content: content})
		case CardNote:
			text := v.Text
			if workspaceDir != "" {
				text = e.renderOutgoingContentForWorkspace(p, v.Text, workspaceDir)
			}
			out.Elements = append(out.Elements, CardNote{Text: text, Tag: v.Tag})
		case CardListItem:
			text := v.Text
			if workspaceDir != "" {
				text = e.renderOutgoingContentForWorkspace(p, v.Text, workspaceDir)
			}
			out.Elements = append(out.Elements, CardListItem{
				Text:     text,
				BtnText:  v.BtnText,
				BtnType:  v.BtnType,
				BtnValue: v.BtnValue,
				Extra:    v.Extra,
			})
		default:
			out.Elements = append(out.Elements, elem)
		}
	}
	return out
}

// sendWithError applies outgoing rate limiting and p.Send. It logs wait
// cancellation and platform failures, and returns a non-nil error on either.
func (e *Engine) sendWithError(p Platform, replyCtx any, content string) error {
	if err := e.waitOutgoing(p); err != nil {
		slog.Warn("outgoing rate limit: context cancelled", "platform", p.Name(), "error", err)
		return err
	}
	return e.sendAlreadyRenderedWithError(p, replyCtx, content)
}

func (e *Engine) sendAlreadyRenderedWithError(p Platform, replyCtx any, content string) error {
	start := time.Now()
	if err := p.Send(e.ctx, replyCtx, content); err != nil {
		slog.Error("platform send failed", "platform", p.Name(), "error", err, "content_len", len(content))
		return err
	}
	if elapsed := time.Since(start); elapsed >= slowPlatformSend {
		slog.Warn("slow platform send", "platform", p.Name(), "elapsed", elapsed, "content_len", len(content))
	}
	return nil
}

// send wraps p.Send with error logging, slow-operation warnings, and outgoing rate limiting.
func (e *Engine) send(p Platform, replyCtx any, content string) {
	_ = e.sendWithError(p, replyCtx, content)
}

// sendRaw sends content without local-reference rendering. This is used for raw
// tool outputs, where preserving the original text is preferable to applying the
// agent-facing reference display transform.
func (e *Engine) sendRaw(p Platform, replyCtx any, content string) {
	if err := e.waitOutgoing(p); err != nil {
		slog.Warn("outgoing rate limit: context cancelled", "platform", p.Name(), "error", err)
		return
	}
	_ = e.sendAlreadyRenderedWithError(p, replyCtx, content)
}

// drainEvents discards any buffered events from the channel.
// Called before a new turn to prevent stale events from a previous turn's
// agent process from being mistaken for the new turn's response.
func drainEvents(ch <-chan Event) {
	drained := 0
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				// Channel is closed; stop immediately to avoid an infinite loop.
				return
			}
			drained++
		default:
			if drained > 0 {
				slog.Warn("drained stale events from previous turn", "count", drained)
			}
			return
		}
	}
}

// replyWithError applies outgoing rate limiting and p.Reply.
func (e *Engine) replyWithError(p Platform, replyCtx any, content string) error {
	if err := e.waitOutgoing(p); err != nil {
		slog.Warn("outgoing rate limit: context cancelled", "platform", p.Name(), "error", err)
		return err
	}
	start := time.Now()
	if err := p.Reply(e.ctx, replyCtx, content); err != nil {
		slog.Error("platform reply failed", "platform", p.Name(), "error", err, "content_len", len(content))
		return err
	}
	if elapsed := time.Since(start); elapsed >= slowPlatformSend {
		slog.Warn("slow platform reply", "platform", p.Name(), "elapsed", elapsed, "content_len", len(content))
	}
	return nil
}

// reply wraps p.Reply with error logging, slow-operation warnings, and outgoing rate limiting.
func (e *Engine) reply(p Platform, replyCtx any, content string) {
	_ = e.replyWithError(p, replyCtx, content)
}

// replyWithButtons sends a reply with inline buttons if the platform supports it,
// otherwise falls back to plain text reply.
func (e *Engine) replyWithButtons(p Platform, replyCtx any, content string, buttons [][]ButtonOption) {
	if err := e.waitOutgoing(p); err != nil {
		slog.Warn("outgoing rate limit: context cancelled", "platform", p.Name(), "error", err)
		return
	}
	if bs, ok := p.(InlineButtonSender); ok {
		if err := bs.SendWithButtons(e.ctx, replyCtx, content, buttons); err == nil {
			return
		}
	}
	e.reply(p, replyCtx, content)
}

func supportsCards(p Platform) bool {
	_, ok := p.(CardSender)
	return ok
}

// replyWithCard sends a structured card via CardSender.
// For platforms without card support, renders as plain text (no intermediate fallback).
func (e *Engine) replyWithCard(p Platform, replyCtx any, card *Card) {
	if card == nil {
		slog.Error("replyWithCard: nil card", "platform", p.Name())
		return
	}
	if err := e.waitOutgoing(p); err != nil {
		slog.Warn("outgoing rate limit: context cancelled", "platform", p.Name(), "error", err)
		return
	}
	if cs, ok := p.(CardSender); ok {
		rendered := e.renderCardForPlatform(p, card)
		if err := cs.ReplyCard(e.ctx, replyCtx, rendered); err != nil {
			slog.Error("card reply failed", "platform", p.Name(), "error", err)
		}
		return
	}
	e.reply(p, replyCtx, e.renderCardForPlatform(p, card).RenderText())
}

// sendWithCard sends a card as a new message (not a reply).
func (e *Engine) sendWithCard(p Platform, replyCtx any, card *Card) {
	if card == nil {
		slog.Error("sendWithCard: nil card", "platform", p.Name())
		return
	}
	if err := e.waitOutgoing(p); err != nil {
		slog.Warn("outgoing rate limit: context cancelled", "platform", p.Name(), "error", err)
		return
	}
	if cs, ok := p.(CardSender); ok {
		rendered := e.renderCardForPlatform(p, card)
		if err := cs.SendCard(e.ctx, replyCtx, rendered); err != nil {
			slog.Error("card send failed", "platform", p.Name(), "error", err)
		}
		return
	}
	e.send(p, replyCtx, e.renderCardForPlatform(p, card).RenderText())
}

// ──────────────────────────────────────────────────────────────
// Card navigation (in-place card updates)
// ──────────────────────────────────────────────────────────────

// handleCardNav is called by platforms that support in-place card updates.
// It routes nav: and act: prefixed actions to the appropriate render function.
func (e *Engine) handleCardNav(action string, sessionKey string) *Card {
	var prefix, body string
	if i := strings.Index(action, ":"); i >= 0 {
		prefix = action[:i]
		body = action[i+1:]
	} else {
		return nil
	}

	cmd, args := body, ""
	if i := strings.IndexByte(body, ' '); i >= 0 {
		cmd = body[:i]
		args = strings.TrimSpace(body[i+1:])
	}

	if prefix == "act" && cmd == "/model" {
		return e.handleModelCardAction(args, sessionKey)
	}

	if prefix == "act" {
		e.executeCardAction(cmd, args, sessionKey)
	}

	switch cmd {
	case "/help":
		return e.renderHelpGroupCard(args)
	case "/model":
		return e.renderModelCard(sessionKey)
	case "/reasoning":
		return e.renderReasoningCard()
	case "/mode":
		return e.renderModeCard()
	case "/lang":
		return e.renderLangCard()
	case "/status":
		return e.renderStatusCard(sessionKey, extractUserID(sessionKey))
	case "/list":
		page := 1
		if args != "" {
			if n, err := strconv.Atoi(args); err == nil && n > 0 {
				page = n
			}
		}
		return e.renderListCardSafe(sessionKey, page)
	case "/dir":
		page := 1
		if args != "" {
			if n, err := strconv.Atoi(args); err == nil && n > 0 {
				page = n
			}
		}
		return e.renderDirCardSafe(sessionKey, page)
	case "/current":
		return e.renderCurrentCard(sessionKey)
	case "/history":
		return e.renderHistoryCard(sessionKey)
	case "/provider":
		return e.renderProviderCard()
	case "/cron":
		return e.renderCronCard(sessionKey, extractUserID(sessionKey))
	case "/heartbeat":
		return e.renderHeartbeatCard()
	case "/commands":
		return e.renderCommandsCard()
	case "/alias":
		return e.renderAliasCard()
	case "/config":
		return e.renderConfigCard()
	case "/skills":
		return e.renderSkillsCard()
	case "/doctor":
		return e.renderDoctorCard()
	case "/whoami":
		return e.renderWhoamiCard(&Message{
			SessionKey: sessionKey,
			UserID:     extractUserID(sessionKey),
			Platform:   extractPlatformName(sessionKey),
		})
	case "/version":
		return e.renderVersionCard()
	case "/new":
		return e.renderCurrentCard(sessionKey)
	case "/switch":
		return e.renderListCardSafe(sessionKey, 1)
	case "/delete-mode":
		if strings.HasPrefix(args, "cancel") {
			return e.renderListCardSafe(sessionKey, 1)
		}
		return e.renderDeleteModeCard(sessionKey)
	case "/stop":
		return e.renderStatusCard(sessionKey, extractUserID(sessionKey))
	case "/upgrade":
		return e.renderUpgradeCard()
	}
	return nil
}

func (e *Engine) handleModelCardAction(args, sessionKey string) *Card {
	agent, sessions := e.sessionContextForKey(sessionKey)
	switcher, ok := agent.(ModelSwitcher)
	if !ok {
		return e.simpleCard(e.i18n.T(MsgCardTitleModel), "indigo", e.i18n.T(MsgModelNotSupported))
	}

	target, ok := parseModelSwitchArgs(strings.Fields(args))
	if !ok {
		return e.renderModelCard(sessionKey)
	}
	target = strings.TrimSpace(target)
	if modelSwitchNeedsLookup(target) {
		fetchCtx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
		models := switcher.AvailableModels(fetchCtx)
		target = resolveModelSwitchTarget(target, models)
		cancel()
	}

	resolved, err := e.switchModelOnAgent(agent, target, agent == e.agent)
	e.cleanupInteractiveState(e.interactiveKeyForSessionKey(sessionKey))
	if err == nil {
		s := sessions.GetOrCreateActive(sessionKey)
		s.SetAgentSessionID("", "")
		s.ClearHistory()
		sessions.Save()
	}

	return e.renderModelSwitchResultCard(resolved, err)
}

// executeCardAction performs the side-effect for act: prefixed actions
// (e.g. switching model/mode/lang) before the card is re-rendered.
func (e *Engine) executeCardAction(cmd, args, sessionKey string) {
	switch cmd {
	case "/model":
		if args == "" {
			return
		}
		agent, sessions := e.sessionContextForKey(sessionKey)
		switcher, ok := agent.(ModelSwitcher)
		if !ok {
			return
		}
		fetchCtx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
		target, ok := parseModelSwitchArgs(strings.Fields(args))
		if !ok {
			cancel()
			return
		}
		target = strings.TrimSpace(target)
		if modelSwitchNeedsLookup(target) {
			models := switcher.AvailableModels(fetchCtx)
			target = resolveModelSwitchTarget(target, models)
		}
		cancel()
		interactiveKey := e.interactiveKeyForSessionKey(sessionKey)
		e.cleanupInteractiveState(interactiveKey)
		e.interactiveMu.Lock()
		state := e.interactiveStates[interactiveKey]
		if state == nil {
			state = &interactiveState{}
			e.interactiveStates[interactiveKey] = state
		}
		e.interactiveMu.Unlock()
		state.mu.Lock()
		state.modelSwitch = &modelSwitchState{phase: "switching", target: target}
		state.mu.Unlock()
		go e.performModelSwitchAsync(sessionKey, state, agent, sessions, target)

	case "/reasoning":
		if args == "" {
			return
		}
		switcher, ok := e.agent.(ReasoningEffortSwitcher)
		if !ok {
			return
		}
		efforts := switcher.AvailableReasoningEfforts()
		target := strings.ToLower(strings.TrimSpace(args))
		if idx, err := strconv.Atoi(target); err == nil && idx >= 1 && idx <= len(efforts) {
			target = efforts[idx-1]
		}
		for _, effort := range efforts {
			if effort == target {
				switcher.SetReasoningEffort(target)
				interactiveKey := e.interactiveKeyForSessionKey(sessionKey)
				e.cleanupInteractiveState(interactiveKey)
				s := e.sessions.GetOrCreateActive(sessionKey)
				s.SetAgentSessionID("", "")
				s.ClearHistory()
				e.sessions.Save()
				return
			}
		}

	case "/mode":
		if args == "" {
			return
		}
		switcher, ok := e.agent.(ModeSwitcher)
		if !ok {
			return
		}
		newMode := strings.ToLower(args)
		switcher.SetMode(newMode)
		interactiveKey := e.interactiveKeyForSessionKey(sessionKey)
		if e.applyLiveModeChange(sessionKey, switcher.GetMode()) {
			e.cleanupInteractiveState(interactiveKey)
			return
		}
		e.cleanupInteractiveState(interactiveKey)
		// Mode change requires a new session to take effect
		s := e.sessions.GetOrCreateActive(sessionKey)
		s.SetAgentSessionID("", "")
		s.ClearHistory()
		e.sessions.Save()

	case "/lang":
		if args == "" {
			return
		}
		target := strings.ToLower(strings.TrimSpace(args))
		var lang Language
		switch target {
		case "en", "english":
			lang = LangEnglish
		case "zh", "cn", "chinese":
			lang = LangChinese
		case "zh-tw", "zh_tw", "zhtw":
			lang = LangTraditionalChinese
		case "ja", "jp", "japanese":
			lang = LangJapanese
		case "es", "spanish":
			lang = LangSpanish
		case "auto":
			lang = LangAuto
		default:
			return
		}
		e.i18n.SetLang(lang)

	case "/provider":
		if args == "" {
			return
		}
		switcher, ok := e.agent.(ProviderSwitcher)
		if !ok {
			return
		}
		if switcher.SetActiveProvider(args) {
			interactiveKey := e.interactiveKeyForSessionKey(sessionKey)
			e.cleanupInteractiveState(interactiveKey)
			if e.providerSaveFunc != nil {
				_ = e.providerSaveFunc(args)
			}
		}

	case "/new":
		interactiveKey := e.interactiveKeyForSessionKey(sessionKey)
		_, sessions := e.sessionContextForKey(sessionKey)
		e.cleanupInteractiveState(interactiveKey)
		sessions.NewSession(sessionKey, "")

	case "/delete-mode":
		e.executeDeleteModeAction(sessionKey, args)

	case "/switch":
		if args == "" {
			return
		}
		agent, sessions := e.sessionContextForKey(sessionKey)
		agentSessions, err := agent.ListSessions(e.ctx)
		if err != nil || len(agentSessions) == 0 {
			return
		}
		agentSessions = filterOwnedSessions(agentSessions, sessions.KnownAgentSessionIDs())
		matched := e.matchSession(agentSessions, sessions, args)
		if matched == nil {
			return
		}
		interactiveKey := e.interactiveKeyForSessionKey(sessionKey)
		e.cleanupInteractiveState(interactiveKey)
		session := sessions.SwitchToAgentSession(sessionKey, matched.ID, agent.Name(), matched.Summary)
		session.ClearHistory()

	case "/dir":
		fields := strings.Fields(args)
		if len(fields) == 0 {
			return
		}
		agent, sessions := e.sessionContextForKey(sessionKey)
		ik := e.interactiveKeyForSessionKey(sessionKey)
		var applyArgs []string
		switch fields[0] {
		case "select":
			if len(fields) < 2 {
				return
			}
			applyArgs = []string{fields[1]}
		case "reset":
			applyArgs = []string{"reset"}
		case "prev":
			applyArgs = []string{"-"}
		default:
			return
		}
		errMsg, _ := e.dirApply(agent, sessions, ik, sessionKey, applyArgs)
		if errMsg != "" {
			slog.Debug("dir card action failed", "message", errMsg)
		}

	case "/stop":
		sessionKey = e.interactiveKeyForSessionKey(sessionKey)
		e.stopInteractiveSession(sessionKey, nil, nil)

	case "/heartbeat":
		if e.heartbeatScheduler == nil {
			return
		}
		switch args {
		case "pause", "stop":
			e.heartbeatScheduler.Pause(e.name)
		case "resume", "start":
			e.heartbeatScheduler.Resume(e.name)
		case "run", "trigger":
			e.heartbeatScheduler.TriggerNow(e.name)
		}

	case "/cron":
		if e.cronScheduler == nil || args == "" {
			return
		}
		subArgs := strings.Fields(args)
		if len(subArgs) < 2 {
			return
		}
		sub, id := subArgs[0], subArgs[1]
		switch sub {
		case "enable":
			_ = e.cronScheduler.EnableJob(id)
		case "disable":
			_ = e.cronScheduler.DisableJob(id)
		case "delete":
			e.cronScheduler.RemoveJob(id)
		case "mute":
			e.cronScheduler.Store().SetMute(id, true)
		case "unmute":
			e.cronScheduler.Store().SetMute(id, false)
		}
	}
}

func (e *Engine) getOrCreateDeleteModeState(sessionKey string, p Platform, replyCtx any) *deleteModeState {
	interactiveKey := e.interactiveKeyForSessionKey(sessionKey)
	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[interactiveKey]
	if !ok || state == nil {
		state = &interactiveState{platform: p, replyCtx: replyCtx}
		e.interactiveStates[interactiveKey] = state
	} else {
		state.platform = p
		state.replyCtx = replyCtx
	}
	e.interactiveMu.Unlock()

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.deleteMode == nil {
		state.deleteMode = &deleteModeState{}
	}
	dm := state.deleteMode
	dm.page = 1
	dm.phase = "select"
	dm.hint = ""
	dm.result = ""
	dm.selectedIDs = make(map[string]struct{})
	return dm
}

func (e *Engine) getDeleteModeState(sessionKey string) *deleteModeState {
	interactiveKey := e.interactiveKeyForSessionKey(sessionKey)
	e.interactiveMu.Lock()
	state := e.interactiveStates[interactiveKey]
	e.interactiveMu.Unlock()
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.deleteMode == nil {
		return nil
	}
	cp := &deleteModeState{
		page:        state.deleteMode.page,
		selectedIDs: make(map[string]struct{}, len(state.deleteMode.selectedIDs)),
		phase:       state.deleteMode.phase,
		hint:        state.deleteMode.hint,
		result:      state.deleteMode.result,
	}
	for id := range state.deleteMode.selectedIDs {
		cp.selectedIDs[id] = struct{}{}
	}
	return cp
}

func (e *Engine) getModelSwitchState(sessionKey string) *modelSwitchState {
	interactiveKey := e.interactiveKeyForSessionKey(sessionKey)
	e.interactiveMu.Lock()
	state := e.interactiveStates[interactiveKey]
	e.interactiveMu.Unlock()
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.modelSwitch == nil {
		return nil
	}
	cp := *state.modelSwitch
	return &cp
}

func (e *Engine) renderDeleteModeCard(sessionKey string) *Card {
	agent, sessions := e.sessionContextForKey(sessionKey)
	agentSessions, err := agent.ListSessions(e.ctx)
	if err != nil {
		return e.simpleCard(e.i18n.T(MsgDeleteModeTitle), "red", err.Error())
	}
	agentSessions = filterOwnedSessions(agentSessions, sessions.KnownAgentSessionIDs())
	dm := e.getDeleteModeState(sessionKey)
	if dm == nil {
		return e.simpleCard(e.i18n.T(MsgDeleteModeTitle), "red", e.i18n.T(MsgDeleteUsage))
	}
	switch dm.phase {
	case "confirm":
		return e.renderDeleteModeConfirmCard(sessions, dm, agentSessions)
	case "result":
		return e.renderDeleteModeResultCard(dm)
	case "deleting":
		return e.renderDeleteModeDeletingCard(dm)
	default:
		return e.renderDeleteModeSelectCard(sessionKey, sessions, dm, agentSessions)
	}
}

func (e *Engine) renderDeleteModeSelectCard(sessionKey string, sessions *SessionManager, dm *deleteModeState, agentSessions []AgentSessionInfo) *Card {
	if len(agentSessions) == 0 {
		return e.simpleCard(e.i18n.T(MsgDeleteModeTitle), "red", e.i18n.T(MsgListEmpty))
	}
	total := len(agentSessions)
	totalPages := (total + listPageSize - 1) / listPageSize
	page := dm.page
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * listPageSize
	end := start + listPageSize
	if end > total {
		end = total
	}

	cb := NewCard().Title(e.i18n.T(MsgDeleteModeTitle), "carmine")
	activeAgentID := sessions.GetOrCreateActive(sessionKey).GetAgentSessionID()
	selectedCount := 0
	for i := start; i < end; i++ {
		s := agentSessions[i]
		isActive := activeAgentID == s.ID
		isSelected := false
		if !isActive {
			_, isSelected = dm.selectedIDs[s.ID]
		}
		marker := "◻"
		if isActive {
			marker = "▶"
		} else if isSelected {
			marker = "☑"
			selectedCount++
		}
		btnText := e.i18n.T(MsgDeleteModeSelect)
		btnType := "default"
		action := fmt.Sprintf("act:/delete-mode toggle %s", s.ID)
		if isActive {
			btnText = e.i18n.T(MsgCardTitleCurrentSession)
			btnType = "primary"
			action = fmt.Sprintf("act:/delete-mode noop %s", s.ID)
		} else if isSelected {
			btnText = e.i18n.T(MsgDeleteModeSelected)
			btnType = "primary"
		}
		cb.ListItemBtn(
			e.i18n.Tf(MsgListItem, marker, i+1, e.deleteSessionDisplayName(sessions, &s), s.MessageCount, s.ModifiedAt.Format("01-02 15:04")),
			btnText,
			btnType,
			action,
		)
	}
	cb.TaggedNote("delete-mode-selected-count", e.i18n.Tf(MsgDeleteModeSelectedCount, selectedCount))
	if dm.hint != "" {
		cb.Note(dm.hint)
	}
	cb.Buttons(
		DangerBtn(e.i18n.T(MsgDeleteModeDeleteSelected), "act:/delete-mode confirm"),
		DefaultBtn(e.i18n.T(MsgDeleteModeCancel), "act:/delete-mode cancel"),
	)

	var navBtns []CardButton
	if page > 1 {
		navBtns = append(navBtns, DefaultBtn(e.i18n.T(MsgCardPrev), fmt.Sprintf("act:/delete-mode page %d", page-1)))
	}
	if page < totalPages {
		navBtns = append(navBtns, DefaultBtn(e.i18n.T(MsgCardNext), fmt.Sprintf("act:/delete-mode page %d", page+1)))
	}
	if len(navBtns) > 0 {
		cb.Buttons(navBtns...)
	}
	return cb.Build()
}

func (e *Engine) renderDeleteModeConfirmCard(sessions *SessionManager, dm *deleteModeState, agentSessions []AgentSessionInfo) *Card {
	selectedNames := e.deleteModeSelectionNames(sessions, dm, agentSessions)
	body := strings.Join(selectedNames, "\n")
	if body == "" {
		body = e.i18n.T(MsgDeleteModeEmptySelection)
	}
	return NewCard().
		Title(e.i18n.T(MsgDeleteModeConfirmTitle), "carmine").
		Markdown(body).
		Buttons(
			DangerBtn(e.i18n.T(MsgDeleteModeConfirmButton), "act:/delete-mode submit"),
			DefaultBtn(e.i18n.T(MsgDeleteModeBackButton), "act:/delete-mode back"),
		).
		Build()
}

func (e *Engine) renderDeleteModeResultCard(dm *deleteModeState) *Card {
	return NewCard().
		Title(e.i18n.T(MsgDeleteModeResultTitle), "turquoise").
		Markdown(dm.result).
		Buttons(DefaultBtn(e.i18n.T(MsgCardBack), "nav:/list 1")).
		Build()
}

func (e *Engine) renderDeleteModeDeletingCard(dm *deleteModeState) *Card {
	return NewCard().
		Title(e.i18n.T(MsgDeleteModeDeletingTitle), "orange").
		Markdown(dm.hint).
		Build()
}

// performDeleteModeAsync runs the actual session deletions in a background
// goroutine so that the card callback can return immediately with a "deleting"
// indicator. Once all deletions finish it updates the interactive state and
// pushes a result card to the originating platform.
func (e *Engine) performDeleteModeAsync(sessionKey string, selectedIDs map[string]struct{}) {
	lines := e.submitDeleteModeSelection(sessionKey, selectedIDs)
	result := strings.Join(lines, "\n")

	// Update the interactive state to "result" phase.
	interactiveKey := e.interactiveKeyForSessionKey(sessionKey)
	e.interactiveMu.Lock()
	state := e.interactiveStates[interactiveKey]
	e.interactiveMu.Unlock()
	if state != nil {
		state.mu.Lock()
		if state.deleteMode != nil {
			state.deleteMode.result = result
			state.deleteMode.hint = ""
			state.deleteMode.phase = "result"
		}
		state.mu.Unlock()
	}

	// Push the result card to the platform proactively.
	e.pushDeleteModeResultCard(sessionKey)
}

// pushDeleteModeResultCard resolves the platform from the session key and
// refreshes the "deleting" card in-place with the final result. Falls back to
// sending a new card if the platform does not support in-place card refresh.
func (e *Engine) pushDeleteModeResultCard(sessionKey string) {
	dm := e.getDeleteModeState(sessionKey)
	if dm == nil {
		return
	}
	card := e.renderDeleteModeResultCard(dm)

	platformName := extractPlatformName(sessionKey)
	var targetPlatform Platform
	for _, p := range e.platforms {
		if p.Name() == platformName {
			targetPlatform = p
			break
		}
	}
	if targetPlatform == nil {
		slog.Warn("delete mode: platform not found for result card", "sessionKey", sessionKey)
		return
	}

	// Prefer in-place card refresh (updates the "deleting" card to show results).
	if refresher, ok := targetPlatform.(CardRefresher); ok {
		if err := refresher.RefreshCard(e.ctx, sessionKey, card); err != nil {
			slog.Warn("delete mode: refresh card failed, falling back to new message", "error", err)
		} else {
			return
		}
	}

	// Fallback: send a new card message.
	rc, ok := targetPlatform.(ReplyContextReconstructor)
	if !ok {
		slog.Warn("delete mode: platform does not support proactive messaging", "platform", platformName)
		return
	}
	rctx, err := rc.ReconstructReplyCtx(sessionKey)
	if err != nil {
		slog.Error("delete mode: reconstruct reply ctx failed", "error", err)
		return
	}
	e.sendWithCard(targetPlatform, rctx, card)
}

func (e *Engine) performModelSwitchAsync(sessionKey string, state *interactiveState, agent Agent, sessions *SessionManager, target string) {
	resolved, err := e.switchModelOnAgent(agent, target, agent == e.agent)
	if err == nil {
		s := sessions.GetOrCreateActive(sessionKey)
		s.SetAgentSessionID("", "")
		s.ClearHistory()
		sessions.Save()
	}

	resultCard := e.renderModelSwitchResultCard(resolved, err)
	if state != nil {
		state.mu.Lock()
		if state.modelSwitch != nil {
			state.modelSwitch.phase = "result"
			state.modelSwitch.target = resolved
			if err != nil {
				state.modelSwitch.result = e.i18n.Tf(MsgModelCardSwitchFailed, err)
			} else {
				state.modelSwitch.result = e.i18n.Tf(MsgModelCardSwitched, resolved)
			}
		}
		state.mu.Unlock()
	}
	e.pushModelSwitchResultCard(sessionKey, resultCard)
	e.cleanupInteractiveState(e.interactiveKeyForSessionKey(sessionKey), state)
}

func (e *Engine) pushModelSwitchResultCard(sessionKey string, card *Card) {
	platformName := extractPlatformName(sessionKey)
	var targetPlatform Platform
	for _, p := range e.platforms {
		if p.Name() == platformName {
			targetPlatform = p
			break
		}
	}
	if targetPlatform == nil {
		slog.Warn("model switch: platform not found for result card", "sessionKey", sessionKey)
		return
	}

	if refresher, ok := targetPlatform.(CardRefresher); ok {
		if err := refresher.RefreshCard(e.ctx, sessionKey, card); err != nil {
			slog.Warn("model switch: refresh card failed, falling back to new message", "error", err)
		} else {
			return
		}
	}

	rc, ok := targetPlatform.(ReplyContextReconstructor)
	if !ok {
		slog.Warn("model switch: platform does not support proactive messaging", "platform", platformName)
		return
	}
	rctx, err := rc.ReconstructReplyCtx(sessionKey)
	if err != nil {
		slog.Error("model switch: reconstruct reply ctx failed", "error", err)
		return
	}
	e.sendWithCard(targetPlatform, rctx, card)
}

func (e *Engine) deleteModeSelectionNames(sessions *SessionManager, dm *deleteModeState, agentSessions []AgentSessionInfo) []string {
	names := make([]string, 0, len(dm.selectedIDs))
	for i := range agentSessions {
		if _, ok := dm.selectedIDs[agentSessions[i].ID]; ok {
			names = append(names, "- "+e.deleteSessionDisplayName(sessions, &agentSessions[i]))
		}
	}
	return names
}

func (e *Engine) executeDeleteModeAction(sessionKey, args string) {
	interactiveKey := e.interactiveKeyForSessionKey(sessionKey)
	e.interactiveMu.Lock()
	state := e.interactiveStates[interactiveKey]
	e.interactiveMu.Unlock()
	if state == nil {
		return
	}

	fields := strings.Fields(args)
	if len(fields) == 0 {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.deleteMode == nil {
		return
	}

	dm := state.deleteMode
	switch fields[0] {
	case "toggle":
		if len(fields) < 2 {
			return
		}
		id := fields[1]
		if _, ok := dm.selectedIDs[id]; ok {
			delete(dm.selectedIDs, id)
		} else {
			dm.selectedIDs[id] = struct{}{}
		}
		dm.phase = "select"
		dm.hint = ""
	case "page":
		if len(fields) < 2 {
			return
		}
		if n, err := strconv.Atoi(fields[1]); err == nil && n > 0 {
			dm.page = n
		}
		dm.phase = "select"
	case "confirm":
		if len(dm.selectedIDs) == 0 {
			dm.phase = "select"
			dm.hint = e.i18n.T(MsgDeleteModeEmptySelection)
			return
		}
		dm.phase = "confirm"
		dm.hint = ""
	case "back":
		dm.phase = "select"
	case "submit":
		// Capture selected IDs and switch to "deleting" phase immediately
		// so the card callback can return a loading card without blocking.
		ids := make(map[string]struct{}, len(dm.selectedIDs))
		for id := range dm.selectedIDs {
			ids[id] = struct{}{}
		}
		dm.selectedIDs = make(map[string]struct{})
		dm.phase = "deleting"
		dm.hint = e.i18n.Tf(MsgDeleteModeDeletingBody, len(ids))
		go e.performDeleteModeAsync(sessionKey, ids)
	case "form-submit":
		dm.selectedIDs = parseDeleteModeSelectedIDs(fields[1:])
		if len(dm.selectedIDs) == 0 {
			dm.phase = "select"
			dm.hint = e.i18n.T(MsgDeleteModeEmptySelection)
			return
		}
		dm.phase = "confirm"
		dm.hint = ""
	case "cancel":
		state.deleteMode = nil
	}
}

func parseDeleteModeSelectedIDs(args []string) map[string]struct{} {
	ids := make(map[string]struct{})
	for _, arg := range args {
		for _, id := range strings.Split(arg, ",") {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			ids[id] = struct{}{}
		}
	}
	return ids
}

func (e *Engine) submitDeleteModeSelection(sessionKey string, selectedIDs map[string]struct{}) []string {
	agent, sessions := e.sessionContextForKey(sessionKey)
	deleter, ok := agent.(SessionDeleter)
	if !ok {
		return []string{e.i18n.T(MsgDeleteNotSupported)}
	}
	agentSessions, err := agent.ListSessions(e.ctx)
	if err != nil {
		return []string{e.i18n.Tf(MsgError, err)}
	}
	agentSessions = filterOwnedSessions(agentSessions, sessions.KnownAgentSessionIDs())
	seen := make(map[string]struct{}, len(agentSessions))
	lines := make([]string, 0, len(selectedIDs))
	for i := range agentSessions {
		seen[agentSessions[i].ID] = struct{}{}
		if _, ok := selectedIDs[agentSessions[i].ID]; !ok {
			continue
		}
		if line := e.deleteSingleSessionReply(&Message{SessionKey: sessionKey}, deleter, &agentSessions[i]); line != "" {
			lines = append(lines, line)
		}
	}
	missingIDs := make([]string, 0)
	for id := range selectedIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		missingIDs = append(missingIDs, id)
	}
	sort.Strings(missingIDs)
	for _, id := range missingIDs {
		lines = append(lines, fmt.Sprintf(e.i18n.T(MsgDeleteModeMissingSession), id))
	}
	if len(lines) == 0 {
		lines = append(lines, e.i18n.T(MsgDeleteModeEmptySelection))
	}
	return lines
}

func (e *Engine) renderLangCard() *Card {
	cur := e.i18n.CurrentLang()
	name := langDisplayName(cur)

	langs := []struct{ code, label string }{
		{"en", "English"}, {"zh", "中文"}, {"zh-TW", "繁體中文"},
		{"ja", "日本語"}, {"es", "Español"}, {"auto", "Auto"},
	}
	var opts []CardSelectOption
	initVal := ""
	for _, l := range langs {
		opts = append(opts, CardSelectOption{Text: l.label, Value: "act:/lang " + l.code})
		if string(cur) == l.code || (cur == LangAuto && l.code == "auto") {
			initVal = "act:/lang " + l.code
		}
	}

	return NewCard().
		Title(e.i18n.T(MsgCardTitleLanguage), "wathet").
		Markdown(e.i18n.Tf(MsgLangCurrent, name)).
		Select(e.i18n.T(MsgLangSelectPlaceholder), opts, initVal).
		Buttons(e.cardBackButton()).
		Build()
}

func (e *Engine) renderModelCard(sessionKey string) *Card {
	if ms := e.getModelSwitchState(sessionKey); ms != nil && ms.phase == "switching" {
		return e.renderModelSwitchingCard(ms.target)
	}

	agent := e.agent
	if sessionKey != "" {
		agent, _ = e.sessionContextForKey(sessionKey)
	}

	switcher, ok := agent.(ModelSwitcher)
	if !ok {
		return e.simpleCard(e.i18n.T(MsgCardTitleModel), "indigo", e.i18n.T(MsgModelNotSupported))
	}

	fetchCtx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
	defer cancel()
	models := switcher.AvailableModels(fetchCtx)
	current := switcher.GetModel()

	var sb strings.Builder
	if current == "" {
		sb.WriteString(e.i18n.T(MsgModelDefault))
	} else {
		sb.WriteString(e.i18n.Tf(MsgModelCurrent, current))
	}

	var opts []CardSelectOption
	initVal := ""
	for i, m := range models {
		label := m.Name
		if m.Alias != "" {
			label = m.Alias + " - " + m.Name
		} else if m.Desc != "" {
			label += " — " + m.Desc
		}
		val := fmt.Sprintf("act:/model switch %d", i+1)
		opts = append(opts, CardSelectOption{Text: label, Value: val})
		if m.Name == current {
			initVal = val
		}
	}

	cb := NewCard().Title(e.i18n.T(MsgCardTitleModel), "indigo").
		Markdown(sb.String()).
		Select(e.i18n.T(MsgModelSelectPlaceholder), opts, initVal).
		Buttons(e.cardBackButton())
	cb.Note(e.i18n.T(MsgModelUsage))
	return cb.Build()
}

func (e *Engine) renderModelSwitchingCard(target string) *Card {
	return NewCard().
		Title(e.i18n.T(MsgCardTitleModel), "orange").
		Markdown(e.i18n.Tf(MsgModelCardSwitching, target)).
		Build()
}

func (e *Engine) renderModelSwitchResultCard(target string, err error) *Card {
	if err != nil {
		return NewCard().
			Title(e.i18n.T(MsgCardTitleModel), "red").
			Markdown(e.i18n.Tf(MsgModelCardSwitchFailed, err)).
			Buttons(e.modelCardBackButton()).
			Build()
	}
	return NewCard().
		Title(e.i18n.T(MsgCardTitleModel), "green").
		Markdown(e.i18n.Tf(MsgModelCardSwitched, target)).
		Buttons(e.modelCardBackButton()).
		Build()
}

func (e *Engine) renderReasoningCard() *Card {
	switcher, ok := e.agent.(ReasoningEffortSwitcher)
	if !ok {
		return e.simpleCard(e.i18n.T(MsgCardTitleReasoning), "orange", e.i18n.T(MsgReasoningNotSupported))
	}

	efforts := switcher.AvailableReasoningEfforts()
	current := switcher.GetReasoningEffort()

	var sb strings.Builder
	if current == "" {
		sb.WriteString(e.i18n.T(MsgReasoningDefault))
	} else {
		sb.WriteString(e.i18n.Tf(MsgReasoningCurrent, current))
	}

	var opts []CardSelectOption
	initVal := ""
	for i, effort := range efforts {
		val := fmt.Sprintf("act:/reasoning %d", i+1)
		opts = append(opts, CardSelectOption{Text: effort, Value: val})
		if effort == current {
			initVal = val
		}
	}

	cb := NewCard().Title(e.i18n.T(MsgCardTitleReasoning), "orange").
		Markdown(sb.String()).
		Select(e.i18n.T(MsgReasoningSelectPlaceholder), opts, initVal).
		Buttons(e.cardBackButton())
	cb.Note(e.i18n.T(MsgReasoningUsage))
	return cb.Build()
}

func (e *Engine) renderModeCard() *Card {
	switcher, ok := e.agent.(ModeSwitcher)
	if !ok {
		return e.simpleCard(e.i18n.T(MsgCardTitleMode), "violet", e.i18n.T(MsgModeNotSupported))
	}

	current := switcher.GetMode()
	modes := switcher.PermissionModes()
	zhLike := e.i18n.IsZhLike()

	var sb strings.Builder
	for _, m := range modes {
		marker := "◻"
		if m.Key == current {
			marker = "▶"
		}
		if zhLike {
			sb.WriteString(fmt.Sprintf("%s **%s** — %s\n", marker, m.NameZh, m.DescZh))
		} else {
			sb.WriteString(fmt.Sprintf("%s **%s** — %s\n", marker, m.Name, m.Desc))
		}
	}

	var opts []CardSelectOption
	initVal := ""
	for _, m := range modes {
		label := m.Name
		if zhLike {
			label = m.NameZh
		}
		val := "act:/mode " + m.Key
		opts = append(opts, CardSelectOption{Text: label, Value: val})
		if m.Key == current {
			initVal = val
		}
	}

	cb := NewCard().Title(e.i18n.T(MsgCardTitleMode), "violet").
		Markdown(sb.String()).
		Select(e.i18n.T(MsgModeSelectPlaceholder), opts, initVal).
		Buttons(e.cardBackButton())
	cb.Note(e.modeUsageText(modes))
	return cb.Build()
}

func (e *Engine) renderListCard(sessionKey string, page int) (*Card, error) {
	agent, sessions := e.sessionContextForKey(sessionKey)
	agentSessions, err := agent.ListSessions(e.ctx)
	if err != nil {
		return nil, fmt.Errorf(e.i18n.T(MsgListError), err)
	}
	agentSessions = filterOwnedSessions(agentSessions, sessions.KnownAgentSessionIDs())
	if len(agentSessions) == 0 {
		return e.simpleCard(e.i18n.Tf(MsgCardTitleSessions, agent.Name(), 0), "turquoise", e.i18n.T(MsgListEmpty)), nil
	}

	total := len(agentSessions)
	totalPages := (total + listPageSize - 1) / listPageSize
	if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * listPageSize
	end := start + listPageSize
	if end > total {
		end = total
	}

	agentName := agent.Name()
	activeSession := sessions.GetOrCreateActive(sessionKey)
	activeAgentID := activeSession.GetAgentSessionID()

	var titleStr string
	if totalPages > 1 {
		titleStr = e.i18n.Tf(MsgCardTitleSessionsPaged, agentName, total, page, totalPages)
	} else {
		titleStr = e.i18n.Tf(MsgCardTitleSessions, agentName, total)
	}

	cb := NewCard().Title(titleStr, "turquoise")
	for i := start; i < end; i++ {
		s := agentSessions[i]
		marker := "◻"
		if s.ID == activeAgentID {
			marker = "▶"
		}
		displayName := sessions.GetSessionName(s.ID)
		if displayName != "" {
			displayName = "📌 " + displayName
		} else {
			displayName = strings.ReplaceAll(s.Summary, "\n", " ")
			displayName = strings.Join(strings.Fields(displayName), " ")
			if displayName == "" {
				displayName = e.i18n.T(MsgListEmptySummary)
			}
			if len([]rune(displayName)) > 40 {
				displayName = string([]rune(displayName)[:40]) + "…"
			}
		}
		btnType := "default"
		if s.ID == activeAgentID {
			btnType = "primary"
		}
		cb.ListItemBtn(
			e.i18n.Tf(MsgListItem, marker, i+1, displayName, s.MessageCount, s.ModifiedAt.Format("01-02 15:04")),
			fmt.Sprintf("#%d", i+1),
			btnType,
			fmt.Sprintf("act:/switch %d", i+1),
		)
	}

	var navBtns []CardButton
	if page > 1 {
		navBtns = append(navBtns, e.cardPrevButton(fmt.Sprintf("nav:/list %d", page-1)))
	}
	navBtns = append(navBtns, e.cardBackButton())
	if page < totalPages {
		navBtns = append(navBtns, e.cardNextButton(fmt.Sprintf("nav:/list %d", page+1)))
	}
	cb.Buttons(navBtns...)

	if totalPages > 1 {
		cb.Note(fmt.Sprintf(e.i18n.T(MsgListPageHint), page, totalPages))
	}

	return cb.Build(), nil
}

// dirCardTruncPath shortens absolute paths for card list rows.
func dirCardTruncPath(absPath string) string {
	r := []rune(absPath)
	if len(r) <= 56 {
		return absPath
	}
	return string(r[:53]) + "…"
}

func (e *Engine) renderDirCard(sessionKey string, page int) (*Card, error) {
	agent, _ := e.sessionContextForKey(sessionKey)
	switcher, ok := agent.(WorkDirSwitcher)
	if !ok {
		return nil, fmt.Errorf("%s", e.i18n.T(MsgDirNotSupported))
	}
	currentDir := switcher.GetWorkDir()
	var history []string
	if e.dirHistory != nil {
		history = e.dirHistory.List(e.name)
	}
	total := len(history)
	totalPages := 1
	if total > 0 {
		totalPages = (total + dirCardPageSize - 1) / dirCardPageSize
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * dirCardPageSize
	end := start + dirCardPageSize
	if end > total {
		end = total
	}

	cb := NewCard().Title(e.i18n.T(MsgDirCardTitle), "turquoise")
	cb.Markdown(e.i18n.Tf(MsgDirCurrent, currentDir))
	if total == 0 {
		cb.Note(e.i18n.T(MsgDirCardEmptyHistory))
	} else {
		cb.Divider()
		for i := start; i < end; i++ {
			dir := history[i]
			marker := "◻"
			if dir == currentDir {
				marker = "▶"
			}
			btnType := "default"
			if dir == currentDir {
				btnType = "primary"
			}
			displayPath := dirCardTruncPath(dir)
			cb.ListItemBtn(
				fmt.Sprintf("%s **%d.** `%s`", marker, i+1, displayPath),
				fmt.Sprintf("#%d", i+1),
				btnType,
				fmt.Sprintf("act:/dir select %d", i+1),
			)
		}
	}

	var actionRow []CardButton
	if e.dirHistory != nil && len(history) >= 2 {
		actionRow = append(actionRow, DefaultBtn(e.i18n.T(MsgDirCardPrev), "act:/dir prev"))
	}
	actionRow = append(actionRow, DefaultBtn(e.i18n.T(MsgDirCardReset), "act:/dir reset"))
	cb.Buttons(actionRow...)

	var navBtns []CardButton
	if totalPages > 1 && page > 1 {
		navBtns = append(navBtns, e.cardPrevButton(fmt.Sprintf("nav:/dir %d", page-1)))
	}
	navBtns = append(navBtns, e.cardBackButton())
	if totalPages > 1 && page < totalPages {
		navBtns = append(navBtns, e.cardNextButton(fmt.Sprintf("nav:/dir %d", page+1)))
	}
	cb.Buttons(navBtns...)

	if totalPages > 1 {
		cb.Note(fmt.Sprintf(e.i18n.T(MsgDirCardPageHint), page, totalPages))
	}

	return cb.Build(), nil
}

// ──────────────────────────────────────────────────────────────
// Navigable sub-cards (for in-place card updates)
// ──────────────────────────────────────────────────────────────

func (e *Engine) renderCurrentCard(sessionKey string) *Card {
	_, sessions := e.sessionContextForKey(sessionKey)
	s := sessions.GetOrCreateActive(sessionKey)
	agentID := s.GetAgentSessionID()
	if agentID == "" {
		agentID = e.i18n.T(MsgSessionNotStarted)
	}
	content := fmt.Sprintf(e.i18n.T(MsgCurrentSession), s.Name, agentID, len(s.History))
	return NewCard().
		Title(e.i18n.T(MsgCardTitleCurrentSession), "turquoise").
		Markdown(content).
		Buttons(e.cardBackButton()).
		Build()
}

func (e *Engine) renderHistoryCard(sessionKey string) *Card {
	agent, sessions := e.sessionContextForKey(sessionKey)
	s := sessions.GetOrCreateActive(sessionKey)
	entries := s.GetHistory(10)

	agentSID := s.GetAgentSessionID()
	if len(entries) == 0 && agentSID != "" {
		if hp, ok := agent.(HistoryProvider); ok {
			if agentEntries, err := hp.GetSessionHistory(e.ctx, agentSID, 10); err == nil {
				entries = agentEntries
			}
		}
	}

	if len(entries) == 0 {
		return e.simpleCard(e.i18n.T(MsgCardTitleHistory), "turquoise", e.i18n.T(MsgHistoryEmpty))
	}

	var sb strings.Builder
	for _, h := range entries {
		icon := "👤"
		if h.Role == "assistant" {
			icon = "🤖"
		}
		content := h.Content
		if len([]rune(content)) > 200 {
			content = string([]rune(content)[:200]) + "..."
		}
		sb.WriteString(fmt.Sprintf("%s [%s]\n%s\n\n", icon, h.Timestamp.Format("15:04:05"), content))
	}

	return NewCard().
		Title(e.i18n.Tf(MsgCardTitleHistoryLast, len(entries)), "turquoise").
		Markdown(sb.String()).
		Buttons(e.cardBackButton()).
		Build()
}

func (e *Engine) renderProviderCard() *Card {
	switcher, ok := e.agent.(ProviderSwitcher)
	if !ok {
		return e.simpleCard(e.i18n.T(MsgCardTitleProvider), "indigo", e.i18n.T(MsgProviderNotSupported))
	}

	current := switcher.GetActiveProvider()
	providers := switcher.ListProviders()

	if current == nil && len(providers) == 0 {
		return e.simpleCard(e.i18n.T(MsgCardTitleProvider), "indigo", e.i18n.T(MsgProviderNone))
	}

	var body strings.Builder
	if current != nil {
		body.WriteString(fmt.Sprintf(e.i18n.T(MsgProviderCurrent), current.Name))
		body.WriteString("\n\n")
	}

	cb := NewCard().Title(e.i18n.T(MsgCardTitleProvider), "indigo").Markdown(body.String())
	if len(providers) > 0 {
		var opts []CardSelectOption
		initVal := ""
		for _, prov := range providers {
			label := prov.Name
			if prov.BaseURL != "" {
				label += " (" + prov.BaseURL + ")"
			}
			val := "act:/provider " + prov.Name
			opts = append(opts, CardSelectOption{Text: label, Value: val})
			if current != nil && prov.Name == current.Name {
				initVal = val
			}
		}
		cb.Select(e.i18n.T(MsgProviderSelectPlaceholder), opts, initVal)
	}
	return cb.Buttons(e.cardBackButton()).Build()
}

func (e *Engine) renderCronCard(sessionKey string, userID string) *Card {
	if e.cronScheduler == nil {
		return e.simpleCard(e.i18n.T(MsgCardTitleCron), "orange", e.i18n.T(MsgCronNotAvailable))
	}

	jobs := e.cronScheduler.Store().ListBySessionKey(sessionKey)
	if len(jobs) == 0 {
		return e.simpleCard(e.i18n.T(MsgCardTitleCron), "orange", e.i18n.T(MsgCronEmpty))
	}

	lang := e.i18n.CurrentLang()
	now := time.Now()

	cb := NewCard().Title(e.i18n.T(MsgCardTitleCron), "orange")
	cb.Markdown(fmt.Sprintf(e.i18n.T(MsgCronListTitle), len(jobs)))

	for _, j := range jobs {
		status := "✅"
		if !j.Enabled {
			status = "⏸"
		}

		desc := j.Description
		if desc == "" {
			if j.IsShellJob() {
				desc = "🖥 " + truncateStr(j.Exec, 60)
			} else {
				desc = truncateStr(j.Prompt, 60)
			}
		}
		if j.Mute {
			desc += " [mute]"
		}

		human := CronExprToHuman(j.CronExpr, lang)

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%s %s\n", status, desc))
		sb.WriteString(e.i18n.Tf(MsgCronIDLabel, j.ID))
		sb.WriteString(e.i18n.Tf(MsgCronScheduleLabel, human, j.CronExpr))
		nextRun := e.cronScheduler.NextRun(j.ID)
		if !nextRun.IsZero() {
			fmtStr := cronTimeFormat(nextRun, now)
			sb.WriteString(e.i18n.Tf(MsgCronNextRunLabel, nextRun.Format(fmtStr)))
		}
		if !j.LastRun.IsZero() {
			fmtStr := cronTimeFormat(j.LastRun, now)
			sb.WriteString(e.i18n.Tf(MsgCronLastRunLabel, j.LastRun.Format(fmtStr)))
			if j.LastError != "" {
				sb.WriteString(e.i18n.Tf(MsgCronFailedSuffix, truncateStr(j.LastError, 40)))
			}
			sb.WriteString("\n")
		}
		cb.Markdown(sb.String())

		var btns []CardButton
		if j.Enabled {
			btns = append(btns, DefaultBtn(e.i18n.T(MsgCronBtnDisable), fmt.Sprintf("act:/cron disable %s", j.ID)))
		} else {
			btns = append(btns, PrimaryBtn(e.i18n.T(MsgCronBtnEnable), fmt.Sprintf("act:/cron enable %s", j.ID)))
		}
		if j.Mute {
			btns = append(btns, DefaultBtn(e.i18n.T(MsgCronBtnUnmute), fmt.Sprintf("act:/cron unmute %s", j.ID)))
		} else {
			btns = append(btns, DefaultBtn(e.i18n.T(MsgCronBtnMute), fmt.Sprintf("act:/cron mute %s", j.ID)))
		}
		btns = append(btns, DangerBtn(e.i18n.T(MsgCronBtnDelete), fmt.Sprintf("act:/cron delete %s", j.ID)))
		cb.ButtonsEqual(btns...)
	}

	cb.Divider()
	cb.Note(e.i18n.T(MsgCronCardHint))
	cb.Buttons(e.cardBackButton())
	return cb.Build()
}

func (e *Engine) renderCommandsCard() *Card {
	cmds := e.commands.ListAll()
	if len(cmds) == 0 {
		return e.simpleCard(e.i18n.T(MsgCardTitleCommands), "purple", e.i18n.T(MsgCommandsEmpty))
	}

	var sb strings.Builder
	sb.WriteString(e.i18n.Tf(MsgCommandsTitle, len(cmds)))
	for _, c := range cmds {
		tag := ""
		if c.Source == "agent" {
			tag = e.i18n.T(MsgCommandsTagAgent)
		} else if c.Exec != "" {
			tag = e.i18n.T(MsgCommandsTagShell)
		}
		desc := c.Description
		if desc == "" {
			if c.Exec != "" {
				desc = "$ " + truncateStr(c.Exec, 60)
			} else {
				desc = truncateStr(c.Prompt, 60)
			}
		}
		sb.WriteString(fmt.Sprintf("/%s%s — %s\n", c.Name, tag, desc))
	}

	return NewCard().Title(e.i18n.T(MsgCardTitleCommands), "purple").
		Markdown(sb.String()).
		Note(e.i18n.T(MsgCommandsHint)).
		Buttons(e.cardBackButton()).
		Build()
}

func (e *Engine) renderAliasCard() *Card {
	e.aliasMu.RLock()
	defer e.aliasMu.RUnlock()

	if len(e.aliases) == 0 {
		return e.simpleCard(e.i18n.T(MsgCardTitleAlias), "purple", e.i18n.T(MsgAliasEmpty))
	}

	names := make([]string, 0, len(e.aliases))
	for n := range e.aliases {
		names = append(names, n)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(e.i18n.T(MsgAliasListHeader), len(e.aliases)))
	sb.WriteString("\n")
	for _, n := range names {
		sb.WriteString(fmt.Sprintf("`%s` → `%s`\n", n, e.aliases[n]))
	}

	return NewCard().Title(e.i18n.T(MsgCardTitleAlias), "purple").
		Markdown(sb.String()).
		Buttons(e.cardBackButton()).
		Build()
}

func (e *Engine) renderConfigCard() *Card {
	items := e.configItems()
	isZh := e.i18n.IsZhLike()

	var sb strings.Builder
	sb.WriteString(e.i18n.T(MsgConfigTitle))
	for _, item := range items {
		sb.WriteString(fmt.Sprintf("`%s` = `%s`\n  %s\n\n", item.key, item.getFunc(), item.description(isZh)))
	}

	return NewCard().Title(e.i18n.T(MsgCardTitleConfig), "grey").
		Markdown(sb.String()).
		Note(e.i18n.T(MsgConfigHint)).
		Buttons(e.cardBackButton()).
		Build()
}

func (e *Engine) renderSkillsCard() *Card {
	skills := e.skills.ListAll()
	if len(skills) == 0 {
		return e.simpleCard(e.i18n.T(MsgCardTitleSkills), "purple", e.i18n.T(MsgSkillsEmpty))
	}

	var sb strings.Builder
	sb.WriteString(e.i18n.Tf(MsgSkillsTitle, e.agent.Name(), len(skills)))
	for _, s := range skills {
		sb.WriteString(fmt.Sprintf("  /%s — %s\n", s.Name, s.Description))
	}

	return NewCard().Title(e.i18n.T(MsgCardTitleSkills), "purple").
		Markdown(sb.String()).
		Note(e.i18n.T(MsgSkillsHint)).
		Buttons(e.cardBackButton()).
		Build()
}

func (e *Engine) renderDoctorCard() *Card {
	results := RunDoctorChecks(e.ctx, e.agent, e.platforms)
	report := FormatDoctorResults(results, e.i18n)
	return NewCard().
		Title(e.i18n.T(MsgCardTitleDoctor), "orange").
		Markdown(report).
		Buttons(e.cardBackButton()).
		Build()
}

func (e *Engine) renderVersionCard() *Card {
	return NewCard().
		Title(e.i18n.T(MsgCardTitleVersion), "grey").
		Markdown(VersionInfo).
		Buttons(e.cardBackButton()).
		Build()
}

func (e *Engine) renderUpgradeCard() *Card {
	title := e.i18n.T(MsgCardTitleUpgrade)
	cur := CurrentVersion
	if cur == "" || cur == "dev" {
		return e.simpleCard(title, "grey", e.i18n.T(MsgUpgradeDevBuild))
	}

	type result struct {
		release *ReleaseInfo
		err     error
	}
	ch := make(chan result, 1)
	useGitee := e.i18n.IsZhLike()
	go func() {
		r, err := CheckForUpdate(cur, useGitee)
		ch <- result{r, err}
	}()

	var content string
	select {
	case res := <-ch:
		if res.err != nil {
			content = e.i18n.Tf(MsgError, res.err)
		} else if res.release == nil {
			content = fmt.Sprintf(e.i18n.T(MsgUpgradeUpToDate), cur)
		} else {
			body := res.release.Body
			if len([]rune(body)) > 300 {
				body = string([]rune(body)[:300]) + "…"
			}
			content = fmt.Sprintf(e.i18n.T(MsgUpgradeAvailable), cur, res.release.TagName, body)
		}
	case <-time.After(8 * time.Second):
		content = "⏱ " + e.i18n.T(MsgUpgradeChecking) + e.i18n.T(MsgUpgradeTimeoutSuffix)
	}

	return NewCard().
		Title(title, "grey").
		Markdown(content).
		Buttons(e.cardBackButton()).
		Build()
}

// ──────────────────────────────────────────────────────────────
// /memory command
// ──────────────────────────────────────────────────────────────

func (e *Engine) cmdMemory(p Platform, msg *Message, args []string) {
	mp, ok := e.agent.(MemoryFileProvider)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryNotSupported))
		return
	}

	if len(args) == 0 {
		// /memory — show project memory
		e.showMemoryFile(p, msg, mp.ProjectMemoryFile(), false)
		return
	}

	sub := matchSubCommand(strings.ToLower(args[0]), []string{"add", "global", "show", "help"})
	switch sub {
	case "add":
		text := strings.TrimSpace(strings.Join(args[1:], " "))
		if text == "" {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryAddUsage))
			return
		}
		e.appendMemoryFile(p, msg, mp.ProjectMemoryFile(), text)

	case "global":
		if len(args) == 1 {
			// /memory global — show global memory
			e.showMemoryFile(p, msg, mp.GlobalMemoryFile(), true)
			return
		}
		if strings.ToLower(args[1]) == "add" {
			text := strings.TrimSpace(strings.Join(args[2:], " "))
			if text == "" {
				e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryAddUsage))
				return
			}
			e.appendMemoryFile(p, msg, mp.GlobalMemoryFile(), text)
		} else {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryAddUsage))
		}

	case "show":
		e.showMemoryFile(p, msg, mp.ProjectMemoryFile(), false)

	case "help", "--help", "-h":
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryAddUsage))

	default:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryAddUsage))
	}
}

func (e *Engine) showMemoryFile(p Platform, msg *Message, filePath string, isGlobal bool) {
	if filePath == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryNotSupported))
		return
	}

	data, err := os.ReadFile(filePath)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgMemoryEmpty), filePath))
		return
	}

	content := string(data)
	if len([]rune(content)) > 2000 {
		content = string([]rune(content)[:2000]) + "\n\n... (truncated)"
	}

	if isGlobal {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgMemoryShowGlobal), filePath, content))
	} else {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgMemoryShowProject), filePath, content))
	}
}

func (e *Engine) appendMemoryFile(p Platform, msg *Message, filePath, text string) {
	if filePath == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryNotSupported))
		return
	}

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgMemoryAddFailed), err))
		return
	}

	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgMemoryAddFailed), err))
		return
	}
	defer f.Close()

	entry := "\n- " + text + "\n"
	if _, err := f.WriteString(entry); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgMemoryAddFailed), err))
		return
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgMemoryAdded), filePath))
}

// ──────────────────────────────────────────────────────────────
// /cron command
// ──────────────────────────────────────────────────────────────

func (e *Engine) cmdCron(p Platform, msg *Message, args []string) {
	if e.cronScheduler == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCronNotAvailable))
		return
	}

	if len(args) == 0 {
		if !supportsCards(p) {
			e.cmdCronList(p, msg)
			return
		}
		e.replyWithCard(p, msg.ReplyCtx, e.renderCronCard(msg.SessionKey, msg.UserID))
		return
	}

	sub := matchSubCommand(strings.ToLower(args[0]), []string{
		"add", "addexec", "list", "del", "delete", "rm", "remove", "enable", "disable", "mute", "unmute", "setup",
	})
	switch sub {
	case "add":
		e.cmdCronAdd(p, msg, args[1:])
	case "addexec":
		e.cmdCronAddExec(p, msg, args[1:])
	case "list":
		e.cmdCronList(p, msg)
	case "del", "delete", "rm", "remove":
		e.cmdCronDel(p, msg, args[1:])
	case "enable":
		e.cmdCronToggle(p, msg, args[1:], true)
	case "disable":
		e.cmdCronToggle(p, msg, args[1:], false)
	case "mute":
		e.cmdCronMute(p, msg, args[1:], true)
	case "unmute":
		e.cmdCronMute(p, msg, args[1:], false)
	case "setup":
		e.cmdCronSetup(p, msg)
	default:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCronUsage))
	}
}

func (e *Engine) cmdCronAdd(p Platform, msg *Message, args []string) {
	// /cron add <min> <hour> <day> <month> <weekday> <prompt...>
	if len(args) < 6 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCronAddUsage))
		return
	}

	cronExpr := strings.Join(args[:5], " ")
	prompt := strings.Join(args[5:], " ")

	job := &CronJob{
		ID:         GenerateCronID(),
		Project:    e.name,
		SessionKey: msg.SessionKey,
		CronExpr:   cronExpr,
		Prompt:     prompt,
		Enabled:    true,
		CreatedAt:  time.Now(),
	}

	if err := e.cronScheduler.AddJob(job); err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronAdded), job.ID, cronExpr, truncateStr(prompt, 60)))
}

func (e *Engine) cmdCronAddExec(p Platform, msg *Message, args []string) {
	if !e.isAdmin(msg.UserID) {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgAdminRequired), "/cron addexec"))
		return
	}

	// /cron addexec <min> <hour> <day> <month> <weekday> <shell command...>
	if len(args) < 6 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCronAddExecUsage))
		return
	}

	cronExpr := strings.Join(args[:5], " ")
	shellCmd := strings.Join(args[5:], " ")

	job := &CronJob{
		ID:         GenerateCronID(),
		Project:    e.name,
		SessionKey: msg.SessionKey,
		CronExpr:   cronExpr,
		Exec:       shellCmd,
		Enabled:    true,
		CreatedAt:  time.Now(),
	}

	if err := e.cronScheduler.AddJob(job); err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronAddedExec), job.ID, cronExpr, truncateStr(shellCmd, 60)))
}

func (e *Engine) cmdCronList(p Platform, msg *Message) {
	jobs := e.cronScheduler.Store().ListByProject(e.name)
	if len(jobs) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCronEmpty))
		return
	}

	lang := e.i18n.CurrentLang()
	now := time.Now()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(e.i18n.T(MsgCronListTitle), len(jobs)))
	sb.WriteString("\n")
	sb.WriteString("\n")

	for i, j := range jobs {
		if i > 0 {
			sb.WriteString("\n")
		}

		status := "✅"
		if !j.Enabled {
			status = "⏸"
		}
		desc := j.Description
		if desc == "" {
			if j.IsShellJob() {
				desc = "🖥 " + truncateStr(j.Exec, 60)
			} else {
				desc = truncateStr(j.Prompt, 60)
			}
		}
		if j.Mute {
			desc += " [mute]"
		}
		sb.WriteString(fmt.Sprintf("%s %s\n", status, desc))

		sb.WriteString(fmt.Sprintf("ID: %s\n", j.ID))

		human := CronExprToHuman(j.CronExpr, lang)
		sb.WriteString(e.i18n.Tf(MsgCronScheduleLabel, human, j.CronExpr))

		nextRun := e.cronScheduler.NextRun(j.ID)
		if !nextRun.IsZero() {
			fmtStr := cronTimeFormat(nextRun, now)
			sb.WriteString(e.i18n.Tf(MsgCronNextRunLabel, nextRun.Format(fmtStr)))
		}

		if !j.LastRun.IsZero() {
			fmtStr := cronTimeFormat(j.LastRun, now)
			sb.WriteString(e.i18n.Tf(MsgCronLastRunLabel, j.LastRun.Format(fmtStr)))
			if j.LastError != "" {
				sb.WriteString(fmt.Sprintf(" (failed: %s)", truncateStr(j.LastError, 40)))
			}
			sb.WriteString("\n")
		}
	}

	sb.WriteString(fmt.Sprintf("\n%s", e.i18n.T(MsgCronListFooter)))
	e.reply(p, msg.ReplyCtx, sb.String())
}

func (e *Engine) cmdCronDel(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCronDelUsage))
		return
	}
	id := args[0]
	if e.cronScheduler.RemoveJob(id) {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronDeleted), id))
	} else {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronNotFound), id))
	}
}

func (e *Engine) cmdCronToggle(p Platform, msg *Message, args []string, enable bool) {
	if len(args) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCronDelUsage))
		return
	}
	id := args[0]
	var err error
	if enable {
		err = e.cronScheduler.EnableJob(id)
	} else {
		err = e.cronScheduler.DisableJob(id)
	}
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	if enable {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronEnabled), id))
	} else {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronDisabled), id))
	}
}

func (e *Engine) cmdCronMute(p Platform, msg *Message, args []string, mute bool) {
	if len(args) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCronDelUsage))
		return
	}
	id := args[0]
	if !e.cronScheduler.Store().SetMute(id, mute) {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronNotFound), id))
		return
	}
	if mute {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronMuted), id))
	} else {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronUnmuted), id))
	}
}

func (e *Engine) cmdCronSetup(p Platform, msg *Message) {
	result, baseName, err := e.setupMemoryFile()
	switch result {
	case setupNative:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSetupNative))
	case setupNoMemory:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgRelaySetupNoMemory))
	case setupExists:
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgRelaySetupExists), baseName))
	case setupError:
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
	case setupOK:
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronSetupOK), baseName))
	}
}

// ──────────────────────────────────────────────────────────────
// Heartbeat management commands
// ──────────────────────────────────────────────────────────────

func (e *Engine) cmdHeartbeat(p Platform, msg *Message, args []string) {
	if e.heartbeatScheduler == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgHeartbeatNotAvailable))
		return
	}

	status := e.heartbeatScheduler.Status(e.name)
	if status == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgHeartbeatNotAvailable))
		return
	}

	sub := "status"
	if len(args) > 0 {
		sub = matchSubCommand(strings.ToLower(args[0]), []string{
			"status", "pause", "stop", "resume", "start", "run", "trigger", "interval",
		})
	}

	switch sub {
	case "status", "":
		if supportsCards(p) {
			e.replyWithCard(p, msg.ReplyCtx, e.renderHeartbeatCard())
			return
		}
		e.cmdHeartbeatStatusText(p, msg, status)
	case "pause", "stop":
		e.heartbeatScheduler.Pause(e.name)
		if supportsCards(p) {
			e.replyWithCard(p, msg.ReplyCtx, e.renderHeartbeatCard())
		} else {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgHeartbeatPaused))
		}
	case "resume", "start":
		e.heartbeatScheduler.Resume(e.name)
		if supportsCards(p) {
			e.replyWithCard(p, msg.ReplyCtx, e.renderHeartbeatCard())
		} else {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgHeartbeatResumed))
		}
	case "run", "trigger":
		e.heartbeatScheduler.TriggerNow(e.name)
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgHeartbeatTriggered))
	case "interval":
		if len(args) < 2 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgHeartbeatUsage))
			return
		}
		mins, err := strconv.Atoi(args[1])
		if err != nil || mins <= 0 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgHeartbeatInvalidMins))
			return
		}
		e.heartbeatScheduler.SetInterval(e.name, mins)
		if supportsCards(p) {
			e.replyWithCard(p, msg.ReplyCtx, e.renderHeartbeatCard())
		} else {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgHeartbeatInterval), mins))
		}
	default:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgHeartbeatUsage))
	}
}

func (e *Engine) cmdHeartbeatStatusText(p Platform, msg *Message, st *HeartbeatStatus) {
	stateStr, yesNo := e.heartbeatLocalizedHelpers()

	lastRunStr := ""
	if !st.LastRun.IsZero() {
		lang := e.i18n.CurrentLang()
		switch lang {
		case LangChinese, LangTraditionalChinese:
			lastRunStr = "上次执行: " + st.LastRun.Format("01-02 15:04:05") + "\n"
		case LangJapanese:
			lastRunStr = "最終実行: " + st.LastRun.Format("01-02 15:04:05") + "\n"
		default:
			lastRunStr = "Last run: " + st.LastRun.Format("01-02 15:04:05") + "\n"
		}
		if st.LastError != "" {
			lastRunStr += "⚠️ " + truncateStr(st.LastError, 80) + "\n"
		}
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgHeartbeatStatus),
		stateStr(st.Paused),
		st.IntervalMins,
		yesNo(st.OnlyWhenIdle),
		yesNo(st.Silent),
		st.RunCount,
		st.ErrorCount,
		st.SkippedBusy,
		lastRunStr,
	))
}

func (e *Engine) heartbeatLocalizedHelpers() (stateStr func(paused bool) string, yesNo func(bool) string) {
	lang := e.i18n.CurrentLang()
	switch lang {
	case LangChinese, LangTraditionalChinese:
		stateStr = func(paused bool) string {
			if paused {
				return "⏸ 已暂停"
			}
			return "▶️ 运行中"
		}
		yesNo = func(b bool) string {
			if b {
				return "是"
			}
			return "否"
		}
	case LangJapanese:
		stateStr = func(paused bool) string {
			if paused {
				return "⏸ 一時停止"
			}
			return "▶️ 実行中"
		}
		yesNo = func(b bool) string {
			if b {
				return "はい"
			}
			return "いいえ"
		}
	default:
		stateStr = func(paused bool) string {
			if paused {
				return "⏸ paused"
			}
			return "▶️ running"
		}
		yesNo = func(b bool) string {
			if b {
				return "yes"
			}
			return "no"
		}
	}
	return
}

func (e *Engine) renderHeartbeatCard() *Card {
	if e.heartbeatScheduler == nil {
		return e.simpleCard(e.i18n.T(MsgCardTitleHeartbeat), "purple", e.i18n.T(MsgHeartbeatNotAvailable))
	}
	st := e.heartbeatScheduler.Status(e.name)
	if st == nil {
		return e.simpleCard(e.i18n.T(MsgCardTitleHeartbeat), "purple", e.i18n.T(MsgHeartbeatNotAvailable))
	}

	stateStr, yesNo := e.heartbeatLocalizedHelpers()
	lang := e.i18n.CurrentLang()

	lastRunStr := ""
	if !st.LastRun.IsZero() {
		switch lang {
		case LangChinese, LangTraditionalChinese:
			lastRunStr = "上次执行: " + st.LastRun.Format("01-02 15:04:05") + "\n"
		case LangJapanese:
			lastRunStr = "最終実行: " + st.LastRun.Format("01-02 15:04:05") + "\n"
		default:
			lastRunStr = "Last run: " + st.LastRun.Format("01-02 15:04:05") + "\n"
		}
		if st.LastError != "" {
			lastRunStr += "⚠️ " + truncateStr(st.LastError, 80) + "\n"
		}
	}

	body := fmt.Sprintf(e.i18n.T(MsgHeartbeatStatus),
		stateStr(st.Paused),
		st.IntervalMins,
		yesNo(st.OnlyWhenIdle),
		yesNo(st.Silent),
		st.RunCount,
		st.ErrorCount,
		st.SkippedBusy,
		lastRunStr,
	)

	cb := NewCard().Title(e.i18n.T(MsgCardTitleHeartbeat), "purple").Markdown(body)

	var actionBtns []CardButton
	if st.Paused {
		actionBtns = append(actionBtns, PrimaryBtn("▶️ Resume", "act:/heartbeat resume"))
	} else {
		actionBtns = append(actionBtns, DefaultBtn("⏸ Pause", "act:/heartbeat pause"))
	}
	actionBtns = append(actionBtns, DefaultBtn("💓 Run Now", "act:/heartbeat run"))
	cb.Buttons(actionBtns...)

	cb.Buttons(e.cardBackButton())

	return cb.Build()
}

// ──────────────────────────────────────────────────────────────
// Custom command execution & management
// ──────────────────────────────────────────────────────────────

func (e *Engine) executeCustomCommand(p Platform, msg *Message, cmd *CustomCommand, args []string) {
	if cmd.Exec != "" && !e.isAdmin(msg.UserID) {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgAdminRequired), "/"+cmd.Name))
		return
	}
	// If this is an exec command, run shell command directly
	if cmd.Exec != "" {
		go e.executeShellCommand(p, msg, cmd, args)
		return
	}

	// Otherwise, use prompt template
	prompt := ExpandPrompt(cmd.Prompt, args)

	session := e.sessions.GetOrCreateActive(msg.SessionKey)
	if !session.TryLock() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPreviousProcessing))
		return
	}

	slog.Info("executing custom command",
		"command", cmd.Name,
		"source", cmd.Source,
		"user", msg.UserName,
	)

	msg.Content = prompt
	go e.processInteractiveMessage(p, msg, session)
}

// executeShellCommand runs a shell command and sends the output to the user.
func (e *Engine) executeShellCommand(p Platform, msg *Message, cmd *CustomCommand, args []string) {
	slog.Info("executing shell command",
		"command", cmd.Name,
		"exec", cmd.Exec,
		"user", msg.UserName,
	)

	// Expand placeholders in exec command
	execCmd := ExpandPrompt(cmd.Exec, args)

	// Determine working directory
	workDir := cmd.WorkDir
	if workDir == "" {
		// Default to agent's work_dir if available
		if e.agent != nil {
			if agentOpts, ok := e.agent.(interface{ GetWorkDir() string }); ok {
				workDir = agentOpts.GetWorkDir()
			}
		}
	}
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(e.ctx, 60*time.Second)
	defer cancel()

	// Execute command using the native shell so Windows config commands work too.
	var shellCmd *exec.Cmd
	if runtime.GOOS == "windows" {
		shellCmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", execCmd)
	} else {
		shellCmd = exec.CommandContext(ctx, "sh", "-c", execCmd)
	}
	shellCmd.Dir = workDir
	envVars := []string{
		"CC_PROJECT=" + e.name,
		"CC_SESSION_KEY=" + msg.SessionKey,
	}
	// Prepend the cc-connect binary dir on Windows only (native shell fix);
	// on Unix it would change command resolution for user scripts.
	if runtime.GOOS == "windows" {
		if exePath, err := os.Executable(); err == nil {
			binDir := filepath.Dir(exePath)
			if curPath := os.Getenv("PATH"); curPath != "" {
				envVars = append(envVars, "PATH="+binDir+string(filepath.ListSeparator)+curPath)
			} else {
				envVars = append(envVars, "PATH="+binDir)
			}
		}
	}
	shellCmd.Env = MergeEnv(os.Environ(), envVars)
	output, err := shellCmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandExecTimeout), cmd.Name))
		return
	}

	if err != nil {
		errMsg := string(output)
		if errMsg == "" {
			errMsg = err.Error()
		}
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandExecError), cmd.Name, truncateStr(errMsg, 1000)))
		return
	}

	result := strings.TrimSpace(string(output))
	if result == "" {
		result = e.i18n.T(MsgCommandExecSuccess)
	} else if len(result) > 4000 {
		result = result[:3997] + "..."
	}

	e.reply(p, msg.ReplyCtx, result)
}

func (e *Engine) cmdCommands(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		if !supportsCards(p) {
			e.cmdCommandsList(p, msg)
			return
		}
		e.replyWithCard(p, msg.ReplyCtx, e.renderCommandsCard())
		return
	}

	sub := matchSubCommand(strings.ToLower(args[0]), []string{
		"list", "add", "addexec", "del", "delete", "rm", "remove",
	})
	switch sub {
	case "list":
		e.cmdCommandsList(p, msg)
	case "add":
		e.cmdCommandsAdd(p, msg, args[1:])
	case "addexec":
		e.cmdCommandsAddExec(p, msg, args[1:])
	case "del", "delete", "rm", "remove":
		e.cmdCommandsDel(p, msg, args[1:])
	default:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCommandsUsage))
	}
}

func (e *Engine) cmdCommandsList(p Platform, msg *Message) {
	cmds := e.commands.ListAll()
	if len(cmds) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCommandsEmpty))
		return
	}

	var sb strings.Builder
	sb.WriteString(e.i18n.Tf(MsgCommandsTitle, len(cmds)))

	for _, c := range cmds {
		// Tag
		tag := ""
		if c.Source == "agent" {
			tag = " [agent]"
		} else if c.Exec != "" {
			tag = " [shell]"
		}
		sb.WriteString(fmt.Sprintf("/%s%s\n", c.Name, tag))

		// Description or fallback
		desc := c.Description
		if desc == "" {
			if c.Exec != "" {
				desc = "$ " + truncateStr(c.Exec, 60)
			} else {
				desc = truncateStr(c.Prompt, 60)
			}
		}
		sb.WriteString(fmt.Sprintf("  %s\n\n", desc))
	}

	sb.WriteString(e.i18n.T(MsgCommandsHint))
	e.reply(p, msg.ReplyCtx, sb.String())
}

func (e *Engine) cmdCommandsAdd(p Platform, msg *Message, args []string) {
	// /commands add <name> <prompt...>
	if len(args) < 2 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCommandsAddUsage))
		return
	}

	name := strings.ToLower(args[0])
	prompt := strings.Join(args[1:], " ")

	if _, exists := e.commands.Resolve(name); exists {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandsAddExists), name, name))
		return
	}

	e.commands.Add(name, "", prompt, "", "", "config")

	if e.commandSaveAddFunc != nil {
		if err := e.commandSaveAddFunc(name, "", prompt, "", ""); err != nil {
			slog.Error("failed to persist command", "error", err)
		}
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandsAdded), name, truncateStr(prompt, 80)))
}

func (e *Engine) cmdCommandsAddExec(p Platform, msg *Message, args []string) {
	if !e.isAdmin(msg.UserID) {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgAdminRequired), "/commands addexec"))
		return
	}
	// /commands addexec <name> <shell command...>
	// /commands addexec --work-dir <dir> <name> <shell command...>
	if len(args) < 2 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCommandsAddExecUsage))
		return
	}

	// Parse --work-dir flag
	workDir := ""
	i := 0
	if args[0] == "--work-dir" && len(args) >= 3 {
		workDir = args[1]
		i = 2
	}

	if i >= len(args) {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCommandsAddExecUsage))
		return
	}

	name := strings.ToLower(args[i])
	execCmd := ""
	if i+1 < len(args) {
		execCmd = strings.Join(args[i+1:], " ")
	}

	if execCmd == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCommandsAddExecUsage))
		return
	}

	if _, exists := e.commands.Resolve(name); exists {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandsAddExists), name, name))
		return
	}

	e.commands.Add(name, "", "", execCmd, workDir, "config")

	if e.commandSaveAddFunc != nil {
		if err := e.commandSaveAddFunc(name, "", "", execCmd, workDir); err != nil {
			slog.Error("failed to persist command", "error", err)
		}
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandsExecAdded), name, truncateStr(execCmd, 80)))
}

func (e *Engine) cmdCommandsDel(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCommandsDelUsage))
		return
	}
	name := strings.ToLower(args[0])

	if !e.commands.Remove(name) {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandsNotFound), name))
		return
	}

	if e.commandSaveDelFunc != nil {
		if err := e.commandSaveDelFunc(name); err != nil {
			slog.Error("failed to persist command removal", "error", err)
		}
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandsDeleted), name))
}

// ──────────────────────────────────────────────────────────────
// Skill discovery & execution
// ──────────────────────────────────────────────────────────────

func (e *Engine) executeSkill(p Platform, msg *Message, skill *Skill, args []string) {
	prompt := BuildSkillInvocationPrompt(skill, args)

	session := e.sessions.GetOrCreateActive(msg.SessionKey)
	if !session.TryLock() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPreviousProcessing))
		return
	}

	slog.Info("executing skill",
		"skill", skill.Name,
		"source", skill.Source,
		"user", msg.UserName,
	)

	msg.Content = prompt
	go e.processInteractiveMessage(p, msg, session)
}

func (e *Engine) cmdSkills(p Platform, msg *Message) {
	if !supportsCards(p) {
		skills := e.skills.ListAll()
		if len(skills) == 0 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSkillsEmpty))
			return
		}

		var sb strings.Builder
		sb.WriteString(e.i18n.Tf(MsgSkillsTitle, e.agent.Name(), len(skills)))

		for _, s := range skills {
			sb.WriteString(fmt.Sprintf("  /%s — %s\n", s.Name, s.Description))
		}

		sb.WriteString("\n" + e.i18n.T(MsgSkillsHint))
		if _, skillsOmitted := e.menuCommandsForPlatform(p.Name()); skillsOmitted && strings.EqualFold(p.Name(), "telegram") {
			sb.WriteString("\n" + e.i18n.T(MsgSkillsTelegramMenuHint))
		}
		e.reply(p, msg.ReplyCtx, sb.String())
		return
	}

	e.replyWithCard(p, msg.ReplyCtx, e.renderSkillsCard())
}

// ── /config command ──────────────────────────────────────────

// configItem describes a configurable runtime parameter.
type configItem struct {
	key     string
	desc    string // en description
	descZh  string // zh description
	getFunc func() string
	setFunc func(string) error
}

func (ci configItem) description(isZh bool) string {
	if isZh && ci.descZh != "" {
		return ci.descZh
	}
	return ci.desc
}

func (e *Engine) configItems() []configItem {
	return []configItem{
		{
			key:    "mode",
			desc:   "Display mode: full, compact, quiet",
			descZh: "显示模式: full, compact, quiet",
			getFunc: func() string {
				if e.display.Mode == "" {
					return "full"
				}
				return e.display.Mode
			},
			setFunc: func(v string) error {
				switch v {
				case "full":
					e.display.Mode = "full"
					e.display.ThinkingMessages = true
					e.display.ToolMessages = true
				case "compact", "quiet":
					e.display.Mode = v
					e.display.ThinkingMessages = false
					e.display.ToolMessages = false
				default:
					return fmt.Errorf("must be full, compact, or quiet")
				}
				if e.displaySaveFunc != nil {
					tm := e.display.ThinkingMessages
					tool := e.display.ToolMessages
					return e.displaySaveFunc(&v, &tm, nil, nil, &tool)
				}
				return nil
			},
		},
		{
			key:    "thinking_messages",
			desc:   "Whether thinking messages are shown (true/false)",
			descZh: "是否显示思考消息 (true/false)",
			getFunc: func() string {
				return fmt.Sprintf("%t", e.display.ThinkingMessages)
			},
			setFunc: func(v string) error {
				b, err := strconv.ParseBool(v)
				if err != nil {
					return fmt.Errorf("invalid boolean: %s", v)
				}
				e.display.ThinkingMessages = b
				if e.displaySaveFunc != nil {
					return e.displaySaveFunc(nil, &b, nil, nil, nil)
				}
				return nil
			},
		},
		{
			key:    "thinking_max_len",
			desc:   "Max chars for thinking messages (0=no truncation)",
			descZh: "思考消息最大长度 (0=不截断)",
			getFunc: func() string {
				return fmt.Sprintf("%d", e.display.ThinkingMaxLen)
			},
			setFunc: func(v string) error {
				n, err := strconv.Atoi(v)
				if err != nil {
					return fmt.Errorf("invalid integer: %s", v)
				}
				if n < 0 {
					return fmt.Errorf("value must be >= 0")
				}
				e.display.ThinkingMaxLen = n
				if e.displaySaveFunc != nil {
					return e.displaySaveFunc(nil, nil, &n, nil, nil)
				}
				return nil
			},
		},
		{
			key:    "tool_messages",
			desc:   "Whether tool progress messages are shown (true/false)",
			descZh: "是否显示工具进度消息 (true/false)",
			getFunc: func() string {
				return fmt.Sprintf("%t", e.display.ToolMessages)
			},
			setFunc: func(v string) error {
				b, err := strconv.ParseBool(v)
				if err != nil {
					return fmt.Errorf("invalid boolean: %s", v)
				}
				e.display.ToolMessages = b
				if e.displaySaveFunc != nil {
					return e.displaySaveFunc(nil, nil, nil, nil, &b)
				}
				return nil
			},
		},
		{
			key:    "tool_max_len",
			desc:   "Max chars for tool use messages (0=no truncation)",
			descZh: "工具消息最大长度 (0=不截断)",
			getFunc: func() string {
				return fmt.Sprintf("%d", e.display.ToolMaxLen)
			},
			setFunc: func(v string) error {
				n, err := strconv.Atoi(v)
				if err != nil {
					return fmt.Errorf("invalid integer: %s", v)
				}
				if n < 0 {
					return fmt.Errorf("value must be >= 0")
				}
				e.display.ToolMaxLen = n
				if e.displaySaveFunc != nil {
					return e.displaySaveFunc(nil, nil, nil, &n, nil)
				}
				return nil
			},
		},
	}
}

func (e *Engine) cmdConfig(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		if !supportsCards(p) {
			items := e.configItems()
			isZh := e.i18n.IsZhLike()
			var sb strings.Builder
			sb.WriteString(e.i18n.T(MsgConfigTitle))
			for _, item := range items {
				sb.WriteString(fmt.Sprintf("`%s` = `%s`\n  %s\n\n", item.key, item.getFunc(), item.description(isZh)))
			}
			sb.WriteString(e.i18n.T(MsgConfigHint))
			e.reply(p, msg.ReplyCtx, sb.String())
			return
		}

		e.replyWithCard(p, msg.ReplyCtx, e.renderConfigCard())
		return
	}

	items := e.configItems()
	isZh := e.i18n.IsZhLike()
	sub := matchSubCommand(strings.ToLower(args[0]), []string{"get", "set", "reload"})

	switch sub {
	case "reload":
		e.cmdConfigReload(p, msg)
		return
	case "get":
		if len(args) < 2 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgConfigGetUsage))
			return
		}
		key := strings.ToLower(args[1])
		for _, item := range items {
			if item.key == key {
				e.reply(p, msg.ReplyCtx, fmt.Sprintf("`%s` = `%s`\n  %s", key, item.getFunc(), item.description(isZh)))
				return
			}
		}
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgConfigKeyNotFound, key))

	case "set":
		if len(args) < 3 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgConfigSetUsage))
			return
		}
		key := strings.ToLower(args[1])
		value := args[2]
		for _, item := range items {
			if item.key == key {
				if err := item.setFunc(value); err != nil {
					e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
					return
				}
				e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgConfigUpdated, key, item.getFunc()))
				return
			}
		}
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgConfigKeyNotFound, key))

	default:
		key := strings.ToLower(sub)
		for _, item := range items {
			if item.key == key {
				if len(args) >= 2 {
					if err := item.setFunc(args[1]); err != nil {
						e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
						return
					}
					e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgConfigUpdated, key, item.getFunc()))
				} else {
					e.reply(p, msg.ReplyCtx, fmt.Sprintf("`%s` = `%s`\n  %s", key, item.getFunc(), item.description(isZh)))
				}
				return
			}
		}
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgConfigKeyNotFound, key))
	}
}

// ── /whoami command ─────────────────────────────────────────

func (e *Engine) cmdWhoami(p Platform, msg *Message) {
	if supportsCards(p) {
		e.replyWithCard(p, msg.ReplyCtx, e.renderWhoamiCard(msg))
		return
	}
	e.reply(p, msg.ReplyCtx, e.formatWhoamiText(msg))
}

func (e *Engine) formatWhoamiText(msg *Message) string {
	var sb strings.Builder
	sb.WriteString(e.i18n.T(MsgWhoamiTitle))
	sb.WriteString("\n")

	if msg.UserID != "" {
		sb.WriteString(fmt.Sprintf("User ID: `%s`\n", msg.UserID))
	} else {
		sb.WriteString("User ID: (unknown)\n")
	}
	if msg.UserName != "" {
		sb.WriteString(fmt.Sprintf("Name: %s\n", msg.UserName))
	}
	if msg.Platform != "" {
		sb.WriteString(fmt.Sprintf("Platform: %s\n", msg.Platform))
	}

	chatID := extractChannelID(msg.SessionKey)
	if chatID != "" {
		sb.WriteString(fmt.Sprintf("Chat ID: `%s`\n", chatID))
	}
	sb.WriteString(fmt.Sprintf("Session Key: `%s`\n", msg.SessionKey))

	sb.WriteString("\n")
	sb.WriteString(e.i18n.T(MsgWhoamiUsage))
	return sb.String()
}

func (e *Engine) renderWhoamiCard(msg *Message) *Card {
	userID := msg.UserID
	if userID == "" {
		userID = "(unknown)"
	}

	var body strings.Builder
	body.WriteString(fmt.Sprintf("**User ID:**  `%s`\n", userID))
	if msg.UserName != "" {
		body.WriteString(fmt.Sprintf("**%s:**  %s\n", e.i18n.T(MsgWhoamiName), msg.UserName))
	}
	if msg.Platform != "" {
		body.WriteString(fmt.Sprintf("**%s:**  %s\n", e.i18n.T(MsgWhoamiPlatform), msg.Platform))
	}
	chatID := extractChannelID(msg.SessionKey)
	if chatID != "" {
		body.WriteString(fmt.Sprintf("**Chat ID:**  `%s`\n", chatID))
	}
	body.WriteString(fmt.Sprintf("**Session Key:**  `%s`\n", msg.SessionKey))

	return NewCard().
		Title(e.i18n.T(MsgWhoamiCardTitle), "blue").
		Markdown(body.String()).
		Divider().
		Note(e.i18n.T(MsgWhoamiUsage)).
		Buttons(e.cardBackButton()).
		Build()
}

// ── /doctor command ─────────────────────────────────────────

func (e *Engine) cmdDoctor(p Platform, msg *Message) {
	results := RunDoctorChecks(e.ctx, e.agent, e.platforms)
	report := FormatDoctorResults(results, e.i18n)
	e.reply(p, msg.ReplyCtx, report)
}

func (e *Engine) cmdUpgrade(p Platform, msg *Message, args []string) {
	subCmd := ""
	if len(args) > 0 {
		subCmd = matchSubCommand(args[0], []string{"confirm", "check"})
	}

	if subCmd == "confirm" {
		e.cmdUpgradeConfirm(p, msg)
		return
	}

	// Default: check for updates
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgUpgradeChecking))

	cur := CurrentVersion
	if cur == "" || cur == "dev" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgUpgradeDevBuild))
		return
	}

	useGitee := e.i18n.IsZhLike()
	release, err := CheckForUpdate(cur, useGitee)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	if release == nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgUpgradeUpToDate), cur))
		return
	}

	body := release.Body
	if len([]rune(body)) > 300 {
		body = string([]rune(body)[:300]) + "…"
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgUpgradeAvailable), cur, release.TagName, body))
}

func (e *Engine) cmdUpgradeConfirm(p Platform, msg *Message) {
	cur := CurrentVersion
	if cur == "" || cur == "dev" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgUpgradeDevBuild))
		return
	}

	useGitee := e.i18n.IsZhLike()
	release, err := CheckForUpdate(cur, useGitee)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	if release == nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgUpgradeUpToDate), cur))
		return
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgUpgradeDownloading), release.TagName))

	if err := SelfUpdate(release.TagName, useGitee); err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgUpgradeSuccess), release.TagName))

	// Auto-restart to apply the update
	select {
	case RestartCh <- RestartRequest{
		SessionKey: msg.SessionKey,
		Platform:   p.Name(),
	}:
	default:
	}
}

func (e *Engine) cmdConfigReload(p Platform, msg *Message) {
	if e.configReloadFunc == nil {
		e.reply(p, msg.ReplyCtx, "❌ Config reload not available")
		return
	}
	result, err := e.configReloadFunc()
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgConfigReloaded),
		result.DisplayUpdated, result.ProvidersUpdated, result.CommandsUpdated))
}

func (e *Engine) cmdRestart(p Platform, msg *Message) {
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgRestarting))
	select {
	case RestartCh <- RestartRequest{
		SessionKey: msg.SessionKey,
		Platform:   p.Name(),
	}:
	default:
	}
}

func (e *Engine) cmdAlias(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		if !supportsCards(p) {
			e.cmdAliasList(p, msg)
			return
		}
		e.replyWithCard(p, msg.ReplyCtx, e.renderAliasCard())
		return
	}

	sub := matchSubCommand(strings.ToLower(args[0]), []string{"list", "add", "del", "delete", "remove"})
	switch sub {
	case "list":
		e.cmdAliasList(p, msg)
	case "add":
		e.cmdAliasAdd(p, msg, args[1:])
	case "del", "delete", "remove":
		e.cmdAliasDel(p, msg, args[1:])
	default:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgAliasUsage))
	}
}

func (e *Engine) cmdAliasList(p Platform, msg *Message) {
	e.aliasMu.RLock()
	defer e.aliasMu.RUnlock()

	if len(e.aliases) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgAliasEmpty))
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(e.i18n.T(MsgAliasListHeader), len(e.aliases)))
	sb.WriteString("\n")

	names := make([]string, 0, len(e.aliases))
	for n := range e.aliases {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, n := range names {
		sb.WriteString(fmt.Sprintf("  %s → %s\n", n, e.aliases[n]))
	}
	e.reply(p, msg.ReplyCtx, strings.TrimRight(sb.String(), "\n"))
}

func (e *Engine) cmdAliasAdd(p Platform, msg *Message, args []string) {
	if len(args) < 2 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgAliasUsage))
		return
	}
	name := args[0]
	command := strings.Join(args[1:], " ")
	if !strings.HasPrefix(command, "/") {
		command = "/" + command
	}

	e.aliasMu.Lock()
	e.aliases[name] = command
	e.aliasMu.Unlock()

	if e.aliasSaveAddFunc != nil {
		if err := e.aliasSaveAddFunc(name, command); err != nil {
			slog.Error("alias: save failed", "error", err)
		}
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgAliasAdded), name, command))
}

func (e *Engine) cmdAliasDel(p Platform, msg *Message, args []string) {
	if len(args) < 1 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgAliasUsage))
		return
	}
	name := args[0]

	e.aliasMu.Lock()
	_, exists := e.aliases[name]
	if exists {
		delete(e.aliases, name)
	}
	e.aliasMu.Unlock()

	if !exists {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgAliasNotFound), name))
		return
	}

	if e.aliasSaveDelFunc != nil {
		if err := e.aliasSaveDelFunc(name); err != nil {
			slog.Error("alias: save failed", "error", err)
		}
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgAliasDeleted), name))
}

func (e *Engine) cmdDelete(p Platform, msg *Message, args []string) {
	agent, sessions, _, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}
	deleter, ok := agent.(SessionDeleter)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDeleteNotSupported))
		return
	}

	if len(args) == 0 {
		if supportsCards(p) {
			_ = e.getOrCreateDeleteModeState(msg.SessionKey, p, msg.ReplyCtx)
			e.replyWithCard(p, msg.ReplyCtx, e.renderDeleteModeCard(msg.SessionKey))
			return
		}
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDeleteUsage))
		return
	}
	if len(args) > 1 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDeleteUsage))
		return
	}

	agentSessions, err := agent.ListSessions(e.ctx)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	agentSessions = filterOwnedSessions(agentSessions, sessions.KnownAgentSessionIDs())

	prefix := strings.TrimSpace(args[0])
	if isExplicitDeleteBatchArg(prefix) {
		indices, err := parseDeleteBatchIndices(prefix, len(agentSessions))
		if err != nil {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDeleteUsage))
			return
		}
		e.cmdDeleteBatch(p, msg, deleter, agentSessions, indices)
		return
	}
	var matched *AgentSessionInfo

	if idx, err := strconv.Atoi(prefix); err == nil && idx >= 1 && idx <= len(agentSessions) {
		matched = &agentSessions[idx-1]
	} else {
		for i := range agentSessions {
			if strings.HasPrefix(agentSessions[i].ID, prefix) {
				matched = &agentSessions[i]
				break
			}
		}
	}

	if matched == nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgSwitchNoMatch), prefix))
		return
	}

	e.deleteSingleSession(p, msg, deleter, matched)
}

func isExplicitDeleteBatchArg(arg string) bool {
	if strings.Contains(arg, ",") {
		return true
	}
	if !strings.Contains(arg, "-") {
		return false
	}
	for _, r := range arg {
		if (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func parseDeleteBatchIndices(spec string, max int) ([]int, error) {
	parts := strings.Split(spec, ",")
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty batch spec")
	}
	seen := make(map[int]struct{}, len(parts))
	indices := make([]int, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty batch item")
		}

		if strings.Contains(part, "-") {
			bounds := strings.Split(part, "-")
			if len(bounds) != 2 || bounds[0] == "" || bounds[1] == "" {
				return nil, fmt.Errorf("invalid range %q", part)
			}
			start, err := strconv.Atoi(bounds[0])
			if err != nil {
				return nil, err
			}
			end, err := strconv.Atoi(bounds[1])
			if err != nil {
				return nil, err
			}
			if start < 1 || end < 1 || start > end || end > max {
				return nil, fmt.Errorf("range %q out of bounds", part)
			}
			for idx := start; idx <= end; idx++ {
				if _, ok := seen[idx]; ok {
					continue
				}
				seen[idx] = struct{}{}
				indices = append(indices, idx)
			}
			continue
		}

		idx, err := strconv.Atoi(part)
		if err != nil {
			return nil, err
		}
		if idx < 1 || idx > max {
			return nil, fmt.Errorf("index %d out of bounds", idx)
		}
		if _, ok := seen[idx]; ok {
			continue
		}
		seen[idx] = struct{}{}
		indices = append(indices, idx)
	}

	return indices, nil
}

func (e *Engine) cmdDeleteBatch(p Platform, msg *Message, deleter SessionDeleter, sessions []AgentSessionInfo, indices []int) {
	lines := make([]string, 0, len(indices))
	for _, idx := range indices {
		matched := &sessions[idx-1]
		if line := e.deleteSingleSessionReply(msg, deleter, matched); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDeleteUsage))
		return
	}
	e.reply(p, msg.ReplyCtx, strings.Join(lines, "\n"))
}

func (e *Engine) deleteSingleSession(p Platform, msg *Message, deleter SessionDeleter, matched *AgentSessionInfo) {
	e.reply(p, msg.ReplyCtx, e.deleteSingleSessionReply(msg, deleter, matched))
}

func (e *Engine) deleteSingleSessionReply(msg *Message, deleter SessionDeleter, matched *AgentSessionInfo) string {
	if matched == nil {
		return ""
	}

	// Prevent deleting the currently active session
	_, sessions := e.sessionContextForKey(msg.SessionKey)
	activeSession := sessions.GetOrCreateActive(msg.SessionKey)
	if activeSession.GetAgentSessionID() == matched.ID {
		return e.i18n.T(MsgDeleteActiveDenied)
	}

	displayName := e.deleteSessionDisplayName(sessions, matched)

	if err := deleter.DeleteSession(e.ctx, matched.ID); err != nil {
		return e.i18n.Tf(MsgFailedToDeleteSession, displayName, err)
	}

	// Keep local session snapshot aligned with agent-side deletion.
	sessions.DeleteByAgentSessionID(matched.ID)
	sessions.SetSessionName(matched.ID, "")
	return fmt.Sprintf(e.i18n.T(MsgDeleteSuccess), displayName)
}

func (e *Engine) deleteSessionDisplayName(sessions *SessionManager, matched *AgentSessionInfo) string {
	displayName := sessions.GetSessionName(matched.ID)
	if displayName == "" {
		displayName = matched.Summary
	}
	if displayName == "" {
		shortID := matched.ID
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}
		displayName = shortID
	}
	return displayName
}

// toolCodeLang picks the code block language hint for tool display.
func toolCodeLang(toolName, input string) string {
	switch toolName {
	case "shell", "run_shell_command", "Bash":
		return "bash"
	case "write_file", "WriteFile", "replace", "ReplaceInFile":
		if strings.Contains(input, "\n- ") || strings.Contains(input, "\n+ ") {
			return "diff"
		}
	}
	// Fallback: detect diff-like content
	if strings.Contains(input, "\n- ") && strings.Contains(input, "\n+ ") {
		return "diff"
	}
	return ""
}

func (e *Engine) formatToolResultEventFallback(toolName, result, status string, exitCode *int, success *bool) string {
	statusLabel := e.i18n.T(MsgToolResultFmtStatus)
	exitLabel := e.i18n.T(MsgToolResultFmtExit)
	noOutput := e.i18n.T(MsgToolResultFmtNoOutput)
	dot := "⚪"
	if success != nil {
		if *success {
			dot = "🟢"
		} else {
			dot = "🔴"
		}
	}
	var lines []string
	first := "🧾"
	if strings.TrimSpace(toolName) != "" {
		first += " " + strings.TrimSpace(toolName)
	}
	lines = append(lines, first)
	if strings.TrimSpace(status) != "" || success != nil {
		s := strings.TrimSpace(status)
		if s == "" {
			if success != nil && *success {
				s = e.i18n.T(MsgToolResultFmtOk)
			} else if success != nil && !*success {
				s = e.i18n.T(MsgToolResultFmtFailed)
			}
		}
		lines = append(lines, fmt.Sprintf("%s %s: %s", dot, statusLabel, s))
	}
	if exitCode != nil {
		lines = append(lines, fmt.Sprintf("🔢 %s: %d", exitLabel, *exitCode))
	}
	if strings.TrimSpace(result) != "" {
		lines = append(lines, "```text\n"+strings.TrimSpace(result)+"\n```")
	} else {
		lines = append(lines, "_"+noOutput+"_")
	}
	return strings.Join(lines, "\n")
}

// truncateIf truncates s to maxLen runes. 0 means no truncation.
func truncateIf(s string, maxLen int) string {
	if maxLen <= 0 {
		return s
	}
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	return string([]rune(s)[:maxLen]) + "..."
}

func splitMessage(text string, maxLen int) []string {
	runes := []rune(text)
	if len(runes) <= maxLen {
		return []string{text}
	}
	var chunks []string

	for len(runes) > 0 {
		if len(runes) <= maxLen {
			chunks = append(chunks, string(runes))
			break
		}

		end := maxLen

		// Try to split at newline boundary within the rune window.
		// Convert the candidate chunk back to a string for newline search.
		candidate := string(runes[:end])
		if idx := strings.LastIndex(candidate, "\n"); idx > 0 {
			// idx is a byte offset within candidate; convert to rune offset.
			runeIdx := utf8.RuneCountInString(candidate[:idx])
			if runeIdx >= end/2 {
				end = runeIdx + 1
			}
		}

		chunks = append(chunks, string(runes[:end]))
		runes = runes[end:]
	}
	return chunks
}

// sendTTSReply synthesizes fullResponse text and sends audio to the platform.
// Called asynchronously after EventResult; text reply is always sent first.
func (e *Engine) sendTTSReply(p Platform, replyCtx any, text string) {
	slog.Debug("tts: sendTTSReply called", "platform", p.Name(), "text_len", len(text))
	if e.tts == nil {
		slog.Warn("tts: e.tts is nil, skipping")
		return
	}
	if e.tts.TTS == nil {
		slog.Warn("tts: e.tts.TTS is nil, skipping")
		return
	}
	if e.tts.MaxTextLen > 0 && utf8.RuneCountInString(text) > e.tts.MaxTextLen {
		slog.Warn("tts: text exceeds max_text_len, skipping synthesis", "len", utf8.RuneCountInString(text), "max", e.tts.MaxTextLen)
		return
	}
	slog.Info("tts: starting synthesis", "voice", e.tts.Voice, "text_len", len(text))
	opts := TTSSynthesisOpts{Voice: e.tts.Voice}
	audioData, format, err := e.tts.TTS.Synthesize(e.ctx, StripMarkdown(text), opts)
	if err != nil {
		slog.Error("tts: synthesis failed", "error", err)
		return
	}
	slog.Info("tts: synthesis successful", "format", format, "audio_size", len(audioData))
	as, ok := p.(AudioSender)
	if !ok {
		slog.Warn("tts: platform does not support audio sending", "platform", p.Name())
		return
	}
	if err := as.SendAudio(e.ctx, replyCtx, audioData, format); err != nil {
		slog.Error("tts: platform audio send failed", "platform", p.Name(), "error", err)
		return
	}
	slog.Info("tts: audio sent successfully", "platform", p.Name())
}

// ──────────────────────────────────────────────────────────────
// Bot-to-bot relay
// ──────────────────────────────────────────────────────────────

// HandleRelay processes a relay message synchronously: starts or resumes a
// dedicated relay session, sends the message to the agent, and blocks until
// the complete response is collected (or the relay context times out).
func (e *Engine) HandleRelay(ctx context.Context, fromProject, chatID, message string) (string, error) {
	relaySessionKey := "relay:" + fromProject + ":" + chatID
	session := e.sessions.GetOrCreateActive(relaySessionKey)

	if inj, ok := e.agent.(SessionEnvInjector); ok {
		envVars := []string{
			"CC_PROJECT=" + e.name,
			"CC_SESSION_KEY=" + relaySessionKey,
		}
		if exePath, err := os.Executable(); err == nil {
			binDir := filepath.Dir(exePath)
			if curPath := os.Getenv("PATH"); curPath != "" {
				envVars = append(envVars, "PATH="+binDir+string(filepath.ListSeparator)+curPath)
			}
		}
		inj.SetSessionEnv(envVars)
	}

	// Use the engine context (not the relay timeout context) so that the
	// agent process is not killed when the relay deadline fires. The relay
	// timeout only controls how long we *wait* for the response.
	agentSession, err := e.agent.StartSession(e.ctx, session.GetAgentSessionID())
	if err != nil {
		// Resume failed — fall back to a fresh session so the relay is not
		// permanently broken by a corrupted/stale session ID.
		if session.GetAgentSessionID() != "" {
			slog.Warn("relay: session resume failed, trying fresh session",
				"relay_key", relaySessionKey, "error", err)
			session.SetAgentSessionID("", e.agent.Name())
			e.sessions.Save()
			agentSession, err = e.agent.StartSession(e.ctx, "")
		}
		if err != nil {
			return "", fmt.Errorf("start relay session: %w", err)
		}
	}

	if session.CompareAndSetAgentSessionID(agentSession.CurrentSessionID(), e.agent.Name()) {
		e.sessions.Save()
	}

	if err := agentSession.Send(message, nil, nil); err != nil {
		agentSession.Close()
		return "", fmt.Errorf("send relay message: %w", err)
	}

	var textParts []string
	for event := range agentSession.Events() {
		switch event.Type {
		case EventText:
			if event.Content != "" {
				textParts = append(textParts, event.Content)
			}
			if event.SessionID != "" {
				if session.CompareAndSetAgentSessionID(event.SessionID, e.agent.Name()) {
					e.sessions.Save()
				}
			}
		case EventToolResult:
			out := strings.TrimSpace(event.Content)
			if out == "" {
				out = strings.TrimSpace(event.ToolResult)
			}
			if out != "" {
				tn := strings.TrimSpace(event.ToolName)
				if tn == "" {
					tn = "tool"
				}
				textParts = append(textParts, fmt.Sprintf(e.i18n.T(MsgToolResult), tn, out)+"\n\n")
			}
		case EventResult:
			// Use agentSession.CurrentSessionID() for the same reason as above.
			if currentID := agentSession.CurrentSessionID(); currentID != "" {
				session.SetAgentSessionID(currentID, e.agent.Name())
				e.sessions.Save()
			}
			resp := event.Content
			if resp == "" && len(textParts) > 0 {
				resp = strings.Join(textParts, "")
			}
			if resp == "" {
				resp = "(empty response)"
			}
			slog.Info("relay: turn complete", "from", fromProject, "to", e.name, "response_len", len(resp))
			agentSession.Close()
			return resp, nil
		case EventError:
			agentSession.Close()
			if event.Error != nil {
				return "", event.Error
			}
			return "", fmt.Errorf("agent error (no details)")
		case EventPermissionRequest:
			// Auto-approve all permissions in relay mode
			_ = agentSession.RespondPermission(event.RequestID, PermissionResult{
				Behavior:     "allow",
				UpdatedInput: event.ToolInputRaw,
			})
		}
		if ctx.Err() != nil {
			// Relay timed out. Let the agent finish its turn in the
			// background so the session state is saved cleanly and the
			// session remains resumable for the next relay call.
			go e.drainRelaySession(agentSession, session, relaySessionKey)
			return relayPartialResponseOrError(ctx.Err(), textParts, fromProject, e.name)
		}
	}

	// Event channel closed without EventResult.
	agentSession.Close()

	if ctx.Err() != nil {
		return relayPartialResponseOrError(ctx.Err(), textParts, fromProject, e.name)
	}

	if len(textParts) > 0 {
		return strings.Join(textParts, ""), nil
	}
	return "", fmt.Errorf("relay: agent process exited without response")
}

func relayPartialResponseOrError(ctxErr error, textParts []string, fromProject, toProject string) (string, error) {
	if len(textParts) == 0 {
		return "", ctxErr
	}

	resp := strings.Join(textParts, "")
	slog.Warn("relay: context done before final result; returning partial response",
		"from", fromProject,
		"to", toProject,
		"error", ctxErr,
		"response_len", len(resp),
	)
	return resp, nil
}

// drainRelaySession runs in a goroutine after a relay timeout. It lets the
// agent finish its current turn (saving the session ID for future resumption),
// auto-approves any permission requests, and then closes the session. A 10-minute
// safety timeout prevents the goroutine from leaking if the agent hangs.
func (e *Engine) drainRelaySession(agentSession AgentSession, session *Session, relaySessionKey string) {
	timer := time.NewTimer(10 * time.Minute)
	defer timer.Stop()

	for {
		select {
		case ev, ok := <-agentSession.Events():
			if !ok {
				// Event channel closed — session ended naturally.
				agentSession.Close()
				return
			}
			if ev.SessionID != "" {
				session.SetAgentSessionID(ev.SessionID, e.agent.Name())
				e.sessions.Save()
			}
			switch ev.Type {
			case EventResult:
				slog.Info("relay: background drain completed (agent finished turn)",
					"relay_key", relaySessionKey)
				agentSession.Close()
				return
			case EventError:
				slog.Warn("relay: background drain got error",
					"relay_key", relaySessionKey, "error", ev.Error)
				agentSession.Close()
				return
			case EventPermissionRequest:
				_ = agentSession.RespondPermission(ev.RequestID, PermissionResult{
					Behavior:     "allow",
					UpdatedInput: ev.ToolInputRaw,
				})
			}
		case <-timer.C:
			slog.Warn("relay: background drain timed out, closing session",
				"relay_key", relaySessionKey)
			agentSession.Close()
			return
		case <-e.ctx.Done():
			agentSession.Close()
			return
		}
	}
}

// cmdBind handles /bind — establishes a relay binding between bots in a group chat.
//
// Usage:
//
//	/bind <project>           — bind current bot with another project in this group
//	/bind remove              — remove all bindings for this group
//	/bind -<project>          — remove specific project from binding
//	/bind                     — show current binding status
//
// The <project> argument is the project name from config.toml [[projects]].
// Multiple projects can be bound together for relay.
func (e *Engine) cmdBind(p Platform, msg *Message, args []string) {
	if e.relayManager == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgRelayNotAvailable))
		return
	}

	_, chatID, err := parseSessionKeyParts(msg.SessionKey)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgRelayNotAvailable))
		return
	}

	if len(args) == 0 {
		e.cmdBindStatus(p, msg.ReplyCtx, chatID)
		return
	}

	otherProject := args[0]

	// Handle removal commands
	if otherProject == "remove" || otherProject == "rm" || otherProject == "unbind" || otherProject == "del" || otherProject == "clear" {
		e.relayManager.Unbind(chatID)
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgRelayUnbound))
		return
	}

	if otherProject == "setup" {
		e.cmdBindSetup(p, msg)
		return
	}

	if otherProject == "help" || otherProject == "-h" || otherProject == "--help" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgRelayUsage))
		return
	}

	// Handle removal with - prefix: /bind -project
	if strings.HasPrefix(otherProject, "-") {
		projectToRemove := strings.TrimPrefix(otherProject, "-")
		if e.relayManager.RemoveFromBind(chatID, projectToRemove) {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgRelayBindRemoved), projectToRemove))
		} else {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgRelayBindNotFound), projectToRemove))
		}
		return
	}

	if otherProject == e.name {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgRelayBindSelf))
		return
	}

	// Validate the target project exists
	if !e.relayManager.HasEngine(otherProject) {
		available := e.relayManager.ListEngineNames()
		var others []string
		for _, n := range available {
			if n != e.name {
				others = append(others, n)
			}
		}
		if len(others) == 0 {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgRelayNoTarget), otherProject))
		} else {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgRelayNotFound), otherProject, strings.Join(others, ", ")))
		}
		return
	}

	// Add current project and target project to binding
	e.relayManager.AddToBind(p.Name(), chatID, e.name)
	e.relayManager.AddToBind(p.Name(), chatID, otherProject)

	// Get all bound projects for status message
	binding := e.relayManager.GetBinding(chatID)
	var boundProjects []string
	for proj := range binding.Bots {
		boundProjects = append(boundProjects, proj)
	}

	reply := fmt.Sprintf(e.i18n.T(MsgRelayBindSuccess), strings.Join(boundProjects, " ↔ "), otherProject, otherProject)

	if _, ok := e.agent.(SystemPromptSupporter); !ok {
		if mp, ok := e.agent.(MemoryFileProvider); ok {
			reply += fmt.Sprintf(e.i18n.T(MsgRelaySetupHint), filepath.Base(mp.ProjectMemoryFile()))
		}
	}

	e.reply(p, msg.ReplyCtx, reply)
}

func (e *Engine) cmdBindStatus(p Platform, replyCtx any, chatID string) {
	binding := e.relayManager.GetBinding(chatID)
	if binding == nil {
		e.reply(p, replyCtx, e.i18n.T(MsgRelayNoBinding))
		return
	}
	var parts []string
	for proj := range binding.Bots {
		parts = append(parts, proj)
	}
	e.reply(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgRelayBound), strings.Join(parts, " ↔ ")))
}

const ccConnectInstructionMarker = "<!-- cc-connect-instructions -->"

type setupResult int

const (
	setupOK       setupResult = iota // instructions written successfully
	setupExists                      // instructions already present
	setupNative                      // agent supports system prompt natively
	setupNoMemory                    // agent has no memory file support
	setupError                       // write error
)

// setupMemoryFile appends AgentSystemPrompt() to the agent's project memory
// file. It returns the result, the filename (for messages), and any error.
func (e *Engine) setupMemoryFile() (setupResult, string, error) {
	if _, ok := e.agent.(SystemPromptSupporter); ok {
		return setupNative, "", nil
	}

	mp, ok := e.agent.(MemoryFileProvider)
	if !ok {
		return setupNoMemory, "", nil
	}

	filePath := mp.ProjectMemoryFile()
	if filePath == "" {
		return setupNoMemory, "", nil
	}

	baseName := filepath.Base(filePath)

	existing, _ := os.ReadFile(filePath)
	existingText := string(existing)
	block := "\n" + ccConnectInstructionMarker + "\n" + AgentSystemPrompt() + "\n"
	if idx := strings.Index(existingText, ccConnectInstructionMarker); idx >= 0 {
		if strings.Contains(existingText[idx:], AgentSystemPrompt()) {
			return setupExists, baseName, nil
		}
		updated := strings.TrimRight(existingText[:idx], "\n") + block
		if err := os.WriteFile(filePath, []byte(updated), 0o644); err != nil {
			return setupError, baseName, err
		}
		return setupOK, baseName, nil
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return setupError, baseName, err
	}

	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return setupError, baseName, err
	}
	defer f.Close()

	if _, err := f.WriteString(block); err != nil {
		return setupError, baseName, err
	}

	return setupOK, baseName, nil
}

func (e *Engine) cmdBindSetup(p Platform, msg *Message) {
	result, baseName, err := e.setupMemoryFile()
	switch result {
	case setupNative:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSetupNative))
	case setupNoMemory:
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgRelaySetupNoMemory))
	case setupExists:
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgRelaySetupExists), baseName))
	case setupError:
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
	case setupOK:
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgRelaySetupOK), baseName))
	}
}

// buildSenderPrompt prepends a sender identity header to content when
// injectSender is enabled and userID is non-empty. When userName is available
// it is included as sender_name so the agent can identify who sent the message
// by display name (useful in shared channel sessions with multiple users).
func (e *Engine) buildSenderPrompt(content, userID, userName, platform, sessionKey string) string {
	if !e.injectSender || userID == "" {
		return content
	}
	chatID := extractChannelID(sessionKey)
	if userName != "" {
		safeName := strings.NewReplacer(`"`, `'`, "\n", " ", "\r", "").Replace(userName)
		return fmt.Sprintf("[cc-connect sender_id=%s sender_name=\"%s\" platform=%s chat_id=%s]\n%s", userID, safeName, platform, chatID, content)
	}
	return fmt.Sprintf("[cc-connect sender_id=%s platform=%s chat_id=%s]\n%s", userID, platform, chatID, content)
}

func extractChannelID(sessionKey string) string {
	// Format: "platform:channelID:userID" or "platform:channelID"
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

func extractUserID(sessionKey string) string {
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) >= 3 {
		return parts[2]
	}
	return ""
}

func extractPlatformName(sessionKey string) string {
	if i := strings.IndexByte(sessionKey, ':'); i >= 0 {
		return sessionKey[:i]
	}
	return sessionKey
}

func workspaceChannelKey(platformName, channelID string) string {
	if channelID == "" {
		return ""
	}
	if platformName == "" {
		return channelID
	}
	return platformName + ":" + channelID
}

func extractWorkspaceChannelKey(sessionKey string) string {
	return workspaceChannelKey(extractPlatformName(sessionKey), extractChannelID(sessionKey))
}

// effectiveChannelID returns the channel identifier from a Message.
// It prefers the platform-provided ChannelKey (e.g. "chatID:threadID" for forum topics)
// and falls back to parsing the session key.
func effectiveChannelID(msg *Message) string {
	if msg.ChannelKey != "" {
		return msg.ChannelKey
	}
	return extractChannelID(msg.SessionKey)
}

// effectiveWorkspaceChannelKey returns the workspace binding key from a Message.
func effectiveWorkspaceChannelKey(msg *Message) string {
	if msg.ChannelKey != "" {
		return workspaceChannelKey(msg.Platform, msg.ChannelKey)
	}
	return extractWorkspaceChannelKey(msg.SessionKey)
}

// commandContext resolves the appropriate agent, session manager, and interactive key
// for a command. In multi-workspace mode, it routes to the bound workspace if present.
func (e *Engine) commandContext(p Platform, msg *Message) (Agent, *SessionManager, string, error) {
	if !e.multiWorkspace {
		return e.agent, e.sessions, msg.SessionKey, nil
	}
	channelID := effectiveChannelID(msg)
	channelKey := effectiveWorkspaceChannelKey(msg)
	if channelKey == "" || channelID == "" {
		return e.agent, e.sessions, msg.SessionKey, nil
	}
	workspace, _, err := e.resolveWorkspace(p, channelID)
	if err != nil {
		return nil, nil, "", err
	}
	if workspace == "" {
		return e.agent, e.sessions, msg.SessionKey, nil
	}
	agent, sessions, interactiveKey, _, err := e.workspaceContext(workspace, msg.SessionKey)
	if err != nil {
		return nil, nil, "", err
	}
	return agent, sessions, interactiveKey, nil
}

// sessionContextForKey resolves the agent and session manager for a sessionKey.
// It uses existing workspace bindings and falls back to global context if unresolved.
func (e *Engine) sessionContextForKey(sessionKey string) (Agent, *SessionManager) {
	if !e.multiWorkspace || e.workspaceBindings == nil {
		return e.agent, e.sessions
	}
	channelKey := extractWorkspaceChannelKey(sessionKey)
	if channelKey == "" {
		return e.agent, e.sessions
	}
	if b, _, usable := e.lookupEffectiveWorkspaceBinding(channelKey); usable {
		if wsAgent, wsSessions, err := e.getOrCreateWorkspaceAgent(normalizeWorkspacePath(b.Workspace)); err == nil {
			return wsAgent, wsSessions
		}
	}
	return e.agent, e.sessions
}

// interactiveKeyForSessionKey returns the interactive state key for a sessionKey.
// In multi-workspace mode, it prefixes with the bound workspace path when available.
func (e *Engine) interactiveKeyForSessionKey(sessionKey string) string {
	if !e.multiWorkspace || e.workspaceBindings == nil {
		return sessionKey
	}
	channelKey := extractWorkspaceChannelKey(sessionKey)
	if channelKey == "" {
		return sessionKey
	}
	if b, _, usable := e.lookupEffectiveWorkspaceBinding(channelKey); usable {
		return normalizeWorkspacePath(b.Workspace) + ":" + sessionKey
	}
	return sessionKey
}

// lookupEffectiveWorkspaceBinding returns the effective binding for a channel
// plus whether the bound workspace is currently usable.
func (e *Engine) lookupEffectiveWorkspaceBinding(channelKey string) (*WorkspaceBinding, string, bool) {
	if !e.multiWorkspace || e.workspaceBindings == nil || channelKey == "" {
		return nil, "", false
	}

	projectKey := "project:" + e.name
	b, bindingKey := e.workspaceBindings.LookupEffective(projectKey, channelKey)
	if b == nil {
		return nil, "", false
	}

	if _, err := os.Stat(b.Workspace); err != nil {
		slog.Warn("bound workspace directory missing",
			"workspace", b.Workspace, "channel_key", channelKey, "binding_scope", bindingKey)
		if bindingKey != sharedWorkspaceBindingsKey {
			e.workspaceBindings.Unbind(bindingKey, channelKey)
		}
		return b, bindingKey, false
	}

	return b, bindingKey, true
}

// resolveWorkspace resolves a channel to a workspace directory.
// Returns (workspacePath, channelName, error).
// If workspacePath is empty, the init flow should be triggered.
func (e *Engine) resolveWorkspace(p Platform, channelID string) (string, string, error) {
	channelKey := workspaceChannelKey(p.Name(), channelID)

	// Step 1: Check existing binding
	if b, _, usable := e.lookupEffectiveWorkspaceBinding(channelKey); b != nil {
		if !usable {
			return "", b.ChannelName, nil
		}
		return normalizeWorkspacePath(b.Workspace), b.ChannelName, nil
	}

	// Step 2: Resolve channel name for convention match
	channelName := ""
	if resolver, ok := p.(ChannelNameResolver); ok {
		name, err := resolver.ResolveChannelName(channelID)
		if err != nil {
			slog.Warn("failed to resolve channel name", "channel", channelID, "err", err)
		} else {
			channelName = name
		}
	}

	if channelName == "" {
		return "", "", nil
	}

	// Step 3: Convention match — check if base_dir/<channel-name> exists
	candidate := filepath.Join(e.baseDir, channelName)
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		// Auto-bind
		projectKey := "project:" + e.name
		normalized := normalizeWorkspacePath(candidate)
		e.workspaceBindings.Bind(projectKey, channelKey, channelName, normalized)
		slog.Info("workspace auto-bound by convention",
			"channel", channelName, "workspace", normalized)
		return normalized, channelName, nil
	}

	return "", channelName, nil
}

// handleWorkspaceInitFlow manages the conversational workspace setup.
// Returns true if the message was consumed by the init flow.
func (e *Engine) handleWorkspaceInitFlow(p Platform, msg *Message, channelName string) bool {
	channelKey := effectiveWorkspaceChannelKey(msg)

	e.initFlowsMu.Lock()
	flow, exists := e.initFlows[channelKey]
	e.initFlowsMu.Unlock()

	content := strings.TrimSpace(msg.Content)

	if !exists {
		if strings.HasPrefix(content, "/") {
			return false
		}
		e.initFlowsMu.Lock()
		e.initFlows[channelKey] = &workspaceInitFlow{
			state:       "awaiting_url",
			channelName: channelName,
		}
		e.initFlowsMu.Unlock()
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsNotFoundHint))
		return true
	}

	// Slash commands always take priority over the init flow — let them
	// pass through to handleCommand. Clean up the stale flow since the
	// user is issuing explicit commands instead of following the clone guide.
	if strings.HasPrefix(content, "/") {
		e.initFlowsMu.Lock()
		delete(e.initFlows, channelKey)
		e.initFlowsMu.Unlock()
		return false
	}

	switch flow.state {
	case "awaiting_url":
		// Accept local directory paths: bind directly without cloning.
		if looksLikeLocalDir(content) {
			dirPath, resolveErr := resolveLocalDirPath(content, e.baseDir)
			if resolveErr != nil {
				e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsInitDirNotFound, content))
				return true
			}
			info, err := os.Stat(dirPath)
			if err != nil || !info.IsDir() {
				e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsInitDirNotFound, content))
				return true
			}
			projectKey := "project:" + e.name
			e.workspaceBindings.Bind(projectKey, channelKey, flow.channelName, normalizeWorkspacePath(dirPath))
			e.initFlowsMu.Lock()
			delete(e.initFlows, channelKey)
			e.initFlowsMu.Unlock()
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(
				"Bound workspace `%s` to this channel. Ready.", dirPath))
			return true
		}

		if !looksLikeGitURL(content) {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWsInitInvalidTarget))
			return true
		}
		repoName := extractRepoName(content)
		cloneTo := filepath.Join(e.baseDir, repoName)

		e.initFlowsMu.Lock()
		flow.repoURL = content
		flow.cloneTo = cloneTo
		flow.state = "awaiting_confirm"
		e.initFlowsMu.Unlock()

		e.reply(p, msg.ReplyCtx, fmt.Sprintf(
			"I'll clone `%s` to `%s` and bind it to this channel. OK? (yes/no)", content, cloneTo))
		return true

	case "awaiting_confirm":
		lower := strings.ToLower(content)
		if lower != "yes" && lower != "y" {
			e.initFlowsMu.Lock()
			delete(e.initFlows, channelKey)
			e.initFlowsMu.Unlock()
			e.reply(p, msg.ReplyCtx, "Cancelled. Send a repo URL anytime to try again.")
			return true
		}

		e.reply(p, msg.ReplyCtx, fmt.Sprintf("Cloning `%s` to `%s`...", flow.repoURL, flow.cloneTo))

		if err := gitClone(flow.repoURL, flow.cloneTo); err != nil {
			e.initFlowsMu.Lock()
			delete(e.initFlows, channelKey)
			e.initFlowsMu.Unlock()
			e.reply(p, msg.ReplyCtx, fmt.Sprintf("Clone failed: %v\nSend a repo URL to try again.", err))
			return true
		}

		projectKey := "project:" + e.name
		e.workspaceBindings.Bind(projectKey, channelKey, flow.channelName, normalizeWorkspacePath(flow.cloneTo))

		e.initFlowsMu.Lock()
		delete(e.initFlows, channelKey)
		e.initFlowsMu.Unlock()

		e.reply(p, msg.ReplyCtx, fmt.Sprintf(
			"Clone complete. Bound workspace `%s` to this channel. Ready.", flow.cloneTo))
		return true
	}

	return false
}

func looksLikeGitURL(s string) bool {
	return strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "git@") ||
		strings.HasPrefix(s, "ssh://")
}

// resolveLocalDirPath resolves a user-provided directory path to an absolute
// path, expanding ~/... and joining relative paths with baseDir. It rejects
// paths that escape baseDir via ../ traversal.
func resolveLocalDirPath(target, baseDir string) (string, error) {
	dirPath := target
	if strings.HasPrefix(dirPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot resolve home directory: %w", err)
		}
		dirPath = filepath.Join(home, dirPath[2:])
	} else if !filepath.IsAbs(dirPath) {
		dirPath = filepath.Join(baseDir, dirPath)
	}
	cleaned := filepath.Clean(dirPath)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		resolved = cleaned
	}
	if baseDir != "" && !filepath.IsAbs(target) {
		cleanBase := filepath.Clean(baseDir)
		if evalBase, err := filepath.EvalSymlinks(cleanBase); err == nil {
			cleanBase = evalBase
		}
		if !strings.HasPrefix(resolved, cleanBase+string(filepath.Separator)) && resolved != cleanBase {
			return "", fmt.Errorf("path escapes workspace base directory")
		}
	}
	return resolved, nil
}

// looksLikeLocalDir returns true if the string looks like a local directory
// path (absolute path, home-relative, dot-relative, or a bare name that
// doesn't look like a URL).
func looksLikeLocalDir(s string) bool {
	if s == "" {
		return false
	}
	return strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "~/") ||
		strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "../") ||
		(!strings.Contains(s, "://") && !strings.Contains(s, "@"))
}

func extractRepoName(url string) string {
	url = strings.TrimSuffix(url, ".git")
	// Handle git@host:org/repo format
	if idx := strings.LastIndex(url, ":"); idx != -1 && strings.HasPrefix(url, "git@") {
		remainder := url[idx+1:]
		parts := strings.Split(remainder, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	// Handle https://host/org/repo format
	parts := strings.Split(url, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return "workspace"
}

func gitClone(repoURL, dest string) error {
	cmd := exec.Command("git", "clone", repoURL, dest)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// ── Context usage indicator ──────────────────────────────────

const modelContextWindow = 200_000 // generic fallback window for heuristic context estimates

// contextIndicator returns a suffix like "\n[ctx: ~42%]" based on SDK-reported input tokens.
func contextIndicator(inputTokens int) string {
	if inputTokens <= 0 {
		return ""
	}
	pct := inputTokens * 100 / modelContextWindow
	if pct > 100 {
		pct = 100
	}
	return fmt.Sprintf("\n[ctx: ~%d%%]", pct)
}

// ctxSelfReportRe matches agent self-reported context lines like "[ctx: ~42%]".
var ctxSelfReportRe = regexp.MustCompile(`(?m)\n?\[ctx: ~\d+%\]`)

// parseSelfReportedCtx extracts the percentage from a self-reported "[ctx: ~XX%]" line.
func parseSelfReportedCtx(s string) int {
	m := ctxSelfReportRe.FindString(s)
	if m == "" {
		return 0
	}
	start := strings.Index(m, "~") + 1
	end := strings.Index(m, "%")
	if start <= 0 || end <= start {
		return 0
	}
	v, _ := strconv.Atoi(m[start:end])
	return v
}

func (e *Engine) cmdWeb(p Platform, msg *Message, args []string) {
	subCmd := ""
	if len(args) > 0 {
		subCmd = matchSubCommand(strings.ToLower(args[0]),
			[]string{"setup", "status"})
	}

	switch subCmd {
	case "setup":
		e.cmdWebSetup(p, msg)
	default:
		e.cmdWebStatus(p, msg)
	}
}

func (e *Engine) cmdWebSetup(p Platform, msg *Message) {
	if !WebAssetsAvailable() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWebNotSupported))
		return
	}
	if e.webSetupFunc == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWebNotSupported))
		return
	}

	port, token, needRestart, err := e.webSetupFunc()
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	url := fmt.Sprintf("http://localhost:%d", port)
	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgWebSetupSuccess), url, token))
	if needRestart {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWebNeedRestart))
	}
}

func (e *Engine) cmdWebStatus(p Platform, msg *Message) {
	if !WebAssetsAvailable() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWebNotSupported))
		return
	}
	if e.webStatusFunc == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWebNotSupported))
		return
	}

	url := e.webStatusFunc()
	if url == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWebNotEnabled))
		return
	}
	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgWebStatus), url))
}
