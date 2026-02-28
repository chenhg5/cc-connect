package core

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

const maxPlatformMessageLen = 4000

// Engine routes messages between platforms and the agent.
type Engine struct {
	agent     Agent
	platforms []Platform
	sessions  *SessionManager
	ctx       context.Context
	cancel    context.CancelFunc
}

func NewEngine(ag Agent, platforms []Platform) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		agent:     ag,
		platforms: platforms,
		sessions:  NewSessionManager(),
		ctx:       ctx,
		cancel:    cancel,
	}
}

func (e *Engine) Start() error {
	for _, p := range e.platforms {
		if err := p.Start(e.handleMessage); err != nil {
			return fmt.Errorf("start platform %s: %w", p.Name(), err)
		}
		slog.Info("platform started", "name", p.Name())
	}
	slog.Info("engine started", "agent", e.agent.Name(), "platforms", len(e.platforms))
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
	sessions := e.sessions.ListSessions(msg.SessionKey)
	if len(sessions) == 0 {
		_ = p.Reply(e.ctx, msg.ReplyCtx, "No sessions yet. Send a message to start one.")
		return
	}

	activeID := e.sessions.ActiveSessionID(msg.SessionKey)
	var sb strings.Builder
	sb.WriteString("📋 Sessions:\n\n")
	for _, s := range sessions {
		marker := "  "
		if s.ID == activeID {
			marker = "▶ "
		}
		sb.WriteString(fmt.Sprintf("%s%s | %s | %d msgs | updated %s\n",
			marker, s.ID, s.Name, len(s.History), s.UpdatedAt.Format("01-02 15:04")))
	}
	_ = p.Reply(e.ctx, msg.ReplyCtx, sb.String())
}

func (e *Engine) cmdSwitch(p Platform, msg *Message, args []string) {
	if len(args) == 0 {
		_ = p.Reply(e.ctx, msg.ReplyCtx, "Usage: /switch <session_id or name>")
		return
	}
	target := strings.Join(args, " ")
	s, err := e.sessions.SwitchSession(msg.SessionKey, target)
	if err != nil {
		_ = p.Reply(e.ctx, msg.ReplyCtx, fmt.Sprintf("❌ %v", err))
		return
	}
	_ = p.Reply(e.ctx, msg.ReplyCtx,
		fmt.Sprintf("✅ Switched to: %s (id: %s, %d msgs)", s.Name, s.ID, len(s.History)))
}

func (e *Engine) cmdCurrent(p Platform, msg *Message) {
	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	_ = p.Reply(e.ctx, msg.ReplyCtx, fmt.Sprintf(
		"📌 Current session: %s (id: %s)\nMessages: %d\nCreated: %s\nUpdated: %s",
		s.Name, s.ID, len(s.History),
		s.CreatedAt.Format("2006-01-02 15:04:05"),
		s.UpdatedAt.Format("2006-01-02 15:04:05")))
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
	_ = p.Reply(e.ctx, msg.ReplyCtx, `/new [name]       — Create a new session
/list             — List all sessions
/switch <id|name> — Switch to a session
/current          — Show current session info
/history [n]      — Show last n messages (default 10)
/help             — Show this help`)
}

// ──────────────────────────────────────────────────────────────
// Agent message processing
// ──────────────────────────────────────────────────────────────

func (e *Engine) processMessage(p Platform, msg *Message, session *Session) {
	session.AddHistory("user", msg.Content)

	responseCh, err := e.agent.Execute(e.ctx, session.AgentSessionID, msg.Content)
	if err != nil {
		slog.Error("agent execution failed", "error", err)
		_ = p.Reply(e.ctx, msg.ReplyCtx, fmt.Sprintf("❌ Execution failed: %v", err))
		return
	}

	var fullResponse string
	var toolSummary []string

	for ev := range responseCh {
		if ev.Error != nil {
			slog.Error("agent event error", "error", ev.Error)
			_ = p.Reply(e.ctx, msg.ReplyCtx, fmt.Sprintf("❌ Error: %v", ev.Error))
			return
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

	if fullResponse == "" {
		fullResponse = "(empty response)"
	}

	if len(toolSummary) > 0 {
		fullResponse += "\n\n---\n📎 Tools used:\n" + strings.Join(toolSummary, "\n")
	}

	session.AddHistory("assistant", fullResponse)

	for _, chunk := range splitMessage(fullResponse, maxPlatformMessageLen) {
		if err := p.Reply(e.ctx, msg.ReplyCtx, chunk); err != nil {
			slog.Error("failed to send reply", "platform", p.Name(), "error", err)
			return
		}
	}
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
