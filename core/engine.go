package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxPlatformMessageLen = 4000

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

// VersionInfo is set by main at startup so that /version works.
var VersionInfo string

// CurrentVersion is the semver tag (e.g. "v1.2.0-beta.1"), set by main.
var CurrentVersion string

// RestartRequest carries info needed to send a post-restart notification.
type RestartRequest struct {
	SessionKey string `json:"session_key"`
	Platform   string `json:"platform"`
}

// SaveRestartNotify persists restart info so the new process can send
// a "restart successful" message after startup.
func SaveRestartNotify(dataDir string, req RestartRequest) error {
	dir := filepath.Join(dataDir, "run")
	os.MkdirAll(dir, 0o755)
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
		if err := p.Send(e.ctx, rctx, text); err != nil {
			slog.Debug("restart notify: send failed", "error", err)
		}
		return
	}
}

// RestartCh is signaled when /restart is invoked. main listens on it
// to perform a graceful shutdown followed by syscall.Exec.
var RestartCh = make(chan RestartRequest, 1)

// DisplayCfg controls truncation of intermediate messages.
// A value of -1 means "use default", 0 means "no truncation".
type DisplayCfg struct {
	ThinkingMaxLen int // max runes for thinking preview; 0 = no truncation
	ToolMaxLen     int // max runes for tool use preview; 0 = no truncation
}

// RateLimitCfg controls per-session message rate limiting.
type RateLimitCfg struct {
	MaxMessages int           // max messages per window; 0 = disabled
	Window      time.Duration // sliding window size
}

// Engine routes messages between platforms and the agent for a single project.
type Engine struct {
	name         string
	agent        Agent
	platforms    []Platform
	sessions     *SessionManager
	ctx          context.Context
	cancel       context.CancelFunc
	i18n         *I18n
	speech       SpeechCfg
	tts          *TTSCfg
	display      DisplayCfg
	defaultQuiet bool
	startedAt    time.Time

	providerSaveFunc       func(providerName string) error
	providerAddSaveFunc    func(p ProviderConfig) error
	providerRemoveSaveFunc func(name string) error

	ttsSaveFunc func(mode string) error

	commandSaveAddFunc func(name, description, prompt, exec, workDir string) error
	commandSaveDelFunc func(name string) error

	displaySaveFunc  func(thinkingMaxLen, toolMaxLen *int) error
	configReloadFunc func() (*ConfigReloadResult, error)

	cronScheduler *CronScheduler

	commands *CommandRegistry
	skills   *SkillRegistry
	aliases  map[string]string // trigger → command (e.g. "帮助" → "/help")
	aliasMu  sync.RWMutex

	aliasSaveAddFunc func(name, command string) error
	aliasSaveDelFunc func(name string) error

	bannedWords []string
	bannedMu    sync.RWMutex

	disabledCmds map[string]bool
	adminFrom    string // comma-separated user IDs for privileged commands; "*" = all allowed users; "" = deny

	rateLimiter      *RateLimiter
	streamPreview    StreamPreviewCfg
	relayManager     *RelayManager
	eventIdleTimeout time.Duration

	// Interactive agent session management
	interactiveMu     sync.Mutex
	interactiveStates map[string]*interactiveState // key = sessionKey

	quietMu sync.RWMutex
	quiet   bool // when true, suppress thinking and tool progress messages globally
}

// interactiveState tracks a running interactive agent session and its permission state.
type interactiveState struct {
	agentSession AgentSession
	platform     Platform
	replyCtx     any
	mu           sync.Mutex
	pending      *pendingPermission
	approveAll   bool // when true, auto-approve all permission requests for this session
	quiet        bool // when true, suppress thinking and tool progress for this session
	fromVoice    bool // true if current turn originated from voice transcription
	deleteMode   *deleteModeState
}

type deleteModeState struct {
	page        int
	selectedIDs map[string]struct{}
	phase       string
	hint        string
	result      string
}

// pendingPermission represents a permission request waiting for user response.
type pendingPermission struct {
	RequestID    string
	ToolName     string
	ToolInput    map[string]any
	InputPreview string
	Resolved     chan struct{} // closed when user responds
	resolveOnce  sync.Once
}

// resolve safely closes the Resolved channel exactly once.
func (pp *pendingPermission) resolve() {
	pp.resolveOnce.Do(func() { close(pp.Resolved) })
}

func NewEngine(name string, ag Agent, platforms []Platform, sessionStorePath string, lang Language) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		name:              name,
		agent:             ag,
		platforms:         platforms,
		sessions:          NewSessionManager(sessionStorePath),
		ctx:               ctx,
		cancel:            cancel,
		i18n:              NewI18n(lang),
		display:           DisplayCfg{ThinkingMaxLen: defaultThinkingMaxLen, ToolMaxLen: defaultToolMaxLen},
		commands:          NewCommandRegistry(),
		skills:            NewSkillRegistry(),
		aliases:           make(map[string]string),
		interactiveStates: make(map[string]*interactiveState),
		startedAt:         time.Now(),
		streamPreview:     DefaultStreamPreviewCfg(),
		eventIdleTimeout:  defaultEventIdleTimeout,
	}

	if cp, ok := ag.(CommandProvider); ok {
		e.commands.SetAgentDirs(cp.CommandDirs())
	}
	if sp, ok := ag.(SkillProvider); ok {
		e.skills.SetDirs(sp.SkillDirs())
	}

	return e
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

// SetDefaultQuiet sets whether new sessions start in quiet mode.
func (e *Engine) SetDefaultQuiet(q bool) {
	e.defaultQuiet = q
}

