package ymsagent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

const toolResultMaxRunes = 1000
const ymsEnvStatusKey = "yms-env"

// handleEvent dispatches one Pi RPC JSON-Line event to the right mapping
// helper. Unknown / lifecycle events are silently logged.
func (s *session) handleEvent(raw map[string]any) {
	t, _ := raw["type"].(string)
	switch t {
	case "response":
		s.handleResponse(raw)
	case "message_update":
		s.handleMessageUpdate(raw)
	case "message_end":
		s.handleMessageEnd(raw)
	case "tool_execution_start":
		s.handleToolStart(raw)
	case "tool_execution_update":
		s.handleToolUpdate(raw)
	case "tool_execution_end":
		s.handleToolEnd(raw)
	case "agent_start", "turn_start", "message_start":
		slog.Debug("yms-rca: lifecycle", "type", t)
	case "agent_end":
		s.handleAgentEnd(raw)
	case "turn_end":
		if turnEndToolCalls(raw) > 0 {
			s.awaitingPostToolSummary.Store(true)
			slog.Debug("yms-rca: tool turn ended; waiting for summary turn")
			return
		}
		// turn_end fires only for agent-runtime turns (LLM calls). Slash
		// commands like /debug-rpc-confirm DO NOT emit turn_end — pi-rpc's
		// docs are explicit that turn_end is "Turn completes (includes
		// assistant message and tool results)". The universal completion
		// signal that covers both is `response command=prompt` (handled
		// in handleResponse below). turn_end is still useful as a
		// defensive emit point for LLM turns and to mirror agent_end.
		if s.completeTurnResult(nil, true) {
			s.busy.Store(false)
		}
	case "extension_ui_request":
		s.handleExtensionUIRequest(raw)
	case "extension_error":
		s.emit(core.Event{Type: core.EventError,
			Error: fmt.Errorf("yms-rca: %s", asString(raw, "message", "extension error"))})
	default:
		slog.Debug("yms-rca: unhandled event", "type", t)
	}
}

func turnEndToolCalls(raw map[string]any) int {
	if v, ok := numAny(raw["toolCallsInTurn"]); ok {
		return v
	}
	data, _ := raw["data"].(map[string]any)
	if data == nil {
		return 0
	}
	if v, ok := numAny(data["toolCallsInTurn"]); ok {
		return v
	}
	return 0
}

// handleResponse handles `response command=...` frames.
func (s *session) handleResponse(raw map[string]any) {
	cmd, _ := raw["command"].(string)
	success, _ := raw["success"].(bool)
	data, _ := raw["data"].(map[string]any)
	switch cmd {
	case "get_state":
		if success && data != nil {
			if sid, ok := data["sessionId"].(string); ok && sid != "" {
				s.sessionID.Store(sid)
				slog.Debug("yms-rca: session id learned", "session_id", sid)
			}
		}
	case "get_session_stats":
		if success && data != nil {
			s.updateContextUsage(data)
		}
	case "prompt":
		// `response command=prompt` is pi-rpc's "prompt handler returned"
		// signal. For LLM turns it fires AFTER agent_end (which already
		// finalized the turn). For slash commands it can arrive before or
		// after the terminal `message_end` carrying the result text.
		//
		// Stale-ack guard: pi-rpc echoes back the id we sent in the prompt
		// frame. A late ack carrying a prior turn's id must not touch the
		// current turn's state — otherwise a Send issued between two
		// turns would see a stale ack mutate promptAcked / busy.
		respID := asString(raw, "id", "")
		currentID, _ := s.currentPromptID.Load().(string)
		if respID != "" && currentID != "" && respID != currentID {
			slog.Debug("yms-rca: ignoring stale prompt ack",
				"resp_id", respID, "current_id", currentID)
			return
		}
		if !success {
			msg := asString(raw, "errorMessage", "")
			if msg == "" && data != nil {
				if em, ok := data["error"].(string); ok {
					msg = em
				}
			}
			if msg == "" {
				msg = "yms-rca: prompt failed"
			}
			s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("%s", msg)})
			// Errors finalize immediately — no trailing text to wait for.
			s.maybeEmitTurnResult(nil)
			s.busy.Store(false)
			return
		}
		// success: pi-rpc has acked, but we cannot clear busy yet — the
		// trailing `message_end` (slash command) or the already-finalized
		// agent_end/turn_end (LLM turn) is the true terminator. Clearing
		// busy here opens a race: a second Send between the ack and the
		// message_end would CAS-pass and reset promptAcked, causing the
		// original turn's terminal message_end to silently drop its
		// EventResult.
		//
		// If agent_end already finalized (LLM turn): busy is already false
		// and this Store is a no-op — but we don't write false here so we
		// don't accidentally clobber a NEW turn started in the meantime.
		// Setting promptAcked is also no-op for LLM turns (latch is set,
		// message_end customType=yms-command will not fire as terminator
		// because turnResultEmitted is already true).
		s.promptAcked.Store(true)
		s.maybeFinalizeSlashCommandTurn()
	}
}

