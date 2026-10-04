package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type managedTurn struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	StartedAt   *int64           `json:"startedAt"`
	CompletedAt *int64           `json:"completedAt"`
	ID          string           `json:"id"`
	Status      string           `json:"status"`
	Items       []map[string]any `json:"items"`
}
type managedThread struct {
	ID                   string          `json:"id"`
	Cwd                  string          `json:"cwd"`
	Preview              string          `json:"preview"`
	Source               json.RawMessage `json:"source"`
	CanAcceptDirectInput *bool           `json:"canAcceptDirectInput"`
	UpdatedAt            int64           `json:"updatedAt"`
	Turns                []managedTurn   `json:"turns"`
}
type managedSnapshot struct {
	Thread          managedThread   `json:"thread"`
	Cwd             string          `json:"cwd"`
	Model           string          `json:"model"`
	ModelProvider   string          `json:"modelProvider"`
	ApprovalPolicy  json.RawMessage `json:"approvalPolicy"`
	Sandbox         json.RawMessage `json:"sandbox"`
	ReasoningEffort *string         `json:"reasoningEffort"`
}
type managedRequest struct {
	rawID          json.RawMessage
	method         string
	turn           string
	decisions      []string
	choicePayloads map[string]any
	permissions    any
	questions      []core.UserQuestion
	responding     bool
}

type managedSession struct {
	agent              *managedAgent
	ctx                context.Context
	cancel             context.CancelFunc
	events             chan core.Event // runLoop exclusively emits and closes this channel
	done               chan struct{}
	closeOnce          sync.Once
	opMu               sync.Mutex
	mu                 sync.Mutex
	rpc                *daemonRPC
	thread             string
	turn               string
	connected          bool
	closed             bool
	canInput           bool
	pending            map[string]*managedRequest
	completed          map[string]bool
	usageWarningLevels map[string]int
	usageSnapshots     map[string]managedUsageSnapshot
	noticeKeys         map[string]string
	textStreams        map[string]*managedTextStream
	seenItems          map[string]bool
	finalText          []string
	preamble           string
	decoder            *appServerSession // reuse transport-independent item/usage conversion only
}

func newManagedSession(ctx context.Context, a *managedAgent, id string) (*managedSession, error) {
	lifetime, cancel := context.WithCancel(ctx)
	s := &managedSession{agent: a, ctx: lifetime, cancel: cancel, events: make(chan core.Event, 256), done: make(chan struct{}), pending: map[string]*managedRequest{}, completed: map[string]bool{}, seenItems: map[string]bool{}, canInput: true}
	s.decoder = &appServerSession{ctx: lifetime, events: make(chan core.Event, 64)}
	rpc, err := a.openRPC(lifetime)
	if err != nil {
		cancel()
		return nil, err
	}
	s.rpc = rpc
	var snapshot managedSnapshot
	if id != "" {
		err = rpc.request(lifetime, "thread/resume", map[string]any{"threadId": id}, &snapshot)
	} else {
		err = rpc.request(lifetime, "thread/start", a.newThreadParams(), &snapshot)
	}
	if err == nil {
		err = s.applySnapshot(snapshot, id, rpc)
	}
	if err != nil {
		rpc.close()
		cancel()
		return nil, fmt.Errorf("codex managed thread attach/start: %w", err)
	}
	if id == "" {
		a.mu.RLock()
		s.preamble = buildCodexPromptPreamble(a.systemPrompt, a.appendPrompt)
		a.mu.RUnlock()
	}
	go s.runLoop(rpc)
	return s, nil
}

func (a *managedAgent) newThreadParams() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()
	cwd, _ := filepath.Abs(a.workDir)
	approval, sandbox := appServerModeSettings(a.mode)
	params := map[string]any{"cwd": cwd, "approvalPolicy": approval, "approvalsReviewer": "user", "sandbox": sandbox}
	model := core.GetProviderModel(a.providers, a.activeIdx, a.model)
	if model != "" {
		params["model"] = model
	}
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if name := a.providers[a.activeIdx].Name; name != "" {
			params["modelProvider"] = name
		}
	}
	if a.reasoningEffort != "" {
		params["config"] = map[string]any{"model_reasoning_effort": a.reasoningEffort}
	}
	return params
}

