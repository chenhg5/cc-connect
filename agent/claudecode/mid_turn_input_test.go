package claudecode

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Claude Code reads stream-json user messages from one persistent stdin, so
// the engine may deliver busy-time messages straight into the running turn.
func TestClaudeSession_AcceptsMidTurnInput(t *testing.T) {
	var s core.AgentSession = &claudeSession{}
	sender, ok := s.(core.MidTurnInputSender)
	if !ok || !sender.AcceptsMidTurnInput() {
		t.Fatal("claudeSession must accept mid-turn input")
	}
}
