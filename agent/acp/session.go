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

	cmd *exec.Cmd
	tr  *transport

	acpSessMu sync.RWMutex
	acpSessID string

	sendMu sync.Mutex

	permMu   sync.Mutex
	permByID map[string]permState

	toolInputMu   sync.Mutex
	toolInputByID map[string]string // toolCallId -> summarized tool input

	// modesMu guards availableModes, currentMode and modeConfigID. Populated
	// on handshake (session/new or session/load) and updated whenever
	// SetLiveMode succeeds or the server announces a mode change.
	// modeConfigID is non-empty when the mode selector came from the
	// configOptions mechanism (switch via session/set_config_option);
	// empty means the `modes` block is used (switch via session/set_mode).
	modesMu        sync.RWMutex
	availableModes []acpModeInfo
	currentMode    string
	modeConfigID   string

	// modelsMu guards availableModels, currentModel and modelConfigID.
	// Populated on handshake and updated when SetLiveModel succeeds or the
	// server announces a model change. modelConfigID is non-empty when the
	// model selector came from configOptions (switch via
	// session/set_config_option); empty means the `models` block is used
	// (switch via session/set_model).
	modelsMu        sync.RWMutex
	availableModels []acpModelInfo
	currentModel    string
	modelConfigID   string

	callbacks sessionCallbacks // may be nil (tests, integration harness)
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
	command         string
	args            []string
	extraEnv        []string
	workDir         string
	resumeSessionID string
	authMethod      string
	initialMode     string           // if non-empty, applied via SetLiveMode after session/new
	callbacks       sessionCallbacks // may be nil
}