func (s *managedSession) applySnapshot(snapshot managedSnapshot, expected string, rpc *daemonRPC) error {
	id := snapshot.Thread.ID
	cwd := snapshot.Cwd
	if cwd == "" {
		cwd = snapshot.Thread.Cwd
	}
	if id == "" || (expected != "" && id != expected) {
		return fmt.Errorf("snapshot returned a different or empty thread ID")
	}
	if !sameManagedCwd(s.agent.GetWorkDir(), cwd) {
		return fmt.Errorf("thread is outside configured work_dir")
	}
	if managedSubagent(snapshot.Thread.Source) {
		return fmt.Errorf("only root threads can be attached")
	}
	s.mu.Lock()
	previousTurn := s.turn
	s.thread = id
	s.turn = ""
	s.connected = true
	s.rpc = rpc
	s.canInput = snapshot.Thread.CanAcceptDirectInput == nil || *snapshot.Thread.CanAcceptDirectInput
	for _, turn := range snapshot.Thread.Turns {
		if turn.Status == "inProgress" {
			s.turn = turn.ID
		}
	}
	if previousTurn != s.turn {
		s.finalText = nil
	}
	s.mu.Unlock()
	s.decoder.runtimeMu.Lock()
	if s.decoder.workDir == "" {
		s.decoder.workDir = cwd
	}
	s.decoder.model = snapshot.Model
	s.decoder.effort = ""
	if snapshot.ReasoningEffort != nil {
		s.decoder.effort = *snapshot.ReasoningEffort
	}
	s.decoder.runtimeMu.Unlock()
	return nil
}

func (s *managedSession) runLoop(rpc *daemonRPC) {
	defer close(s.done)
	defer close(s.events)
	for {
		for {
			select {
			case <-s.ctx.Done():
				return
			case m, ok := <-rpc.messages:
				if !ok {
					goto disconnected
				}
				if err := s.handleMessage(m); err != nil {
					slog.Warn("codex managed event decode", "method", m.Method, "error", err)
				}
			}
		}
	disconnected:
		if s.ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		s.connected = false
		ids := make([]string, 0, len(s.pending))
		for id := range s.pending {
			ids = append(ids, id)
		}
		s.pending = map[string]*managedRequest{}
		s.mu.Unlock()
		for _, id := range ids {
			s.emit(core.Event{Type: core.EventPermissionResolved, RequestID: id})
		}
		s.emit(core.Event{Type: core.EventRuntimeStatus, Content: "reconnecting"})
		replacement, err := s.reconnect()
		if err != nil {
			s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("codex managed disconnected: %w", err)})
			s.mu.Lock()
			s.closed = true
			s.mu.Unlock()
			return
		}
		rpc = replacement
		s.emit(core.Event{Type: core.EventRuntimeStatus, Content: "connected"})
	}
}

func (s *managedSession) reconnect() (*daemonRPC, error) {
	var last error = fmt.Errorf("reconnection disabled")
	s.mu.Lock()
	expectedTurn := s.turn
	s.mu.Unlock()
	for attempt := 0; attempt < s.agent.daemon.reconnect; attempt++ {
		timer := time.NewTimer(time.Duration(attempt+1) * 300 * time.Millisecond)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return nil, s.ctx.Err()
		case <-timer.C:
		}
		rpc, err := s.agent.openRPC(s.ctx)
		if err != nil {
			last = err
			continue
		}
		var snapshot managedSnapshot
		id := s.CurrentSessionID()
		err = rpc.request(s.ctx, "thread/resume", map[string]any{"threadId": id}, &snapshot)
		if err == nil {
			err = s.applySnapshot(snapshot, id, rpc)
		}
		if err == nil {
			err = s.reconcileReconnect(snapshot, expectedTurn, rpc)
		}
		if err != nil {
			rpc.close()
			s.mu.Lock()
			s.connected = false
			s.mu.Unlock()
			last = err
			continue
		}
		s.mu.Lock()
		s.rpc = rpc
		s.mu.Unlock()
		return rpc, nil
	}
	return nil, last
}

