package core

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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
}

func NewEngine(name string, ag Agent, platforms []Platform, sessionStorePath string) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		name:      name,
		agent:     ag,
		platforms: platforms,
		sessions:  NewSessionManager(sessionStorePath),
		ctx:       ctx,
		cancel:    cancel,
	}
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

	session := e.sessions.GetOrCreateActive(msg.SessionKey)
	if !session.TryLock() {
		_ = p.Reply(e.ctx, msg.ReplyCtx, "⏳ Previous request still processing, please wait...")
		return
	}

	slog.Info("processing message",
		"platform", msg.Platform,
		"user", msg.UserName,
		"session", session.ID,
	)

	go func() {
		defer session.Unlock()
		e.processMessage(p, msg, session)
	}()
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
	case "/help":
		e.cmdHelp(p, msg)
	default:
		_ = p.Reply(e.ctx, msg.ReplyCtx, fmt.Sprintf("Unknown command: %s\nType /help for available commands.", cmd))
	}
}

func (e *Engine) cmdNew(p Platform, msg *Message, args []string) {
	name := "session"
	if len(args) > 0 {
		name = strings.Join(args, " ")
	}
	s := e.sessions.NewSession(msg.SessionKey, name)
	_ = p.Reply(e.ctx, msg.ReplyCtx,
		fmt.Sprintf("✅ New session created: %s (id: %s)", s.Name, s.ID))
}

func (e *Engine) cmdList(p Platform, msg *Message) {
	agentSessions, err := e.agent.ListSessions(e.ctx)
	if err != nil {
		_ = p.Reply(e.ctx, msg.ReplyCtx, fmt.Sprintf("❌ Failed to list sessions: %v", err))
		return
	}
	if len(agentSessions) == 0 {
		_ = p.Reply(e.ctx, msg.ReplyCtx, "No Claude Code sessions found for this project.")
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
	_ = p.Reply(e.ctx, msg.ReplyCtx, sb.String())
}

func (e *Engine) cmdSwitch(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		_ = p.Reply(e.ctx, msg.ReplyCtx, "Usage: /switch <session_id_prefix>")
		return
	}
	prefix := strings.TrimSpace(args[0])

	agentSessions, err := e.agent.ListSessions(e.ctx)
	if err != nil {
		_ = p.Reply(e.ctx, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
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
		_ = p.Reply(e.ctx, msg.ReplyCtx, fmt.Sprintf("❌ No session matching prefix %q", prefix))
		return
	}

	session := e.sessions.GetOrCreateActive(msg.SessionKey)
	session.AgentSessionID = matched.ID
	session.Name = matched.Summary
	e.sessions.Save()

	_ = p.Reply(e.ctx, msg.ReplyCtx,
		fmt.Sprintf("✅ Switched to: %s (%s, %d msgs)", matched.Summary, matched.ID[:8], matched.MessageCount))
}

func (e *Engine) cmdCurrent(p Platform, msg *Message) {
	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	agentID := s.AgentSessionID
	if agentID == "" {
		agentID = "(new — not yet started)"
	}
	_ = p.Reply(e.ctx, msg.ReplyCtx, fmt.Sprintf(
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
		_ = p.Reply(e.ctx, msg.ReplyCtx, "No history in current session.")
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
	_ = p.Reply(e.ctx, msg.ReplyCtx, sb.String())
}

func (e *Engine) cmdHelp(p Platform, msg *Message) {
	_ = p.Reply(e.ctx, msg.ReplyCtx, `/new [name]         — Start a brand-new Claude session
/list               — List Claude Code sessions for this project
/switch <id_prefix> — Resume an existing Claude session
/current            — Show current active session
/history [n]        — Show last n messages (default 10)
/help               — Show this help`)
}

// ──────────────────────────────────────────────────────────────
// Agent message processing
// ──────────────────────────────────────────────────────────────

func (e *Engine) processMessage(p Platform, msg *Message, session *Session) {
	session.AddHistory("user", msg.Content)

	fullResponse, toolSummary, err := e.runAgent(session, msg.Content)

	if err != nil && strings.Contains(err.Error(), "already in use") {
		oldID := session.AgentSessionID
		session.AgentSessionID = ""
		slog.Warn("session in use, starting new session", "old_session", oldID)
		_ = p.Reply(e.ctx, msg.ReplyCtx, "⚠️ Previous Claude session is locked, starting new session...")
		fullResponse, toolSummary, err = e.runAgent(session, msg.Content)
	}

	if err != nil {
		slog.Error("agent event error", "error", err)
		_ = p.Reply(e.ctx, msg.ReplyCtx, fmt.Sprintf("❌ Error: %v", err))
		return
	}

	if fullResponse == "" {
		fullResponse = "(empty response)"
	}

	if len(toolSummary) > 0 {
		fullResponse += "\n\n---\n📎 Tools used:\n" + strings.Join(toolSummary, "\n")
	}

	session.AddHistory("assistant", fullResponse)

	slog.Debug("agent response",
		"session", session.ID,
		"agent_session", session.AgentSessionID,
		"tools", len(toolSummary),
		"response_len", len(fullResponse),
		"response_preview", truncate(fullResponse, 200),
	)

	for _, chunk := range splitMessage(fullResponse, maxPlatformMessageLen) {
		if err := p.Reply(e.ctx, msg.ReplyCtx, chunk); err != nil {
			slog.Error("failed to send reply", "platform", p.Name(), "error", err)
			return
		}
	}

	e.sessions.Save()
}

// runAgent executes the agent and collects the full response.
// Returns (response, toolSummary, error). On the first fatal event error it
// drains remaining events and returns the error.
func (e *Engine) runAgent(session *Session, prompt string) (string, []string, error) {
	responseCh, err := e.agent.Execute(e.ctx, session.AgentSessionID, prompt)
	if err != nil {
		return "", nil, err
	}

	var fullResponse string
	var toolSummary []string

	for ev := range responseCh {
		if ev.Error != nil {
			for range responseCh { // drain
			}
			return "", nil, ev.Error
		}

		switch ev.Type {
		case EventToolUse:
			summary := ev.ToolName
			if ev.ToolInput != "" {
				summary += ": " + ev.ToolInput
			}
			toolSummary = append(toolSummary, "- "+summary)
		case EventResult, EventText:
			if ev.Content != "" {
				fullResponse = ev.Content
			}
		}

		if ev.SessionID != "" && session.AgentSessionID == "" {
			session.AgentSessionID = ev.SessionID
		}
	}

	return fullResponse, toolSummary, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
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
