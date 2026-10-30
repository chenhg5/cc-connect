package dsh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// dshSession is one cc-connect session backed by a dsh session on the server.
// It holds one aggregated WebSocket event stream (filtered to this session's
// id) and translates dsh session events into core.Event values.
type dshSession struct {
	client    *rpcClient
	workDir   string
	sessionID string
	timeout   time.Duration // per-turn watchdog (0 = disabled)

	ctx    context.Context
	cancel context.CancelFunc

	events     chan core.Event
	emitMu     sync.RWMutex // excludes channel close from every event producer
	alive      atomic.Bool
	closeOnce  sync.Once
	readerDone chan struct{} // closed when the mux reader goroutine exits

	// mux reader lifecycle
	frames <-chan muxFrame

	// pending server interactions, keyed by the frame's rpcId
	pendingMu        sync.Mutex
	respondMu        sync.Mutex // serialize retries until the server acknowledges
	pendingApprovals map[string]pendingApproval
	pendingQuestions map[string]pendingQuestion

	// turn state
	commandCancel     context.CancelFunc // guarded by turnMu
	promptCancel      context.CancelFunc // interrupts a stalled submission receipt
	cancellation      *turnCancellation  // blocks new submissions until cancellation completes
	turnOpMu          sync.Mutex         // serializes prompt submission and cancellation RPCs
	turnMu            sync.Mutex
	currentTurn       int    // latest native turn, or expected next turn before turn/start
	nativeTurn        int    // latest turn confirmed by a native start/end or snapshot
	watchdogTurn      int    // native turn owning turnDone
	watchdogRPCID     string // native user/message source binding for the latest Send
	watchdogEchoed    bool   // matching native echo permits completion after reordered start
	watchdogAfterTurn int    // native turn observed before this prompt was submitted
	turnUncertain     bool   // an unsuccessful receipt cannot prove whether its prompt ran
	lastUsage         *tokenUsage
	toolNames         map[string]string   // callId → tool name (for tool/result)
	seenCalls         map[string]struct{} // callIds already surfaced as EventToolUse
	turnDone          chan struct{}       // closed on completion/cancellation; fresh per Send()

	// delta aggregation: dsh streams token-level chunks; coalesce them into
	// engine-sized EventText/EventThinking pieces so platform delivery is not
	// word-by-word.
	textAgg  *chunkAggregator
	thinkAgg *chunkAggregator

	// first-send env bootstrap (CC_PROJECT / CC_SESSION_KEY for cc-connect CLI)
	envBlock    string
	envInjected bool // guarded by turnOpMu
	modeMu      sync.Mutex
}

type turnCancellation struct {
	done chan struct{}
	err  error
}

type pendingApproval struct {
	sessionID  string
	approvalID string
}

type pendingQuestion struct {
	sessionID string
	items     []wireQuestion
}

