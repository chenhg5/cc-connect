package dsh

import (
	"context"
	"encoding/json"
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
	alive      atomic.Bool
	closeOnce  sync.Once
	readerDone chan struct{} // closed when the mux reader goroutine exits

	// mux reader lifecycle
	frames <-chan muxFrame

	// pending server interactions, keyed by the frame's rpcId
	pendingMu        sync.Mutex
	pendingApprovals map[string]pendingApproval
	pendingQuestions map[string]pendingQuestion

	// turn state
	turnMu      sync.Mutex
	currentTurn int
	lastUsage   *tokenUsage
	toolNames   map[string]string   // callId → tool name (for tool/result)
	seenCalls   map[string]struct{} // callIds already surfaced as EventToolUse
	turnDone    chan struct{}       // signaled on turn/end; fresh per Send()

	// delta aggregation: dsh streams token-level chunks; coalesce them into
	// engine-sized EventText/EventThinking pieces so platform delivery is not
	// word-by-word.
	textAgg  *chunkAggregator
	thinkAgg *chunkAggregator

	// first-send env bootstrap (CC_PROJECT / CC_SESSION_KEY for cc-connect CLI)
	envMu       sync.Mutex
	envBlock    string
	envInjected bool
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

	// Apply configured model/effort overrides (best effort — the server's
	// stored selection wins when the model cannot be routed).
	if model != "" || effort != "" {
		applyModel(ctx, client, created.SessionID, model, effort)
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
	frames, err := client.openMux(sessCtx, created.SessionID)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dsh: open event stream: %w", err)
	}
	s.frames = frames
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

	// One-time env bootstrap on the first message of a fresh session.
	s.envMu.Lock()
	if s.envBlock != "" && !s.envInjected {
		prompt = s.envBlock + "\n" + prompt
		s.envInjected = true
	}
	s.envMu.Unlock()

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

	// Arm the per-turn watchdog before submitting.
	turnDone := make(chan struct{}, 1)
	s.turnMu.Lock()
	s.turnDone = turnDone
	s.turnMu.Unlock()

	if err := s.client.call(s.ctx, "session.prompt", map[string]any{
		"sessionId": s.sessionID,
		"mode":      "queue",
		"content":   content,
	}, nil); err != nil {
		s.turnMu.Lock()
		s.turnDone = nil
		s.turnMu.Unlock()
		return fmt.Errorf("dsh: prompt: %w", err)
	}

	if s.timeout > 0 {
		go s.turnWatchdog(turnDone)
	}
	return nil
}

// turnWatchdog emits an error and cancels the turn when it runs past timeout.
func (s *dshSession) turnWatchdog(turnDone chan struct{}) {
	select {
	case <-turnDone:
	case <-s.ctx.Done():
	case <-time.After(s.timeout):
		slog.Warn("dsh: turn timed out, cancelling", "session_id", s.sessionID, "timeout", s.timeout)
		s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("dsh: turn timed out after %s", s.timeout)})
		if err := s.client.call(s.ctx, "session.cancel", map[string]any{"sessionId": s.sessionID}, nil); err != nil {
			slog.Warn("dsh: cancel after timeout failed", "error", err)
		}
	}
}

func (s *dshSession) RespondPermission(requestID string, result core.PermissionResult) error {
	s.pendingMu.Lock()
	approval, isApproval := s.pendingApprovals[requestID]
	question, isQuestion := s.pendingQuestions[requestID]
	if isApproval {
		delete(s.pendingApprovals, requestID)
	}
	if isQuestion {
		delete(s.pendingQuestions, requestID)
	}
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
		entry := map[string]any{"id": item.ID}
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
			out = append(out, map[string]any{"id": item.ID, "custom": ""})
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
	if err := s.client.call(s.ctx, "session.cancel", map[string]any{"sessionId": s.sessionID}, nil); err != nil {
		return fmt.Errorf("dsh: cancel turn: %w", err)
	}
	return nil
}

func (s *dshSession) Events() <-chan core.Event {
	return s.events
}

func (s *dshSession) CurrentSessionID() string {
	return s.sessionID
}

