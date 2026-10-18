package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// toolInputCacheMaxEntries caps toolInputByID growth; beyond this we evict
// roughly half the map (iteration order is arbitrary) to bound memory.
const toolInputCacheMaxEntries = 1000

type acpSession struct {
	workDir string
	events  chan core.Event
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	alive   atomic.Bool

	// replaying is true while session/load is in flight. The agent replays the
	// whole history as session/update notifications before answering; those
	// are absorbed for state but not emitted, because nothing drains events
	// until StartSession returns and a long replay would fill the bounded
	// channel and block the transport read loop (deadlocking the load).
	// It currently only guards tryLoadSession (session/load). Any future
	// resume-style call that makes the agent replay history (e.g.
	// session/resume) must set it too.
	replaying atomic.Bool

	cmd *exec.Cmd
	tr  *transport

	acpSessMu sync.RWMutex
	acpSessID string

	sendMu sync.Mutex

	permMu   sync.Mutex
	permByID map[string]permState

	toolInputMu   sync.Mutex
	toolInputByID map[string]string // toolCallId -> summarized tool input
	toolNameByID  map[string]string // toolCallId -> title or kind from tool_call
	// toolResultByID holds the last tool output emitted per toolCallId so the
	// same output repeated by later updates (e.g. Devin's terminal preview and
	// exit update) is not shown twice.
	toolResultByID map[string]string

	// modesMu guards availableModes and currentMode. Both fields are
	// populated on handshake (session/new or session/load response) and
	// updated whenever SetLiveMode succeeds or the server announces a
	// mode change via session/update.
	modesMu        sync.RWMutex
	availableModes []acpModeInfo
	currentMode    string

	callbacks sessionCallbacks // may be nil (tests, integration harness)

	// promptImages is set from initialize promptCapabilities.image.
	// When true, Send transmits images as ACP image content blocks.
	promptImages bool

	// modeAliases rewrites user mode names before they are matched
	// against the modes this session advertised. Copied at start.
	modeAliases map[string]string

	configMu      sync.RWMutex
	configOptions []acpConfigOption
	legacyModels  *acpModelsBlock
	reportedModel string

	// modelResolver maps a configured model onto an advertised option.
	modelResolver func(input string, optionValues []string) (value string, impliedEffort string)

	// compactCommand is the agent's compress prompt, in addition to "/compact".
	compactCommand string
	// compactionNotifyMethod is the vendor notification that reports async
	// compaction progress. Empty disables compaction tracking.
	compactionNotifyMethod string
	// showModelDisplayName enables GetModelDisplayName (set by wrappers).
	showModelDisplayName bool
	compactMu            sync.Mutex
	compactStatus        string

	// lastUpdate is the time of the newest session/update for this
	// session. updateNotify wakes a compaction quiet-wait.
	updateMu     sync.Mutex
	lastUpdate   time.Time
	updateNotify chan struct{}

	warnMu          sync.Mutex
	startupWarnings []string

	usageMu sync.Mutex
	usage   *core.ContextUsage
}

type permState struct {
	RPCID   json.RawMessage
	Options []permissionOption
}

// acpSessionConfig bundles the inputs newACPSession needs. It's a
// struct rather than a long positional argument list because we keep
// adding optional knobs (initialMode, callbacks) and would otherwise
// break every call site each time.
type acpSessionConfig struct {
	command                string
	args                   []string
	extraEnv               []string
	workDir                string
	resumeSessionID        string
	authMethod             string
	initialMode            string // if non-empty, applied via session/set_mode after session/new
	initialModel           string // configured model, applied after initialMode
	initialEffort          string // configured reasoning effort, applied after the model
	modelResolver          func(input string, optionValues []string) (value string, impliedEffort string)
	compactCommand         string
	compactionNotifyMethod string
	showModelName          bool
	modeAliases            map[string]string
	callbacks              sessionCallbacks // may be nil
}

