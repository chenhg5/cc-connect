package core

import "context"

// Platform abstracts a messaging platform (Feishu, DingTalk, Slack, etc.).
type Platform interface {
	Name() string
	Start(handler MessageHandler) error
	Reply(ctx context.Context, replyCtx any, content string) error
	Stop() error
}

// MessageHandler is called by platforms when a new message arrives.
type MessageHandler func(p Platform, msg *Message)

// Agent abstracts an AI coding assistant (Claude Code, Cursor, Gemini CLI, etc.).
type Agent interface {
	Name() string
	Execute(ctx context.Context, sessionID string, prompt string) (<-chan Event, error)
	// ListSessions returns sessions known to the agent backend (e.g. from Claude Code's session index).
	ListSessions(ctx context.Context) ([]AgentSessionInfo, error)
	Stop() error
}
