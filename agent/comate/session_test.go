package comate

// Tests for the comate agent. Two layers:
//
//  1. Pure-logic tests (args building, delta conversion, mode normalization)
//     run without any process.
//  2. Process tests spawn a fake `comatecli` shell script (placed first on
//     PATH) that records its argv and emits canned event-stream JSONL, so
//     turn lifecycle, multi-turn resume, approval suspend/resume and force
//     kill are all exercised without the real CLI.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// ── pure-logic tests ──────────────────────────────────────────

func TestNormalizeMode(t *testing.T) {
	tests := map[any]string{
		"Agent": "agent",
		" ask":  "ask",
		"PLAN":  "plan",
		"cloud": "cloud",
		"Bot":   "bot",
		"":      "",
		"bogus": "",
		42:      "",
		nil:     "",
	}
	for raw, want := range tests {
		if got := normalizeMode(raw); got != want {
			t.Fatalf("normalizeMode(%v) = %q, want %q", raw, got, want)
		}
	}
}

func TestBuildRunArgs_FreshTurn(t *testing.T) {
	cs := newComateSession(context.Background(), "comatecli", nil, "/tmp/project", "", "", false, false, false, "", nil)

	args := cs.buildRunArgs("hello")

	want := []string{
		"run",
		"--display", "event-stream",
		"--cwd", "/tmp/project",
		"--disable-auto-approve",
		"-q", "hello",
	}
	if len(args) != len(want) {
		t.Fatalf("args len = %d, want %d, args=%v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q, args=%v", i, args[i], want[i], args)
		}
	}
}

func TestBuildRunArgs_ResumeUsesConversationFlag(t *testing.T) {
	cs := newComateSession(context.Background(), "comatecli", nil, "/tmp/project", "", "", false, false, false, "conv-1", nil)

	args := cs.buildRunArgs("next message")

	if !containsSequence(args, []string{"-c", "conv-1"}) {
		t.Fatalf("args missing -c conv-1: %v", args)
	}
	if !containsSequence(args, []string{"-q", "next message"}) {
		t.Fatalf("args missing query: %v", args)
	}
	if containsSequence(args, []string{"--approve"}) || containsSequence(args, []string{"--reject"}) {
		t.Fatalf("query turn must not carry approval flags: %v", args)
	}
}

func TestBuildRunArgs_AutoApproveOmitsDisableFlag(t *testing.T) {
	cs := newComateSession(context.Background(), "comatecli", nil, "/tmp/project", "", "", true, false, false, "", nil)

	args := cs.buildRunArgs("hello")

	if containsSequence(args, []string{"--disable-auto-approve"}) {
		t.Fatalf("auto_approve=true must omit --disable-auto-approve: %v", args)
	}
}

func TestBuildRunArgs_OptionalFlags(t *testing.T) {
	cs := newComateSession(context.Background(), "comatecli", nil, "/tmp/project", "GLM-5.3", "plan", false, true, true, "", nil)

	args := cs.buildRunArgs("hello")

	if !containsSequence(args, []string{"--model", "GLM-5.3"}) {
		t.Fatalf("args missing --model: %v", args)
	}
	if !containsSequence(args, []string{"--mode", "plan"}) {
		t.Fatalf("args missing --mode: %v", args)
	}
	if !containsSequence(args, []string{"--disable-hooks"}) {
		t.Fatalf("args missing --disable-hooks: %v", args)
	}
	if !containsSequence(args, []string{"--enable-interactive-tools"}) {
		t.Fatalf("args missing --enable-interactive-tools: %v", args)
	}
}

func TestBuildRunArgs_DefaultWorkDirOmitsCwd(t *testing.T) {
	cs := newComateSession(context.Background(), "comatecli", nil, ".", "", "", false, false, false, "", nil)

	args := cs.buildRunArgs("hello")

	if containsSequence(args, []string{"--cwd"}) {
		t.Fatalf("default work_dir must omit --cwd: %v", args)
	}
}