func newACPSession(ctx context.Context, cfg acpSessionConfig) (*acpSession, error) {
	absWorkDir := canonicalCwd(cfg.workDir)

	sessionCtx, cancel := context.WithCancel(ctx)
	s := &acpSession{
		workDir:                absWorkDir,
		events:                 make(chan core.Event, 128),
		ctx:                    sessionCtx,
		cancel:                 cancel,
		permByID:               make(map[string]permState),
		toolInputByID:          make(map[string]string),
		toolNameByID:           make(map[string]string),
		acpSessID:              cfg.resumeSessionID,
		callbacks:              cfg.callbacks,
		modeAliases:            cloneModeAliases(cfg.modeAliases),
		modelResolver:          cfg.modelResolver,
		compactCommand:         cfg.compactCommand,
		compactionNotifyMethod: cfg.compactionNotifyMethod,
		showModelDisplayName:   cfg.showModelName,
		updateNotify:           make(chan struct{}, 1),
	}
	s.alive.Store(true)

	cmd := exec.CommandContext(sessionCtx, cfg.command, cfg.args...)
	cmd.Dir = absWorkDir
	cmd.Env = core.MergeEnv(os.Environ(), cfg.extraEnv)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("acp: stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(&stderrBuf, os.Stderr)

	s.cmd = cmd
	s.tr = newTransport(stdout, stdin, s.onNotification, s.onServerRequest)

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("acp: start %s: %w", cfg.command, err)
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.tr.readLoop(sessionCtx)
		waitErr := cmd.Wait()
		if waitErr != nil {
			msg := stderrBuf.String()
			if msg != "" {
				slog.Error("acp: process exited", "error", waitErr, "stderr", msg)
				s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("%s", strings.TrimSpace(msg))})
			} else {
				slog.Debug("acp: process exited", "error", waitErr)
			}
		}
		s.alive.Store(false)
	}()

	if err := s.handshake(cfg.resumeSessionID, cfg.authMethod); err != nil {
		_ = s.Close()
		return nil, err
	}

	// Mode, then model, then thought_level. Failures are surfaced
	// through StartupWarning and do not fail the session.
	s.applyStartupPreferences(cfg.initialMode, cfg.initialModel, cfg.initialEffort)

	return s, nil
}

// handshake runs initialize → optional authenticate → session/load or
// session/new, and caches modes, config options, and capabilities.
func (s *acpSession) handshake(resumeSessionID string, authMethod string) error {
	initOut, err := s.rpcInitialize()
	if err != nil {
		return err
	}
	s.applyInitialize(initOut)
	if err := s.maybeAuthenticate(authMethod); err != nil {
		return err
	}
	return s.openOrCreateSession(resumeSessionID, initOut.AgentCapabilities.LoadSession)
}

func (s *acpSession) rpcInitialize() (acpInitializeResult, error) {
	res, err := s.tr.call(s.ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{
				"readTextFile":  false,
				"writeTextFile": false,
			},
			"terminal": false,
		},
		"clientInfo": map[string]any{
			"name":    "cc-connect",
			"version": "1.0.0",
		},
	})
	if err != nil {
		return acpInitializeResult{}, fmt.Errorf("acp: initialize: %w", err)
	}
	var initOut acpInitializeResult
	if err := json.Unmarshal(res, &initOut); err != nil {
		return acpInitializeResult{}, fmt.Errorf("acp: parse initialize result: %w", err)
	}
	return initOut, nil
}

func (s *acpSession) applyInitialize(initOut acpInitializeResult) {
	listSupported := sessionListSupported(&initOut)
	deleteSupported := sessionDeleteSupported(&initOut)
	s.promptImages = initOut.AgentCapabilities.PromptCapabilities.Image
	slog.Debug("acp: initialized",
		"protocol", initOut.ProtocolVersion,
		"load_session", initOut.AgentCapabilities.LoadSession,
		"list_sessions", listSupported,
		"delete_session", deleteSupported,
		"prompt_images", s.promptImages,
	)
	if s.callbacks != nil {
		s.callbacks.reportListSupported(listSupported)
		s.callbacks.reportDeleteSupported(deleteSupported)
	}
}