func (e *Engine) SetLanguageSaveFunc(fn func(Language) error) {
	e.i18n.SetSaveFunc(fn)
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

func (e *Engine) SetCronScheduler(cs *CronScheduler) {
	e.cronScheduler = cs
}

func (e *Engine) SetCommandSaveAddFunc(fn func(name, description, prompt, exec, workDir string) error) {
	e.commandSaveAddFunc = fn
}

func (e *Engine) SetCommandSaveDelFunc(fn func(name string) error) {
	e.commandSaveDelFunc = fn
}

func (e *Engine) SetDisplaySaveFunc(fn func(thinkingMaxLen, toolMaxLen *int) error) {
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

// SetDisabledCommands sets the list of command IDs that are disabled for this project.
func (e *Engine) SetDisabledCommands(cmds []string) {
	m := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		c = strings.ToLower(strings.TrimPrefix(c, "/"))
		// Resolve alias names to canonical IDs
		id := matchPrefix(c, builtinCommands)
		if id != "" {
			m[id] = true
		} else {
			m[c] = true
		}
	}
	e.disabledCmds = m
}

// SetAdminFrom sets the admin allowlist for privileged commands.
// "*" means all users who pass allow_from are admins.
// Empty string means privileged commands are denied for everyone.
func (e *Engine) SetAdminFrom(adminFrom string) {
	e.adminFrom = strings.TrimSpace(adminFrom)
	if e.adminFrom == "" && !e.disabledCmds["shell"] {
		slog.Warn("admin_from is not set — privileged commands (/shell, /restart, /upgrade) are blocked. "+
			"Set admin_from in config to enable them, or use disabled_commands to hide them.",
			"project", e.name)
	}
}

// privilegedCommands are commands that require admin_from authorization.
var privilegedCommands = map[string]bool{
	"shell":   true,
	"restart": true,
	"upgrade": true,
}

// isAdmin checks whether the given user ID is authorized for privileged commands.
// Unlike AllowList, empty adminFrom means deny-all (fail-closed).
func (e *Engine) isAdmin(userID string) bool {
	af := strings.TrimSpace(e.adminFrom)
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
func (e *Engine) SetRateLimitCfg(cfg RateLimitCfg) {
	e.rateLimiter = NewRateLimiter(cfg.MaxMessages, cfg.Window)
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

// RemoveCommand removes a custom command by name. Returns false if not found.
func (e *Engine) RemoveCommand(name string) bool {
	return e.commands.Remove(name)
}

func (e *Engine) ProjectName() string {
	return e.name
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
	if targetPlatform == nil {
		return fmt.Errorf("platform %q not found for session %q", platformName, sessionKey)
	}

	rc, ok := targetPlatform.(ReplyContextReconstructor)
	if !ok {
		return fmt.Errorf("platform %q does not support proactive messaging (cron)", platformName)
	}

	replyCtx, err := rc.ReconstructReplyCtx(sessionKey)
	if err != nil {
		return fmt.Errorf("reconstruct reply context: %w", err)
	}

	// Notify user that a cron job is executing (unless silent)
	silent := false
	if e.cronScheduler != nil {
		silent = e.cronScheduler.IsSilent(job)
	}
	if !silent {
		desc := job.Description
		if desc == "" {
			desc = truncateStr(job.Prompt, 40)
		}
		e.send(targetPlatform, replyCtx, fmt.Sprintf("⏰ %s", desc))
	}

	msg := &Message{
		SessionKey: sessionKey,
		Platform:   platformName,
		UserID:     "cron",
		UserName:   "cron",
		Content:    job.Prompt,
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
	for _, p := range e.platforms {
		if err := p.Start(e.handleMessage); err != nil {
			slog.Warn("platform start failed", "project", e.name, "platform", p.Name(), "error", err)
			startErrs = append(startErrs, fmt.Errorf("[%s] start platform %s: %w", e.name, p.Name(), err))
			continue
		}
		slog.Info("platform started", "project", e.name, "platform", p.Name())

		// Register commands on platforms that support it (e.g. Telegram setMyCommands)
		if registrar, ok := p.(CommandRegistrar); ok {
			commands := e.GetAllCommands()
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

	// Log summary
	startedCount := len(e.platforms) - len(startErrs)
	if len(startErrs) > 0 {
		slog.Warn("engine started with some failures", "project", e.name, "agent", e.agent.Name(), "started", startedCount, "failed", len(startErrs))
	} else {
		slog.Info("engine started", "project", e.name, "agent", e.agent.Name(), "platforms", len(e.platforms))
	}

	// Only return error if ALL platforms failed
	if len(startErrs) == len(e.platforms) && len(e.platforms) > 0 {
		return startErrs[0] // Return first error
	}
	return nil
}

func (e *Engine) Stop() error {
	// Stop platforms first to prevent new incoming messages
	var errs []error
	for _, p := range e.platforms {
		if err := p.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("stop platform %s: %w", p.Name(), err))
		}
	}

	// Now cancel context and clean up sessions
	e.cancel()

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

	if err := e.agent.Stop(); err != nil {
		errs = append(errs, fmt.Errorf("stop agent %s: %w", e.agent.Name(), err))
	}
	if len(errs) > 0 {
		return fmt.Errorf("engine stop errors: %v", errs)
	}
	return nil
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
		"has_images", len(msg.Images) > 0, "has_audio", msg.Audio != nil,
	)

	// Voice message: transcribe to text first
	if msg.Audio != nil {
		e.handleVoiceMessage(p, msg)
		return
	}

	content := strings.TrimSpace(msg.Content)
	if content == "" && len(msg.Images) == 0 {
		return
	}

	// Resolve aliases: check if the first word (or whole content) matches an alias
	content = e.resolveAlias(content)
	msg.Content = content

	// Rate limit check
	if e.rateLimiter != nil && !e.rateLimiter.Allow(msg.SessionKey) {
		slog.Info("message rate limited", "session", msg.SessionKey, "user", msg.UserName)
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

	session := e.sessions.GetOrCreateActive(msg.SessionKey)
	if !session.TryLock() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPreviousProcessing))
		return
	}

	slog.Info("processing message",
		"platform", msg.Platform,
		"user", msg.UserName,
		"session", session.ID,
	)

	go e.processInteractiveMessage(p, msg, session)
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
	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[msg.SessionKey]
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
	defer session.Unlock()

	if e.ctx.Err() != nil {
		return
	}

	turnStart := time.Now()

	e.i18n.DetectAndSet(msg.Content)
	session.AddHistory("user", msg.Content)

	state := e.getOrCreateInteractiveState(msg.SessionKey, p, msg.ReplyCtx, session)

	// Update reply context for this turn
	state.mu.Lock()
	state.platform = p
	state.replyCtx = msg.ReplyCtx
	state.mu.Unlock()

	if state.agentSession == nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), "failed to start agent session"))
		return
	}

	// Start typing indicator if platform supports it
	var stopTyping func()
	if ti, ok := p.(TypingIndicator); ok {
		stopTyping = ti.StartTyping(e.ctx, msg.ReplyCtx)
	}
	defer func() {
		if stopTyping != nil {
			stopTyping()
		}
	}()

	// Drain any stale events left in the channel from a previous turn.
	// This prevents the next processInteractiveEvents from reading an old
	// EventResult that was pushed after the previous turn already returned.
	drainEvents(state.agentSession.Events())

	sendStart := time.Now()
	state.mu.Lock()
	state.fromVoice = msg.FromVoice
	state.mu.Unlock()
	if err := state.agentSession.Send(msg.Content, msg.Images); err != nil {
		slog.Error("failed to send prompt", "error", err)

		if !state.agentSession.Alive() {
			e.cleanupInteractiveState(msg.SessionKey)
			e.send(p, msg.ReplyCtx, e.i18n.T(MsgSessionRestarting))

			state = e.getOrCreateInteractiveState(msg.SessionKey, p, msg.ReplyCtx, session)
			if state.agentSession == nil {
				e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), "failed to restart agent session"))
				return
			}
			sendStart = time.Now()
			if err := state.agentSession.Send(msg.Content, msg.Images); err != nil {
				e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), err))
				return
			}
		} else {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), err))
			return
		}
	}
	if elapsed := time.Since(sendStart); elapsed >= slowAgentSend {
		slog.Warn("slow agent send", "elapsed", elapsed, "session", msg.SessionKey, "content_len", len(msg.Content))
	}

	e.processInteractiveEvents(state, session, msg.SessionKey, msg.MessageID, turnStart)
}