func (s *dshSession) Alive() bool {
	return s.alive.Load()
}

func (s *dshSession) Close() error {
	s.closeOnce.Do(func() {
		s.alive.Store(false)
		s.textAgg.Cancel()
		s.thinkAgg.Cancel()
		s.cancel()
		// Wait for the mux reader to finish (the stream drops on cancel).
		select {
		case <-s.readerDone:
		case <-time.After(8 * time.Second):
			slog.Warn("dsh: close timed out waiting for mux reader")
		}
		close(s.events)
	})
	return nil
}

// ── mux reader ──────────────────────────────────────────────────

func (s *dshSession) readLoop() {
	defer close(s.readerDone)
	defer s.emitStreamError()
	for frame := range s.frames {
		if frame.SessionID != s.sessionID {
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
		s.currentTurn = data.Turn
		s.lastUsage = nil
		s.toolNames = make(map[string]string)
		s.seenCalls = make(map[string]struct{})
		s.turnMu.Unlock()
		// Deliver anything left over from a previous turn before the new one.
		s.textAgg.Flush()
		s.thinkAgg.Flush()

	case "user/message":
		// Echo of our own prompt — not model output.

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
	// Deliver any buffered deltas before closing the turn.
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

	s.turnMu.Lock()
	usage := s.lastUsage
	s.lastUsage = nil
	s.turnMu.Unlock()

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

	s.turnMu.Lock()
	if s.turnDone != nil {
		select {
		case s.turnDone <- struct{}{}:
		default:
		}
	}
	s.turnMu.Unlock()
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

// emit delivers one event to the engine. Critical events (turn completion,
// permission/question requests, errors) block until the engine consumes them
// — losing one would leave the engine stuck on a turn. Non-critical events
// (text/thinking/tool deltas) are dropped when the channel is full: the
// engine accumulates text from what it receives and a stuck permission wait
// must not stall the socket reader into losing the turn/end frame.
func (s *dshSession) emit(evt core.Event) {
	critical := evt.Type == core.EventResult ||
		evt.Type == core.EventPermissionRequest ||
		evt.Type == core.EventError
	if critical {
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}
		return
	}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	default:
		slog.Debug("dsh: dropping non-critical event (engine busy)", "type", evt.Type)
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
func applyModel(ctx context.Context, client *rpcClient, sessionID, model, effort string) {
	var models sessionModels
	if err := client.call(ctx, "session.models", map[string]any{"sessionId": sessionID}, &models); err != nil {
		slog.Warn("dsh: cannot fetch model catalog for configured model", "model", model, "error", err)
		return
	}

	// Resolve the target route: the explicit override when given, otherwise
	// the session's current selection (so a bare effort override still lands).
	provider, modelID := "", ""
	if model != "" {
		provider, modelID = model, ""
		if i := strings.LastIndex(model, "/"); i >= 0 {
			provider, modelID = model[:i], model[i+1:]
		}
	} else if models.Current.Provider != "" && models.Current.Model != "" {
		provider, modelID = models.Current.Provider, models.Current.Model
	}

	found := false
	for _, g := range models.Groups {
		if provider != "" && g.ID != provider && g.Name != provider {
			continue
		}
		for _, m := range g.Models {
			if modelID != "" && m.ID != modelID {
				continue
			}
			payload := map[string]any{
				"sessionId": sessionID,
				"provider":  g.ID,
				"model":     m.ID,
			}
			if effort != "" {
				payload["reasoningEffort"] = effort
			}
			var selected struct {
				Selected struct {
					Provider string `json:"provider"`
					Model    string `json:"model"`
				} `json:"selected"`
			}
			err := client.call(ctx, "session.selectModel", payload, &selected)
			if err != nil {
				slog.Warn("dsh: selectModel failed", "provider", g.ID, "model", m.ID, "effort", effort, "error", err)
				return
			}
			slog.Info("dsh: model applied", "session_id", sessionID, "provider", g.ID, "model", m.ID, "effort", effort)
			found = true
			break
		}
		if found {
			break
		}
	}
	if !found && model != "" {
		slog.Warn("dsh: configured model not found in catalog", "model", model)
	}
}