func (s *acpSession) maybeAuthenticate(authMethod string) error {
	if strings.TrimSpace(authMethod) == "" {
		return nil
	}
	if _, err := s.tr.call(s.ctx, "authenticate", map[string]any{
		"methodId": authMethod,
	}); err != nil {
		return fmt.Errorf("acp: authenticate (%s): %w", authMethod, err)
	}
	slog.Debug("acp: authenticated", "method_id", authMethod)
	return nil
}

func (s *acpSession) openOrCreateSession(resumeSessionID string, loadSupported bool) error {
	wantResume := resumeSessionID != "" && resumeSessionID != core.ContinueSession
	if wantResume && loadSupported && s.tryLoadSession(resumeSessionID) {
		return nil
	}
	return s.createSession()
}

// tryLoadSession resumes an existing session. A successful load that omits
// sessionId is still a success: some agents (Devin) return modes and
// configOptions only, and the id we asked to load stays in effect.
func (s *acpSession) tryLoadSession(resumeSessionID string) bool {
	s.replaying.Store(true)
	defer s.replaying.Store(false)
	loadRes, err := s.tr.call(s.ctx, "session/load", map[string]any{
		"sessionId":  resumeSessionID,
		"cwd":        canonicalCwd(s.workDir),
		"mcpServers": []any{},
	})
	if err != nil {
		slog.Warn("acp: session/load failed, starting new session", "error", err)
		return false
	}
	if err := s.absorbSessionResult(loadRes, false); err != nil {
		slog.Warn("acp: session/load parse failed, starting new session", "error", err)
		return false
	}
	return true
}

func (s *acpSession) createSession() error {
	newRes, err := s.tr.call(s.ctx, "session/new", map[string]any{
		"cwd":        canonicalCwd(s.workDir),
		"mcpServers": []any{},
	})
	if err != nil {
		return fmt.Errorf("acp: session/new: %w", err)
	}
	if err := s.absorbSessionResult(newRes, true); err != nil {
		if err == errEmptyACPSessionID {
			return fmt.Errorf("acp: session/new: empty sessionId")
		}
		return fmt.Errorf("acp: parse session/new: %w", err)
	}
	return nil
}

// absorbModes copies a modes block into the session's cache and fans
// it out to the parent agent callbacks (if any). Both the session and
// the agent need the information: the session uses it to validate
// SetLiveMode inputs; the agent uses it to render `/mode` menus in IM.
func (s *acpSession) absorbModes(block *acpModesBlock) {
	if block == nil || len(block.AvailableModes) == 0 {
		return
	}
	s.modesMu.Lock()
	s.availableModes = append(s.availableModes[:0], block.AvailableModes...)
	if block.CurrentModeID != "" {
		s.currentMode = block.CurrentModeID
	}
	s.modesMu.Unlock()
	if s.callbacks != nil {
		s.callbacks.reportModes(*block)
	}
}

func (s *acpSession) setACPSessionID(id string) {
	s.acpSessMu.Lock()
	s.acpSessID = id
	s.acpSessMu.Unlock()
}

func (s *acpSession) currentACPSessionID() string {
	s.acpSessMu.RLock()
	defer s.acpSessMu.RUnlock()
	return s.acpSessID
}

// CurrentMode returns the ACP modeId most recently applied or reported
// for this session. Empty when the server never sent a modes block.
func (s *acpSession) CurrentMode() string {
	s.modesMu.RLock()
	defer s.modesMu.RUnlock()
	return s.currentMode
}