func (s *session) maybeFinalizeSlashCommandTurn() {
	if !s.promptAcked.Load() || !s.slashCommandEnded.Load() {
		return
	}
	s.maybeEmitTurnResult(nil)
	s.busy.Store(false)
}

func (s *session) updateContextUsage(data map[string]any) {
	cu, _ := data["contextUsage"].(map[string]any)
	tk, _ := data["tokens"].(map[string]any)

	usage := &core.ContextUsage{}
	hasAny := false

	if cu != nil {
		if v, ok := numAny(cu["tokens"]); ok {
			usage.UsedTokens = v
			hasAny = true
		} else {
			// nil during compaction — keep last snapshot.
			if existing := s.contextUsage.Load(); existing != nil {
				return
			}
		}
		if v, ok := numAny(cu["contextWindow"]); ok {
			usage.ContextWindow = v
			hasAny = true
		}
	}
	if tk != nil {
		if v, ok := numAny(tk["total"]); ok {
			usage.TotalTokens = v
			hasAny = true
		}
		if v, ok := numAny(tk["input"]); ok {
			usage.InputTokens = v
			hasAny = true
		}
		if v, ok := numAny(tk["output"]); ok {
			usage.OutputTokens = v
			hasAny = true
		}
		if v, ok := numAny(tk["cacheRead"]); ok {
			usage.CachedInputTokens = v
			hasAny = true
		}
	}
	if !hasAny {
		return
	}
	s.contextUsage.Store(usage)
}

func (s *session) handleMessageUpdate(raw map[string]any) {
	ame, _ := raw["assistantMessageEvent"].(map[string]any)
	if ame == nil {
		return
	}
	sub, _ := ame["type"].(string)
	switch sub {
	case "text_delta":
		delta, _ := ame["delta"].(string)
		if delta != "" {
			s.recordAssistantTextForDedupe(delta)
			if s.awaitingPostToolSummary.Load() {
				s.postToolTextEmitted.Store(true)
			}
			s.emitText(delta)
		}
	case "thinking_delta":
		if d, ok := ame["delta"].(string); ok && d != "" {
			s.thinkingBuf.WriteString(d)
		}
	case "thinking_end":
		if s.thinkingBuf.Len() > 0 {
			s.emit(core.Event{Type: core.EventThinking, Content: s.thinkingBuf.String()})
			s.thinkingBuf.Reset()
		}
	case "toolcall_end":
		s.emitToolFromMessage(ame)
	}
}

func (s *session) emitToolFromMessage(ame map[string]any) {
	msg, _ := ame["message"].(map[string]any)
	if msg == nil {
		msg, _ = ame["partial"].(map[string]any)
	}
	if msg == nil {
		return
	}
	contentArr, _ := msg["content"].([]any)
	idx := 0
	if ci, ok := ame["contentIndex"].(float64); ok {
		idx = int(ci)
	}
	if idx < 0 || idx >= len(contentArr) {
		return
	}
	item, _ := contentArr[idx].(map[string]any)
	if item == nil {
		return
	}
	if itemType, _ := item["type"].(string); itemType != "toolCall" {
		return
	}
	s.awaitingPostToolSummary.Store(true)
	callID, _ := item["id"].(string)
	if callID != "" && s.markToolUseSeen(callID) {
		return // already emitted via tool_execution_start
	}
	name, _ := item["name"].(string)
	input := extractToolInput(item)
	s.emit(core.Event{Type: core.EventToolUse, ToolName: name, ToolInput: input})
}

