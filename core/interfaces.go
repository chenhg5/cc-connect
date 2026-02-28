package core

import "context"

// Platform abstracts a messaging platform (Feishu, DingTalk, Slack, etc.).
// Each implementation handles its own connection protocol (WebSocket, HTTP, etc.)
// and translates platform-specific messages into the unified Message type.
type Platform interface {
	Name() string
	Start(handler MessageHandler) error
	Reply(ctx context.Context, replyCtx any, content string) error
	Stop() error
}

// MessageHandler is called by platforms when a new message arrives.
// The Platform reference is passed so the engine knows which platform to reply through.
type MessageHandler func(p Platform, msg *Message)

// Agent abstracts an AI coding assistant (Claude Code, Cursor, Gemini CLI, etc.).
// Execute sends a prompt and returns a channel that streams response chunks.
type Agent interface {
	Name() string
	Execute(ctx context.Context, sessionID string, prompt string) (<-chan Response, error)
	Stop() error
}