// newDshSession creates or resumes a dsh session and attaches the event stream.
func newDshSession(ctx context.Context, client *rpcClient, workDir, sessionID, agentPreset, model, effort string, timeout time.Duration, sessionEnv []string) (*dshSession, error) {
	// Create (fresh) or resume (preallocated id + same cwd → same session).
	payload := map[string]any{"cwd": workDir}
	if sessionID != "" {
		payload["sessionId"] = sessionID
	}
	if agentPreset != "" {
		payload["agentPreset"] = agentPreset
	}
	var created struct {
		SessionID string `json:"sessionId"`
	}
	if err := client.call(ctx, "session.create", payload, &created); err != nil {
		return nil, fmt.Errorf("dsh: create session: %w", err)
	}
	if created.SessionID == "" {
		return nil, fmt.Errorf("dsh: session.create returned empty session id")
	}
	slog.Info("dsh: session ready", "session_id", created.SessionID, "cwd", workDir, "resumed", sessionID != "")

	// Apply explicit overrides before accepting prompts; selection failures
	// must not silently run a different model.
	if model != "" || effort != "" {
		if err := applyModel(ctx, client, created.SessionID, model, effort); err != nil {
			return nil, err
		}
	}

	sessCtx, cancel := context.WithCancel(ctx)
	s := &dshSession{
		client:           client,
		workDir:          workDir,
		sessionID:        created.SessionID,
		timeout:          timeout,
		ctx:              sessCtx,
		cancel:           cancel,
		events:           make(chan core.Event, 1024),
		readerDone:       make(chan struct{}),
		pendingApprovals: make(map[string]pendingApproval),
		pendingQuestions: make(map[string]pendingQuestion),
		toolNames:        make(map[string]string),
		seenCalls:        make(map[string]struct{}),
	}
	s.textAgg = newChunkAggregator(40, 500*time.Millisecond, func(text string) {
		s.emit(core.Event{Type: core.EventText, Content: text})
	})
	s.thinkAgg = newChunkAggregator(40, 500*time.Millisecond, func(text string) {
		s.emit(core.Event{Type: core.EventThinking, Content: text})
	})
	s.alive.Store(true)
	if block := buildEnvBlock(sessionEnv); block != "" {
		s.envBlock = block
	}

	// Open the aggregated stream; the reader filters to this session and
	// protects critical frames (turn/end, approvals, questions) from loss.
	frames, initialTurn, err := client.openMux(sessCtx, created.SessionID)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dsh: open event stream: %w", err)
	}
	s.frames = frames
	s.currentTurn = initialTurn
	s.nativeTurn = initialTurn
	go s.readLoop()
	return s, nil
}

// buildEnvBlock renders the per-session environment injected into the first
// user message so the model can invoke the cc-connect CLI (cron, timer,
// relay) with the right project/session context. The dsh server process does
// not carry these variables, so they must travel in-band.
func buildEnvBlock(env []string) string {
	if len(env) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("[cc-connect session environment]\n")
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			continue // PATH is meaningless to a remote server process
		}
		sb.WriteString(kv)
		sb.WriteString("\n")
	}
	sb.WriteString("When invoking the cc-connect CLI (cron/timer/relay), use these values or the CC_* environment variables they define.\n")
	return sb.String()
}

// ── core.AgentSession ───────────────────────────────────────────

func (s *dshSession) Send(prompt string, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("dsh: session is closed")
	}

	if s.client.token != "" && len(images) == 0 && len(files) == 0 && strings.HasPrefix(strings.TrimSpace(prompt), "/") {
		if err := s.sendNativeCommand(prompt); !errors.Is(err, core.ErrNotSupported) {
			return err
		}
	}
	content := []map[string]any{{"type": "text", "text": prompt}}

	// Images ride as base64 parts; files land on disk and are referenced.
	for _, img := range images {
		mediaType := img.MimeType
		switch mediaType {
		case "image/png", "image/jpeg", "image/webp", "image/gif":
		default:
			mediaType = "image/png"
		}
		content = append(content, map[string]any{
			"type":      "image",
			"mediaType": mediaType,
			"data":      base64Encode(img.Data),
			"name":      img.FileName,
		})
	}
	if len(files) > 0 {
		paths := core.SaveFilesToDisk(s.workDir, messageID, files)
		if len(paths) > 0 {
			content[0]["text"] = content[0]["text"].(string) + "\n\n(Attached files saved locally, please read them: " + strings.Join(paths, ", ") + ")"
		}
	}

	// Platform message IDs can repeat on retries; every submission needs its own
	// transport identity so an old echo cannot claim the new watchdog.
	rpcID := mintRPCID()
	if err := s.lockSubmission(); err != nil {
		return err
	}
	defer s.turnOpMu.Unlock()
	// Retire the previous watchdog before a new prompt can reach the server.
	turnDone := make(chan struct{})
	s.stopWatchdogLocked()
	s.turnDone = turnDone
	s.currentTurn++
	s.watchdogTurn = s.currentTurn
	if s.turnUncertain || s.client.token != "" {
		s.watchdogTurn = 0 // wait for the native echo of this exact RPC
	}
	s.watchdogRPCID = rpcID
	s.watchdogAfterTurn = s.nativeTurn
	promptCtx, cancelPrompt := context.WithCancel(s.ctx)
	promptCtx = context.WithValue(promptCtx, promptRPCIDKey{}, rpcID)
	s.promptCancel = cancelPrompt
	s.turnMu.Unlock()
	defer func() {
		cancelPrompt()
		s.turnMu.Lock()
		s.promptCancel = nil // turnOpMu still excludes the next submission
		s.turnMu.Unlock()
	}()
	if s.timeout > 0 {
		go s.turnWatchdog(turnDone)
	}

	// Serialize bootstrap selection with submission. A failed receipt leaves
	// it eligible for the next Send, including one already waiting for the lock.
	if s.envBlock != "" && !s.envInjected {
		content[0]["text"] = s.envBlock + "\n" + content[0]["text"].(string)
	}

	payload := map[string]any{"sessionId": s.sessionID, "mode": "queue", "content": content}
	if s.client.token != "" {
		// Keep both protocol ids aligned: native versions may surface either
		// the envelope rpcId or requestId in user/message.source.rpcId.
		payload["requestId"] = rpcID
	}
	if err := s.client.call(promptCtx, "session.prompt", payload, nil); err != nil {
		s.turnMu.Lock()
		// Even a non-2xx receipt can follow backend acceptance. The next
		// watchdog must bind to its RPC echo, not a guessed turn number.
		s.turnUncertain = true
		if s.turnDone == turnDone {
			s.stopWatchdogLocked()
		}
		s.turnMu.Unlock()
		return fmt.Errorf("dsh: prompt: %w", err)
	}
	s.envInjected = true

	return nil
}