func TestBuildResumeArgs(t *testing.T) {
	for _, decision := range []string{"--approve", "--reject"} {
		cs := newComateSession(context.Background(), "comatecli", nil, "/tmp/project", "m", "agent", false, false, false, "conv-1", nil)

		args := cs.buildResumeArgs(decision)

		want := []string{"run", "--display", "event-stream", "-c", "conv-1", decision, "--cwd", "/tmp/project"}
		if len(args) != len(want) {
			t.Fatalf("args len = %d, want %d, args=%v", len(args), len(want), args)
		}
		for i := range want {
			if args[i] != want[i] {
				t.Fatalf("args[%d] = %q, want %q, args=%v", i, args[i], want[i], args)
			}
		}
	}
}

func TestContinueSessionTreatedAsFresh(t *testing.T) {
	cs := newComateSession(context.Background(), "comatecli", nil, "/tmp", "", "", false, false, false, core.ContinueSession, nil)
	defer cs.Close()

	if got := cs.CurrentSessionID(); got != "" {
		t.Errorf("ContinueSession should be treated as fresh: sessionID = %q, want empty", got)
	}
}

func TestEmitTextDelta(t *testing.T) {
	cs := newComateSession(context.Background(), "comatecli", nil, "/tmp", "", "", false, false, false, "", nil)
	defer cs.Close()

	// First snapshot → full text.
	cs.emitTextDelta("ok")
	if ev := nextEvent(t, cs); ev.Type != core.EventText || ev.Content != "ok" {
		t.Fatalf("first delta = %#v, want EventText{ok}", ev)
	}

	// Accumulated snapshot → suffix only.
	cs.emitTextDelta("ok world")
	if ev := nextEvent(t, cs); ev.Content != " world" {
		t.Fatalf("second delta = %#v, want EventText{\" world\"}", ev)
	}

	// Identical snapshot → no event.
	cs.emitTextDelta("ok world")
	select {
	case ev := <-cs.Events():
		t.Fatalf("identical snapshot emitted event: %#v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	// Non-prefix snapshot → re-anchored block.
	cs.emitTextDelta("fresh")
	if ev := nextEvent(t, cs); ev.Content != "\n\nfresh" {
		t.Fatalf("reset delta = %#v, want EventText{\"\\n\\nfresh\"}", ev)
	}
}

func TestExtractAssistantText(t *testing.T) {
	messages := []comateMessage{
		{Role: "user", Elements: []comateElement{{Children: []comateChild{{Type: "TEXT", Content: "user text"}}}}},
		{Role: "assistant", Elements: []comateElement{
			{Children: []comateChild{{Type: "TEXT", Content: "part1"}}},
			{Children: []comateChild{{Type: "TEXT", Content: "part2"}, {Type: "OTHER", Content: "ignored"}}},
		}},
		{Role: "assistant", Elements: []comateElement{{Children: []comateChild{{Type: "TEXT", Content: "part3"}}}}},
	}
	if got := extractAssistantText(messages); got != "part1part2part3" {
		t.Fatalf("extractAssistantText = %q, want %q", got, "part1part2part3")
	}
	if got := extractAssistantText(nil); got != "" {
		t.Fatalf("extractAssistantText(nil) = %q, want empty", got)
	}
}

func TestSummarizeApproval(t *testing.T) {
	if got := summarizeApproval(comateApproval{ToolName: "run_command", Command: "echo hi"}); got != "echo hi" {
		t.Fatalf("summarizeApproval = %q, want command", got)
	}
	got := summarizeApproval(comateApproval{ToolName: "edit_file", Params: map[string]any{"path": "a.go"}})
	if !strings.Contains(got, "a.go") {
		t.Fatalf("summarizeApproval(params) = %q, want params JSON", got)
	}
	if got := summarizeApproval(comateApproval{ToolName: "mystery"}); got != "mystery" {
		t.Fatalf("summarizeApproval = %q, want tool name", got)
	}
}

// ── process tests (fake comatecli on PATH) ────────────────────

func TestSend_FreshTurn_AnnouncesConversationIDAndStreams(t *testing.T) {
	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	argsFile := filepath.Join(workDir, "args.txt")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$COMATE_ARGS_FILE\"\n" +
		"printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-new\",\"status\":\"Running\"}}'\n" +
		"printf '%s\\n' '{\"type\":\"partial-message-data\",\"messages\":[{\"role\":\"assistant\",\"elements\":[{\"children\":[{\"type\":\"TEXT\",\"content\":\"ok\"}]}]}]}'\n" +
		"printf '%s\\n' '{\"type\":\"partial-message-data\",\"messages\":[{\"role\":\"assistant\",\"elements\":[{\"children\":[{\"type\":\"TEXT\",\"content\":\"ok world\"}]}]}]}'\n" +
		"printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-new\",\"status\":\"Completed\"}}'\n"
	powershellScript := `
[IO.File]::WriteAllLines($env:COMATE_ARGS_FILE, (fakeComateArgs))
[Console]::Out.WriteLine('{"type":"conversation-status","conversationInfo":{"id":"conv-new","status":"Running"}}')
[Console]::Out.WriteLine('{"type":"partial-message-data","messages":[{"role":"assistant","elements":[{"children":[{"type":"TEXT","content":"ok"}]}]}]}')
[Console]::Out.WriteLine('{"type":"partial-message-data","messages":[{"role":"assistant","elements":[{"children":[{"type":"TEXT","content":"ok world"}]}]}]}')
[Console]::Out.WriteLine('{"type":"conversation-status","conversationInfo":{"id":"conv-new","status":"Completed"}}')
`
	writeFakeComateScript(t, binDir, script, powershellScript)

	t.Setenv("COMATE_ARGS_FILE", argsFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cs := newComateSession(context.Background(), "comatecli", nil, workDir, "", "", false, false, false, "", nil)
	defer cs.Close()

	if err := cs.Send("hello", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// 1. Session ID announcement.
	ev := nextEvent(t, cs)
	if ev.Type != core.EventText || ev.SessionID != "conv-new" || ev.Content != "" {
		t.Fatalf("first event = %#v, want EventText{SessionID: conv-new, Content: \"\"}", ev)
	}
	if got := cs.CurrentSessionID(); got != "conv-new" {
		t.Fatalf("CurrentSessionID = %q, want conv-new", got)
	}

	// 2-3. Delta text.
	if ev := nextEvent(t, cs); ev.Type != core.EventText || ev.Content != "ok" {
		t.Fatalf("second event = %#v, want EventText{ok}", ev)
	}
	if ev := nextEvent(t, cs); ev.Type != core.EventText || ev.Content != " world" {
		t.Fatalf("third event = %#v, want EventText{\" world\"}", ev)
	}

	// 4. Turn result.
	if ev := nextEvent(t, cs); ev.Type != core.EventResult || !ev.Done || ev.SessionID != "conv-new" {
		t.Fatalf("fourth event = %#v, want EventResult{Done: true, SessionID: conv-new}", ev)
	}

	args := waitForArgsFile(t, argsFile)
	if !containsSequence(args, []string{"run", "--display", "event-stream"}) ||
		!containsSequence(args, []string{"--disable-auto-approve", "-q", "hello"}) {
		t.Fatalf("args missing run prelude: %v", args)
	}
	if containsSequence(args, []string{"-c"}) {
		t.Fatalf("fresh turn must not carry -c: %v", args)
	}
}

func TestSend_MultiTurn_UsesConversationID(t *testing.T) {
	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	argsFile := filepath.Join(workDir, "args.txt")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$COMATE_ARGS_FILE\"\n" +
		"printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Running\"}}'\n" +
		"printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Completed\"}}'\n"
	powershellScript := `
[IO.File]::WriteAllLines($env:COMATE_ARGS_FILE, (fakeComateArgs))
[Console]::Out.WriteLine('{"type":"conversation-status","conversationInfo":{"id":"conv-1","status":"Running"}}')
[Console]::Out.WriteLine('{"type":"conversation-status","conversationInfo":{"id":"conv-1","status":"Completed"}}')
`
	writeFakeComateScript(t, binDir, script, powershellScript)

	t.Setenv("COMATE_ARGS_FILE", argsFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cs := newComateSession(context.Background(), "comatecli", nil, workDir, "", "", false, false, false, "", nil)
	defer cs.Close()

	if err := cs.Send("first", "", nil, nil); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if ev := nextEvent(t, cs); ev.Type != core.EventText || ev.SessionID != "conv-1" {
		t.Fatalf("announcement = %#v, want EventText{SessionID: conv-1}", ev)
	}
	if ev := nextEvent(t, cs); ev.Type != core.EventResult || !ev.Done {
		t.Fatalf("first result = %#v, want EventResult{Done: true}", ev)
	}

	// Wait for the first process to fully release the turn guard before
	// sending the follow-up (EventResult may precede process exit).
	waitTurnIdle(t, cs)

	if err := cs.Send("second", "", nil, nil); err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if ev := nextEvent(t, cs); ev.Type != core.EventResult || !ev.Done {
		t.Fatalf("second result = %#v, want EventResult{Done: true}", ev)
	}

	args := waitForArgsFile(t, argsFile)
	if !containsSequence(args, []string{"-c", "conv-1"}) || !containsSequence(args, []string{"-q", "second"}) {
		t.Fatalf("second turn args missing -c conv-1 / -q second: %v", args)
	}
}

func TestSend_SuspendedTurn_EmitsNoEventResult(t *testing.T) {
	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	argsFile := filepath.Join(workDir, "args.txt")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$COMATE_ARGS_FILE\"\n" +
		"printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Running\"}}'\n" +
		"printf '%s\\n' '{\"type\":\"pending-approvals\",\"pendingApprovals\":[{\"toolId\":\"call_1\",\"conversationId\":\"conv-1\",\"toolName\":\"run_command\",\"command\":\"echo hi > f.txt\",\"params\":{\"command\":\"echo hi > f.txt\"}}]}'\n" +
		"printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Completed\"}}'\n"
	powershellScript := `
[IO.File]::WriteAllLines($env:COMATE_ARGS_FILE, (fakeComateArgs))
[Console]::Out.WriteLine('{"type":"conversation-status","conversationInfo":{"id":"conv-1","status":"Running"}}')
[Console]::Out.WriteLine('{"type":"pending-approvals","pendingApprovals":[{"toolId":"call_1","conversationId":"conv-1","toolName":"run_command","command":"echo hi > f.txt","params":{"command":"echo hi > f.txt"}}]}')
[Console]::Out.WriteLine('{"type":"conversation-status","conversationInfo":{"id":"conv-1","status":"Completed"}}')
`
	writeFakeComateScript(t, binDir, script, powershellScript)

	t.Setenv("COMATE_ARGS_FILE", argsFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cs := newComateSession(context.Background(), "comatecli", nil, workDir, "", "", false, false, false, "", nil)
	defer cs.Close()

	if err := cs.Send("create a file", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	ev := nextEvent(t, cs)
	if ev.Type != core.EventText || ev.SessionID != "conv-1" {
		t.Fatalf("announcement = %#v, want EventText{SessionID: conv-1}", ev)
	}

	ev = nextEvent(t, cs)
	if ev.Type != core.EventPermissionRequest {
		t.Fatalf("second event = %#v, want EventPermissionRequest", ev)
	}
	if ev.RequestID != "call_1" || ev.ToolName != "run_command" {
		t.Fatalf("permission request = %#v, want RequestID call_1 ToolName run_command", ev)
	}
	if !strings.Contains(ev.ToolInput, "echo hi > f.txt") {
		t.Fatalf("ToolInput = %q, want command summary", ev.ToolInput)
	}
	if got, _ := ev.ToolInputRaw["command"].(string); got != "echo hi > f.txt" {
		t.Fatalf("ToolInputRaw[command] = %q, want raw params echo", got)
	}

	// The turn is suspended: no terminal event may arrive.
	select {
	case e := <-cs.Events():
		t.Fatalf("suspended turn emitted terminal event: %#v", e)
	case <-time.After(1200 * time.Millisecond):
	}

	if !cs.Alive() {
		t.Fatal("session must stay alive while suspended")
	}
	if !cs.hasPending() {
		t.Fatal("pending approval must be registered")
	}
}

func TestRespondPermission_ApproveSpawnsNewProcess(t *testing.T) {
	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	argsFile := filepath.Join(workDir, "args.txt")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$COMATE_ARGS_FILE\"\n" +
		"case \"$*\" in\n" +
		"  *--approve*)\n" +
		"    printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Running\"}}'\n" +
		"    printf '%s\\n' '{\"type\":\"partial-message-data\",\"messages\":[{\"role\":\"assistant\",\"elements\":[{\"children\":[{\"type\":\"TEXT\",\"content\":\"done\"}]}]}]}'\n" +
		"    printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Completed\"}}'\n" +
		"    ;;\n" +
		"  *)\n" +
		"    printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Running\"}}'\n" +
		"    printf '%s\\n' '{\"type\":\"pending-approvals\",\"pendingApprovals\":[{\"toolId\":\"call_1\",\"conversationId\":\"conv-1\",\"toolName\":\"run_command\",\"command\":\"echo hi\"}]}'\n" +
		"    printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Completed\"}}'\n" +
		"    ;;\n" +
		"esac\n"
	powershellScript := `
[IO.File]::WriteAllLines($env:COMATE_ARGS_FILE, (fakeComateArgs))
$args = (fakeComateArgs) -join ' '
if ($args -like '*--approve*') {
  [Console]::Out.WriteLine('{"type":"conversation-status","conversationInfo":{"id":"conv-1","status":"Running"}}')
  [Console]::Out.WriteLine('{"type":"partial-message-data","messages":[{"role":"assistant","elements":[{"children":[{"type":"TEXT","content":"done"}]}]}]}')
  [Console]::Out.WriteLine('{"type":"conversation-status","conversationInfo":{"id":"conv-1","status":"Completed"}}')
} else {
  [Console]::Out.WriteLine('{"type":"conversation-status","conversationInfo":{"id":"conv-1","status":"Running"}}')
  [Console]::Out.WriteLine('{"type":"pending-approvals","pendingApprovals":[{"toolId":"call_1","conversationId":"conv-1","toolName":"run_command","command":"echo hi"}]}')
  [Console]::Out.WriteLine('{"type":"conversation-status","conversationInfo":{"id":"conv-1","status":"Completed"}}')
}
`
	writeFakeComateScript(t, binDir, script, powershellScript)

	t.Setenv("COMATE_ARGS_FILE", argsFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cs := newComateSession(context.Background(), "comatecli", nil, workDir, "", "", false, false, false, "", nil)
	defer cs.Close()

	if err := cs.Send("do it", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	nextEvent(t, cs) // announcement
	if ev := nextEvent(t, cs); ev.Type != core.EventPermissionRequest {
		t.Fatalf("expected permission request, got %#v", ev)
	}
	suspendArgs := waitForArgsFile(t, argsFile)
	if containsSequence(suspendArgs, []string{"--approve"}) {
		t.Fatalf("suspended turn must not carry --approve: %v", suspendArgs)
	}

	if err := cs.RespondPermission("call_1", core.PermissionResult{Behavior: "allow"}); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}

	args := waitForArgsFile(t, argsFile)
	if !containsSequence(args, []string{"run", "--display", "event-stream", "-c", "conv-1", "--approve"}) {
		t.Fatalf("resume args missing approval: %v", args)
	}
	if containsSequence(args, []string{"-q"}) {
		t.Fatalf("resume turn must not carry -q: %v", args)
	}
	if containsSequence(args, []string{"--disable-auto-approve"}) {
		t.Fatalf("resume turn must not carry --disable-auto-approve: %v", args)
	}

	if ev := nextEvent(t, cs); ev.Type != core.EventText || ev.Content != "done" {
		t.Fatalf("resume text = %#v, want EventText{done}", ev)
	}
	if ev := nextEvent(t, cs); ev.Type != core.EventResult || !ev.Done {
		t.Fatalf("resume result = %#v, want EventResult{Done: true}", ev)
	}
	if cs.hasPending() {
		t.Fatal("pending approval must be cleared after RespondPermission")
	}
}

func TestRespondPermission_DenySpawnsReject(t *testing.T) {
	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	argsFile := filepath.Join(workDir, "args.txt")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$COMATE_ARGS_FILE\"\n" +
		"case \"$*\" in\n" +
		"  *--approve*)\n" +
		"    printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Running\"}}'\n" +
		"    printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Completed\"}}'\n" +
		"    ;;\n" +
		"  *--reject*)\n" +
		"    printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Running\"}}'\n" +
		"    printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Completed\"}}'\n" +
		"    ;;\n" +
		"  *)\n" +
		"    printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Running\"}}'\n" +
		"    printf '%s\\n' '{\"type\":\"pending-approvals\",\"pendingApprovals\":[{\"toolId\":\"call_1\",\"toolName\":\"run_command\",\"command\":\"echo hi\"}]}'\n" +
		"    printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Completed\"}}'\n" +
		"    ;;\n" +
		"esac\n"
	powershellScript := `
[IO.File]::WriteAllLines($env:COMATE_ARGS_FILE, (fakeComateArgs))
[Console]::Out.WriteLine('{"type":"conversation-status","conversationInfo":{"id":"conv-1","status":"Running"}}')
if ((fakeComateArgs) -join ' ' -notlike '*--approve*') {
  [Console]::Out.WriteLine('{"type":"pending-approvals","pendingApprovals":[{"toolId":"call_1","toolName":"run_command","command":"echo hi"}]}')
}
[Console]::Out.WriteLine('{"type":"conversation-status","conversationInfo":{"id":"conv-1","status":"Completed"}}')
`
	writeFakeComateScript(t, binDir, script, powershellScript)

	t.Setenv("COMATE_ARGS_FILE", argsFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cs := newComateSession(context.Background(), "comatecli", nil, workDir, "", "", false, false, false, "", nil)
	defer cs.Close()

	if err := cs.Send("do it", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	nextEvent(t, cs) // announcement
	nextEvent(t, cs) // permission request
	waitTurnIdle(t, cs)

	if err := cs.RespondPermission("call_1", core.PermissionResult{Behavior: "deny", Message: "no"}); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}

	args := waitForArgsFile(t, argsFile)
	if !containsSequence(args, []string{"-c", "conv-1", "--reject"}) {
		t.Fatalf("deny resume args missing --reject: %v", args)
	}
	if ev := nextEvent(t, cs); ev.Type != core.EventResult || !ev.Done {
		t.Fatalf("reject result = %#v, want EventResult{Done: true}", ev)
	}
}

func TestRespondPermission_UnknownRequestID(t *testing.T) {
	cs := newComateSession(context.Background(), "comatecli", nil, "/tmp", "", "", false, false, false, "", nil)
	defer cs.Close()

	err := cs.RespondPermission("does-not-exist", core.PermissionResult{Behavior: "allow"})
	if err == nil || !strings.Contains(err.Error(), "unknown or already-resolved") {
		t.Fatalf("RespondPermission error = %v, want unknown request error", err)
	}
}

func TestSend_RejectsConcurrentTurn(t *testing.T) {
	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	startsFile := filepath.Join(workDir, "starts.txt")
	releaseFile := filepath.Join(workDir, "release")
	script := "#!/bin/sh\n" +
		"printf 'started\\n' >> \"$COMATE_STARTS_FILE\"\n" +
		"while [ ! -f \"$COMATE_RELEASE_FILE\" ]; do sleep 0.01; done\n" +
		"printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Completed\"}}'\n"
	powershellScript := `
[IO.File]::AppendAllText($env:COMATE_STARTS_FILE, "started` + "`n" + `")
while (-not (Test-Path $env:COMATE_RELEASE_FILE)) { Start-Sleep -Milliseconds 10 }
[Console]::Out.WriteLine('{"type":"conversation-status","conversationInfo":{"id":"conv-1","status":"Completed"}}')
`
	writeFakeComateScript(t, binDir, script, powershellScript)

	t.Setenv("COMATE_STARTS_FILE", startsFile)
	t.Setenv("COMATE_RELEASE_FILE", releaseFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cs := newComateSession(context.Background(), "comatecli", nil, workDir, "", "", false, false, false, "conv-1", nil)
	defer func() { _ = cs.Close() }()

	if err := cs.Send("first", "", nil, nil); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	waitForFileContains(t, startsFile, "started")

	err := cs.Send("steer", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "turn already in progress") {
		t.Fatalf("second Send error = %v, want turn already in progress", err)
	}

	data, err := os.ReadFile(startsFile)
	if err != nil {
		t.Fatalf("read starts file: %v", err)
	}
	if got := strings.Count(string(data), "started"); got != 1 {
		t.Fatalf("started process count = %d, want 1", got)
	}

	if err := os.WriteFile(releaseFile, nil, 0o644); err != nil {
		t.Fatalf("release fake comatecli: %v", err)
	}
	waitTurnIdle(t, cs)

	if err := cs.Send("after", "", nil, nil); err != nil {
		t.Fatalf("Send after first process exited: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(startsFile)
		if err == nil && strings.Count(string(data), "started") == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ = os.ReadFile(startsFile)
	if got := strings.Count(string(data), "started"); got != 2 {
		t.Fatalf("started process count = %d, want 2", got)
	}
}

func TestReadLoop_IgnoresNonJSONLinesAndStderrNoise(t *testing.T) {
	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	script := "#!/bin/sh\n" +
		"echo '2026-11-01T00:00:00.000Z [Langfuse SDK] [WARN] not json' >&2\n" +
		"echo 'plain text garbage'\n" +
		"printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Running\"}}'\n" +
		"printf '%s\\n' '{\"type\":\"conversation-status\",\"conversationInfo\":{\"id\":\"conv-1\",\"status\":\"Completed\"}}'\n"
	powershellScript := `
[Console]::Error.WriteLine('2026-11-01T00:00:00.000Z [Langfuse SDK] [WARN] not json')
[Console]::Out.WriteLine('plain text garbage')
[Console]::Out.WriteLine('{"type":"conversation-status","conversationInfo":{"id":"conv-1","status":"Running"}}')
[Console]::Out.WriteLine('{"type":"conversation-status","conversationInfo":{"id":"conv-1","status":"Completed"}}')
`
	writeFakeComateScript(t, binDir, script, powershellScript)

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cs := newComateSession(context.Background(), "comatecli", nil, workDir, "", "", false, false, false, "", nil)
	defer cs.Close()

	if err := cs.Send("hello", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if ev := nextEvent(t, cs); ev.Type != core.EventText || ev.SessionID != "conv-1" {
		t.Fatalf("announcement = %#v, want EventText{SessionID: conv-1}", ev)
	}
	if ev := nextEvent(t, cs); ev.Type != core.EventResult || !ev.Done {
		t.Fatalf("result = %#v, want EventResult{Done: true}", ev)
	}
	select {
	case ev := <-cs.Events():
		t.Fatalf("noise produced extra event: %#v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestClose_ForceKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group semantics differ on windows")
	}

	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	script := "#!/bin/sh\n" +
		"(sleep 30) &\n" +
		"wait\n"
	if err := os.WriteFile(filepath.Join(binDir, "comatecli"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake comatecli: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	oldCloseTimeout := comateSessionCloseTimeout
	oldForceKillWait := comateSessionForceKillWait
	comateSessionCloseTimeout = 50 * time.Millisecond
	comateSessionForceKillWait = 500 * time.Millisecond
	t.Cleanup(func() {
		comateSessionCloseTimeout = oldCloseTimeout
		comateSessionForceKillWait = oldForceKillWait
	})

	cs := newComateSession(context.Background(), "comatecli", nil, workDir, "", "", false, false, false, "", nil)

	if err := cs.Send("hello", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	time.Sleep(100 * time.Millisecond) // let the process group start

	closeStarted := time.Now()
	if err := cs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed := time.Since(closeStarted); elapsed > time.Second {
		t.Fatalf("Close took too long after force kill: %v", elapsed)
	}

	select {
	case evt, ok := <-cs.Events():
		if ok {
			t.Fatalf("unexpected event after Close: %#v", evt)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("timed out waiting for events channel to close")
	}
}

func TestNew_OptionsParsing(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "comatecli"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write comatecli stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	agent, err := New(map[string]any{
		"work_dir":                 "/tmp/project",
		"model":                    "GLM-5.3",
		"mode":                     "Ask",
		"auto_approve":             true,
		"disable_hooks":            true,
		"enable_interactive_tools": true,
		"cmd":                      "comatecli --verbose",
		"env":                      map[string]any{"COMATE_ENV_KEY": "value"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := agent.(*Agent)
	if a.cmd != "comatecli" || len(a.cliExtraArgs) != 1 || a.cliExtraArgs[0] != "--verbose" {
		t.Fatalf("cmd parsing = %q %v, want comatecli [--verbose]", a.cmd, a.cliExtraArgs)
	}
	if a.model != "GLM-5.3" || a.mode != "ask" || !a.autoApprove || !a.disableHooks || !a.enableInteractiveTools {
		t.Fatalf("option parsing = model=%q mode=%q auto=%v hooks=%v interactive=%v",
			a.model, a.mode, a.autoApprove, a.disableHooks, a.enableInteractiveTools)
	}
	if a.CLIBinaryName() != "comatecli" || a.CLIDisplayName() != "Comate" {
		t.Fatalf("doctor info = %q/%q", a.CLIBinaryName(), a.CLIDisplayName())
	}
	if a.Name() != "comate" {
		t.Fatalf("Name = %q", a.Name())
	}
}

func TestNew_MissingBinary(t *testing.T) {
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	_, err := New(map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "not found in PATH") {
		t.Fatalf("New error = %v, want not-found-in-PATH error", err)
	}
}

// ── test helpers ──────────────────────────────────────────────

func nextEvent(t *testing.T, cs *comateSession) core.Event {
	t.Helper()
	select {
	case ev := <-cs.Events():
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for event")
		return core.Event{}
	}
}

func waitTurnIdle(t *testing.T, cs *comateSession) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for cs.turnInFlight.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if cs.turnInFlight.Load() {
		t.Fatal("turn remained in flight after comatecli process exited")
	}
}

const fakeComatePowerShellPrelude = `
function fakeComateArgs {
  if ([string]::IsNullOrWhiteSpace($env:COMATE_FAKE_ARGS_FILE) -or -not (Test-Path -LiteralPath $env:COMATE_FAKE_ARGS_FILE)) {
    return @()
  }
  return @(Get-Content -LiteralPath $env:COMATE_FAKE_ARGS_FILE)
}
`

func writeFakeComateScript(t *testing.T, dir, shellScript, powershellScript string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		psPath := filepath.Join(dir, "comatecli.ps1")
		if err := os.WriteFile(psPath, []byte(fakeComatePowerShellPrelude+powershellScript), 0o644); err != nil {
			t.Fatalf("write fake comatecli powershell script: %v", err)
		}
		cmdPath := filepath.Join(dir, "comatecli.cmd")
		cmdScript := "@echo off\r\n" +
			"setlocal\r\n" +
			"set \"COMATE_FAKE_SCRIPT=%~dp0comatecli.ps1\"\r\n" +
			"set \"COMATE_FAKE_ARGS_FILE=%TEMP%\\comate-fake-args-%RANDOM%-%RANDOM%.txt\"\r\n" +
			"type nul > \"%COMATE_FAKE_ARGS_FILE%\"\r\n" +
			":args\r\n" +
			"if \"%~1\"==\"\" goto run\r\n" +
			">> \"%COMATE_FAKE_ARGS_FILE%\" echo(%~1\r\n" +
			"shift\r\n" +
			"goto args\r\n" +
			":run\r\n" +
			"powershell -NoProfile -ExecutionPolicy Bypass -File \"%COMATE_FAKE_SCRIPT%\"\r\n" +
			"set \"COMATE_FAKE_EXIT=%ERRORLEVEL%\"\r\n" +
			"del \"%COMATE_FAKE_ARGS_FILE%\" >nul 2>nul\r\n" +
			"exit /b %COMATE_FAKE_EXIT%\r\n"
		if err := os.WriteFile(cmdPath, []byte(cmdScript), 0o755); err != nil {
			t.Fatalf("write fake comatecli cmd shim: %v", err)
		}
		return
	}
	scriptPath := filepath.Join(dir, "comatecli")
	if err := os.WriteFile(scriptPath, []byte(shellScript), 0o755); err != nil {
		t.Fatalf("write fake comatecli: %v", err)
	}
}

func waitForArgsFile(t *testing.T, path string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			text := strings.TrimSpace(string(data))
			if text != "" {
				if text != last {
					last = text
					time.Sleep(20 * time.Millisecond)
					continue
				}
				lines := strings.Split(text, "\n")
				args := make([]string, 0, len(lines))
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if line != "" {
						args = append(args, line)
					}
				}
				if len(args) > 0 {
					return args
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for non-empty args file: %s", path)
	return nil
}

func waitForFileContains(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, _ := os.ReadFile(path)
	t.Fatalf("file %s: got %q, want substring %q", path, string(data), want)
}

func containsSequence(args, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for i := 0; i+len(want) <= len(args); i++ {
		match := true
		for j := range want {
			if args[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