func (e *Engine) getOrCreateInteractiveState(sessionKey string, p Platform, replyCtx any, session *Session) *interactiveState {
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()

	state, ok := e.interactiveStates[sessionKey]
	if ok && state.agentSession != nil && state.agentSession.Alive() {
		return state
	}

	// Preserve quiet setting from existing state (e.g. set via /quiet before session started)
	quietMode := e.defaultQuiet
	if ok && state != nil {
		state.mu.Lock()
		quietMode = state.quiet
		state.mu.Unlock()
	}

	// Inject per-session env vars so the agent subprocess can call `cc-connect cron add` etc.
	if inj, ok := e.agent.(SessionEnvInjector); ok {
		envVars := []string{
			"CC_PROJECT=" + e.name,
			"CC_SESSION_KEY=" + sessionKey,
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

	// Check if context is already canceled (e.g. during shutdown/restart)
	if e.ctx.Err() != nil {
		slog.Debug("skipping session start: context canceled", "session_key", sessionKey)
		state = &interactiveState{platform: p, replyCtx: replyCtx, quiet: quietMode}
		e.interactiveStates[sessionKey] = state
		return state
	}

	startAt := time.Now()
	agentSession, err := e.agent.StartSession(e.ctx, session.AgentSessionID)
	startElapsed := time.Since(startAt)
	if err != nil {
		slog.Error("failed to start interactive session", "error", err, "elapsed", startElapsed)
		state = &interactiveState{platform: p, replyCtx: replyCtx, quiet: quietMode}
		e.interactiveStates[sessionKey] = state
		return state
	}
	if startElapsed >= slowAgentStart {
		slog.Warn("slow agent session start", "elapsed", startElapsed, "agent", e.agent.Name(), "session_id", session.AgentSessionID)
	}

	state = &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     replyCtx,
		quiet:        quietMode,
	}
	e.interactiveStates[sessionKey] = state

	slog.Info("interactive session started", "session_key", sessionKey, "agent_session", session.AgentSessionID, "elapsed", startElapsed)
	return state
}

func (e *Engine) cleanupInteractiveState(sessionKey string) {
	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[sessionKey]
	delete(e.interactiveStates, sessionKey)
	e.interactiveMu.Unlock()

	if ok && state != nil && state.agentSession != nil {
		slog.Debug("cleanupInteractiveState: closing agent session", "session", sessionKey)
		closeStart := time.Now()

		done := make(chan struct{})
		go func() {
			state.agentSession.Close()
			close(done)
		}()

		select {
		case <-done:
			if elapsed := time.Since(closeStart); elapsed >= slowAgentClose {
				slog.Warn("slow agent session close", "elapsed", elapsed, "session", sessionKey)
			}
		case <-time.After(10 * time.Second):
			slog.Error("agent session close timed out (10s), abandoning", "session", sessionKey)
		}
	}
}

const defaultEventIdleTimeout = 2 * time.Hour

func (e *Engine) processInteractiveEvents(state *interactiveState, session *Session, sessionKey string, msgID string, turnStart time.Time) {
	var textParts []string
	toolCount := 0
	waitStart := time.Now()
	firstEventLogged := false

	state.mu.Lock()
	sp := newStreamPreview(e.streamPreview, state.platform, state.replyCtx, e.ctx)
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
	for {
		var event Event
		var ok bool

		select {
		case event, ok = <-events:
			if !ok {
				goto channelClosed
			}
		case <-idleCh:
			slog.Error("agent session idle timeout: no events for too long, killing session",
				"session_key", sessionKey, "timeout", e.eventIdleTimeout, "elapsed", time.Since(turnStart))
			sp.finish("")
			state.mu.Lock()
			p := state.platform
			replyCtx := state.replyCtx
			state.mu.Unlock()
			e.send(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgError), "agent session timed out (no response)"))
			e.cleanupInteractiveState(sessionKey)
			return
		case <-e.ctx.Done():
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
		replyCtx := state.replyCtx
		sessionQuiet := state.quiet
		state.mu.Unlock()

		e.quietMu.RLock()
		globalQuiet := e.quiet
		e.quietMu.RUnlock()

		quiet := globalQuiet || sessionQuiet

		switch event.Type {
		case EventThinking:
			if !quiet && event.Content != "" {
				sp.freeze()
				preview := truncateIf(event.Content, e.display.ThinkingMaxLen)
				e.send(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgThinking), preview))
			}

		case EventToolUse:
			toolCount++
			if !quiet {
				sp.freeze()
				inputPreview := truncateIf(event.ToolInput, e.display.ToolMaxLen)
				// Use code block if content is long (>5 lines or >200 chars), otherwise inline code
				lineCount := strings.Count(inputPreview, "\n") + 1
				var formattedInput string
				if lineCount > 5 || utf8.RuneCountInString(inputPreview) > 200 {
					formattedInput = fmt.Sprintf("```\n%s\n```", inputPreview)
				} else {
					formattedInput = fmt.Sprintf("`%s`", inputPreview)
				}
				e.send(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgTool), toolCount, event.ToolName, formattedInput))
			}

		case EventText:
			if event.Content != "" {
				textParts = append(textParts, event.Content)
				if sp.canPreview() {
					sp.appendText(event.Content)
				}
			}
			if event.SessionID != "" {
				session.mu.Lock()
				if session.AgentSessionID == "" {
					session.AgentSessionID = event.SessionID
					pendingName := session.Name
					session.mu.Unlock()
					if pendingName != "" && pendingName != "session" && pendingName != "default" {
						e.sessions.SetSessionName(event.SessionID, pendingName)
					}
					e.sessions.Save()
				} else {
					session.mu.Unlock()
				}
			}

		case EventPermissionRequest:
			state.mu.Lock()
			autoApprove := state.approveAll
			state.mu.Unlock()

			if autoApprove {
				slog.Debug("auto-approving (approve-all)", "request_id", event.RequestID, "tool", event.ToolName)
				_ = state.agentSession.RespondPermission(event.RequestID, PermissionResult{
					Behavior:     "allow",
					UpdatedInput: event.ToolInputRaw,
				})
				continue
			}

			// Stop streaming preview before sending permission prompt
			sp.freeze()

			slog.Info("permission request",
				"request_id", event.RequestID,
				"tool", event.ToolName,
			)

			permLimit := e.display.ToolMaxLen
			if permLimit > 0 {
				permLimit = permLimit * 8 / 5 // permission prompts get ~1.6x more room
			}
			toolInput := truncateIf(event.ToolInput, permLimit)
			prompt := fmt.Sprintf(e.i18n.T(MsgPermissionPrompt), event.ToolName, toolInput)
			e.sendPermissionPrompt(p, replyCtx, prompt, event.ToolName, toolInput)

			pending := &pendingPermission{
				RequestID:    event.RequestID,
				ToolName:     event.ToolName,
				ToolInput:    event.ToolInputRaw,
				InputPreview: event.ToolInput,
				Resolved:     make(chan struct{}),
			}
			state.mu.Lock()
			state.pending = pending
			state.mu.Unlock()

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
			if event.SessionID != "" {
				session.mu.Lock()
				session.AgentSessionID = event.SessionID
				session.mu.Unlock()
			}

			fullResponse := event.Content
			if fullResponse == "" && len(textParts) > 0 {
				fullResponse = strings.Join(textParts, "")
			}
			if fullResponse == "" {
				fullResponse = e.i18n.T(MsgEmptyResponse)
			}

			session.AddHistory("assistant", fullResponse)
			e.sessions.Save()

			turnDuration := time.Since(turnStart)
			slog.Info("turn complete",
				"session", session.ID,
				"agent_session", session.AgentSessionID,
				"msg_id", msgID,
				"tools", toolCount,
				"response_len", len(fullResponse),
				"turn_duration", turnDuration,
			)

			replyStart := time.Now()

			// If streaming preview was active, try to finalize in-place
			if sp.finish(fullResponse) {
				slog.Debug("EventResult: finalized via stream preview", "response_len", len(fullResponse))
			} else {
				slog.Debug("EventResult: sending via p.Send (preview inactive or failed)", "response_len", len(fullResponse), "chunks", len(splitMessage(fullResponse, maxPlatformMessageLen)))
				for _, chunk := range splitMessage(fullResponse, maxPlatformMessageLen) {
					if err := p.Send(e.ctx, replyCtx, chunk); err != nil {
						slog.Error("failed to send reply", "error", err, "msg_id", msgID)
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
				if mode == "always" || (mode == "voice_only" && fromVoice) {
					go e.sendTTSReply(p, replyCtx, fullResponse)
				}
			}

			return

		case EventError:
			sp.finish("") // clean up preview on error
			if event.Error != nil {
				slog.Error("agent error", "error", event.Error)
				e.send(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgError), event.Error))
			}
			return
		}
	}

channelClosed:
	// Channel closed - process exited unexpectedly
	slog.Warn("agent process exited", "session_key", sessionKey)
	e.cleanupInteractiveState(sessionKey)

	if len(textParts) > 0 {
		state.mu.Lock()
		p := state.platform
		replyCtx := state.replyCtx
		state.mu.Unlock()

		fullResponse := strings.Join(textParts, "")
		session.AddHistory("assistant", fullResponse)

		if sp.finish(fullResponse) {
			slog.Debug("stream preview: finalized in-place (process exited)")
		} else {
			for _, chunk := range splitMessage(fullResponse, maxPlatformMessageLen) {
				e.send(p, replyCtx, chunk)
			}
		}
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
	{[]string{"tts"}, "tts"},
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

	if cmdID != "" && e.disabledCmds[cmdID] {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandDisabled), "/"+cmdID))
		return true
	}

	if cmdID != "" && privilegedCommands[cmdID] && !e.isAdmin(msg.UserID) {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgAdminRequired), "/"+cmdID))
		return true
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
	case "tts":
		e.cmdTTS(p, msg, args)
	default:
		if custom, ok := e.commands.Resolve(cmd); ok {
			e.executeCustomCommand(p, msg, custom, args)
			return true
		}
		if skill := e.skills.Resolve(cmd); skill != nil {
			e.executeSkill(p, msg, skill, args)
			return true
		}
		// Not a cc-connect command — notify user, then fall through to agent
		e.send(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgUnknownCommand), "/"+cmd))
		return false
	}
	return true
}