// Return with turnOpMu and turnMu held after earlier cancellations complete.
func (s *dshSession) lockSubmission() error {
	for {
		s.turnOpMu.Lock()
		s.turnMu.Lock()
		cancellation := s.cancellation
		if cancellation == nil {
			return nil
		}
		s.turnMu.Unlock()
		s.turnOpMu.Unlock()
		select {
		case <-cancellation.done:
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
}

// turnWatchdog emits an error and cancels the turn when it runs past timeout.
func (s *dshSession) turnWatchdog(turnDone chan struct{}) {
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	select {
	case <-turnDone:
	case <-s.ctx.Done():
	case <-timer.C:
		cancelled, err := s.cancelTurn(turnDone)
		if !cancelled {
			return
		}
		slog.Warn("dsh: turn timed out, cancelling", "session_id", s.sessionID, "timeout", s.timeout)
		if err != nil {
			slog.Warn("dsh: cancel after timeout failed", "error", err)
		}
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("dsh: turn timed out after %s", s.timeout)})
	}
}

// Caller holds turnMu. Closing the captured channel disarms exactly its timer.
func (s *dshSession) stopWatchdogLocked() {
	if s.turnDone != nil {
		close(s.turnDone)
		s.turnDone = nil
	}
	s.watchdogTurn = 0
	s.watchdogEchoed = false
}

func (s *dshSession) RespondPermission(requestID string, result core.PermissionResult) error {
	s.respondMu.Lock()
	defer s.respondMu.Unlock()
	s.pendingMu.Lock()
	approval, isApproval := s.pendingApprovals[requestID]
	question, isQuestion := s.pendingQuestions[requestID]
	s.pendingMu.Unlock()

	switch {
	case isApproval:
		outcome := "rejected"
		if result.Behavior == "allow" {
			outcome = "allowed-once"
		}
		err := s.client.respond(s.ctx, requestID, map[string]any{
			"sessionId":  approval.sessionID,
			"approvalId": approval.approvalID,
			"outcome":    outcome,
		})
		if err != nil {
			return fmt.Errorf("dsh: approval response: %w", err)
		}
		s.pendingMu.Lock()
		delete(s.pendingApprovals, requestID)
		s.pendingMu.Unlock()
		slog.Info("dsh: approval answered", "approval_id", approval.approvalID, "outcome", outcome)
		return nil

	case isQuestion:
		answers := buildQuestionAnswers(question, result)
		err := s.client.respond(s.ctx, requestID, map[string]any{
			"sessionId": question.sessionID,
			"answer":    map[string]any{"answers": answers},
		})
		if err != nil {
			return fmt.Errorf("dsh: question response: %w", err)
		}
		s.pendingMu.Lock()
		delete(s.pendingQuestions, requestID)
		s.pendingMu.Unlock()
		slog.Info("dsh: questions answered", "count", len(answers))
		return nil

	default:
		return fmt.Errorf("dsh: no pending request %q", requestID)
	}
}