func (s *session) handleMessageEnd(raw map[string]any) {
	msg, _ := raw["message"].(map[string]any)
	if msg == nil {
		return
	}
	role, _ := msg["role"].(string)
	switch role {
	case "toolResult":
		toolName, _ := msg["toolName"].(string)
		callID, _ := msg["toolCallId"].(string)
		if callID != "" && s.markToolDoneSeen(callID) {
			return // already emitted via tool_execution_end
		}
		output := firstTextFromContent(msg["content"])
		s.emit(core.Event{
			Type:       core.EventToolResult,
			ToolName:   toolName,
			ToolResult: truncStr(output, toolResultMaxRunes),
		})
	case "assistant":
		if errMsg, _ := msg["errorMessage"].(string); errMsg != "" {
			s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("%s", errMsg)})
		}
		s.handleAssistantMessageEnd(msg)
	case "custom":
		display, _ := msg["display"].(bool)
		customType, _ := msg["customType"].(string)
		text := firstTextFromContent(msg["content"])
		if text == "" {
			text = asString(msg, "text", "")
		}
		clean := collapseBlankLines(stripANSI(text))
		switch customType {
		case "yms-command":
			if display && clean != "" {
				s.emitText(clean)
			}
			// Slash-command terminator: yms-rca may emit this message_end
			// before or after response/prompt. Finalize only after both the
			// terminal text and prompt ack are observed so the engine receives
			// EventText before EventResult without reopening the Send race.
			s.slashCommandEnded.Store(true)
			s.maybeFinalizeSlashCommandTurn()
		case "yms-rca.env-switch":
			s.updateCurrentProfile(profileFromEnvSwitchMessage(msg, clean))
			if clean == "" {
				return
			}
			if display {
				s.emitText(clean)
			} else {
				s.emit(core.Event{Type: core.EventThinking, Content: clean})
			}
		default:
			if clean == "" {
				return
			}
			if display {
				s.emitText(clean)
			} else {
				s.emit(core.Event{Type: core.EventThinking, Content: clean})
			}
		}
	}
}

func (s *session) handleToolStart(raw map[string]any) {
	callID := asString(raw, "toolCallId", "")
	name := asString(raw, "toolName", "")
	if callID != "" && s.markToolUseSeen(callID) {
		return
	}
	s.awaitingPostToolSummary.Store(true)
	var input string
	if args, ok := raw["arguments"].(map[string]any); ok {
		input = summariseArgs(args)
	}
	s.emit(core.Event{Type: core.EventToolUse, ToolName: name, ToolInput: input})
}

func (s *session) handleToolUpdate(raw map[string]any) {
	progress := asString(raw, "progress", "")
	if progress == "" {
		if m, ok := raw["message"].(string); ok {
			progress = m
		}
	}
	if progress == "" {
		return
	}
	clean := collapseBlankLines(stripANSI(progress))
	s.emit(core.Event{Type: core.EventThinking, Content: clean})
}

func (s *session) handleToolEnd(raw map[string]any) {
	callID := asString(raw, "toolCallId", "")
	name := asString(raw, "toolName", "")
	if callID != "" {
		_ = s.markToolDoneSeen(callID)
	}
	result := asString(raw, "result", "")
	if result == "" {
		if r, ok := raw["output"].(string); ok {
			result = r
		}
	}
	status := asString(raw, "status", "")
	evt := core.Event{
		Type:       core.EventToolResult,
		ToolName:   name,
		ToolResult: truncStr(result, toolResultMaxRunes),
		ToolStatus: status,
	}
	if ec, ok := numAny(raw["exitCode"]); ok {
		evt.ToolExitCode = &ec
	}
	if v, ok := raw["success"].(bool); ok {
		evt.ToolSuccess = &v
	}
	s.emit(evt)
}

func (s *session) handleAssistantMessageEnd(msg map[string]any) {
	hasToolCall := contentHasToolCall(msg["content"])
	text := firstTextFromContent(msg["content"])
	if text == "" {
		text = asString(msg, "text", "")
	}
	clean := collapseBlankLines(stripANSI(text))
	if strings.TrimSpace(clean) != "" {
		suffix, _ := s.assistantTextEndSuffix(clean)
		if strings.TrimSpace(suffix) != "" {
			if s.awaitingPostToolSummary.Load() && !hasToolCall {
				s.postToolTextEmitted.Store(true)
			}
			s.emitText(suffix)
		}
	}
	if hasToolCall {
		s.awaitingPostToolSummary.Store(true)
		return
	}
	s.assistantMessageEnded.Store(true)
	s.flushPendingTurnResult(nil, false)
}

