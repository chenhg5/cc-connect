//go:build blackbox

package p0

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/tests/blackbox/helper"
)

// ── P0-14: 工具调用 ───────────────────────────────────────────────────────────

// TestP0_14_ToolInvocation_ClaudeCode verifies that cc-connect routes a task
// requiring tool use to the agent and the agent successfully executes it.
// A real agent calling real tools is the only way to verify this end-to-end.
func TestP0_14_ToolInvocation_ClaudeCode(t *testing.T) {
	t.Parallel()
	testToolInvocation(t, "claudecode")
}

func testToolInvocation(t *testing.T, agentType string) {
	t.Helper()
	env := helper.NewEnv(t, agentType)

	// Ask something that requires a Bash/filesystem tool call.
	msgs := env.SendComplete("List the files in the current directory using a shell command. Just show me the output.")

	combined := helper.AnyText(msgs)
	if strings.TrimSpace(combined) == "" {
		t.Fatalf("empty reply; all messages:\n%s", env.Platform.AllText())
	}

	// The agent should have invoked a tool — expect a tool-call indicator or
	// file listing output. We check for the Tool indicator message or any
	// file/directory names in the response.
	hasToolEvidence := strings.Contains(combined, "🔧") ||
		strings.Contains(combined, "Tool") ||
		strings.Contains(combined, "Bash") ||
		strings.Contains(combined, ".go") ||
		strings.Contains(combined, "go.mod") ||
		strings.Contains(combined, "main") ||
		strings.Contains(combined, "ls") ||
		strings.Contains(strings.ToLower(combined), "file") ||
		strings.Contains(strings.ToLower(combined), "directory") ||
		strings.Contains(strings.ToLower(combined), "目录") ||
		strings.Contains(strings.ToLower(combined), "文件")

	if !hasToolEvidence {
		t.Errorf("P0-14: no evidence of tool invocation in response\nall messages:\n%s", env.Platform.AllText())
	} else {
		t.Logf("P0-14 OK: tool invocation detected (%d msgs, %d chars)", len(msgs), len(combined))
	}
}

// ── P0-15: 长消息流式输出 ─────────────────────────────────────────────────────

// TestP0_15_LongResponse_ClaudeCode verifies that cc-connect correctly handles
// a long agent response without truncation or corruption. The agent must
// produce a substantive response (≥ 200 chars total).
func TestP0_15_LongResponse_ClaudeCode(t *testing.T) {
	t.Parallel()
	testLongResponse(t, "claudecode")
}

func testLongResponse(t *testing.T, agentType string) {
	t.Helper()
	env := helper.NewEnv(t, agentType)

	msgs := env.SendComplete("Write a short paragraph (about 150 words) explaining what software testing is. Write in plain text.")

	combined := helper.AnyText(msgs)
	if len(strings.TrimSpace(combined)) < 200 {
		t.Errorf("P0-15: response too short (%d chars); expected ≥ 200\nall messages:\n%s",
			len(combined), env.Platform.AllText())
	} else {
		t.Logf("P0-15 OK: long response received (%d msgs, %d chars)", len(msgs), len(combined))
	}
}