func (s *managedSession) emit(event core.Event) {
	select {
	case s.events <- event:
	case <-s.ctx.Done():
	}
}
func (s *managedSession) Events() <-chan core.Event { return s.events }
func (s *managedSession) CurrentSessionID() string  { s.mu.Lock(); defer s.mu.Unlock(); return s.thread }
func (s *managedSession) RuntimeState() core.AgentRuntimeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return core.AgentRuntimeState{SessionID: s.thread, TurnID: s.turn, Connected: s.connected && !s.closed, CanSteer: s.canInput && s.agent.daemon.steer}
}
func (s *managedSession) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.ctx.Err() == nil
}
func (s *managedSession) GetWorkDir() string         { return s.decoder.GetWorkDir() }
func (s *managedSession) GetModel() string           { return s.decoder.GetModel() }
func (s *managedSession) GetReasoningEffort() string { return s.decoder.GetReasoningEffort() }

func (s *managedSession) ReadRuntimeSettings(ctx context.Context) (core.AgentRuntimeSettings, error) {
	if _, err := s.connection(); err != nil {
		return core.AgentRuntimeSettings{}, err
	}
	rpc, err := s.agent.openRPC(ctx)
	if err != nil {
		return core.AgentRuntimeSettings{}, err
	}
	defer rpc.close()
	id := s.CurrentSessionID()
	var snapshot managedSnapshot
	// Metadata-only resume reports current server settings, including changes
	// by other clients. Never supply configuration overrides or alter turn state.
	if err := rpc.request(ctx, "thread/resume", map[string]any{"threadId": id, "excludeTurns": true}, &snapshot); err != nil {
		return core.AgentRuntimeSettings{}, err
	}
	cwd := snapshot.Cwd
	if cwd == "" {
		cwd = snapshot.Thread.Cwd
	}
	if snapshot.Thread.ID != id || !sameManagedCwd(s.agent.GetWorkDir(), cwd) || managedSubagent(snapshot.Thread.Source) {
		return core.AgentRuntimeSettings{}, fmt.Errorf("codex managed settings returned an out-of-scope thread")
	}
	settings := core.AgentRuntimeSettings{Model: snapshot.Model, Provider: snapshot.ModelProvider}
	if snapshot.ReasoningEffort != nil {
		settings.ReasoningEffort = *snapshot.ReasoningEffort
	}
	if len(snapshot.ApprovalPolicy) > 0 && string(snapshot.ApprovalPolicy) != "null" {
		if json.Unmarshal(snapshot.ApprovalPolicy, &settings.ApprovalPolicy) != nil {
			settings.ApprovalPolicy = string(snapshot.ApprovalPolicy)
		}
	}
	var sandbox struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(snapshot.Sandbox, &sandbox) == nil {
		settings.Sandbox = sandbox.Type
	}
	s.decoder.runtimeMu.Lock()
	s.decoder.model = settings.Model
	s.decoder.effort = settings.ReasoningEffort
	s.decoder.runtimeMu.Unlock()
	return settings, nil
}
func (s *managedSession) GetContextUsage() *core.ContextUsage { return s.decoder.GetContextUsage() }
func (s *managedSession) GetUsage(ctx context.Context) (*core.UsageReport, error) {
	rpc, err := s.connection()
	if err != nil {
		return s.decoder.cachedUsage(), err
	}
	var payload appServerRateLimitsResponse
	if err := rpc.request(ctx, "account/rateLimits/read", map[string]any{}, &payload); err != nil {
		return s.decoder.cachedUsage(), err
	}
	s.decoder.storeUsage(mapAppServerRateLimits(payload))
	return s.decoder.cachedUsage(), nil
}
func (s *managedSession) connection() (*daemonRPC, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.connected || s.closed || s.ctx.Err() != nil {
		return nil, fmt.Errorf("codex managed connection is unavailable")
	}
	return s.rpc, nil
}
func (s *managedSession) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.connected = false
		rpc := s.rpc
		s.mu.Unlock()
		s.cancel()
		rpc.close()
	})
	<-s.done
	return nil
}

