package claudecode

import "testing"

func TestClaudeSession_SupportsMidTurnSteering(t *testing.T) {
	var s *claudeSession
	if s.SupportsMidTurnSteering() != true {
		t.Fatal("claudeSession must declare mid-turn steering support (stdin folds into the running turn)")
	}
}