func (e *Engine) cmdNew(p Platform, msg *Message, args []string) {
	slog.Info("cmdNew: cleaning up old session", "session_key", msg.SessionKey)
	e.cleanupInteractiveState(msg.SessionKey)
	slog.Info("cmdNew: cleanup done, creating new session", "session_key", msg.SessionKey)
	name := ""
	if len(args) > 0 {
		name = strings.Join(args, " ")
	}
	s := e.sessions.NewSession(msg.SessionKey, name)
	if name != "" {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgNewSessionCreatedName), name))
	} else {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgNewSessionCreated))
	}
	_ = s
}

const listPageSize = 20

func (e *Engine) cmdList(p Platform, msg *Message, args []string) {
	if !supportsCards(p) {
		agentSessions, err := e.agent.ListSessions(e.ctx)
		if err != nil {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgListError), err))
			return
		}
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

		agentName := e.agent.Name()
		activeSession := e.sessions.GetOrCreateActive(msg.SessionKey)
		activeAgentID := activeSession.AgentSessionID

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
			displayName := e.sessions.GetSessionName(s.ID)
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
	agentSessions, err := e.agent.ListSessions(e.ctx)
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
		return
	}

	matched := e.matchSession(agentSessions, query)
	if matched == nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgSwitchNoMatch), query))
		return
	}

	slog.Info("cmdSwitch: cleaning up old session", "session_key", msg.SessionKey)
	e.cleanupInteractiveState(msg.SessionKey)
	slog.Info("cmdSwitch: cleanup done", "session_key", msg.SessionKey)

	session := e.sessions.GetOrCreateActive(msg.SessionKey)
	session.AgentSessionID = matched.ID
	session.Name = matched.Summary
	session.ClearHistory()
	e.sessions.Save()

	shortID := matched.ID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	displayName := e.sessions.GetSessionName(matched.ID)
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
func (e *Engine) matchSession(sessions []AgentSessionInfo, query string) *AgentSessionInfo {
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
		name := e.sessions.GetSessionName(sessions[i].ID)
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
		name := e.sessions.GetSessionName(sessions[i].ID)
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

	go func() {
		workDir := ""
		if wd, ok := e.agent.(interface{ GetWorkDir() string }); ok {
			workDir = wd.GetWorkDir()
		}
		if workDir == "" {
			workDir, _ = os.Getwd()
		}

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
		if len(result) > 4000 {
			result = result[:3997] + "..."
		}

		e.reply(p, msg.ReplyCtx, fmt.Sprintf("$ %s\n```\n%s\n```", shellCmd, result))
	}()
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
	agentSessions, err := e.agent.ListSessions(e.ctx)
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgSearchError), err))
		return
	}

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
		customName := e.sessions.GetSessionName(s.ID)
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

	// Check if first arg is a number → naming a specific session by list index
	var targetID string
	var name string

	if idx, err := strconv.Atoi(args[0]); err == nil && idx >= 1 {
		// /name <number> <name...>
		if len(args) < 2 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgNameUsage))
			return
		}
		agentSessions, err := e.agent.ListSessions(e.ctx)
		if err != nil {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
			return
		}
		if idx > len(agentSessions) {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgSwitchNoSession), idx))
			return
		}
		targetID = agentSessions[idx-1].ID
		name = strings.Join(args[1:], " ")
	} else {
		// /name <name...> → current session
		session := e.sessions.GetOrCreateActive(msg.SessionKey)
		targetID = session.AgentSessionID
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

	e.sessions.SetSessionName(targetID, name)

	shortID := targetID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgNameSet), name, shortID))
}

func (e *Engine) cmdCurrent(p Platform, msg *Message) {
	if !supportsCards(p) {
		s := e.sessions.GetOrCreateActive(msg.SessionKey)
		agentID := s.AgentSessionID
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
		platNames := make([]string, len(e.platforms))
		for i, pl := range e.platforms {
			platNames[i] = pl.Name()
		}
		platformStr := strings.Join(platNames, ", ")
		if len(platNames) == 0 {
			platformStr = "-"
		}

		uptimeStr := formatDurationI18n(time.Since(e.startedAt), e.i18n.CurrentLang())

		cur := e.i18n.CurrentLang()
		langStr := fmt.Sprintf("%s (%s)", string(cur), langDisplayName(cur))

		var modeStr string
		if ms, ok := e.agent.(ModeSwitcher); ok {
			mode := ms.GetMode()
			if mode != "" {
				modeStr = e.i18n.Tf(MsgStatusMode, mode)
			}
		}

		e.quietMu.RLock()
		globalQuiet := e.quiet
		e.quietMu.RUnlock()

		e.interactiveMu.Lock()
		state, hasState := e.interactiveStates[msg.SessionKey]
		e.interactiveMu.Unlock()

		sessionQuiet := false
		if hasState && state != nil {
			state.mu.Lock()
			sessionQuiet = state.quiet
			state.mu.Unlock()
		}

		quietStr := e.i18n.T(MsgQuietOffShort)
		if globalQuiet || sessionQuiet {
			quietStr = e.i18n.T(MsgQuietOnShort)
		}
		modeStr += e.i18n.Tf(MsgStatusQuiet, quietStr)

		s := e.sessions.GetOrCreateActive(msg.SessionKey)
		sessionDisplayName := e.sessions.GetSessionName(s.AgentSessionID)
		if sessionDisplayName == "" {
			sessionDisplayName = s.Name
		}
		sessionStr := e.i18n.Tf(MsgStatusSession, sessionDisplayName, len(s.History))

		var cronStr string
		if e.cronScheduler != nil {
			jobs := e.cronScheduler.Store().ListBySessionKey(msg.SessionKey)
			if len(jobs) > 0 {
				enabledCount := 0
				for _, j := range jobs {
					if j.Enabled {
						enabledCount++
					}
				}
				cronStr = e.i18n.Tf(MsgStatusCron, len(jobs), enabledCount)
			}
		}

		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgStatusTitle,
			e.name,
			e.agent.Name(),
			platformStr,
			uptimeStr,
			langStr,
			modeStr,
			sessionStr,
			cronStr,
		))
		return
	}

	e.replyWithCard(p, msg.ReplyCtx, e.renderStatusCard(msg.SessionKey))
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
		return e.simpleCard(e.i18n.Tf(MsgCardTitleSessions, e.agent.Name(), 0), "red", err.Error())
	}
	return card
}