func managedInput(text string) []map[string]any {
	return []map[string]any{{"type": "text", "text": text, "text_elements": []any{}}}
}

func (s *managedSession) Send(prompt, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	_, err := s.SendTurn(prompt, messageID, images, files)
	return err
}

func (s *managedSession) SendTurn(prompt, messageID string, images []core.ImageAttachment, files []core.FileAttachment) (string, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	state := s.RuntimeState()
	if state.TurnID != "" {
		return "", fmt.Errorf("codex managed turn is active; use /steer instead: %w", core.ErrAgentTurnBusy)
	}
	if !s.canAcceptInput() {
		return "", fmt.Errorf("codex managed thread does not accept direct input")
	}
	rpc, err := s.connection()
	if err != nil {
		return "", err
	}
	if len(files) > 0 {
		prompt = core.AppendFileRefs(prompt, core.SaveFilesToDisk(s.GetWorkDir(), messageID, files))
	}
	prompt, paths, err := s.decoder.stageImages(prompt, images)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	preamble := s.preamble
	s.mu.Unlock()
	inputs := managedInput(prependCodexPromptPreamble(prompt, preamble))
	for _, path := range paths {
		inputs = append(inputs, map[string]any{"type": "localImage", "path": path})
	}
	// Settings belong to the existing shared thread. Even an explicit config
	// model/mode/provider applies only at thread/start, never to attached turns.
	var result turnStartResponse
	params := map[string]any{"threadId": state.SessionID, "input": inputs}
	if err := rpc.request(s.ctx, "turn/start", params, &result); err != nil {
		return "", err
	}
	if result.Turn.ID == "" {
		return "", fmt.Errorf("codex managed turn/start returned empty turn ID")
	}
	s.mu.Lock()
	s.preamble = ""
	if !s.completed[result.Turn.ID] && s.turn == "" {
		s.turn = result.Turn.ID
	}
	s.mu.Unlock()
	return result.Turn.ID, nil
}
func (s *managedSession) canAcceptInput() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.canInput }
func (s *managedSession) Steer(expected, text string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	state := s.RuntimeState()
	if !state.CanSteer {
		return fmt.Errorf("codex managed steer is disabled or thread does not accept input")
	}
	if expected == "" || expected != state.TurnID || strings.TrimSpace(text) == "" {
		return fmt.Errorf("codex managed steer requires the attached active turn and non-empty text")
	}
	rpc, err := s.connection()
	if err != nil {
		return err
	}
	var response struct {
		TurnID string `json:"turnId"`
	}
	if err := rpc.request(s.ctx, "turn/steer", map[string]any{"threadId": state.SessionID, "expectedTurnId": expected, "input": managedInput(text)}, &response); err != nil {
		return err
	}
	if response.TurnID != expected {
		return fmt.Errorf("codex managed steer returned a different turn ID")
	}
	return nil
}
func (s *managedSession) CancelTurn() error {
	return s.CancelExpectedTurn("")
}

func (s *managedSession) CancelExpectedTurn(expected string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !s.agent.daemon.interrupt {
		return fmt.Errorf("codex managed interrupt is disabled")
	}
	state := s.RuntimeState()
	if expected != "" && state.TurnID != expected {
		return nil // The target ended; never interrupt its replacement.
	}
	if state.TurnID == "" {
		return fmt.Errorf("codex managed has no active turn")
	}
	rpc, err := s.connection()
	if err != nil {
		return err
	}
	return rpc.request(s.ctx, "turn/interrupt", map[string]any{"threadId": state.SessionID, "turnId": state.TurnID}, nil)
}
