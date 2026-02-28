package core

import "time"

// Message represents a unified incoming message from any platform.
type Message struct {
	SessionKey string // unique key for user context, e.g. "feishu:{chatID}:{userID}"
	Platform   string
	UserID     string
	UserName   string
	Content    string
	ReplyCtx   any // platform-specific context needed for replying
}

// EventType distinguishes different kinds of agent output.
type EventType string

const (
	EventText    EventType = "text"     // intermediate or final text
	EventToolUse EventType = "tool_use" // tool invocation info
	EventResult  EventType = "result"   // final aggregated result
	EventError   EventType = "error"
)

// Event represents a single piece of agent output streamed back to the engine.
type Event struct {
	Type      EventType
	Content   string
	ToolName  string // populated for EventToolUse
	ToolInput string // human-readable summary of tool input
	SessionID string // agent-managed session ID for conversation continuity
	Done      bool
	Error     error
}

// HistoryEntry is one turn in a conversation.
type HistoryEntry struct {
	Role      string    `json:"role"` // "user" or "assistant"
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// AgentSessionInfo describes one session as reported by the agent backend.
type AgentSessionInfo struct {
	ID           string
	Summary      string
	MessageCount int
	ModifiedAt   time.Time
	GitBranch    string
}
