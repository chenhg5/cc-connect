package core

// Message represents a unified incoming message from any platform.
type Message struct {
	SessionKey string // unique key for session lookup, e.g. "feishu:{chatID}:{userID}"
	Platform   string
	UserID     string
	UserName   string
	Content    string
	ReplyCtx   any // platform-specific context needed for replying
}

// Response represents a chunk of agent output.
type Response struct {
	Content   string
	SessionID string // agent-managed session ID for conversation continuity
	Done      bool
	Error     error
}