// buildQuestionAnswers maps the engine's answers map (question text → answer
// text) back to dsh's AskUserQuestionAnswerItem format (question id →
// selected option labels / custom text).
func buildQuestionAnswers(q pendingQuestion, result core.PermissionResult) []map[string]any {
	rawAnswers, _ := result.UpdatedInput["answers"].(map[string]any)
	var out []map[string]any
	for _, item := range q.items {
		ansText := ""
		if rawAnswers != nil {
			if v, ok := rawAnswers[item.Question].(string); ok {
				ansText = v
			}
		}
		if ansText == "" {
			continue
		}
		entry := map[string]any{"id": item.ID, "selected": []string{}}
		if selected, custom := splitAnswer(ansText, item); len(selected) > 0 {
			entry["selected"] = selected
			if custom != "" {
				entry["custom"] = custom
			}
		} else {
			entry["custom"] = ansText
		}
		out = append(out, entry)
	}
	if len(out) == 0 {
		// Fallback: answer everything with the raw text so the tool unblocks.
		for _, item := range q.items {
			out = append(out, map[string]any{"id": item.ID, "custom": "", "selected": []string{}})
		}
	}
	return out
}

// splitAnswer splits a (possibly multi-select) answer into dsh selected
// labels and an optional custom remainder. An answer composed entirely of
// option labels yields selected=[labels]; anything else is free text and is
// reported as custom (with any leading label parts kept in selected).
func splitAnswer(text string, q wireQuestion) (selected []string, custom string) {
	labels := make(map[string]bool)
	for _, o := range q.Options {
		labels[o.Label] = true
	}
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '，' || r == '、' })
	var nonLabel []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if labels[p] {
			selected = append(selected, p)
		} else {
			nonLabel = append(nonLabel, p)
		}
	}
	if len(nonLabel) > 0 {
		custom = strings.TrimSpace(strings.Join(nonLabel, ", "))
	}
	return selected, custom
}

func (s *dshSession) CancelTurn() error {
	_, err := s.cancelTurn(nil)
	return err
}

// Cancel the receipt before waiting for RPC ordering; otherwise a server that
// accepts a prompt but stalls its response could also prevent /stop forever.
// A non-nil expected channel limits a watchdog to its own turn.
func (s *dshSession) cancelTurn(expected chan struct{}) (bool, error) {
	s.turnMu.Lock()
	if expected != nil && s.turnDone != expected {
		s.turnMu.Unlock()
		return false, nil
	}
	if pending := s.cancellation; pending != nil {
		s.turnMu.Unlock()
		select {
		case <-pending.done:
			return false, pending.err
		case <-s.ctx.Done():
			return false, s.ctx.Err()
		}
	}
	pending := &turnCancellation{done: make(chan struct{})}
	s.cancellation = pending
	if s.promptCancel != nil {
		s.promptCancel()
	}
	s.stopWatchdogLocked()
	s.turnMu.Unlock()
	s.turnOpMu.Lock()
	defer s.turnOpMu.Unlock()
	defer func() {
		s.turnMu.Lock()
		s.cancellation = nil
		close(pending.done)
		s.turnMu.Unlock()
	}()
	s.turnMu.Lock()
	commandCancel := s.commandCancel
	s.stopWatchdogLocked()
	s.turnMu.Unlock()
	if commandCancel != nil {
		commandCancel()
		return true, nil
	}
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	if err := s.client.call(ctx, "session.cancel", map[string]any{"sessionId": s.sessionID}, nil); err != nil {
		pending.err = fmt.Errorf("dsh: cancel turn: %w", err)
	}
	return true, pending.err
}

