package opencode

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Mid-turn upstream overloads must be resumed, not reported: the agent opts into
// the engine's continue-after-error path.
var _ core.ContinueAfterErrorAgent = (*Agent)(nil)

func TestAgent_ContinueAfterErrorPrompt(t *testing.T) {
	if got := (&Agent{}).ContinueAfterErrorPrompt(); got != core.DefaultContinueAfterErrorPrompt {
		t.Fatalf("ContinueAfterErrorPrompt() = %q, want the default resume prompt", got)
	}
}
