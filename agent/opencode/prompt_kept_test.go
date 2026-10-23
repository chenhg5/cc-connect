package opencode

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// A retried OpenCode turn must not replay the prompt: the conversation already
// holds it, so a replay would add it (and its images) once more per retry.
func TestAgentKeepsPromptOnRetriableError(t *testing.T) {
	var a any = &Agent{}
	k, ok := a.(core.PromptKeptOnErrorAgent)
	if !ok || !k.PromptKeptOnRetriableError() {
		t.Fatal("opencode agent must report that a retriable error leaves the prompt recorded")
	}
}