func (s *session) handleAgentEnd(raw map[string]any) {
	// agent_end fires once per LLM call and carries token usage. We do NOT
	// clear busy here — the universal turn boundary is turn_end. Emit the
	// EventResult once final assistant text is available; turn_end's emit will
	// be a no-op thanks to maybeEmitTurnResult's atomic dedup.
	s.completeTurnResult(raw, false)
	// async refresh of context window after the LLM call.
	go s.requestSessionStats()
}

func (s *session) completeTurnResult(raw map[string]any, clearBusy bool) bool {
	if s.turnResultEmitted.Load() {
		return true
	}
	if s.awaitingPostToolSummary.Load() && !s.postToolTextEmitted.Load() && !s.assistantMessageEnded.Load() {
		s.deferTurnResult(raw, clearBusy)
		return false
	}
	if !s.currentPromptSlashCommand.Load() && !s.hasAssistantTextOrEnd() {
		s.deferTurnResult(raw, clearBusy)
		return false
	}
	if s.flushPendingTurnResult(raw, clearBusy) {
		return true
	}
	return s.maybeEmitTurnResult(raw)
}

// maybeEmitTurnResult emits EventResult{Done:true} exactly once per turn.
// raw may be nil (slash-command turn) or the agent_end frame (carries usage).
// Returns true if this call performed the emit.
func (s *session) maybeEmitTurnResult(raw map[string]any) bool {
	if !s.turnResultEmitted.CompareAndSwap(false, true) {
		return false
	}
	s.emitProfileFooter()
	s.emit(s.buildTurnResult(raw))
	return true
}

func (s *session) buildTurnResult(raw map[string]any) core.Event {
	sid := s.CurrentSessionID()
	evt := core.Event{Type: core.EventResult, SessionID: sid, Done: true}
	if raw != nil {
		if u, ok := raw["usage"].(map[string]any); ok {
			if v, ok := numAny(u["input"]); ok {
				evt.InputTokens = v
			}
			if v, ok := numAny(u["output"]); ok {
				evt.OutputTokens = v
			}
		}
	}
	return evt
}

func (s *session) deferTurnResult(raw map[string]any, clearBusy bool) {
	evt := s.buildTurnResult(raw)
	s.pendingResultMu.Lock()
	defer s.pendingResultMu.Unlock()
	if s.pendingResult == nil {
		s.pendingResult = &pendingTurnResult{evt: evt, clearBusy: clearBusy}
		return
	}
	if evt.SessionID != "" {
		s.pendingResult.evt.SessionID = evt.SessionID
	}
	if evt.InputTokens != 0 {
		s.pendingResult.evt.InputTokens = evt.InputTokens
	}
	if evt.OutputTokens != 0 {
		s.pendingResult.evt.OutputTokens = evt.OutputTokens
	}
	s.pendingResult.clearBusy = s.pendingResult.clearBusy || clearBusy
}

func (s *session) flushPendingTurnResult(raw map[string]any, clearBusy bool) bool {
	s.pendingResultMu.Lock()
	pending := s.pendingResult
	if pending != nil {
		evt := s.buildTurnResult(raw)
		mergeTurnResultEvent(&pending.evt, evt)
		pending.clearBusy = pending.clearBusy || clearBusy
	}
	s.pendingResult = nil
	s.pendingResultMu.Unlock()
	if pending == nil {
		return false
	}
	if s.turnResultEmitted.CompareAndSwap(false, true) {
		s.emitProfileFooter()
		s.emit(pending.evt)
	}
	if pending.clearBusy {
		s.busy.Store(false)
	}
	return true
}

func mergeTurnResultEvent(dst *core.Event, src core.Event) {
	if src.SessionID != "" {
		dst.SessionID = src.SessionID
	}
	if src.InputTokens != 0 {
		dst.InputTokens = src.InputTokens
	}
	if src.OutputTokens != 0 {
		dst.OutputTokens = src.OutputTokens
	}
}

