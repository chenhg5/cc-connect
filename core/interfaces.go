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
// Execute sends a prompt and returns a channel that streams Event values.
// The channel may emit tool_use events (showing what the agent did), text events,
// and a final result event.
type Agent interface {
	Name() string
	Execute(ctx context.Context, sessionID string, prompt string) (<-chan Event, error)
	Stop() error
}