func (e *Engine) renderStatusCard(sessionKey string) *Card {
	platNames := make([]string, len(e.platforms))
	for i, pl := range e.platforms {
		platNames[i] = pl.Name()
	}
	platformStr := strings.Join(platNames, ", ")
	if len(platNames) == 0 {
		platformStr = "-"
	}

	uptimeStr := formatDurationI18n(time.Since(e.startedAt), e.i18n.CurrentLang())

	cur := e.i18n.CurrentLang()
	langStr := fmt.Sprintf("%s (%s)", string(cur), langDisplayName(cur))

	var modeStr string
	if ms, ok := e.agent.(ModeSwitcher); ok {
		mode := ms.GetMode()
		if mode != "" {
			modeStr = e.i18n.Tf(MsgStatusMode, mode)
		}
	}

	e.quietMu.RLock()
	globalQuiet := e.quiet
	e.quietMu.RUnlock()

	e.interactiveMu.Lock()
	state, hasState := e.interactiveStates[sessionKey]
	e.interactiveMu.Unlock()

	sessionQuiet := false
	if hasState && state != nil {
		state.mu.Lock()
		sessionQuiet = state.quiet
		state.mu.Unlock()
	}

	quietStr := e.i18n.T(MsgQuietOffShort)
	if globalQuiet || sessionQuiet {
		quietStr = e.i18n.T(MsgQuietOnShort)
	}
	modeStr += e.i18n.Tf(MsgStatusQuiet, quietStr)

	s := e.sessions.GetOrCreateActive(sessionKey)
	sessionDisplayName := e.sessions.GetSessionName(s.AgentSessionID)
	if sessionDisplayName == "" {
		sessionDisplayName = s.Name
	}
	sessionStr := e.i18n.Tf(MsgStatusSession, sessionDisplayName, len(s.History))

	var cronStr string
	if e.cronScheduler != nil {
		jobs := e.cronScheduler.Store().ListBySessionKey(sessionKey)
		if len(jobs) > 0 {
			enabledCount := 0
			for _, j := range jobs {
				if j.Enabled {
					enabledCount++
				}
			}
			cronStr = e.i18n.Tf(MsgStatusCron, len(jobs), enabledCount)
		}
	}

	statusText := e.i18n.Tf(MsgStatusTitle,
		e.name,
		e.agent.Name(),
		platformStr,
		uptimeStr,
		langStr,
		modeStr,
		sessionStr,
		cronStr,
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

	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	n := 10
	if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
		n = v
	}

	entries := s.GetHistory(n)
	if len(entries) == 0 && s.AgentSessionID != "" {
		if hp, ok := e.agent.(HistoryProvider); ok {
			if agentEntries, err := hp.GetSessionHistory(e.ctx, s.AgentSessionID, n); err == nil {
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
	e.replyWithCard(p, msg.ReplyCtx, e.renderHelpCardForPlatform(p.Name()))
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
				{command: "/quiet", action: "act:/quiet"},
			},
		},
		{
			key:      "tools",
			titleKey: MsgHelpToolsSection,
			items: []helpCardItem{
				{command: "/cron", action: "nav:/cron"},
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
				{command: "/config", action: "nav:/config"},
				{command: "/version", action: "nav:/version"},
				{command: "/upgrade", action: "nav:/upgrade"},
				{command: "/restart", action: "cmd:/restart"},
			},
		},
	}
}

func (e *Engine) renderHelpCard() *Card {
	return e.renderHelpCardForPlatform("")
}

func (e *Engine) renderHelpCardForPlatform(platform string) *Card {
	return e.renderHelpGroupCardForPlatform(platform, defaultHelpGroup)
}

func splitHelpTabRows(platform string, tabs []CardButton) [][]CardButton {
	if strings.EqualFold(strings.TrimSpace(platform), "feishu") {
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

func (e *Engine) renderHelpGroupCardForPlatform(platform string, groupKey string) *Card {
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
	for _, row := range splitHelpTabRows(platform, tabs) {
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
		if e.disabledCmds[c.id] {
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
		if seenCmds[strings.ToLower(s.Name)] {
			continue
		}
		seenCmds[strings.ToLower(s.Name)] = true

		desc := s.Description
		if desc == "" {
			desc = "Skill"
		}

		commands = append(commands, BotCommandInfo{
			Command:     s.Name,
			Description: desc,
		})
	}

	return commands
}

func (e *Engine) cmdModel(p Platform, msg *Message, args []string) {
	switcher, ok := e.agent.(ModelSwitcher)
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
				desc := m.Desc
				if desc != "" {
					desc = " — " + desc
				}
				sb.WriteString(fmt.Sprintf("%s%d. %s%s\n", marker, i+1, m.Name, desc))

				label := m.Name
				if m.Name == current {
					label = "▶ " + label
				}
				row = append(row, ButtonOption{Text: label, Data: fmt.Sprintf("cmd:/model %d", i+1)})
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
		e.replyWithCard(p, msg.ReplyCtx, e.renderModelCard())
		return
	}

	fetchCtx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	models := switcher.AvailableModels(fetchCtx)

	target := args[0]
	if idx, err := strconv.Atoi(target); err == nil && idx >= 1 && idx <= len(models) {
		target = models[idx-1].Name
	}

	switcher.SetModel(target)
	e.cleanupInteractiveState(msg.SessionKey)

	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	s.AgentSessionID = ""
	s.ClearHistory()
	e.sessions.Save()

	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgModelChanged, target))
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
	e.cleanupInteractiveState(msg.SessionKey)

	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	s.AgentSessionID = ""
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
				marker := "  "
				if m.Key == current {
					marker = "▶ "
				}
				if zhLike {
					sb.WriteString(fmt.Sprintf("%s**%s** — %s\n", marker, m.NameZh, m.DescZh))
				} else {
					sb.WriteString(fmt.Sprintf("%s**%s** — %s\n", marker, m.Name, m.Desc))
				}
			}
			sb.WriteString(e.i18n.T(MsgModeUsage))

			var buttons [][]ButtonOption
			var row []ButtonOption
			for _, m := range modes {
				label := m.Name
				if zhLike {
					label = m.NameZh
				}
				if m.Key == current {
					label = "▶ " + label
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

	e.cleanupInteractiveState(msg.SessionKey)

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
	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgModeChanged), displayName))
}

func (e *Engine) cmdQuiet(p Platform, msg *Message, args []string) {
	// /quiet global — toggle global quiet for all sessions
	if len(args) > 0 && args[0] == "global" {
		e.quietMu.Lock()
		e.quiet = !e.quiet
		quiet := e.quiet
		e.quietMu.Unlock()

		if quiet {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgQuietGlobalOn))
		} else {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgQuietGlobalOff))
		}
		return
	}

	// /quiet — toggle per-session quiet
	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[msg.SessionKey]
	e.interactiveMu.Unlock()

	if !ok || state == nil {
		state = &interactiveState{platform: p, replyCtx: msg.ReplyCtx, quiet: true}
		e.interactiveMu.Lock()
		e.interactiveStates[msg.SessionKey] = state
		e.interactiveMu.Unlock()
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgQuietOn))
		return
	}

	state.mu.Lock()
	state.quiet = !state.quiet
	quiet := state.quiet
	state.mu.Unlock()

	if quiet {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgQuietOn))
	} else {
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
	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[msg.SessionKey]
	e.interactiveMu.Unlock()

	if !ok || state == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgNoExecution))
		return
	}

	// Cancel pending permission if any
	state.mu.Lock()
	pending := state.pending
	quietMode := state.quiet
	if pending != nil {
		state.pending = nil
	}
	state.mu.Unlock()
	if pending != nil {
		pending.resolve()
	}

	e.cleanupInteractiveState(msg.SessionKey)

	// Preserve quiet preference across stop
	if quietMode {
		e.interactiveMu.Lock()
		if s, ok := e.interactiveStates[msg.SessionKey]; ok {
			s.mu.Lock()
			s.quiet = quietMode
			s.mu.Unlock()
		} else {
			e.interactiveStates[msg.SessionKey] = &interactiveState{platform: p, replyCtx: msg.ReplyCtx, quiet: quietMode}
		}
		e.interactiveMu.Unlock()
	}

	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgExecutionStopped))
}

func (e *Engine) cmdCompress(p Platform, msg *Message) {
	compressor, ok := e.agent.(ContextCompressor)
	if !ok || compressor.CompressCommand() == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCompressNotSupported))
		return
	}

	e.interactiveMu.Lock()
	state, hasState := e.interactiveStates[msg.SessionKey]
	e.interactiveMu.Unlock()

	if !hasState || state == nil || state.agentSession == nil || !state.agentSession.Alive() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCompressNoSession))
		return
	}

	session := e.sessions.GetOrCreateActive(msg.SessionKey)
	if !session.TryLock() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPreviousProcessing))
		return
	}

	e.send(p, msg.ReplyCtx, e.i18n.T(MsgCompressing))

	go func() {
		defer session.Unlock()

		state.mu.Lock()
		state.platform = p
		state.replyCtx = msg.ReplyCtx
		state.mu.Unlock()

		drainEvents(state.agentSession.Events())

		cmd := compressor.CompressCommand()
		if err := state.agentSession.Send(cmd, nil); err != nil {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), err))
			if !state.agentSession.Alive() {
				e.cleanupInteractiveState(msg.SessionKey)
			}
			return
		}

		e.processCompressEvents(state, msg.SessionKey, p, msg.ReplyCtx)
	}()
}