func (s *session) handleExtensionUIRequest(raw map[string]any) {
	id, _ := raw["id"].(string)
	method, _ := raw["method"].(string)
	if method == "setStatus" {
		s.updateCurrentProfile(profileFromStatusEvent(raw))
		return
	}
	if id == "" {
		slog.Warn("yms-rca: extension_ui_request without id", "method", method)
		return
	}
	switch method {
	case "confirm":
		title := asString(raw, "title", "yms-rca request")
		msg := asString(raw, "message", "")
		s.handleConfirmRequest(id, title, msg)
	case "notify":
		text := asString(raw, "message", "")
		notifyType := asString(raw, "notifyType", "")
		if notifyType == "error" {
			s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("%s", text)})
		} else {
			s.emit(core.Event{Type: core.EventThinking, Content: text})
		}
	case "setWidget", "setTitle", "set_editor_text":
		slog.Debug("yms-rca: extension UI passthrough", "method", method)
	case "select", "input", "editor":
		_ = s.writeFrame(map[string]any{
			"type":      "extension_ui_response",
			"id":        id,
			"cancelled": true,
		})
		s.emit(core.Event{Type: core.EventThinking,
			Content: fmt.Sprintf("yms-rca: %s UI not supported in IM bridge", method)})
	default:
		slog.Debug("yms-rca: unknown extension UI method", "method", method)
	}
}

func (s *session) handleConfirmRequest(id, title, message string) {
	mode := s.currentMode()
	switch mode {
	case "yolo", "bypassPermissions":
		_ = s.writeFrame(map[string]any{
			"type": "extension_ui_response", "id": id, "confirmed": true,
		})
		s.emit(core.Event{Type: core.EventThinking,
			Content: fmt.Sprintf("yms-rca: auto-approved (%s): %s", mode, title)})
	case "dontAsk":
		_ = s.writeFrame(map[string]any{
			"type": "extension_ui_response", "id": id, "confirmed": false,
		})
		s.emit(core.Event{Type: core.EventThinking,
			Content: "yms-rca: auto-declined (dontAsk): " + title})
	default:
		// IMPORTANT: register pending + timer BEFORE emit; otherwise an
		// engine that immediately calls RespondPermission will not find
		// the pending entry.
		s.registerPending(id, title)
		s.emit(core.Event{
			Type:      core.EventPermissionRequest,
			RequestID: id,
			ToolName:  title,
			ToolInput: stripANSI(message),
			ToolInputRaw: map[string]any{
				"title":   title,
				"message": message,
				"method":  "confirm",
			},
		})
	}
}

func (s *session) currentMode() string {
	s.confirmMu.Lock()
	defer s.confirmMu.Unlock()
	return s.mode
}

func (s *session) resetAssistantText() {
	s.assistantTextMu.Lock()
	defer s.assistantTextMu.Unlock()
	s.assistantText.Reset()
}

func (s *session) recordAssistantTextForDedupe(text string) {
	s.assistantTextMu.Lock()
	defer s.assistantTextMu.Unlock()
	s.assistantText.WriteString(normalizeAssistantTextForDedupe(text))
}

func (s *session) assistantTextEndSuffix(finalText string) (string, bool) {
	s.assistantTextMu.Lock()
	defer s.assistantTextMu.Unlock()

	emitted := s.assistantText.String()
	switch {
	case emitted == "":
		s.assistantText.WriteString(finalText)
		return finalText, true
	case finalText == emitted || strings.TrimSpace(finalText) == strings.TrimSpace(emitted):
		return "", false
	case strings.HasPrefix(finalText, emitted):
		suffix := finalText[len(emitted):]
		s.assistantText.WriteString(suffix)
		return suffix, true
	default:
		s.assistantText.WriteString(finalText)
		return finalText, true
	}
}

func normalizeAssistantTextForDedupe(text string) string {
	return collapseBlankLines(stripANSI(text))
}

func (s *session) hasAssistantTextOrEnd() bool {
	if s.assistantMessageEnded.Load() {
		return true
	}
	s.assistantTextMu.Lock()
	defer s.assistantTextMu.Unlock()
	return s.assistantText.Len() > 0
}

