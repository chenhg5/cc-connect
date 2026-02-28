package core

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const maxPlatformMessageLen = 4000

// Engine routes messages between platforms and the agent for a single project.
type Engine struct {
	name      string
	agent     Agent
	platforms []Platform
	sessions  *SessionManager
	ctx       context.Context
	cancel    context.CancelFunc
	i18n      *I18n

	// Interactive agent session management
	interactiveMu     sync.Mutex
	interactiveStates map[string]*interactiveState // key = sessionKey
}

// interactiveState tracks a running interactive agent session and its permission state.
type interactiveState struct {
	agentSession AgentSession
	platform     Platform
	replyCtx     any
	mu           sync.Mutex
	pending      *pendingPermission
	approveAll   bool // when true, auto-approve all permission requests for this session
	quiet        bool // when true, suppress thinking and tool progress messages
}

// pendingPermission represents a permission request waiting for user response.
type pendingPermission struct {
	RequestID    string
	ToolName     string
	ToolInput    map[string]any
	InputPreview string
	Resolved     chan struct{} // closed when user responds
}

func NewEngine(name string, ag Agent, platforms []Platform, sessionStorePath string, lang Language) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		name:              name,
		agent:             ag,
		platforms:         platforms,
		sessions:          NewSessionManager(sessionStorePath),
		ctx:               ctx,
		cancel:            cancel,
		i18n:              NewI18n(lang),
		interactiveStates: make(map[string]*interactiveState),
	}
}

func (e *Engine) SetLanguageSaveFunc(fn func(Language) error) {
	e.i18n.SetSaveFunc(fn)
}

func (e *Engine) Start() error {
	for _, p := range e.platforms {
		if err := p.Start(e.handleMessage); err != nil {
			return fmt.Errorf("[%s] start platform %s: %w", e.name, p.Name(), err)
		}
		slog.Info("platform started", "project", e.name, "platform", p.Name())
	}
	slog.Info("engine started", "project", e.name, "agent", e.agent.Name(), "platforms", len(e.platforms))
	return nil
}

func (e *Engine) Stop() error {
	e.cancel()

	e.interactiveMu.Lock()
	for key, state := range e.interactiveStates {
		if state.agentSession != nil {
			state.agentSession.Close()
		}
		delete(e.interactiveStates, key)
	}
	e.interactiveMu.Unlock()

	var errs []error
	for _, p := range e.platforms {
		if err := p.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("stop platform %s: %w", p.Name(), err))
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

func (e *Engine) handleMessage(p Platform, msg *Message) {
	content := strings.TrimSpace(msg.Content)
	if content == "" {
		return
	}

	if strings.HasPrefix(content, "/") {
		e.handleCommand(p, msg, content)
		return
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
	close(pending.Resolved)

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

	if err := state.agentSession.Send(msg.Content); err != nil {
		slog.Error("failed to send prompt", "error", err)

		if !state.agentSession.Alive() {
			e.cleanupInteractiveState(msg.SessionKey)
			e.send(p, msg.ReplyCtx, e.i18n.T(MsgSessionRestarting))

			state = e.getOrCreateInteractiveState(msg.SessionKey, p, msg.ReplyCtx, session)
			if state.agentSession == nil {
				e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), "failed to restart agent session"))
				return
			}
			if err := state.agentSession.Send(msg.Content); err != nil {
				e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), err))
				return
			}
		} else {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgError), err))
			return
		}
	}

	e.processInteractiveEvents(state, session, msg.SessionKey)
}

func (e *Engine) getOrCreateInteractiveState(sessionKey string, p Platform, replyCtx any, session *Session) *interactiveState {
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()

	state, ok := e.interactiveStates[sessionKey]
	if ok && state.agentSession != nil && state.agentSession.Alive() {
		return state
	}

	agentSession, err := e.agent.StartSession(e.ctx, session.AgentSessionID)
	if err != nil {
		slog.Error("failed to start interactive session", "error", err)
		state = &interactiveState{platform: p, replyCtx: replyCtx}
		e.interactiveStates[sessionKey] = state
		return state
	}

	state = &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     replyCtx,
	}
	e.interactiveStates[sessionKey] = state

	slog.Info("interactive session started", "session_key", sessionKey, "agent_session", session.AgentSessionID)
	return state
}

func (e *Engine) cleanupInteractiveState(sessionKey string) {
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()

	state, ok := e.interactiveStates[sessionKey]
	if ok && state.agentSession != nil {
		state.agentSession.Close()
	}
	delete(e.interactiveStates, sessionKey)
}