// SetLiveMode applies a permission mode change to the running session
// via `session/set_mode`. Returns true on success, false if the mode
// is unknown / the call errors / the session is closed.
//
// This is the implementation of core.LiveModeSwitcher for ACP
// sessions; the engine invokes it when the user runs `/mode <x>`,
// `/plan`, `/bypass`, etc. while a session is active.
//
// Client-side validation is important because at least one ACP server
// (devin acp in 2026.4.9) silently accepts unknown modeIds without
// any error, so a server-only check would let typos go undetected.
func (s *acpSession) SetLiveMode(mode string) bool {
	if !s.alive.Load() {
		return false
	}
	sid := s.currentACPSessionID()
	if sid == "" {
		return false
	}
	modeID := s.matchAvailableMode(mode)
	if modeID == "" {
		slog.Debug("acp: SetLiveMode rejected unknown mode",
			"mode", mode,
			"session_id", sid,
		)
		return false
	}
	if _, err := s.tr.call(s.ctx, "session/set_mode", map[string]any{
		"sessionId": sid,
		"modeId":    modeID,
	}); err != nil {
		slog.Warn("acp: session/set_mode failed",
			"mode", modeID,
			"session_id", sid,
			"error", err,
		)
		return false
	}
	s.modesMu.Lock()
	s.currentMode = modeID
	s.modesMu.Unlock()
	if s.callbacks != nil {
		// Re-publish current modeId so Agent.GetMode stays in sync.
		s.modesMu.RLock()
		available := append([]acpModeInfo(nil), s.availableModes...)
		s.modesMu.RUnlock()
		s.callbacks.reportModes(acpModesBlock{
			CurrentModeID:  modeID,
			AvailableModes: available,
		})
	}
	slog.Info("acp: live mode applied", "mode", modeID, "session_id", sid)
	return true
}

// matchAvailableMode resolves a user-typed mode string to a known ACP
// modeId. Aliases are applied first, then id and display name are
// compared case-insensitively. Returns "" when the resolved id is not
// in the modes this session advertised, so a typo is not sent.
func (s *acpSession) matchAvailableMode(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	aliased := lookupModeAlias(s.modeAliases, input)
	s.modesMu.RLock()
	modes := append([]acpModeInfo(nil), s.availableModes...)
	s.modesMu.RUnlock()
	if id := matchACPModeID(aliased, modes); id != "" {
		return id
	}
	return matchACPModeID(input, modes)
}

func matchACPModeID(input string, modes []acpModeInfo) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	for _, m := range modes {
		if strings.EqualFold(m.ID, input) || strings.EqualFold(m.Name, input) {
			return m.ID
		}
	}
	return ""
}

func (s *acpSession) onNotification(method string, params json.RawMessage) {
	if notify := strings.TrimSpace(s.compactionNotifyMethod); notify != "" && method == notify {
		s.noteCompaction(params)
		return
	}
	if method != "session/update" {
		// Vendor extensions ("_cognition.ai/...") are not user-visible.
		if strings.HasPrefix(method, "_") {
			slog.Debug("acp: ignoring vendor notification", "method", method)
			return
		}
		slog.Debug("acp: notification", "method", method)
		return
	}
	s.noteSessionUpdate(params)
	s.cacheToolCallInput(params)
	s.maybeAbsorbCurrentModeUpdate(params)
	s.maybeAbsorbConfigOptionUpdate(params)
	s.maybeAbsorbSessionInfoUpdate(params)
	s.maybeAbsorbUsageUpdate(params)
	sid := s.currentACPSessionID()
	slog.Debug("acp: session/update", "session_id", sid, "params", string(params))
	if s.replaying.Load() {
		return
	}
	events := mapSessionUpdate(sid, params)
	if kind, update, _ := peekSessionUpdate(params); kind == "tool_call_update" {
		events = s.refineToolUpdateEvents(update, events)
	}
	for _, ev := range events {
		s.emit(ev)
	}
}