func (s *dshSession) Events() <-chan core.Event {
	return s.events
}

// Remote snapshots omit completed history and replay live pending interactions.
func (s *dshSession) PreserveStartupEvents() bool { return s.client.token != "" }

func (s *dshSession) CurrentSessionID() string {
	return s.sessionID
}

func (s *dshSession) Alive() bool {
	return s.alive.Load()
}

// SetLiveMode applies only to this session, and reports success only after
// the server accepts the command. Never change the shared server defaults.
func (s *dshSession) SetLiveMode(mode string) bool {
	if err := s.setMode(mode); err != nil {
		slog.Warn("dsh: session mode change failed", "session_id", s.sessionID, "error", err)
		return false
	}
	return true
}

func (s *dshSession) setMode(mode string) error {
	s.modeMu.Lock()
	defer s.modeMu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	var out struct {
		Matched bool `json:"matched"`
	}
	if err := s.client.call(ctx, "command.execute", map[string]any{
		"sessionId": s.sessionID,
		"line":      "/permission " + modePreset(normalizeMode(mode)),
	}, &out); err != nil {
		return fmt.Errorf("dsh: apply session permission mode: %w", err)
	}
	if !out.Matched {
		return fmt.Errorf("dsh: session permission command is unavailable")
	}
	return nil
}

func (s *dshSession) Close() error {
	s.closeOnce.Do(func() {
		s.alive.Store(false)
		// Unblock producers before waiting for aggregation or socket readers.
		s.cancel()
		if s.client.sessionOwned {
			s.client.closeRemote()
		}
		s.textAgg.Cancel()
		s.thinkAgg.Cancel()
		// Wait for the mux reader to finish (the stream drops on cancel).
		<-s.readerDone
		s.emitMu.Lock()
		close(s.events)
		s.emitMu.Unlock()
	})
	return nil
}

// ── mux reader ──────────────────────────────────────────────────

func (s *dshSession) readLoop() {
	defer close(s.readerDone)
	defer s.emitStreamError()
	for frame := range s.frames {
		if frame.Type != "stream/error" && frame.SessionID != s.sessionID {
			continue
		}
		s.handleFrame(frame)
	}
	// Stream ended: mark the session dead so the engine respawns on the next
	// message instead of silently sending prompts nowhere.
	s.alive.Store(false)
}

// emitStreamError notifies the engine that the event stream dropped.
func (s *dshSession) emitStreamError() {
	if s.ctx.Err() != nil {
		return // intentional close
	}
	s.alive.Store(false)
	s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("dsh: event stream disconnected (is the dsh web server still running?)")})
}

func (s *dshSession) handleFrame(f muxFrame) {
	switch f.Type {
	case "session/event":
		s.handleSessionEvent(f.Event)
	case "approval/requested":
		s.handleApprovalRequested(f)
	case "question/requested":
		s.handleQuestionRequested(f)
	case "interaction/cancelled":
		s.pendingMu.Lock()
		delete(s.pendingApprovals, f.RPCID)
		delete(s.pendingQuestions, f.RPCID)
		s.pendingMu.Unlock()
	case "approval/resolved", "question/resolved":
		// Nothing to do — the pending entry was already consumed by the
		// response, and stale entries expire with the session.
	case "stream/error":
		msg := "unknown"
		if f.Error != nil {
			msg = f.Error.Message
		}
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("dsh: stream error: %s", msg)})
	default:
		// session/subscribed, session/queue, session/projection, ... — ignored.
	}
}