func (e *Engine) processInteractiveEvents(state *interactiveState, session *Session, sessionKey string) {
	var textParts []string
	toolCount := 0

	for event := range state.agentSession.Events() {
		if e.ctx.Err() != nil {
			return
		}

		state.mu.Lock()
		p := state.platform
		replyCtx := state.replyCtx
		state.mu.Unlock()

		switch event.Type {
		case EventThinking:
			if !state.quiet && event.Content != "" {
				preview := truncate(event.Content, 300)
				e.send(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgThinking), preview))
			}

		case EventToolUse:
			toolCount++
			if !state.quiet {
				inputPreview := truncate(event.ToolInput, 500)
				e.send(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgTool), toolCount, event.ToolName, inputPreview))
			}

		case EventText:
			if event.Content != "" {
				textParts = append(textParts, event.Content)
			}
			if event.SessionID != "" && session.AgentSessionID == "" {
				session.AgentSessionID = event.SessionID
				e.sessions.Save()
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

			slog.Info("permission request",
				"request_id", event.RequestID,
				"tool", event.ToolName,
			)

			prompt := fmt.Sprintf(e.i18n.T(MsgPermissionPrompt), event.ToolName, truncate(event.ToolInput, 800))
			e.send(p, replyCtx, prompt)

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

			<-pending.Resolved
			slog.Info("permission resolved", "request_id", event.RequestID)

		case EventResult:
			if event.SessionID != "" {
				session.AgentSessionID = event.SessionID
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

			slog.Debug("turn complete",
				"session", session.ID,
				"agent_session", session.AgentSessionID,
				"tools", toolCount,
				"response_len", len(fullResponse),
			)

			for _, chunk := range splitMessage(fullResponse, maxPlatformMessageLen) {
				if err := p.Send(e.ctx, replyCtx, chunk); err != nil {
					slog.Error("failed to send reply", "error", err)
					return
				}
			}
			return

		case EventError:
			if event.Error != nil {
				slog.Error("agent error", "error", event.Error)
				e.send(p, replyCtx, fmt.Sprintf(e.i18n.T(MsgError), event.Error))
			}
			return
		}
	}

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
		for _, chunk := range splitMessage(fullResponse, maxPlatformMessageLen) {
			e.send(p, replyCtx, chunk)
		}
	}
}

// ──────────────────────────────────────────────────────────────
// Command handling
// ──────────────────────────────────────────────────────────────

func (e *Engine) handleCommand(p Platform, msg *Message, raw string) {
	parts := strings.Fields(raw)
	cmd := strings.ToLower(parts[0])
	args := parts[1:]

	switch cmd {
	case "/new":
		e.cmdNew(p, msg, args)
	case "/list", "/sessions":
		e.cmdList(p, msg)
	case "/switch":
		e.cmdSwitch(p, msg, args)
	case "/current":
		e.cmdCurrent(p, msg)
	case "/history":
		e.cmdHistory(p, msg, args)
	case "/allow":
		e.cmdAllow(p, msg, args)
	case "/quiet":
		e.cmdQuiet(p, msg)
	case "/stop":
		e.cmdStop(p, msg)
	case "/help":
		e.cmdHelp(p, msg)
	default:
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("Unknown command: %s\nType /help for available commands.", cmd))
	}
}

func (e *Engine) cmdNew(p Platform, msg *Message, args []string) {
	e.cleanupInteractiveState(msg.SessionKey)
	name := "session"
	if len(args) > 0 {
		name = strings.Join(args, " ")
	}
	s := e.sessions.NewSession(msg.SessionKey, name)
	e.reply(p, msg.ReplyCtx,
		fmt.Sprintf("✅ New session created: %s (id: %s)", s.Name, s.ID))
}

func (e *Engine) cmdList(p Platform, msg *Message) {
	agentSessions, err := e.agent.ListSessions(e.ctx)
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ Failed to list sessions: %v", err))
		return
	}
	if len(agentSessions) == 0 {
		e.reply(p, msg.ReplyCtx, "No Claude Code sessions found for this project.")
		return
	}

	activeSession := e.sessions.GetOrCreateActive(msg.SessionKey)
	activeAgentID := activeSession.AgentSessionID

	limit := 20
	if len(agentSessions) < limit {
		limit = len(agentSessions)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Claude Code Sessions** (%d)\n\n", len(agentSessions)))
	for i := 0; i < limit; i++ {
		s := agentSessions[i]
		marker := "◻"
		if s.ID == activeAgentID {
			marker = "▶"
		}
		shortID := s.ID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		summary := s.Summary
		if summary == "" {
			summary = "(empty)"
		}
		sb.WriteString(fmt.Sprintf("%s `%s` · %s · **%d** msgs · %s\n",
			marker, shortID, summary, s.MessageCount, s.ModifiedAt.Format("01-02 15:04")))
	}
	if len(agentSessions) > limit {
		sb.WriteString(fmt.Sprintf("\n... and %d more\n", len(agentSessions)-limit))
	}
	sb.WriteString("\n`/switch <id>` to switch session")
	e.reply(p, msg.ReplyCtx, sb.String())
}