// maybeAbsorbCurrentModeUpdate watches session/update notifications
// for `current_mode_update` (server-driven mode switch, e.g. when the
// user toggles modes via the Windsurf/IDE UI while cc-connect is
// connected). Keeping currentMode in sync here means the IM `/mode`
// indicator reflects the true server state rather than the last
// client-initiated value.
func (s *acpSession) maybeAbsorbCurrentModeUpdate(params json.RawMessage) {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &wrap) != nil || len(wrap.Update) == 0 {
		return
	}
	var head struct {
		Kind          string `json:"sessionUpdate"`
		CurrentModeID string `json:"currentModeId"`
	}
	if json.Unmarshal(wrap.Update, &head) != nil {
		return
	}
	if head.Kind != "current_mode_update" || head.CurrentModeID == "" {
		return
	}
	s.modesMu.Lock()
	s.currentMode = head.CurrentModeID
	available := append([]acpModeInfo(nil), s.availableModes...)
	s.modesMu.Unlock()
	if s.callbacks != nil {
		s.callbacks.reportModes(acpModesBlock{
			CurrentModeID:  head.CurrentModeID,
			AvailableModes: available,
		})
	}
}

// cacheToolCallInput extracts and caches rawInput from tool_call and tool_call_update
// session updates so that handlePermissionRequest can look it up by toolCallId.
// OpenCode ACP bug (#7370): rawInput is empty in tool_call and request_permission,
// but populated in tool_call_update. We cache from both sources.
func (s *acpSession) evictToolInputCacheIfNeededLocked() {
	if len(s.toolInputByID) < toolInputCacheMaxEntries && len(s.toolNameByID) < toolInputCacheMaxEntries {
		return
	}
	target := toolInputCacheMaxEntries / 2
	for k := range s.toolInputByID {
		if len(s.toolInputByID) <= target && len(s.toolNameByID) <= target {
			break
		}
		delete(s.toolInputByID, k)
		delete(s.toolNameByID, k)
		delete(s.toolResultByID, k)
	}
	for k := range s.toolNameByID {
		if len(s.toolNameByID) <= target {
			break
		}
		delete(s.toolNameByID, k)
		delete(s.toolInputByID, k)
		delete(s.toolResultByID, k)
	}
}

func (s *acpSession) cacheToolCallInput(params json.RawMessage) {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &wrap) != nil || len(wrap.Update) == 0 {
		return
	}
	var head struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if json.Unmarshal(wrap.Update, &head) != nil {
		return
	}
	switch head.SessionUpdate {
	case "tool_call", "tool_call_update":
		var tc struct {
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			Kind       string          `json:"kind"`
			RawInput   json.RawMessage `json:"rawInput"`
		}
		if json.Unmarshal(wrap.Update, &tc) != nil || tc.ToolCallID == "" {
			return
		}
		name := strings.TrimSpace(tc.Title)
		if name == "" {
			name = strings.TrimSpace(tc.Kind)
		}
		input := ""
		if len(tc.RawInput) > 0 && string(tc.RawInput) != "null" {
			input = summarizeACPToolInput(tc.Kind, tc.RawInput)
		}
		if name == "" && input == "" {
			return
		}
		s.storeToolCache(tc.ToolCallID, name, input)
		slog.Info("acp: cached tool call", "toolCallId", tc.ToolCallID, "name", name, "input", input, "update", head.SessionUpdate)
	}
}

func (s *acpSession) storeToolCache(id, name, input string) {
	s.toolInputMu.Lock()
	defer s.toolInputMu.Unlock()
	if s.toolInputByID == nil {
		s.toolInputByID = make(map[string]string)
	}
	if s.toolNameByID == nil {
		s.toolNameByID = make(map[string]string)
	}
	s.evictToolInputCacheIfNeededLocked()
	if name != "" {
		s.toolNameByID[id] = name
	}
	if input != "" {
		s.toolInputByID[id] = input
	}
}