func (s *dshSession) handleSessionEvent(raw json.RawMessage) {
	var ev sessionEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		slog.Debug("dsh: unparseable session event", "error", err)
		return
	}
	var data sessionEventData
	_ = json.Unmarshal(ev.Data, &data)

	switch ev.Type {
	case "turn/start":
		s.turnMu.Lock()
		if data.Turn > s.nativeTurn {
			s.nativeTurn = data.Turn
		}
		if data.Turn > s.currentTurn {
			s.currentTurn = data.Turn
		}
		if s.turnDone != nil && s.client.token == "" && !s.turnUncertain && data.Turn >= s.watchdogTurn {
			s.watchdogTurn = data.Turn
		}
		s.lastUsage = nil
		s.toolNames = make(map[string]string)
		s.seenCalls = make(map[string]struct{})
		s.turnMu.Unlock()
		// Deliver anything left over from a previous turn before the new one.
		s.textAgg.Flush()
		s.thinkAgg.Flush()

	case "user/message":
		// Native echoes carry the originating HTTP RPC id even if its receipt
		// failed. This binds the latest watchdog without assuming acceptance.
		s.turnMu.Lock()
		if data.Source != nil && data.Source.RPCID == s.watchdogRPCID && s.turnDone != nil {
			// DSH 0.2.1-rc.2 emits turn/start before this echo. Also tolerate
			// an early queued echo: it must not bind to a turn already
			// observed before Send, and a later turn/end can complete it.
			s.watchdogTurn = max(s.nativeTurn, s.watchdogAfterTurn+1)
			if !s.turnUncertain {
				// Accepted queued submissions reserve successive native turns,
				// including when neither prompt has emitted turn/start yet.
				s.watchdogTurn = max(s.watchdogTurn, s.currentTurn)
			}
			s.watchdogEchoed = true
			if s.turnUncertain {
				// A rejected receipt can leave a speculative reservation ahead
				// of the server. Reconcile it only after this RPC's own echo;
				// otherwise later successful prompts inherit the wrong number.
				s.currentTurn = s.watchdogTurn
			} else {
				s.currentTurn = max(s.currentTurn, s.nativeTurn)
			}
			s.turnUncertain = false
		}
		s.turnMu.Unlock()

	case "assistant/chunk":
		s.handleChunk(data.Chunk)

	case "assistant/message":
		// The assembled message duplicates the chunk stream; only the usage
		// accounting is new information.
		if data.Usage != nil {
			s.turnMu.Lock()
			s.lastUsage = data.Usage
			s.turnMu.Unlock()
		}

	case "tool/call":
		s.turnMu.Lock()
		s.toolNames[data.CallID] = data.Name
		_, dup := s.seenCalls[data.CallID]
		s.seenCalls[data.CallID] = struct{}{}
		s.turnMu.Unlock()
		s.textAgg.Flush()
		s.thinkAgg.Flush()
		if dup {
			return // already surfaced from tool-call-delta
		}
		s.emit(core.Event{
			Type:      core.EventToolUse,
			ToolName:  data.Name,
			ToolInput: prettyToolArgs(data.Args),
		})

	case "tool/result":
		s.turnMu.Lock()
		name := s.toolNames[data.CallID]
		s.turnMu.Unlock()
		text, isError := toolResultText(data.Message)
		status := "completed"
		if data.Error != nil || isError {
			status = "failed"
		}
		if data.Error != nil && text == "" {
			text = data.Error.Name
			if data.Error.Message != "" {
				text = data.Error.Message
			}
		}
		s.emit(core.Event{
			Type:       core.EventToolResult,
			ToolName:   name,
			Content:    truncate(text, 500),
			ToolStatus: status,
		})

	case "turn/end":
		s.finishTurn(data)

	default:
		slog.Debug("dsh: unhandled session event", "type", ev.Type)
	}
}

