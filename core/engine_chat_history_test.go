package core

import (
	"os"
	"path/filepath"
	"testing"
)

// observeChatMessage must be a no-op when chat-history sync is disabled and must
// write when enabled (given an explicit resolved workspace).
func TestObserveChatMessageRespectsSyncFlag(t *testing.T) {
	ws := t.TempDir()
	e := &Engine{chatHistory: NewChatHistoryWriter()}
	msg := &Message{UserName: "Jay", Content: "hi"}
	path := filepath.Join(ws, chatHistoryFileName)

	e.chatHistorySync = false
	e.observeChatMessage(msg, ws, "Jay", "hi")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("sync disabled must not write, err=%v", err)
	}

	e.chatHistorySync = true
	e.observeChatMessage(msg, ws, "Jay", "hi")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("sync enabled must write, err=%v", err)
	}
}

// The isolation guarantee: when no workspace is given and the seat has neither a
// workspace pattern nor topic isolation, there is no shared/default file to fall
// back to — the write is skipped entirely.
func TestObserveChatMessageNoSharedFallback(t *testing.T) {
	e := &Engine{
		chatHistory:     NewChatHistoryWriter(),
		chatHistorySync: true,
		// workspacePattern == "" and dispatchTopicIsolation == false
	}
	msg := &Message{UserName: "Jay", Content: "hi", ChannelKey: "telegram:-100:42"}
	// Must not panic and must not write anywhere resolvable.
	e.observeChatMessage(msg, "", "Jay", "hi")
}