func (s *acpSession) cachedTool(id string) (name, input string) {
	s.toolInputMu.Lock()
	defer s.toolInputMu.Unlock()
	return s.toolNameByID[id], s.toolInputByID[id]
}

func (s *acpSession) onServerRequest(method string, id json.RawMessage, params json.RawMessage) {
	switch method {
	case "session/request_permission":
		s.handlePermissionRequest(id, params)
	case "cursor/ask_question", "cursor/create_plan", "cursor/update_todos", "cursor/task", "cursor/generate_image":
		// Cursor CLI extensions — acknowledge so tool flows do not block; IM UX is limited for these.
		slog.Debug("acp: cursor extension request (no-op ack)", "method", method)
		_ = s.tr.respondSuccess(id, map[string]any{})
	default:
		if strings.HasPrefix(method, "cursor/") {
			slog.Debug("acp: unknown cursor extension, ack empty", "method", method)
			_ = s.tr.respondSuccess(id, map[string]any{})
			return
		}
		slog.Info("acp: unhandled server request", "method", method)
		_ = s.tr.respondError(id, -32601, "method not implemented")
	}
}

func (s *acpSession) handlePermissionRequest(id json.RawMessage, params json.RawMessage) {
	var p struct {
		SessionID string `json:"sessionId"`
		ToolCall  struct {
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			Kind       string          `json:"kind"`
			RawInput   json.RawMessage `json:"rawInput"`
			Meta       map[string]any  `json:"_meta"`
		} `json:"toolCall"`
		Options []permissionOption `json:"options"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		_ = s.tr.respondError(id, -32602, "invalid params")
		return
	}
	slog.Debug("acp: permission request raw params", "params", string(params))
	reqKey := jsonIDKey(id)

	s.permMu.Lock()
	s.permByID[reqKey] = permState{RPCID: id, Options: p.Options}
	s.permMu.Unlock()

	rawTool := map[string]any{}
	_ = json.Unmarshal(params, &rawTool)

	// OpenCode ACP bug (#7370): rawInput in request_permission is always {},
	// but tool_call_update (which arrives right after) has the real input.
	// Emit in a goroutine so we don't block the read loop, and wait briefly
	// for tool_call_update to populate the cache. Devin sends no title and
	// puts the shell command in _meta; that does not need the wait.
	metaCmd := editableCommand(p.ToolCall.Meta)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.waitForPermissionCache(p.ToolCall.ToolCallID, p.ToolCall.Title, p.ToolCall.Kind, metaCmd)
		toolName, toolInput := s.permissionDisplay(p.ToolCall.ToolCallID, p.ToolCall.Title, p.ToolCall.Kind, p.ToolCall.RawInput, metaCmd)
		slog.Info("acp: permission request", "request_id", reqKey, "tool", toolName, "input", toolInput)
		s.emit(core.Event{
			Type:         core.EventPermissionRequest,
			RequestID:    reqKey,
			ToolName:     toolName,
			ToolInput:    toolInput,
			ToolInputRaw: rawTool,
			SessionID:    s.currentACPSessionID(),
		})
	}()
}

func editableCommand(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	cmd, _ := meta["cognition.ai/editableCommand"].(string)
	return strings.TrimSpace(cmd)
}

func (s *acpSession) waitForPermissionCache(id, _, _, metaCmd string) {
	// A shell command in _meta is the display input. Other requests may
	// learn rawInput from a tool_call_update that arrives just after.
	if metaCmd != "" {
		return
	}
	for i := 0; i < 10; i++ {
		_, input := s.cachedTool(id)
		if input != "" {
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s *acpSession) permissionDisplay(id, title, kind string, raw json.RawMessage, metaCmd string) (string, string) {
	cachedName, cachedInput := s.cachedTool(id)
	toolName := strings.TrimSpace(title)
	if toolName == "" {
		toolName = strings.TrimSpace(kind)
	}
	if toolName == "" {
		toolName = cachedName
	}
	if toolName == "" && metaCmd != "" {
		toolName = "Shell"
	}
	if toolName == "" {
		toolName = "permission"
	}
	toolInput := cachedInput
	if toolInput == "" {
		toolInput = summarizeACPToolInput(kind, raw)
	}
	if toolInput == "" {
		toolInput = strings.TrimSpace(title)
	}
	if toolInput == "" {
		toolInput = metaCmd
	}
	if toolInput == "" {
		toolInput = id
	}
	return toolName, toolInput
}

func (s *acpSession) emit(ev core.Event) {
	if ev.SessionID == "" {
		ev.SessionID = s.currentACPSessionID()
	}
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
	}
}

func (s *acpSession) Send(prompt string, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("acp: session closed")
	}

	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	prompt, blocks := s.preparePrompt(prompt, messageID, images, files)
	sid := s.currentACPSessionID()
	if sid == "" {
		return fmt.Errorf("acp: no agent session id")
	}
	return s.dispatchPrompt(sid, prompt, blocks)
}

func (s *acpSession) preparePrompt(prompt, messageID string, images []core.ImageAttachment, files []core.FileAttachment) (string, []any) {
	filePaths := core.SaveFilesToDisk(s.workDir, messageID, files)
	prompt = core.AppendFileRefs(prompt, filePaths)
	if len(images) > 0 && !s.promptImages {
		prompt = s.appendImageRefs(prompt, images)
	}
	return prompt, buildPromptBlocks(prompt, images, s.promptImages)
}

func (s *acpSession) dispatchPrompt(sid, prompt string, blocks []any) error {
	params := map[string]any{
		"sessionId": sid,
		"prompt":    blocks,
	}
	slog.Debug("acp: sending session/prompt", "session_id", sid, "prompt_len", len(prompt))
	if s.isCompactPrompt(prompt) {
		s.resetCompaction()
	}
	res, err := s.tr.call(s.ctx, "session/prompt", params)
	if err != nil {
		s.emit(core.Event{Type: core.EventError, Error: err})
		return fmt.Errorf("acp: session/prompt: %w", err)
	}
	slog.Debug("acp: session/prompt response", "session_id", sid, "response_len", len(res))
	inTok, outTok, cacheRead := s.absorbPromptResult(res)
	s.waitForCompaction(prompt)
	// Text was streamed via session/update; engine aggregates EventText.
	s.emit(core.Event{
		Type:                 core.EventResult,
		SessionID:            sid,
		Done:                 true,
		InputTokens:          inTok,
		OutputTokens:         outTok,
		CacheReadInputTokens: cacheRead,
	})
	return nil
}

func (s *acpSession) appendImageRefs(prompt string, images []core.ImageAttachment) string {
	attachDir := filepath.Join(s.workDir, ".cc-connect", "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		slog.Warn("acp: mkdir attachments failed", "error", err)
		return prompt
	}
	var paths []string
	for i, img := range images {
		ext := ".bin"
		switch img.MimeType {
		case "image/jpeg":
			ext = ".jpg"
		case "image/gif":
			ext = ".gif"
		case "image/webp":
			ext = ".webp"
		case "image/png", "":
			ext = ".png"
		}
		fname := fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		fpath := filepath.Join(attachDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
			slog.Error("acp: save image failed", "error", err)
			continue
		}
		paths = append(paths, fpath)
	}
	if len(paths) == 0 {
		return prompt
	}
	if prompt == "" {
		prompt = "User sent image(s)."
	}
	return prompt + "\n\n(Image files saved locally: " + strings.Join(paths, ", ") + ")"
}

func (s *acpSession) RespondPermission(requestID string, result core.PermissionResult) error {
	if !s.alive.Load() {
		return fmt.Errorf("acp: session closed")
	}

	s.permMu.Lock()
	st, ok := s.permByID[requestID]
	if ok {
		delete(s.permByID, requestID)
	}
	s.permMu.Unlock()
	if !ok {
		return fmt.Errorf("acp: unknown permission request %q", requestID)
	}

	allow := strings.EqualFold(result.Behavior, "allow")
	optID := pickPermissionOptionID(allow, st.Options)
	if allow && optID == "" {
		slog.Warn("acp: allow requested but agent sent no options", "request_id", requestID)
		return s.tr.respondError(st.RPCID, -32603, "no permission options from agent")
	}
	res := buildPermissionResult(allow, optID)

	slog.Debug("acp: permission response", "request_id", requestID, "allow", allow, "option_id", optID)
	return s.tr.respondSuccess(st.RPCID, res)
}

func (s *acpSession) Events() <-chan core.Event {
	return s.events
}

func (s *acpSession) CurrentSessionID() string {
	return s.currentACPSessionID()
}

// CancelTurn sends an ACP session/cancel notification to abort the current
// turn while keeping the session alive. The agent should cancel the active
// LLM request and persist state, then wait for the next user message on the
// same connection. This is used by /stop instead of Close().
func (s *acpSession) CancelTurn() error {
	sid := s.currentACPSessionID()
	if sid == "" {
		return fmt.Errorf("acp: no active session to cancel")
	}
	if s.tr == nil {
		return fmt.Errorf("acp: transport not available")
	}
	slog.Debug("acp: cancelling current turn", "session_id", sid)
	return s.tr.sendNotification("session/cancel", map[string]any{
		"sessionId": sid,
	})
}

func (s *acpSession) Alive() bool {
	return s.alive.Load()
}

func (s *acpSession) Close() error {
	s.alive.Store(false)
	s.cancel()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		close(s.events)
	case <-time.After(8 * time.Second):
		slog.Warn("acp: close timed out waiting for I/O loop")
	}
	return nil
}

// summarizeACPToolInput extracts a human-readable summary from ACP tool rawInput.
func summarizeACPToolInput(kind string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return string(raw)
	}
	if len(m) == 0 {
		return ""
	}
	switch strings.ToLower(kind) {
	case "bash", "shell", "terminal", "execute":
		if cmd, ok := m["command"].(string); ok {
			if desc, ok := m["description"].(string); ok && desc != "" {
				return "# " + desc + "\n" + cmd
			}
			return cmd
		}
	case "read", "write", "edit":
		if fp, ok := m["file_path"].(string); ok {
			return fp
		}
		if fp, ok := m["path"].(string); ok {
			return fp
		}
	}
	// Fallback: try extracting command with description before formatting JSON.
	if cmd, ok := m["command"].(string); ok {
		if desc, ok := m["description"].(string); ok && desc != "" {
			return "# " + desc + "\n" + cmd
		}
		return cmd
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return string(b)
}

// refineToolUpdateEvents names tool results from tool_call_update with the
// title cached from the originating tool_call (updates usually omit it, which
// made the raw toolCallId show up as the tool name) and drops a result whose
// output repeats the last one emitted for the same tool call.
func (s *acpSession) refineToolUpdateEvents(update json.RawMessage, events []core.Event) []core.Event {
	var u struct {
		ToolCallID string `json:"toolCallId"`
	}
	if json.Unmarshal(update, &u) != nil || u.ToolCallID == "" {
		return events
	}
	s.toolInputMu.Lock()
	defer s.toolInputMu.Unlock()
	if s.toolResultByID == nil {
		s.toolResultByID = make(map[string]string)
	}
	name := s.toolNameByID[u.ToolCallID]
	out := events[:0]
	for _, ev := range events {
		if ev.Type == core.EventToolResult {
			if name != "" && (ev.ToolName == u.ToolCallID || ev.ToolName == "tool") {
				ev.ToolName = name
			}
			if s.toolResultByID[u.ToolCallID] == ev.Content {
				continue
			}
			s.toolResultByID[u.ToolCallID] = ev.Content
		}
		out = append(out, ev)
	}
	return out
}
