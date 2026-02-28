package core

import (
	"context"
	"fmt"
	"log/slog"
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
	if strings.TrimSpace(msg.Content) == "" {
		return
	}

	session := e.sessions.GetOrCreate(msg.SessionKey)
	if !session.TryLock() {
		_ = p.Reply(e.ctx, msg.ReplyCtx, "⏳ 上一个请求还在处理中，请稍后再试...")
		return
	}

	slog.Info("processing message",
		"platform", msg.Platform,
		"user", msg.UserName,
		"session", msg.SessionKey,
	)

	go func() {
		defer session.Unlock()
		e.processMessage(p, msg, session)
	}()
}

func (e *Engine) processMessage(p Platform, msg *Message, session *Session) {
	responseCh, err := e.agent.Execute(e.ctx, session.AgentSessionID, msg.Content)
	if err != nil {
		slog.Error("agent execution failed", "error", err)
		_ = p.Reply(e.ctx, msg.ReplyCtx, fmt.Sprintf("❌ 执行失败: %v", err))
		return
	}

	var fullResponse string
	for resp := range responseCh {
		if resp.Error != nil {
			slog.Error("agent response error", "error", resp.Error)
			_ = p.Reply(e.ctx, msg.ReplyCtx, fmt.Sprintf("❌ 响应错误: %v", resp.Error))
			return
		}
		if resp.SessionID != "" && session.AgentSessionID == "" {
			session.AgentSessionID = resp.SessionID
		}
		fullResponse = resp.Content
	}

	if fullResponse == "" {
		fullResponse = "(empty response)"
	}

	for _, chunk := range splitMessage(fullResponse, maxPlatformMessageLen) {
		if err := p.Reply(e.ctx, msg.ReplyCtx, chunk); err != nil {
			slog.Error("failed to send reply", "platform", p.Name(), "error", err)
			return
		}
	}
}

// splitMessage breaks long text into chunks that fit platform message limits.
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