// processCompressEvents drains agent events after a compress command.
// Unlike processInteractiveEvents it does NOT record history and treats
// an empty result as success rather than "(empty response)".
func (e *Engine) processCompressEvents(state *interactiveState, sessionKey string, p Platform, replyCtx any) {
	var textParts []string
	events := state.agentSession.Events()

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
		case event, ok = <-events:
			if !ok {
				e.cleanupInteractiveState(sessionKey)
				if len(textParts) > 0 {
					e.send(p, replyCtx, strings.Join(textParts, ""))
				} else {
					e.reply(p, replyCtx, e.i18n.T(MsgCompressDone))
				}
				return
			}
		case <-idleCh:
			e.send(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgError), "compress timed out"))
			e.cleanupInteractiveState(sessionKey)
			return
		case <-e.ctx.Done():
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
			if event.Content != "" {
				textParts = append(textParts, event.Content)
			}
		case EventResult:
			result := event.Content
			if result == "" && len(textParts) > 0 {
				result = strings.Join(textParts, "")
			}
			if result != "" {
				e.send(p, replyCtx, result)
			} else {
				e.reply(p, replyCtx, e.i18n.T(MsgCompressDone))
			}
			return
		case EventError:
			if event.Error != nil {
				e.reply(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgError), event.Error))
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
		e.cleanupInteractiveState(msg.SessionKey)
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
	e.cleanupInteractiveState(msg.SessionKey)

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
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()

	var state *interactiveState
	if sessionKey != "" {
		state = e.interactiveStates[sessionKey]
	} else {
		// Pick the first active session
		for _, s := range e.interactiveStates {
			state = s
			break
		}
	}

	if state == nil {
		return fmt.Errorf("no active session found (key=%q)", sessionKey)
	}

	state.mu.Lock()
	p := state.platform
	replyCtx := state.replyCtx
	state.mu.Unlock()

	if p == nil {
		return fmt.Errorf("no active session found (key=%q)", sessionKey)
	}

	return p.Send(e.ctx, replyCtx, message)
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
		if err := bs.SendWithButtons(e.ctx, replyCtx, prompt, buttons); err == nil {
			return
		}
		slog.Warn("sendPermissionPrompt: inline buttons failed, falling back")
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

// send wraps p.Send with error logging and slow-operation warnings.
func (e *Engine) send(p Platform, replyCtx any, content string) {
	start := time.Now()
	if err := p.Send(e.ctx, replyCtx, content); err != nil {
		slog.Error("platform send failed", "platform", p.Name(), "error", err, "content_len", len(content))
	}
	if elapsed := time.Since(start); elapsed >= slowPlatformSend {
		slog.Warn("slow platform send", "platform", p.Name(), "elapsed", elapsed, "content_len", len(content))
	}
}

// drainEvents discards any buffered events from the channel.
// Called before a new turn to prevent stale events from a previous turn's
// agent process from being mistaken for the new turn's response.
func drainEvents(ch <-chan Event) {
	drained := 0
	for {
		select {
		case <-ch:
			drained++
		default:
			if drained > 0 {
				slog.Warn("drained stale events from previous turn", "count", drained)
			}
			return
		}
	}
}

// reply wraps p.Reply with error logging and slow-operation warnings.
func (e *Engine) reply(p Platform, replyCtx any, content string) {
	start := time.Now()
	if err := p.Reply(e.ctx, replyCtx, content); err != nil {
		slog.Error("platform reply failed", "platform", p.Name(), "error", err, "content_len", len(content))
	}
	if elapsed := time.Since(start); elapsed >= slowPlatformSend {
		slog.Warn("slow platform reply", "platform", p.Name(), "elapsed", elapsed, "content_len", len(content))
	}
}

// replyWithButtons sends a reply with inline buttons if the platform supports it,
// otherwise falls back to plain text reply.
func (e *Engine) replyWithButtons(p Platform, replyCtx any, content string, buttons [][]ButtonOption) {
	if bs, ok := p.(InlineButtonSender); ok {
		if err := bs.SendWithButtons(e.ctx, replyCtx, content, buttons); err == nil {
			return
		}
	}
	e.reply(p, replyCtx, content)
}

func isInlineButtonOnlyPlatform(p Platform) bool {
	if _, ok := p.(InlineButtonSender); !ok {
		return false
	}
	return !supportsCards(p)
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
	if cs, ok := p.(CardSender); ok {
		if err := cs.ReplyCard(e.ctx, replyCtx, card); err != nil {
			slog.Error("card reply failed", "platform", p.Name(), "error", err)
		}
		return
	}
	e.reply(p, replyCtx, card.RenderText())
}

// sendWithCard sends a card as a new message (not a reply).
func (e *Engine) sendWithCard(p Platform, replyCtx any, card *Card) {
	if card == nil {
		slog.Error("sendWithCard: nil card", "platform", p.Name())
		return
	}
	if cs, ok := p.(CardSender); ok {
		if err := cs.SendCard(e.ctx, replyCtx, card); err != nil {
			slog.Error("card send failed", "platform", p.Name(), "error", err)
		}
		return
	}
	e.send(p, replyCtx, card.RenderText())
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

	if prefix == "act" {
		e.executeCardAction(cmd, args, sessionKey)
	}

	switch cmd {
	case "/help":
		platform := ""
		if sessionKey != "" {
			if p, _, err := parseSessionKeyParts(sessionKey); err == nil {
				platform = p
			}
		}
		return e.renderHelpGroupCardForPlatform(platform, args)
	case "/model":
		return e.renderModelCard()
	case "/reasoning":
		return e.renderReasoningCard()
	case "/mode":
		return e.renderModeCard()
	case "/lang":
		return e.renderLangCard()
	case "/status":
		return e.renderStatusCard(sessionKey)
	case "/list":
		page := 1
		if args != "" {
			if n, err := strconv.Atoi(args); err == nil && n > 0 {
				page = n
			}
		}
		return e.renderListCardSafe(sessionKey, page)
	case "/current":
		return e.renderCurrentCard(sessionKey)
	case "/history":
		return e.renderHistoryCard(sessionKey)
	case "/provider":
		return e.renderProviderCard()
	case "/cron":
		return e.renderCronCard(sessionKey)
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
	case "/version":
		return e.renderVersionCard()
	case "/new":
		return e.renderCurrentCard(sessionKey)
	case "/quiet":
		return e.renderStatusCard(sessionKey)
	case "/switch":
		return e.renderListCardSafe(sessionKey, 1)
	case "/delete-mode":
		if strings.HasPrefix(args, "cancel") {
			return e.renderListCardSafe(sessionKey, 1)
		}
		return e.renderDeleteModeCard(sessionKey)
	case "/stop":
		return e.renderStatusCard(sessionKey)
	case "/upgrade":
		return e.renderUpgradeCard()
	}
	return nil
}