func (s *session) clearPendingTurnResult() {
	s.pendingResultMu.Lock()
	defer s.pendingResultMu.Unlock()
	s.pendingResult = nil
}

func (s *session) emitText(content string) {
	if content == "" {
		return
	}
	s.turnTextEmitted.Store(true)
	s.emit(core.Event{Type: core.EventText, Content: content})
}

func (s *session) updateCurrentProfile(profile string) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return
	}
	s.currentProfile.Store(profile)
	if s.profileUpdater != nil {
		s.profileUpdater(profile)
	}
	// Persist the (project, session_key) → profile mapping so a future
	// cc-connect daemon restart can auto-restore this session's profile.
	// "local" is a clear: the user explicitly disconnected (or never
	// connected), so the stored entry — if any — must go.
	if s.profileStore != nil && s.project != "" && s.sessionKey != "" {
		if profile == "local" {
			s.profileStore.Clear(s.project, s.sessionKey)
		} else {
			s.profileStore.Set(s.project, s.sessionKey, profile)
		}
	}
}

func (s *session) currentProfileName() string {
	profile, _ := s.currentProfile.Load().(string)
	return strings.TrimSpace(profile)
}

func (s *session) emitProfileFooter() {
	if !s.turnTextEmitted.Load() {
		return
	}
	profile := s.currentProfileName()
	if profile == "" {
		return
	}
	s.emit(core.Event{Type: core.EventText, Content: "\n\n*profile: " + profile + "*"})
}

func profileFromEnvSwitchMessage(msg map[string]any, text string) string {
	if details, _ := msg["details"].(map[string]any); details != nil {
		if profile := asString(details, "to", ""); profile != "" {
			return profile
		}
	}
	return profileFromEnvSwitchText(text)
}

func profileFromEnvSwitchText(text string) string {
	const marker = "Switched to env:"
	idx := strings.Index(text, marker)
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(text[idx+len(marker):])
	if rest == "" {
		return ""
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], ".,;:()[]")
}

func profileFromStatusEvent(raw map[string]any) string {
	key := asString(raw, "key", "")
	text := firstStatusText(raw)
	if args, _ := raw["args"].([]any); len(args) >= 2 {
		if key == "" {
			key, _ = args[0].(string)
		}
		if text == "" {
			text, _ = args[1].(string)
		}
	}
	if key != ymsEnvStatusKey {
		return ""
	}
	return profileFromStatusText(text)
}

func firstStatusText(raw map[string]any) string {
	for _, key := range []string{"text", "value", "status", "message"} {
		if text := asString(raw, key, ""); text != "" {
			return text
		}
	}
	return ""
}

func profileFromStatusText(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "env:") {
		return ""
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, "env:"))
	if rest == "" {
		return ""
	}
	if idx := strings.IndexAny(rest, " ("); idx >= 0 {
		rest = rest[:idx]
	}
	return strings.TrimSpace(rest)
}

// ── tool de-dup ────────────────────────────────────────────

// markToolUseSeen returns true if id was already seen (caller should skip emit).
func (s *session) markToolUseSeen(id string) bool {
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	if _, ok := s.seenToolUse[id]; ok {
		return true
	}
	s.seenToolUse[id] = struct{}{}
	return false
}

func (s *session) markToolDoneSeen(id string) bool {
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	if _, ok := s.seenToolDone[id]; ok {
		return true
	}
	s.seenToolDone[id] = struct{}{}
	return false
}

// ── small helpers ──────────────────────────────────────────

func asString(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return def
}

// numAny coerces ints/int64/float64 to an int.
func numAny(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

func contentHasToolCall(content any) bool {
	arr, _ := content.([]any)
	for _, c := range arr {
		item, _ := c.(map[string]any)
		if item == nil {
			continue
		}
		if itemType, _ := item["type"].(string); itemType == "toolCall" {
			return true
		}
	}
	return false
}

func extractToolInput(item map[string]any) string {
	args, _ := item["arguments"].(map[string]any)
	if args == nil {
		return ""
	}
	return summariseArgs(args)
}

func summariseArgs(args map[string]any) string {
	for _, key := range []string{"description", "command", "file_path", "pattern", "query"} {
		if s, ok := args[key].(string); ok && s != "" {
			return s
		}
	}
	b, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return truncStr(string(b), 200)
}