func (s *dshSession) handleChunk(chunk *streamChunk) {
	if chunk == nil {
		return
	}
	switch chunk.Type {
	case "text-delta":
		if chunk.Text != "" {
			s.textAgg.Append(chunk.Text)
		}
	case "reasoning-delta":
		if chunk.Text != "" {
			s.thinkAgg.Append(chunk.Text)
		}
	case "tool-call-delta":
		if chunk.Name == "" {
			return
		}
		s.turnMu.Lock()
		_, dup := s.seenCalls[chunk.ID]
		s.seenCalls[chunk.ID] = struct{}{}
		s.toolNames[chunk.ID] = chunk.Name
		s.turnMu.Unlock()
		s.textAgg.Flush()
		s.thinkAgg.Flush()
		if !dup {
			s.emit(core.Event{Type: core.EventToolUse, ToolName: chunk.Name, ToolInput: chunk.ArgsDelt})
		}
	case "usage":
		if chunk.Usage != nil {
			s.turnMu.Lock()
			u := *chunk.Usage
			s.lastUsage = &u
			s.turnMu.Unlock()
		}
	default:
		// block-start / block-end / finish — the deltas already carried the content.
	}
}

func (s *dshSession) finishTurn(data sessionEventData) {
	s.turnMu.Lock()
	var usage *tokenUsage
	if data.Turn > s.nativeTurn {
		s.nativeTurn = data.Turn
	}
	if data.Turn > s.currentTurn {
		s.currentTurn = data.Turn
	}
	usage = s.lastUsage
	s.lastUsage = nil
	if data.Turn == s.watchdogTurn || (s.watchdogEchoed && data.Turn > s.watchdogTurn) {
		s.stopWatchdogLocked()
	}
	s.turnMu.Unlock()

	// Flush each native reply before its terminal event, even when a prompt is
	// already queued. Otherwise the engine can recover it from history and
	// then receive the delayed deltas as duplicate output in the next turn.
	s.textAgg.Flush()
	s.thinkAgg.Flush()

	var reason struct {
		Kind  string `json:"kind"`
		Error *struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error,omitempty"`
	}
	_ = json.Unmarshal(data.Reason, &reason)

	evt := core.Event{Type: core.EventResult, SessionID: s.sessionID, Done: true}
	if usage != nil {
		evt.InputTokens = usage.InputTokens
		evt.OutputTokens = usage.OutputTokens
		evt.CacheReadInputTokens = usage.CacheReadTokens
		evt.CacheCreationInputTokens = usage.CacheWriteTokens
	}

	switch reason.Kind {
	case "", "completed":
		// normal end
	case "aborted", "interrupted":
		// cancelled turn (e.g. /stop) — end quietly
	case "blocked":
		evt.Error = fmt.Errorf("dsh: turn blocked")
	case "max-tokens":
		evt.Error = fmt.Errorf("dsh: turn stopped at max output tokens")
	case "error":
		msg := "unknown error"
		if reason.Error != nil && reason.Error.Message != "" {
			msg = reason.Error.Message
		} else if reason.Error != nil {
			msg = reason.Error.Code
		}
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("dsh: %s", msg)})
		evt.Error = fmt.Errorf("dsh: %s", msg)
	default:
		slog.Debug("dsh: unhandled turn end reason", "kind", reason.Kind)
	}

	s.emit(evt)
}

func (s *dshSession) handleApprovalRequested(f muxFrame) {
	s.textAgg.Flush()
	s.thinkAgg.Flush()

	s.pendingMu.Lock()
	s.pendingApprovals[f.RPCID] = pendingApproval{sessionID: f.SessionID, approvalID: f.ApprovalID}
	s.pendingMu.Unlock()

	s.emit(core.Event{
		Type:      core.EventPermissionRequest,
		RequestID: f.RPCID,
		ToolName:  f.ToolName,
		ToolInput: f.Reason,
	})
}

func (s *dshSession) handleQuestionRequested(f muxFrame) {
	s.textAgg.Flush()
	s.thinkAgg.Flush()

	s.pendingMu.Lock()
	s.pendingQuestions[f.RPCID] = pendingQuestion{sessionID: f.SessionID, items: f.Questions}
	s.pendingMu.Unlock()

	var qs []core.UserQuestion
	for _, q := range f.Questions {
		opts := make([]core.UserQuestionOption, 0, len(q.Options))
		for _, o := range q.Options {
			opts = append(opts, core.UserQuestionOption{Label: o.Label, Description: o.Description})
		}
		qs = append(qs, core.UserQuestion{
			Question:    q.Question,
			Header:      q.Header,
			Options:     opts,
			MultiSelect: q.MultiSelect,
		})
	}
	s.emit(core.Event{
		Type:      core.EventPermissionRequest,
		RequestID: f.RPCID,
		ToolName:  "AskUserQuestion",
		Questions: qs,
	})
}