// executeCardAction performs the side-effect for act: prefixed actions
// (e.g. switching model/mode/lang) before the card is re-rendered.
func (e *Engine) executeCardAction(cmd, args, sessionKey string) {
	switch cmd {
	case "/model":
		if args == "" {
			return
		}
		switcher, ok := e.agent.(ModelSwitcher)
		if !ok {
			return
		}
		fetchCtx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
		defer cancel()
		models := switcher.AvailableModels(fetchCtx)
		target := args
		if idx, err := strconv.Atoi(target); err == nil && idx >= 1 && idx <= len(models) {
			target = models[idx-1].Name
		}
		switcher.SetModel(target)
		e.cleanupInteractiveState(sessionKey)
		s := e.sessions.GetOrCreateActive(sessionKey)
		s.AgentSessionID = ""
		s.ClearHistory()
		e.sessions.Save()

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
				e.cleanupInteractiveState(sessionKey)
				s := e.sessions.GetOrCreateActive(sessionKey)
				s.AgentSessionID = ""
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
		switcher.SetMode(strings.ToLower(args))
		e.cleanupInteractiveState(sessionKey)

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
			e.cleanupInteractiveState(sessionKey)
			if e.providerSaveFunc != nil {
				_ = e.providerSaveFunc(args)
			}
		}

	case "/new":
		e.cleanupInteractiveState(sessionKey)
		e.sessions.NewSession(sessionKey, "")

	case "/delete-mode":
		e.executeDeleteModeAction(sessionKey, args)

	case "/quiet":
		e.interactiveMu.Lock()
		state, ok := e.interactiveStates[sessionKey]
		if !ok || state == nil {
			state = &interactiveState{quiet: true}
			e.interactiveStates[sessionKey] = state
			e.interactiveMu.Unlock()
		} else {
			e.interactiveMu.Unlock()
			state.mu.Lock()
			state.quiet = !state.quiet
			state.mu.Unlock()
		}

	case "/switch":
		if args == "" {
			return
		}
		agentSessions, err := e.agent.ListSessions(e.ctx)
		if err != nil || len(agentSessions) == 0 {
			return
		}
		matched := e.matchSession(agentSessions, args)
		if matched == nil {
			return
		}
		e.cleanupInteractiveState(sessionKey)
		session := e.sessions.GetOrCreateActive(sessionKey)
		session.AgentSessionID = matched.ID
		session.Name = matched.Summary
		session.ClearHistory()
		e.sessions.Save()

	case "/stop":
		e.interactiveMu.Lock()
		state, ok := e.interactiveStates[sessionKey]
		if !ok || state == nil {
			e.interactiveMu.Unlock()
			return
		}
		state.mu.Lock()
		pending := state.pending
		quietMode := state.quiet
		agentSession := state.agentSession
		if pending != nil {
			state.pending = nil
		}
		state.agentSession = nil
		state.mu.Unlock()
		if quietMode {
			e.interactiveStates[sessionKey] = &interactiveState{quiet: true}
		} else {
			delete(e.interactiveStates, sessionKey)
		}
		e.interactiveMu.Unlock()
		if pending != nil {
			pending.resolve()
		}
		if agentSession != nil {
			slog.Debug("cleanupInteractiveState: closing agent session", "session", sessionKey)
			go agentSession.Close()
		}
	}
}

func (e *Engine) getOrCreateDeleteModeState(sessionKey string, p Platform, replyCtx any) *deleteModeState {
	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[sessionKey]
	if !ok || state == nil {
		state = &interactiveState{platform: p, replyCtx: replyCtx}
		e.interactiveStates[sessionKey] = state
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
	e.interactiveMu.Lock()
	state := e.interactiveStates[sessionKey]
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

func (e *Engine) clearDeleteModeState(sessionKey string) {
	e.interactiveMu.Lock()
	state := e.interactiveStates[sessionKey]
	e.interactiveMu.Unlock()
	if state == nil {
		return
	}
	state.mu.Lock()
	state.deleteMode = nil
	state.mu.Unlock()
}

func (e *Engine) renderDeleteModeCard(sessionKey string) *Card {
	agentSessions, err := e.agent.ListSessions(e.ctx)
	if err != nil {
		return e.simpleCard(e.i18n.T(MsgDeleteModeTitle), "red", err.Error())
	}
	dm := e.getDeleteModeState(sessionKey)
	if dm == nil {
		return e.simpleCard(e.i18n.T(MsgDeleteModeTitle), "red", e.i18n.T(MsgDeleteUsage))
	}
	switch dm.phase {
	case "confirm":
		return e.renderDeleteModeConfirmCard(dm, agentSessions)
	case "result":
		return e.renderDeleteModeResultCard(dm)
	default:
		return e.renderDeleteModeSelectCard(sessionKey, dm, agentSessions)
	}
}

func (e *Engine) renderDeleteModeSelectCard(sessionKey string, dm *deleteModeState, agentSessions []AgentSessionInfo) *Card {
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
	for i := start; i < end; i++ {
		s := agentSessions[i]
		marker := "◻"
		if _, ok := dm.selectedIDs[s.ID]; ok {
			marker = "☑"
		}
		if active := e.sessions.GetOrCreateActive(sessionKey).AgentSessionID; active == s.ID {
			marker += " ▶"
		}
		btnText := e.i18n.T(MsgDeleteModeSelect)
		btnType := "default"
		if _, ok := dm.selectedIDs[s.ID]; ok {
			btnText = e.i18n.T(MsgDeleteModeSelected)
			btnType = "primary"
		}
		cb.ListItemBtn(
			e.i18n.Tf(MsgListItem, marker, i+1, e.deleteSessionDisplayName(&s), s.MessageCount, s.ModifiedAt.Format("01-02 15:04")),
			btnText,
			btnType,
			fmt.Sprintf("act:/delete-mode toggle %s", s.ID),
		)
	}
	cb.Note(e.i18n.Tf(MsgDeleteModeSelectedCount, len(dm.selectedIDs)))
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

func (e *Engine) renderDeleteModeConfirmCard(dm *deleteModeState, agentSessions []AgentSessionInfo) *Card {
	selectedNames := e.deleteModeSelectionNames(dm, agentSessions)
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

func (e *Engine) deleteModeSelectionNames(dm *deleteModeState, agentSessions []AgentSessionInfo) []string {
	names := make([]string, 0, len(dm.selectedIDs))
	for i := range agentSessions {
		if _, ok := dm.selectedIDs[agentSessions[i].ID]; ok {
			names = append(names, "- "+e.deleteSessionDisplayName(&agentSessions[i]))
		}
	}
	return names
}

func (e *Engine) executeDeleteModeAction(sessionKey, args string) {
	e.interactiveMu.Lock()
	state := e.interactiveStates[sessionKey]
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
		lines := e.submitDeleteModeSelection(sessionKey, dm)
		dm.selectedIDs = make(map[string]struct{})
		dm.result = strings.Join(lines, "\n")
		dm.hint = ""
		dm.phase = "result"
	case "cancel":
		state.deleteMode = nil
	}
}

func (e *Engine) submitDeleteModeSelection(sessionKey string, dm *deleteModeState) []string {
	deleter, ok := e.agent.(SessionDeleter)
	if !ok {
		return []string{e.i18n.T(MsgDeleteNotSupported)}
	}
	agentSessions, err := e.agent.ListSessions(e.ctx)
	if err != nil {
		return []string{fmt.Sprintf("❌ %v", err)}
	}
	seen := make(map[string]struct{}, len(agentSessions))
	lines := make([]string, 0, len(dm.selectedIDs))
	for i := range agentSessions {
		seen[agentSessions[i].ID] = struct{}{}
		if _, ok := dm.selectedIDs[agentSessions[i].ID]; !ok {
			continue
		}
		if line := e.deleteSingleSessionReply(&Message{SessionKey: sessionKey}, deleter, &agentSessions[i]); line != "" {
			lines = append(lines, line)
		}
	}
	missingIDs := make([]string, 0)
	for id := range dm.selectedIDs {
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

func (e *Engine) renderModelCard() *Card {
	switcher, ok := e.agent.(ModelSwitcher)
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
		if m.Desc != "" {
			label += " — " + m.Desc
		}
		val := fmt.Sprintf("act:/model %d", i+1)
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
	cb.Note(e.i18n.T(MsgModeUsage))
	return cb.Build()
}

func (e *Engine) renderListCard(sessionKey string, page int) (*Card, error) {
	agentSessions, err := e.agent.ListSessions(e.ctx)
	if err != nil {
		return nil, fmt.Errorf(e.i18n.T(MsgListError), err)
	}
	if len(agentSessions) == 0 {
		return e.simpleCard(e.i18n.Tf(MsgCardTitleSessions, e.agent.Name(), 0), "turquoise", e.i18n.T(MsgListEmpty)), nil
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

	agentName := e.agent.Name()
	activeSession := e.sessions.GetOrCreateActive(sessionKey)
	activeAgentID := activeSession.AgentSessionID

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
		displayName := e.sessions.GetSessionName(s.ID)
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

// ──────────────────────────────────────────────────────────────
// Navigable sub-cards (for in-place card updates)
// ──────────────────────────────────────────────────────────────

func (e *Engine) renderCurrentCard(sessionKey string) *Card {
	s := e.sessions.GetOrCreateActive(sessionKey)
	agentID := s.AgentSessionID
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
	s := e.sessions.GetOrCreateActive(sessionKey)
	entries := s.GetHistory(10)

	if len(entries) == 0 && s.AgentSessionID != "" {
		if hp, ok := e.agent.(HistoryProvider); ok {
			if agentEntries, err := hp.GetSessionHistory(e.ctx, s.AgentSessionID, 10); err == nil {
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

func (e *Engine) renderCronCard(sessionKey string) *Card {
	if e.cronScheduler == nil {
		return e.simpleCard(e.i18n.T(MsgCardTitleCron), "orange", e.i18n.T(MsgCronNotAvailable))
	}

	jobs := e.cronScheduler.Store().ListBySessionKey(sessionKey)
	if len(jobs) == 0 {
		return e.simpleCard(e.i18n.T(MsgCardTitleCron), "orange", e.i18n.T(MsgCronEmpty))
	}

	lang := e.i18n.CurrentLang()
	now := time.Now()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(e.i18n.T(MsgCronListTitle), len(jobs)))
	sb.WriteString("\n\n")

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
			desc = truncateStr(j.Prompt, 60)
		}
		sb.WriteString(fmt.Sprintf("%s %s\n", status, desc))
		sb.WriteString(e.i18n.Tf(MsgCronIDLabel, j.ID))
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
				sb.WriteString(e.i18n.Tf(MsgCronFailedSuffix, truncateStr(j.LastError, 40)))
			}
			sb.WriteString("\n")
		}
	}

	return NewCard().Title(e.i18n.T(MsgCardTitleCron), "orange").
		Markdown(sb.String()).
		Buttons(e.cardBackButton()).
		Build()
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
			content = fmt.Sprintf("❌ %s", res.err)
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
		e.replyWithCard(p, msg.ReplyCtx, e.renderCronCard(msg.SessionKey))
		return
	}

	sub := matchSubCommand(strings.ToLower(args[0]), []string{
		"add", "list", "del", "delete", "rm", "remove", "enable", "disable",
	})
	switch sub {
	case "add":
		e.cmdCronAdd(p, msg, args[1:])
	case "list":
		e.cmdCronList(p, msg)
	case "del", "delete", "rm", "remove":
		e.cmdCronDel(p, msg, args[1:])
	case "enable":
		e.cmdCronToggle(p, msg, args[1:], true)
	case "disable":
		e.cmdCronToggle(p, msg, args[1:], false)
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
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
		return
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronAdded), job.ID, cronExpr, truncateStr(prompt, 60)))
}

func (e *Engine) cmdCronList(p Platform, msg *Message) {
	jobs := e.cronScheduler.Store().ListBySessionKey(msg.SessionKey)
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
			desc = truncateStr(j.Prompt, 60)
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
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
		return
	}
	if enable {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronEnabled), id))
	} else {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCronDisabled), id))
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Execute command using shell
	shellCmd := exec.CommandContext(ctx, "sh", "-c", execCmd)
	shellCmd.Dir = workDir
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
					return e.displaySaveFunc(&n, nil)
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
					return e.displaySaveFunc(nil, &n)
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
					e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
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
						e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
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
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %s", err))
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
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %s", err))
		return
	}
	if release == nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgUpgradeUpToDate), cur))
		return
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgUpgradeDownloading), release.TagName))

	if err := SelfUpdate(release.TagName, useGitee); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %s", err))
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
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
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
	deleter, ok := e.agent.(SessionDeleter)
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

	agentSessions, err := e.agent.ListSessions(e.ctx)
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
		return
	}

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
	activeSession := e.sessions.GetOrCreateActive(msg.SessionKey)
	if activeSession.AgentSessionID == matched.ID {
		return e.i18n.T(MsgDeleteActiveDenied)
	}

	displayName := e.deleteSessionDisplayName(matched)

	if err := deleter.DeleteSession(e.ctx, matched.ID); err != nil {
		return fmt.Sprintf("❌ %s: %v", displayName, err)
	}

	e.sessions.SetSessionName(matched.ID, "")
	return fmt.Sprintf(e.i18n.T(MsgDeleteSuccess), displayName)
}