func (e *Engine) cmdSwitch(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		e.reply(p, msg.ReplyCtx, "Usage: /switch <session_id_prefix>")
		return
	}
	prefix := strings.TrimSpace(args[0])

	agentSessions, err := e.agent.ListSessions(e.ctx)
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
		return
	}

	var matched *AgentSessionInfo
	for i := range agentSessions {
		if strings.HasPrefix(agentSessions[i].ID, prefix) {
			matched = &agentSessions[i]
			break
		}
	}
	if matched == nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ No session matching prefix %q", prefix))
		return
	}

	e.cleanupInteractiveState(msg.SessionKey)

	session := e.sessions.GetOrCreateActive(msg.SessionKey)
	session.AgentSessionID = matched.ID
	session.Name = matched.Summary
	e.sessions.Save()

	e.reply(p, msg.ReplyCtx,
		fmt.Sprintf("✅ Switched to: %s (%s, %d msgs)", matched.Summary, matched.ID[:8], matched.MessageCount))
}

func (e *Engine) cmdCurrent(p Platform, msg *Message) {
	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	agentID := s.AgentSessionID
	if agentID == "" {
		agentID = "(new — not yet started)"
	}
	e.reply(p, msg.ReplyCtx, fmt.Sprintf(
		"📌 Current session\nName: %s\nClaude Session: %s\nLocal messages: %d",
		s.Name, agentID, len(s.History)))
}

func (e *Engine) cmdHistory(p Platform, msg *Message, args []string) {
	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	n := 10
	if len(args) > 0 {
		if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
			n = v
		}
	}
	entries := s.GetHistory(n)
	if len(entries) == 0 {
		e.reply(p, msg.ReplyCtx, "No history in current session.")
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📜 History (last %d):\n\n", len(entries)))
	for _, e := range entries {
		icon := "👤"
		if e.Role == "assistant" {
			icon = "🤖"
		}
		content := e.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		sb.WriteString(fmt.Sprintf("%s [%s]\n%s\n\n", icon, e.Timestamp.Format("15:04:05"), content))
	}
	e.reply(p, msg.ReplyCtx, sb.String())
}

func (e *Engine) cmdHelp(p Platform, msg *Message) {
	e.reply(p, msg.ReplyCtx, `/new [name]         — Start a brand-new Claude session
/list               — List Claude Code sessions for this project
/switch <id_prefix> — Resume an existing Claude session
/current            — Show current active session
/history [n]        — Show last n messages (default 10)
/allow <tool>       — Pre-allow a tool (takes effect on next session)
/quiet              — Toggle thinking/tool progress messages
/stop               — Stop current execution
/help               — Show this help`)
}

func (e *Engine) cmdQuiet(p Platform, msg *Message) {
	e.interactiveMu.Lock()
	state, ok := e.interactiveStates[msg.SessionKey]
	e.interactiveMu.Unlock()

	if !ok || state == nil {
		// No state yet, create one so the flag persists
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
	if pending != nil {
		state.pending = nil
	}
	state.mu.Unlock()
	if pending != nil {
		close(pending.Resolved)
	}

	e.cleanupInteractiveState(msg.SessionKey)
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgExecutionStopped))
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

// ──────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────

// send wraps p.Send with error logging.
func (e *Engine) send(p Platform, replyCtx any, content string) {
	if err := p.Send(e.ctx, replyCtx, content); err != nil {
		slog.Error("platform send failed", "platform", p.Name(), "error", err, "content_len", len(content))
	}
}

// reply wraps p.Reply with error logging.
func (e *Engine) reply(p Platform, replyCtx any, content string) {
	if err := p.Reply(e.ctx, replyCtx, content); err != nil {
		slog.Error("platform reply failed", "platform", p.Name(), "error", err, "content_len", len(content))
	}
}

func truncate(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "..."
	}
	return s
}

func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}
	var chunks []string
	for len(text) > 0 {
		end := maxLen
		if end > len(text) {
			end = len(text)
		}
		if end < len(text) {
			if idx := strings.LastIndex(text[:end], "\n"); idx > 0 {
				end = idx + 1
			}
		}
		chunks = append(chunks, text[:end])
		text = text[end:]
	}
	return chunks
}