// ── helpers ─────────────────────────────────────────────────────

// emit applies cancellation-aware backpressure. Every text delta contributes
// to the final answer, so dropping even a nonterminal event loses content.
func (s *dshSession) emit(evt core.Event) {
	s.emitMu.RLock()
	defer s.emitMu.RUnlock()
	if s.ctx.Err() != nil {
		return
	}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

// prettyToolArgs formats raw tool-call arguments JSON for display.
func prettyToolArgs(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var v any
	if json.Unmarshal([]byte(raw), &v) == nil {
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	return truncate(raw, 500)
}

// toolResultText extracts human-readable text and the model-facing error flag
// from a tool-result message.
func toolResultText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var msg struct {
		Content []struct {
			Type    string          `json:"type"`
			Text    string          `json:"text,omitempty"`
			Content json.RawMessage `json:"content,omitempty"` // tool-result block
			IsError bool            `json:"isError,omitempty"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return "", false
	}
	var parts []string
	isError := false
	for _, block := range msg.Content {
		if block.Type == "tool-result" {
			isError = isError || block.IsError
			var inner []struct {
				Type string `json:"type"`
				Text string `json:"text,omitempty"`
			}
			if json.Unmarshal(block.Content, &inner) == nil {
				for _, b := range inner {
					if b.Text != "" {
						parts = append(parts, b.Text)
					}
				}
			}
			continue
		}
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n"), isError
}

func truncate(s string, maxRunes int) string {
	if len(s) <= maxRunes {
		return s
	}
	rs := []rune(s)
	if len(rs) <= maxRunes {
		return s
	}
	return string(rs[:maxRunes]) + "..."
}

// applyModel resolves a "provider/model" (or bare model) override against the
// session's model catalog and applies it via session.selectModel, optionally
// carrying a reasoning-effort override.
func applyModel(ctx context.Context, client *rpcClient, sessionID, model, effort string) error {
	var models sessionModels
	if err := client.call(ctx, "session.models", map[string]any{"sessionId": sessionID}, &models); err != nil {
		return fmt.Errorf("dsh: fetch model catalog: %w", err)
	}
	provider, modelID, err := resolveModelRoute(models, model)
	if err != nil {
		return err
	}
	payload := map[string]any{"sessionId": sessionID, "provider": provider, "model": modelID}
	if effort != "" {
		payload["reasoningEffort"] = effort
	}
	if err := client.call(ctx, "session.selectModel", payload, nil); err != nil {
		return fmt.Errorf("dsh: select model %s/%s: %w", provider, modelID, err)
	}
	return nil
}

// Match complete catalog routes instead of splitting at a slash: a model ID
// may itself contain slashes. Bare IDs must identify exactly one route.
func resolveModelRoute(models sessionModels, model string) (string, string, error) {
	if model == "" {
		model = models.Current.Provider + "/" + models.Current.Model
	}
	for _, g := range models.Groups {
		for _, m := range g.Models {
			if model == g.ID+"/"+m.ID || model == g.Name+"/"+m.ID {
				return g.ID, m.ID, nil
			}
		}
	}
	var provider, modelID string
	for _, g := range models.Groups {
		for _, m := range g.Models {
			if m.ID != model {
				continue
			}
			if provider != "" {
				return "", "", fmt.Errorf("dsh: ambiguous model %q; use provider/model", model)
			}
			provider, modelID = g.ID, m.ID
		}
	}
	if provider == "" {
		return "", "", fmt.Errorf("dsh: model %q not found in catalog", model)
	}
	return provider, modelID, nil
}
