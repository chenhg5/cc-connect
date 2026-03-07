package iflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadExecutionInfoSessionID(t *testing.T) {
	f, err := os.CreateTemp("", "iflow-exec-info-*.json")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(f.Name())

	if _, err := f.WriteString(`{"session-id":"session-123"}`); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	f.Close()

	sid, err := readExecutionInfoSessionID(f.Name())
	if err != nil {
		t.Fatalf("readExecutionInfoSessionID: %v", err)
	}
	if sid != "session-123" {
		t.Fatalf("session id = %q, want session-123", sid)
	}
}

func TestExtractSessionIDFromExecutionInfo(t *testing.T) {
	stderr := `<Execution Info>
{
  "session-id": "session-abc",
  "conversation-id": "cid"
}
</Execution Info>`
	if got := extractSessionIDFromExecutionInfo(stderr); got != "session-abc" {
		t.Fatalf("extractSessionIDFromExecutionInfo = %q", got)
	}
}

func TestIsIFlowAPIFailure(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"Error when talking to iFlow API", true},
		{"Attempt 1 failed. Retrying with backoff... Error: generate data error: fetch failed", true},
		{"normal log only", false},
	}

	for _, tc := range cases {
		if got := isIFlowAPIFailure(tc.in); got != tc.want {
			t.Fatalf("isIFlowAPIFailure(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSummarizeIFlowError(t *testing.T) {
	err := summarizeIFlowError("Error when talking to iFlow API\n\n<Execution Info>", nil)
	if err == nil || err.Error() != "Error when talking to iFlow API" {
		t.Fatalf("unexpected error summary: %v", err)
	}
}

func TestExtractIFlowAssistantEvents(t *testing.T) {
	content := []any{
		map[string]any{"type": "text", "text": "checking"},
		map[string]any{
			"type":  "tool_use",
			"id":    "read_file:1",
			"name":  "read_file",
			"input": map[string]any{"absolute_path": "/tmp/demo.txt"},
		},
	}

	texts, tools := extractIFlowAssistantEvents(content)
	if len(texts) != 1 || texts[0] != "checking" {
		t.Fatalf("texts = %#v", texts)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	if tools[0].ID != "read_file:1" || tools[0].Name != "read_file" {
		t.Fatalf("tool = %#v", tools[0])
	}
	if got := summarizeIFlowToolInput(tools[0].Input); got != "/tmp/demo.txt" {
		t.Fatalf("summarizeIFlowToolInput = %q", got)
	}
}

func TestExtractIFlowToolResults(t *testing.T) {
	content := []any{
		map[string]any{
			"type":        "tool_result",
			"tool_use_id": "read_file:1",
			"content": map[string]any{
				"functionResponse": map[string]any{
					"response": map[string]any{
						"output": "alpha",
					},
				},
			},
		},
		map[string]any{
			"type":        "tool_result",
			"tool_use_id": "glob:2",
			"content": map[string]any{
				"responseParts": map[string]any{
					"functionResponse": map[string]any{
						"response": map[string]any{
							"output": "beta",
						},
					},
				},
			},
		},
	}

	results := extractIFlowToolResults(content)
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	if results[0].ID != "read_file:1" || results[0].Output != "alpha" {
		t.Fatalf("result[0] = %#v", results[0])
	}
	if results[1].ID != "glob:2" || results[1].Output != "beta" {
		t.Fatalf("result[1] = %#v", results[1])
	}
}

func TestSummarizeIFlowToolResultFallback(t *testing.T) {
	content := map[string]any{
		"resultDisplay": "Search results returned.",
	}
	if got := summarizeIFlowToolResult(content); got != "Search results returned." {
		t.Fatalf("summarizeIFlowToolResult = %q", got)
	}
}

func TestStripANSI(t *testing.T) {
	in := "\x1b]2;iFlow\x07\x1b[2K\rHello\x1b[?25l"
	if got := stripANSI(in); got != "Hello" {
		t.Fatalf("stripANSI = %q", got)
	}
}

func TestFindIFlowTranscriptPath(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "session-old.jsonl")
	newPath := filepath.Join(dir, "session-new.jsonl")
	if err := os.WriteFile(oldPath, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("WriteFile old: %v", err)
	}
	if err := os.WriteFile(newPath, []byte("new\n"), 0o644); err != nil {
		t.Fatalf("WriteFile new: %v", err)
	}

	startedAt := time.Now()
	oldTime := startedAt.Add(-5 * time.Second)
	newTime := startedAt.Add(1 * time.Second)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes old: %v", err)
	}
	if err := os.Chtimes(newPath, newTime, newTime); err != nil {
		t.Fatalf("Chtimes new: %v", err)
	}

	if got := findIFlowTranscriptPath(dir, startedAt); got != newPath {
		t.Fatalf("findIFlowTranscriptPath = %q, want %q", got, newPath)
	}
}

func TestIFlowTurnFinalContentFallsBackToToolResult(t *testing.T) {
	turn := &iflowTurn{
		resultText:   "我来查一下。",
		toolFallback: []string{"天气结果"},
		awaitingTool: true,
	}

	if got := turn.finalContent(); got != "我来查一下。\n\n天气结果" {
		t.Fatalf("finalContent = %q", got)
	}
}

func TestIFlowTurnIgnoresDuplicateToolUseAfterToolResult(t *testing.T) {
	turn := &iflowTurn{
		pendingToolIDs: make(map[string]struct{}),
		pendingTools:   make(map[string]iflowToolUse),
		seenToolIDs:    make(map[string]struct{}),
		doneToolIDs:    make(map[string]struct{}),
	}

	added := turn.addPendingTools([]iflowToolUse{{ID: "web_search:0", Name: "web_search"}})
	if len(added) != 1 || !turn.hasPendingTools() {
		t.Fatalf("first add = %#v pending=%v", added, turn.hasPendingTools())
	}

	if !turn.completeTools([]iflowToolResult{{ID: "web_search:0", Output: "search result"}}) {
		t.Fatal("expected tool completion to finish pending set")
	}
	if turn.hasPendingTools() {
		t.Fatal("expected no pending tools after completion")
	}
	if !turn.awaitingTool {
		t.Fatal("expected awaitingTool after tool completion")
	}

	added = turn.addPendingTools([]iflowToolUse{{ID: "web_search:0", Name: "web_search"}})
	if len(added) != 0 {
		t.Fatalf("duplicate add = %#v", added)
	}
	if turn.hasPendingTools() {
		t.Fatal("duplicate tool_use should not recreate pending tool state")
	}
	if !turn.awaitingTool {
		t.Fatal("duplicate tool_use should not clear awaitingTool")
	}

	turn.appendText("北京今天多云")
	if turn.awaitingTool {
		t.Fatal("assistant text should clear awaitingTool")
	}
	if got := turn.finalContent(); got != "北京今天多云" {
		t.Fatalf("finalContent = %q", got)
	}
}

func TestIFlowTurnScheduleResultReplacesFallbackTimer(t *testing.T) {
	turn := &iflowTurn{
		cancel:         func() {},
		pendingToolIDs: make(map[string]struct{}),
		pendingTools:   make(map[string]iflowToolUse),
		awaitingTool:   false,
	}
	turn.resultTimer = time.AfterFunc(time.Hour, func() {})
	defer turn.stopResultTimer()

	turn.scheduleResult(nil)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if turn.readyForResult() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("result timer did not fire; callback was not replaced")
}

func TestIFlowTurnPendingToolTimeoutReleasesTurn(t *testing.T) {
	oldTimeout := iflowPendingToolTimeout
	iflowPendingToolTimeout = 50 * time.Millisecond
	defer func() { iflowPendingToolTimeout = oldTimeout }()

	turn := &iflowTurn{
		cancel:         func() {},
		pendingToolIDs: make(map[string]struct{}),
		pendingTools:   make(map[string]iflowToolUse),
		seenToolIDs:    make(map[string]struct{}),
		doneToolIDs:    make(map[string]struct{}),
	}
	defer turn.stopResultTimer()

	added := turn.addPendingTools([]iflowToolUse{{ID: "run_shell_command:1", Name: "run_shell_command"}})
	if len(added) != 1 {
		t.Fatalf("added = %#v", added)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if turn.readyForResult() {
			content := turn.finalContent()
			if !strings.Contains(content, "timed out") || !strings.Contains(content, "run_shell_command") {
				t.Fatalf("finalContent = %q", content)
			}
			if turn.hasPendingTools() {
				t.Fatal("pending tools should be cleared after timeout")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("pending tool timeout did not release turn")
}

func TestIFlowSessionPendingToolTimeoutClearsBusyState(t *testing.T) {
	oldTimeout := iflowPendingToolTimeout
	oldDefaultTimeout := iflowPendingToolTimeoutDefaultMode
	iflowPendingToolTimeout = 80 * time.Millisecond
	iflowPendingToolTimeoutDefaultMode = 80 * time.Millisecond
	defer func() { iflowPendingToolTimeout = oldTimeout }()
	defer func() { iflowPendingToolTimeoutDefaultMode = oldDefaultTimeout }()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	workDir := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("MkdirAll workDir: %v", err)
	}

	projectKey := iflowProjectKey(iflowResolvedWorkDir(workDir))
	t.Setenv("IFLOW_TEST_PROJECT_KEY", projectKey)

	cmdPath := filepath.Join(t.TempDir(), "fake-iflow.sh")
	script := `#!/bin/sh
set -eu
sid="session-test"
session_dir="$HOME/.iflow/projects/$IFLOW_TEST_PROJECT_KEY"
mkdir -p "$session_dir"
transcript="$session_dir/$sid.jsonl"
cat >>"$transcript" <<'EOF'
{"sessionId":"session-test","type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"run_shell_command:1","name":"run_shell_command","input":{"command":"ls -la"}}]}}
{"sessionId":"session-test","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}
EOF
while :; do sleep 1; done
`
	if err := os.WriteFile(cmdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile fake iflow: %v", err)
	}

	sess, err := newIFlowSession(context.Background(), cmdPath, workDir, "", "default", "", nil)
	if err != nil {
		t.Fatalf("newIFlowSession: %v", err)
	}
	defer sess.Close()

	if err := sess.Send("执行ls", nil); err != nil {
		t.Fatalf("Send #1: %v", err)
	}

	deadline := time.After(5 * time.Second)
	gotResult := false
	for !gotResult {
		select {
		case ev := <-sess.Events():
			if ev.Type != "result" {
				continue
			}
			gotResult = true
			if !strings.Contains(ev.Content, "timed out") {
				t.Fatalf("result content = %q", ev.Content)
			}
		case <-deadline:
			t.Fatal("timeout waiting for result")
		}
	}

	if err := sess.Send("第二条消息", nil); err != nil {
		if strings.Contains(err.Error(), "busy") {
			t.Fatalf("session still busy after timeout result: %v", err)
		}
		t.Fatalf("Send #2 failed: %v", err)
	}
}