func (e *Engine) deleteSessionDisplayName(matched *AgentSessionInfo) string {
	displayName := e.sessions.GetSessionName(matched.ID)
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
	if len(text) <= maxLen {
		return []string{text}
	}
	var chunks []string

	for len(text) > 0 {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}

		end := maxLen

		// Try to split at newline boundary
		if idx := strings.LastIndex(text[:end], "\n"); idx > 0 && idx >= end/2 {
			end = idx + 1
		}

		chunk := text[:end]
		text = text[end:]

		chunks = append(chunks, chunk)
	}
	return chunks
}

// sendTTSReply synthesizes fullResponse text and sends audio to the platform.
// Called asynchronously after EventResult; text reply is always sent first.
func (e *Engine) sendTTSReply(p Platform, replyCtx any, text string) {
	if e.tts == nil {
		return
	}
	if e.tts.MaxTextLen > 0 && utf8.RuneCountInString(text) > e.tts.MaxTextLen {
		slog.Warn("tts: text exceeds max_text_len, skipping synthesis", "len", utf8.RuneCountInString(text), "max", e.tts.MaxTextLen)
		return
	}
	opts := TTSSynthesisOpts{Voice: e.tts.Voice}
	audioData, format, err := e.tts.TTS.Synthesize(e.ctx, text, opts)
	if err != nil {
		slog.Error("tts: synthesis failed", "error", err)
		return
	}
	as, ok := p.(AudioSender)
	if !ok {
		slog.Debug("tts: platform does not support audio sending", "platform", p.Name())
		return
	}
	if err := as.SendAudio(e.ctx, replyCtx, audioData, format); err != nil {
		slog.Error("tts: platform audio send failed", "platform", p.Name(), "error", err)
	}
}

// ──────────────────────────────────────────────────────────────
// Bot-to-bot relay
// ──────────────────────────────────────────────────────────────

// HandleRelay processes a relay message synchronously: starts or resumes a
// dedicated relay session, sends the message to the agent, and blocks until
// the complete response is collected.
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

	agentSession, err := e.agent.StartSession(ctx, session.AgentSessionID)
	if err != nil {
		return "", fmt.Errorf("start relay session: %w", err)
	}

	if session.AgentSessionID == "" {
		session.AgentSessionID = agentSession.CurrentSessionID()
		e.sessions.Save()
	}

	if err := agentSession.Send(message, nil); err != nil {
		return "", fmt.Errorf("send relay message: %w", err)
	}

	var textParts []string
	for event := range agentSession.Events() {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		switch event.Type {
		case EventText:
			if event.Content != "" {
				textParts = append(textParts, event.Content)
			}
			if event.SessionID != "" && session.AgentSessionID == "" {
				session.AgentSessionID = event.SessionID
				e.sessions.Save()
			}
		case EventResult:
			if event.SessionID != "" {
				session.AgentSessionID = event.SessionID
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
			return resp, nil
		case EventError:
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
	}

	if len(textParts) > 0 {
		return strings.Join(textParts, ""), nil
	}
	return "", fmt.Errorf("relay: agent process exited without response")
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

func (e *Engine) cmdBindSetup(p Platform, msg *Message) {
	mp, ok := e.agent.(MemoryFileProvider)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgRelaySetupNoMemory))
		return
	}

	filePath := mp.ProjectMemoryFile()
	if filePath == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgRelaySetupNoMemory))
		return
	}

	existing, _ := os.ReadFile(filePath)
	if strings.Contains(string(existing), ccConnectInstructionMarker) {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgRelaySetupExists), filepath.Base(filePath)))
		return
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
		return
	}

	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
		return
	}
	defer f.Close()

	block := "\n" + ccConnectInstructionMarker + "\n" + AgentSystemPrompt() + "\n"
	if _, err := f.WriteString(block); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
		return
	}

	e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgRelaySetupOK), filepath.Base(filePath)))
}