func newACPSession(ctx context.Context, cfg acpSessionConfig) (*acpSession, error) {
	absWorkDir, err := filepath.Abs(cfg.workDir)
	if err != nil {
		absWorkDir = cfg.workDir
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	s := &acpSession{
		workDir:       absWorkDir,
		events:        make(chan core.Event, 128),
		ctx:           sessionCtx,
		cancel:        cancel,
		permByID:      make(map[string]permState),
		toolInputByID: make(map[string]string),
		acpSessID:     cfg.resumeSessionID,
		callbacks:     cfg.callbacks,
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

	// Apply the agent-level mode preference now that we have a session
	// id. If set_mode fails (e.g. modeId unknown to this backend) we
	// log and carry on with whatever mode the server defaulted to —
	// the alternative would be to reject the session entirely, which
	// is worse UX for a non-critical control.
	if strings.TrimSpace(cfg.initialMode) != "" {
		if ok := s.SetLiveMode(cfg.initialMode); !ok {
			slog.Warn("acp: initial mode could not be applied",
				"mode", cfg.initialMode,
				"session_id", s.currentACPSessionID(),
			)
		}
	}

	return s, nil
}

// handshake runs initialize → optional authenticate → session/load or
// session/new, and caches any modes the server advertises so
// SetLiveMode / PermissionModes can answer correctly.
func (s *acpSession) handshake(resumeSessionID string, authMethod string) error {
	initParams := map[string]any{
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
	}
	res, err := s.tr.call(s.ctx, "initialize", initParams)
	if err != nil {
		return fmt.Errorf("acp: initialize: %w", err)
	}

	var initOut acpInitializeResult
	if err := json.Unmarshal(res, &initOut); err != nil {
		return fmt.Errorf("acp: parse initialize result: %w", err)
	}
	listSupported := len(initOut.AgentCapabilities.SessionCapabilities.List) > 0
	slog.Debug("acp: initialized",
		"protocol", initOut.ProtocolVersion,
		"load_session", initOut.AgentCapabilities.LoadSession,
		"list_sessions", listSupported,
	)
	if s.callbacks != nil {
		s.callbacks.reportListSupported(listSupported)
	}

	if strings.TrimSpace(authMethod) != "" {
		if _, err := s.tr.call(s.ctx, "authenticate", map[string]any{
			"methodId": authMethod,
		}); err != nil {
			return fmt.Errorf("acp: authenticate (%s): %w", authMethod, err)
		}
		slog.Debug("acp: authenticated", "method_id", authMethod)
	}

	wantResume := resumeSessionID != "" && resumeSessionID != core.ContinueSession
	if wantResume && initOut.AgentCapabilities.LoadSession {
		loadParams := map[string]any{
			"sessionId":  resumeSessionID,
			"cwd":        s.workDir,
			"mcpServers": []any{},
		}
		loadRes, err := s.tr.call(s.ctx, "session/load", loadParams)
		if err != nil {
			slog.Warn("acp: session/load failed, starting new session", "error", err)
		} else {
			var lr struct {
				SessionID     string            `json:"sessionId"`
				Modes         *acpModesBlock    `json:"modes"`
				Models        *acpModelsBlock   `json:"models"`
				ConfigOptions []acpConfigOption `json:"configOptions"`
			}
			if json.Unmarshal(loadRes, &lr) == nil && lr.SessionID != "" {
				s.setACPSessionID(lr.SessionID)
				s.absorbConfigOptions(lr.ConfigOptions)
				s.absorbModes(lr.Modes)
				s.absorbModels(lr.Models)
				return nil
			}
		}
	}

	newParams := map[string]any{
		"cwd":        s.workDir,
		"mcpServers": []any{},
	}
	newRes, err := s.tr.call(s.ctx, "session/new", newParams)
	if err != nil {
		return fmt.Errorf("acp: session/new: %w", err)
	}
	var sn struct {
		SessionID     string            `json:"sessionId"`
		Modes         *acpModesBlock    `json:"modes"`
		Models        *acpModelsBlock   `json:"models"`
		ConfigOptions []acpConfigOption `json:"configOptions"`
	}
	if err := json.Unmarshal(newRes, &sn); err != nil {
		return fmt.Errorf("acp: parse session/new: %w", err)
	}
	if sn.SessionID == "" {
		return fmt.Errorf("acp: session/new: empty sessionId")
	}
	s.setACPSessionID(sn.SessionID)
	// configOptions (newer mechanism) first so it claims the model/mode
	// selectors; the legacy blocks then only fill selectors configOptions
	// did not provide (absorbModes/absorbModels skip when the configId is set).
	s.absorbConfigOptions(sn.ConfigOptions)
	s.absorbModes(sn.Modes)
	s.absorbModels(sn.Models)
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
	// configOptions (the newer mechanism) takes precedence: if a mode
	// selector was already absorbed from configOptions, don't let the
	// legacy `modes` block overwrite it. Order-independent by design.
	if s.modeConfigID != "" {
		s.modesMu.Unlock()
		return
	}
	s.availableModes = append(s.availableModes[:0], block.AvailableModes...)
	if block.CurrentModeID != "" {
		s.currentMode = block.CurrentModeID
	}
	s.modesMu.Unlock()
	if s.callbacks != nil {
		s.callbacks.reportModes(*block)
	}
}

// absorbModels caches the ACP `models` block and reports it to the
// parent agent. It is the model-selector counterpart of absorbModes,
// called on handshake (session/new or session/load) and on
// current_model_update notifications.
func (s *acpSession) absorbModels(block *acpModelsBlock) {
	if block == nil || len(block.AvailableModels) == 0 {
		return
	}
	s.modelsMu.Lock()
	// configOptions (the newer mechanism) takes precedence: if a model
	// selector was already absorbed from configOptions, don't let the
	// legacy `models` block overwrite it. Order-independent by design.
	if s.modelConfigID != "" {
		s.modelsMu.Unlock()
		return
	}
	s.availableModels = append(s.availableModels[:0], block.AvailableModels...)
	if block.CurrentModelID != "" {
		s.currentModel = block.CurrentModelID
	}
	s.modelsMu.Unlock()
	if s.callbacks != nil {
		s.callbacks.reportModels(*block)
	}
	slog.Debug("acp: absorbed models", "count", len(block.AvailableModels), "current", block.CurrentModelID)
}

// absorbConfigOptions handles the newer `configOptions` mechanism (an
// alternative to the top-level `models`/`modes` blocks). It normalises the
// model and mode selectors it finds into the same availableModels /
// availableModes caches, and records the originating configId so live
// switching routes through session/set_config_option.
func (s *acpSession) absorbConfigOptions(options []acpConfigOption) {
	for i := range options {
		opt := options[i]
		switch opt.Category {
		case "model":
			models := flattenModelOptions(opt)
			if len(models) == 0 {
				continue
			}
			s.modelsMu.Lock()
			s.availableModels = models
			if opt.CurrentValue != "" {
				s.currentModel = opt.CurrentValue
			}
			s.modelConfigID = opt.ID
			s.modelsMu.Unlock()
			if s.callbacks != nil {
				s.callbacks.reportModels(acpModelsBlock{CurrentModelID: opt.CurrentValue, AvailableModels: models})
			}
			slog.Debug("acp: absorbed model configOption", "configId", opt.ID, "count", len(models), "current", opt.CurrentValue)
		case "mode":
			modes := flattenModeOptions(opt)
			if len(modes) == 0 {
				continue
			}
			s.modesMu.Lock()
			s.availableModes = modes
			if opt.CurrentValue != "" {
				s.currentMode = opt.CurrentValue
			}
			s.modeConfigID = opt.ID
			s.modesMu.Unlock()
			if s.callbacks != nil {
				s.callbacks.reportModes(acpModesBlock{CurrentModeID: opt.CurrentValue, AvailableModes: modes})
			}
			slog.Debug("acp: absorbed mode configOption", "configId", opt.ID, "count", len(modes), "current", opt.CurrentValue)
		}
	}
}

// flattenModelOptions converts a configOption's option list (flat or
// grouped) into acpModelInfo entries.
func flattenModelOptions(opt acpConfigOption) []acpModelInfo {
	var out []acpModelInfo
	for _, o := range opt.Options {
		if o.Group != "" {
			for _, g := range o.Options {
				out = append(out, acpModelInfo{ModelID: g.Value, Name: g.Name, Description: g.Description})
			}
		} else if o.Value != "" {
			out = append(out, acpModelInfo{ModelID: o.Value, Name: o.Name, Description: o.Description})
		}
	}
	return out
}

// flattenModeOptions converts a configOption's option list (flat or
// grouped) into acpModeInfo entries.
func flattenModeOptions(opt acpConfigOption) []acpModeInfo {
	var out []acpModeInfo
	for _, o := range opt.Options {
		if o.Group != "" {
			for _, g := range o.Options {
				out = append(out, acpModeInfo{ID: g.Value, Name: g.Name, Description: g.Description})
			}
		} else if o.Value != "" {
			out = append(out, acpModeInfo{ID: o.Value, Name: o.Name, Description: o.Description})
		}
	}
	return out
}

// SetLiveModel implements core.LiveModelSwitcher. It applies a model
// change to the running session and re-publishes the new state so
// Agent.GetModel stays in sync. Returns true on success, false if the
// model is unknown / the call errors / the session is closed.
//
// The switch routes through session/set_config_option when the model
// selector came from the configOptions mechanism (modelConfigID set), or
// session/set_model when it came from the `models` block.
func (s *acpSession) SetLiveModel(model string) bool {
	if !s.alive.Load() {
		return false
	}
	sid := s.currentACPSessionID()
	if sid == "" {
		return false
	}
	modelID := s.matchAvailableModel(model)
	if modelID == "" {
		slog.Debug("acp: SetLiveModel rejected unknown model", "model", model, "session_id", sid)
		return false
	}

	s.modelsMu.RLock()
	configID := s.modelConfigID
	s.modelsMu.RUnlock()

	if configID != "" {
		if !s.setConfigOption(sid, configID, modelID) {
			return false
		}
	} else {
		if _, err := s.tr.call(s.ctx, "session/set_model", map[string]any{
			"sessionId": sid,
			"modelId":   modelID,
		}); err != nil {
			slog.Warn("acp: session/set_model failed", "model", modelID, "session_id", sid, "error", err)
			return false
		}
	}

	// Both RPCs return without the full updated state, so optimistically
	// update the local cache and re-publish to keep Agent.GetModel in sync.
	s.modelsMu.Lock()
	s.currentModel = modelID
	available := append([]acpModelInfo(nil), s.availableModels...)
	s.modelsMu.Unlock()
	if s.callbacks != nil {
		s.callbacks.reportModels(acpModelsBlock{
			CurrentModelID:  modelID,
			AvailableModels: available,
		})
	}
	slog.Info("acp: live model applied", "model", modelID, "session_id", sid)
	return true
}

// setConfigOption sends a session/set_config_option RPC. Returns true on
// success. Any full configOptions payload in the response is absorbed to
// keep caches fresh.
func (s *acpSession) setConfigOption(sid, configID, value string) bool {
	res, err := s.tr.call(s.ctx, "session/set_config_option", map[string]any{
		"sessionId": sid,
		"configId":  configID,
		"value":     value,
	})
	if err != nil {
		slog.Warn("acp: session/set_config_option failed", "configId", configID, "value", value, "session_id", sid, "error", err)
		return false
	}
	var resp struct {
		ConfigOptions []acpConfigOption `json:"configOptions"`
	}
	if json.Unmarshal(res, &resp) == nil && len(resp.ConfigOptions) > 0 {
		s.absorbConfigOptions(resp.ConfigOptions)
	}
	return true
}

// matchAvailableModel resolves a user-typed model string to a known ACP
// modelId from the cached availableModels list. Matching is case-
// insensitive on both modelId and display name to accommodate IM input.
// Returns "" if nothing matches or if models are unknown.
func (s *acpSession) matchAvailableModel(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	lower := strings.ToLower(input)
	s.modelsMu.RLock()
	defer s.modelsMu.RUnlock()
	for _, m := range s.availableModels {
		if strings.ToLower(m.ModelID) == lower || strings.ToLower(m.Name) == lower {
			return m.ModelID
		}
	}
	return ""
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

// SetLiveMode applies a permission mode change to the running session.
// The switch routes through session/set_config_option when the mode
// selector came from the configOptions mechanism (modeConfigID set), or
// session/set_mode when it came from the `modes` block. Returns true on
// success, false if the mode is unknown / the call errors / the session
// is closed.
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

	s.modesMu.RLock()
	configID := s.modeConfigID
	s.modesMu.RUnlock()

	if configID != "" {
		if !s.setConfigOption(sid, configID, modeID) {
			return false
		}
	} else {
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
// modeId from the cached availableModes list. Matching is case-
// insensitive on both id and display name to accommodate IM input.
// Returns "" if nothing matches or if modes are unknown (first session
// hasn't handshaked yet).
func (s *acpSession) matchAvailableMode(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	lower := strings.ToLower(input)
	s.modesMu.RLock()
	defer s.modesMu.RUnlock()
	for _, m := range s.availableModes {
		if strings.ToLower(m.ID) == lower || strings.ToLower(m.Name) == lower {
			return m.ID
		}
	}
	return ""
}

func (s *acpSession) onNotification(method string, params json.RawMessage) {
	if method != "session/update" {
		slog.Debug("acp: notification", "method", method)
		return
	}
	s.cacheToolCallInput(params)
	s.maybeAbsorbCurrentModeUpdate(params)
	s.maybeAbsorbCurrentModelUpdate(params)
	s.maybeAbsorbConfigOptionUpdate(params)
	sid := s.currentACPSessionID()
	// Debug log to capture raw session/update JSON for troubleshooting vendor compatibility
	slog.Debug("acp: session/update", "session_id", sid, "params", string(params))
	for _, ev := range mapSessionUpdate(sid, params) {
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

// maybeAbsorbCurrentModelUpdate watches session/update notifications for
// `current_model_update` (server-driven model switch, e.g. when the
// agent changes model during execution or the user switches via another
// client). Mirrors maybeAbsorbCurrentModeUpdate.
func (s *acpSession) maybeAbsorbCurrentModelUpdate(params json.RawMessage) {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &wrap) != nil || len(wrap.Update) == 0 {
		return
	}
	var head struct {
		Kind           string `json:"sessionUpdate"`
		CurrentModelID string `json:"currentModelId"`
	}
	if json.Unmarshal(wrap.Update, &head) != nil {
		return
	}
	if head.Kind != "current_model_update" || head.CurrentModelID == "" {
		return
	}
	s.modelsMu.Lock()
	s.currentModel = head.CurrentModelID
	available := append([]acpModelInfo(nil), s.availableModels...)
	s.modelsMu.Unlock()
	if s.callbacks != nil {
		s.callbacks.reportModels(acpModelsBlock{
			CurrentModelID:  head.CurrentModelID,
			AvailableModels: available,
		})
	}
}

// maybeAbsorbConfigOptionUpdate watches session/update notifications for
// `config_option_update` (the configOptions-mechanism counterpart of
// current_model_update / current_mode_update). It re-absorbs the full
// configOptions payload so model/mode caches stay in sync when the agent
// changes a selector during execution.
func (s *acpSession) maybeAbsorbConfigOptionUpdate(params json.RawMessage) {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &wrap) != nil || len(wrap.Update) == 0 {
		return
	}
	var head struct {
		Kind          string            `json:"sessionUpdate"`
		ConfigOptions []acpConfigOption `json:"configOptions"`
	}
	if json.Unmarshal(wrap.Update, &head) != nil {
		return
	}
	if head.Kind != "config_option_update" || len(head.ConfigOptions) == 0 {
		return
	}
	s.absorbConfigOptions(head.ConfigOptions)
}

// cacheToolCallInput extracts and caches rawInput from tool_call and tool_call_update
// session updates so that handlePermissionRequest can look it up by toolCallId.
// OpenCode ACP bug (#7370): rawInput is empty in tool_call and request_permission,
// but populated in tool_call_update. We cache from both sources.
func (s *acpSession) evictToolInputCacheIfNeededLocked() {
	if len(s.toolInputByID) < toolInputCacheMaxEntries {
		return
	}
	target := toolInputCacheMaxEntries / 2
	for k := range s.toolInputByID {
		if len(s.toolInputByID) <= target {
			break
		}
		delete(s.toolInputByID, k)
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
	case "tool_call":
		var tc struct {
			ToolCallID string          `json:"toolCallId"`
			Kind       string          `json:"kind"`
			RawInput   json.RawMessage `json:"rawInput"`
		}
		if json.Unmarshal(wrap.Update, &tc) != nil || tc.ToolCallID == "" || len(tc.RawInput) == 0 {
			return
		}
		s.toolInputMu.Lock()
		s.evictToolInputCacheIfNeededLocked()
		input := summarizeACPToolInput(tc.Kind, tc.RawInput)
		s.toolInputByID[tc.ToolCallID] = input
		s.toolInputMu.Unlock()
		slog.Info("acp: cached tool_call input", "toolCallId", tc.ToolCallID, "kind", tc.Kind, "input", input)
	case "tool_call_update":
		var tc struct {
			ToolCallID string          `json:"toolCallId"`
			RawInput   json.RawMessage `json:"rawInput"`
		}
		if json.Unmarshal(wrap.Update, &tc) != nil || tc.ToolCallID == "" || len(tc.RawInput) == 0 {
			return
		}
		input := summarizeACPToolInput("", tc.RawInput)
		if input == "" {
			return
		}
		s.toolInputMu.Lock()
		s.evictToolInputCacheIfNeededLocked()
		s.toolInputByID[tc.ToolCallID] = input
		s.toolInputMu.Unlock()
		slog.Info("acp: cached tool_call_update input", "toolCallId", tc.ToolCallID, "input", input)
	}
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
		} `json:"toolCall"`
		Options []permissionOption `json:"options"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		_ = s.tr.respondError(id, -32602, "invalid params")
		return
	}
	slog.Debug("acp: permission request raw params", "params", string(params))
	reqKey := jsonIDKey(id)
	toolName := p.ToolCall.Title
	if toolName == "" {
		toolName = p.ToolCall.Kind
	}
	if toolName == "" {
		toolName = "permission"
	}

	s.permMu.Lock()
	s.permByID[reqKey] = permState{RPCID: id, Options: p.Options}
	s.permMu.Unlock()

	rawTool := map[string]any{}
	_ = json.Unmarshal(params, &rawTool)

	// OpenCode ACP bug (#7370): rawInput in request_permission is always {},
	// but tool_call_update (which arrives right after) has the real input.
	// Emit in a goroutine so we don't block the read loop, and wait briefly
	// for tool_call_update to populate the cache.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for i := 0; i < 10; i++ {
			s.toolInputMu.Lock()
			toolInput := s.toolInputByID[p.ToolCall.ToolCallID]
			s.toolInputMu.Unlock()
			if toolInput != "" {
				break
			}
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
		s.toolInputMu.Lock()
		toolInput := s.toolInputByID[p.ToolCall.ToolCallID]
		s.toolInputMu.Unlock()
		if toolInput == "" {
			toolInput = summarizeACPToolInput(p.ToolCall.Kind, p.ToolCall.RawInput)
		}
		if toolInput == "" {
			toolInput = p.ToolCall.Title
		}
		if toolInput == "" {
			toolInput = p.ToolCall.ToolCallID
		}

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

func (s *acpSession) emit(ev core.Event) {
	if ev.SessionID == "" {
		ev.SessionID = s.currentACPSessionID()
	}
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
	}
}

func (s *acpSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("acp: session closed")
	}

	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	filePaths := core.SaveFilesToDisk(s.workDir, files)
	prompt = core.AppendFileRefs(prompt, filePaths)
	if len(images) > 0 {
		prompt = s.appendImageRefs(prompt, images)
	}

	sid := s.currentACPSessionID()
	if sid == "" {
		return fmt.Errorf("acp: no agent session id")
	}

	promptBlocks := []any{
		map[string]any{"type": "text", "text": prompt},
	}
	params := map[string]any{
		"sessionId": sid,
		"prompt":    promptBlocks,
	}

	slog.Debug("acp: sending session/prompt", "session_id", sid, "prompt_len", len(prompt))
	res, err := s.tr.call(s.ctx, "session/prompt", params)
	if err != nil {
		s.emit(core.Event{Type: core.EventError, Error: err})
		return fmt.Errorf("acp: session/prompt: %w", err)
	}
	slog.Debug("acp: session/prompt response", "session_id", sid, "response_len", len(res), "response", string(res))

	// Text was streamed via session/update; engine aggregates EventText.
	s.emit(core.Event{
		Type:      core.EventResult,
		SessionID: sid,
		Done:      true,
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
