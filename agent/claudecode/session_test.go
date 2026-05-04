package claudecode

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestHandleResultParsesUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events: make(chan core.Event, 8),
		ctx:    ctx,
	}
	cs.sessionID.Store("test-session")
	cs.alive.Store(true)

	raw := map[string]any{
		"type":       "result",
		"result":     "done",
		"session_id": "test-session",
		"usage": map[string]any{
			"input_tokens":  float64(150000),
			"output_tokens": float64(2000),
		},
	}

	cs.handleResult(raw)

	evt := <-cs.events
	if evt.InputTokens != 150000 {
		t.Errorf("InputTokens = %d, want 150000", evt.InputTokens)
	}
	if evt.OutputTokens != 2000 {
		t.Errorf("OutputTokens = %d, want 2000", evt.OutputTokens)
	}
}

func TestHandleResultNoUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events: make(chan core.Event, 8),
		ctx:    ctx,
	}
	cs.sessionID.Store("test-session")
	cs.alive.Store(true)

	raw := map[string]any{
		"type":   "result",
		"result": "done",
	}

	cs.handleResult(raw)

	evt := <-cs.events
	if evt.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0", evt.InputTokens)
	}
	if evt.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want 0", evt.OutputTokens)
	}
}

func TestReadLoop_ChildHoldsStdoutPipe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pr, pw := io.Pipe()
	t.Cleanup(func() {
		_ = pw.Close()
	})

	writeDone := make(chan error, 1)
	go func() {
		_, err := io.WriteString(pw, `{"type":"system","session_id":"test-pipe"}`+"\n")
		writeDone <- err
	}()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	cs := &claudeSession{
		cmd:    cmd,
		events: make(chan core.Event, 64),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	cs.alive.Store(true)
	go cs.readLoop(pr, &stderrBuf)

	timeout := time.After(5 * time.Second)
	gotEvent := false
	for {
		select {
		case err := <-writeDone:
			if err != nil {
				t.Fatal(err)
			}
			writeDone = nil
		case evt, ok := <-cs.events:
			if !ok {
				if !gotEvent {
					t.Fatal("events closed but system event lost")
				}
				return
			}
			if evt.SessionID == "test-pipe" {
				gotEvent = true
			}
		case <-timeout:
			t.Fatal("HANG: events not closed within 5s - readLoop stuck in scanner.Scan()")
		}
	}
}

func TestReadLoop_CtxCancelClosesChannels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pr, pw := io.Pipe()
	t.Cleanup(func() {
		_ = pw.Close()
	})

	cmd := helperCommand(ctx, "err-then-sleep")
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	cs := &claudeSession{
		cmd:    cmd,
		events: make(chan core.Event, 64),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	cs.alive.Store(true)
	go cs.readLoop(pr, &stderrBuf)

	time.Sleep(200 * time.Millisecond)
	cancel()

	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-cs.events:
			if !ok {
				goto closed
			}
		case <-timeout:
			t.Fatal("HANG: events not closed within 5s after ctx cancel")
		}
	}
closed:
	select {
	case <-cs.done:
	case <-timeout:
		t.Fatal("HANG: done not closed within 5s after ctx cancel")
	}
}

func TestClaudeSessionClose_IdempotentNoPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := helperCommand(ctx, "stdin-eof-exit")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	cs := &claudeSession{
		cmd:                 cmd,
		stdin:               stdin,
		ctx:                 ctx,
		cancel:              cancel,
		done:                done,
		gracefulStopTimeout: 200 * time.Millisecond,
	}
	cs.alive.Store(true)

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Close panicked: %v", r)
		}
	}()

	if err := cs.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := cs.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestShellJoinArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"empty", nil, ""},
		{"single_plain", []string{"--verbose"}, "--verbose"},
		{"multiple_plain", []string{"--verbose", "--model", "opus"}, "--verbose --model opus"},
		{"arg_with_space", []string{"--prompt", "hello world"}, "--prompt 'hello world'"},
		{"arg_with_tab", []string{"a\tb"}, "'a\tb'"},
		{"arg_with_newline", []string{"line1\nline2"}, "'line1\nline2'"},
		{"arg_with_single_quote", []string{"it's"}, "'it'\\''s'"},
		{"arg_with_double_quote", []string{`say "hi"`}, `'say "hi"'`},
		{"arg_with_backslash", []string{`path\to`}, `'path\to'`},
		{"mixed", []string{"--flag", "has space", "plain", "it's here"}, "--flag 'has space' plain 'it'\\''s here'"},
		{"empty_string_arg", []string{""}, ""},
		{"long_prompt", []string{"--append-system-prompt", "You are a helpful assistant.\nBe concise."}, "--append-system-prompt 'You are a helpful assistant.\nBe concise.'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellJoinArgs(tt.args)
			if got != tt.want {
				t.Errorf("shellJoinArgs(%v)\n  got  = %q\n  want = %q", tt.args, got, tt.want)
			}
		})
	}
}

// -- Zero-downtime restart tests --

// TestExportAndRestoreSession verifies that a session's pipe FDs can be
// exported and then used to reconstruct a working session connected to the
// same agent process.
func TestExportAndRestoreSession(t *testing.T) {
	// Start agent process WITHOUT context binding so that the parent ctx
	// can be cancelled without killing the agent.
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--", "stdin-eof-exit")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	childPID := cmd.Process.Pid
	t.Logf("agent pid=%d", childPID)

	// Create session with pipes but DON'T start readLoop (stdin-eof-exit
	// doesn't write to stdout, so there's nothing to read).
	done := make(chan struct{})
	cs := &claudeSession{
		cmd:    cmd,
		stdin:  stdin,
		events: make(chan core.Event, 8),
		done:   done,
	}
	cs.sessionID.Store("test-export-restore")
	cs.alive.Store(true)

	// Extract original pipe FDs
	if f, ok := stdin.(interface{ Fd() uintptr }); ok {
		cs.stdinFD = int(f.Fd())
	}
	if f, ok := stdout.(interface{ Fd() uintptr }); ok {
		cs.stdoutFD = int(f.Fd())
	}
	t.Logf("original FDs: stdinFD=%d stdoutFD=%d", cs.stdinFD, cs.stdoutFD)

	// Export: dups FDs and clears CLOEXEC
	restartData, err := cs.ExportRestartData()
	if err != nil {
		t.Fatal("ExportRestartData:", err)
	}
	t.Logf("exported: stdinFD=%d stdoutFD=%d agentPID=%d",
		restartData.StdinFD, restartData.StdoutFD, restartData.AgentPID)

	// Validate dup'd FDs
	var stat syscall.Stat_t
	if err := syscall.Fstat(restartData.StdinFD, &stat); err != nil {
		t.Fatal("exported stdin FD invalid:", err)
	}
	if err := syscall.Fstat(restartData.StdoutFD, &stat); err != nil {
		t.Fatal("exported stdout FD invalid:", err)
	}
	for _, fd := range []int{restartData.StdinFD, restartData.StdoutFD} {
		flags, _, err := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
		if err != 0 {
			t.Fatalf("F_GETFD failed for fd %d: %v", fd, err)
		}
		if flags&syscall.FD_CLOEXEC != 0 {
			t.Fatalf("fd %d has CLOEXEC set", fd)
		}
	}

	// Close original pipe FDs (simulating exec closing CLOEXEC FDs).
	// The dup'd FDs remain open and point to the same kernel file descriptions.
	stdin.Close()
	stdout.Close()

	// Restore session from exported FDs
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, err := restoreClaudeSessionFromFDs(ctx, core.SessionRestartData{
		StdinFD:        restartData.StdinFD,
		StdoutFD:       restartData.StdoutFD,
		AgentPID:       restartData.AgentPID,
		AgentSessionID: "restored-test-session",
		PermissionMode: "",
	})
	if err != nil {
		t.Fatal("restoreClaudeSessionFromFDs:", err)
	}
	if !sess.Alive() {
		t.Fatal("restored session not alive")
	}
	if sess.CurrentSessionID() != "restored-test-session" {
		t.Fatalf("session ID mismatch: %q", sess.CurrentSessionID())
	}

	// Send a message through the restored session
	if err := sess.Send("hello from restored session", nil, nil); err != nil {
		t.Fatal("Send failed:", err)
	}
	t.Log("Send succeeded on restored session")

	// Close restored session → closes dup'd stdin → agent sees EOF → exits
	sess.Close()
	t.Log("Close completed")

	// Wait for agent to fully exit
	err = cmd.Wait()
	t.Logf("agent exited: %v", err)

	// Cleanup: kill agent if Wait returned error (e.g. if it's still alive)
	if err != nil {
		_ = cmd.Process.Kill()
		cmd.Wait()
	}
}

// TestFullRestartCycle simulates the full zero-downtime daemon restart flow
// using syscall.ForkExec to ensure dup'd FDs (with CLOEXEC cleared) are
// properly inherited by the child process.
//
//   Phase 1: Start agent, create session
//   Phase 2: Export restart data (dup FDs, clear CLOEXEC)
//   Phase 3: ForkExec child with inherited FDs
//   Phase 4: Child restores session, sends message, reports success
//   Phase 5: Parent validates child's output and cleans up
func TestFullRestartCycle(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		// Skip if running as a helper process
		t.Skip("not a real test")
	}

	// Phase 1: Start agent process.
	agent := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--", "stdin-eof-exit")
	agent.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	stdin, err := agent.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := agent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	agent.Stderr = nil
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	agentPID := agent.Process.Pid
	t.Logf("Phase 1: agent started, pid=%d", agentPID)

	// Phase 2: Create session and export restart data.
	done := make(chan struct{})
	sess := &claudeSession{
		cmd:    agent,
		stdin:  stdin,
		events: make(chan core.Event, 8),
		done:   done,
	}
	sess.sessionID.Store("integration-test-session")
	sess.alive.Store(true)

	if f, ok := stdin.(interface{ Fd() uintptr }); ok {
		sess.stdinFD = int(f.Fd())
	}
	if f, ok := stdout.(interface{ Fd() uintptr }); ok {
		sess.stdoutFD = int(f.Fd())
	}

	restartData, err := sess.ExportRestartData()
	if err != nil {
		t.Fatal("ExportRestartData:", err)
	}
	t.Logf("Phase 2: exported FDs stdinFD=%d stdoutFD=%d",
		restartData.StdinFD, restartData.StdoutFD)

	// Phase 3: Use syscall.ForkExec to spawn child process that inherits
	// the dup'd FDs (CLOEXEC was cleared by ExportRestartData).
	childEnv := append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		fmt.Sprintf("RESTORE_STDIN_FD=%d", restartData.StdinFD),
		fmt.Sprintf("RESTORE_STDOUT_FD=%d", restartData.StdoutFD),
		fmt.Sprintf("RESTORE_AGENT_PID=%d", agentPID),
		"RESTORE_SESSION_ID=integration-test-session",
	)

	childArgs := []string{os.Args[0], "-test.run=TestHelperProcess", "--", "restore-test-child"}

	// Create pipes for child's stdout
	childOutR, childOutW, err := os.Pipe()
	if err != nil {
		t.Fatal("child stdout pipe:", err)
	}

	// Prepare syscall.ProcAttr: child inherits stdin/stdout/stderr from parent,
	// plus the dup'd FDs (CLOEXEC is cleared), plus our pipe for child's stdout.
	files := make([]uintptr, restartData.StdinFD+1)
	// FDs 0,1,2 are stdin, stdout, stderr
	files[0] = uintptr(syscall.Stdin)
	files[1] = childOutW.Fd() // child's stdout goes to our pipe
	files[2] = uintptr(syscall.Stderr)
	// Ensure the dup'd FDs are present in the child's FD table
	// They are already open with CLOEXEC cleared, so ForkExec will inherit them.
	for _, fd := range []int{restartData.StdinFD, restartData.StdoutFD} {
		if fd >= len(files) {
			newFiles := make([]uintptr, fd+1)
			copy(newFiles, files)
			files = newFiles
		}
	}

	childPIDRaw, _, err := syscall.StartProcess(childArgs[0], childArgs, &syscall.ProcAttr{
		Env:   childEnv,
		Files: files[:3], // Only pass 0/1/2 — the rest are inherited automatically
		Dir:   "",
		Sys:   &syscall.SysProcAttr{},
	})
	if err != nil {
		t.Fatal("ForkExec child:", err)
	}
	t.Logf("Phase 3: child (new daemon) started, pid=%d", childPIDRaw)

	// Close parent's write end of child stdout pipe (child owns it now)
	childOutW.Close()

	// Phase 4: Close original pipe FDs and parent's dup'd FDs.
	stdin.Close()
	stdout.Close()
	syscall.Close(restartData.StdinFD)
	syscall.Close(restartData.StdoutFD)
	t.Log("Phase 4: original and dup'd FDs closed in parent")

	// Phase 5: Read child's output and wait
	childOutput, _ := io.ReadAll(childOutR)
	childOutR.Close()

	// Wait for child process
	var ws syscall.WaitStatus
	waitedPID, err := syscall.Wait4(childPIDRaw, &ws, 0, nil)
	t.Logf("Phase 5: child pid=%d exited: err=%v status=%v", waitedPID, err, ws.ExitStatus())
	t.Logf("Child stdout:\n%s", string(childOutput))

	// Cleanup: kill agent if still alive
	_ = agent.Process.Kill()
	_ = agent.Wait()

	if err != nil || ws.ExitStatus() != 0 {
		t.Fatalf("child failed: exit=%d err=%v\nOutput: %s", ws.ExitStatus(), err, string(childOutput))
	}
	if !bytes.Contains(childOutput, []byte("Send succeeded on restored session")) {
		t.Fatal("child did not report successful Send")
	}
}

func helperCommand(ctx context.Context, mode string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcess", "--", mode)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	return cmd
}

// TestHelperProcess lets this test binary act as a tiny external command for
// cases that need a process with controlled lifetime semantics.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "sleep":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "err-then-sleep":
		_, _ = os.Stderr.WriteString("helper: starting up\n")
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "stdin-eof-exit":
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "restore-test-child":
		os.Exit(runRestoreIntegrationChild())
	}
}

func runRestoreIntegrationChild() int {
	fmt.Fprintf(os.Stderr, "CHILD: starting\n")

	stdinFD, err := strconv.Atoi(os.Getenv("RESTORE_STDIN_FD"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "CHILD: RESTORE_STDIN_FD: %v\n", err)
		return 1
	}
	stdoutFD, err := strconv.Atoi(os.Getenv("RESTORE_STDOUT_FD"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "CHILD: RESTORE_STDOUT_FD: %v\n", err)
		return 1
	}
	agentPID, err := strconv.Atoi(os.Getenv("RESTORE_AGENT_PID"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "CHILD: RESTORE_AGENT_PID: %v\n", err)
		return 1
	}
	sessionID := os.Getenv("RESTORE_SESSION_ID")
	fmt.Fprintf(os.Stderr, "CHILD: FDs stdin=%d stdout=%d pid=%d\n", stdinFD, stdoutFD, agentPID)

	fmt.Fprintf(os.Stderr, "CHILD: fstat stdin...\n")
	var stat syscall.Stat_t
	if err := syscall.Fstat(stdinFD, &stat); err != nil {
		fmt.Fprintf(os.Stderr, "CHILD: stdin FD %d invalid: %v\n", stdinFD, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "CHILD: fstat stdout...\n")
	if err := syscall.Fstat(stdoutFD, &stat); err != nil {
		fmt.Fprintf(os.Stderr, "CHILD: stdout FD %d invalid: %v\n", stdoutFD, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "CHILD: kill(0) agent...\n")
	if err := syscall.Kill(agentPID, syscall.Signal(0)); err != nil {
		fmt.Fprintf(os.Stderr, "CHILD: agent died: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "CHILD: calls restoreClaudeSessionFromFDs...\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, err := restoreClaudeSessionFromFDs(ctx, core.SessionRestartData{
		StdinFD:        stdinFD,
		StdoutFD:       stdoutFD,
		AgentPID:       agentPID,
		AgentSessionID: sessionID,
		PermissionMode: "",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "CHILD: restore failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "CHILD: alive=%v id=%s\n", sess.Alive(), sess.CurrentSessionID())
	if !sess.Alive() {
		fmt.Fprintf(os.Stderr, "CHILD: not alive\n")
		return 1
	}
	if sess.CurrentSessionID() != sessionID {
		fmt.Fprintf(os.Stderr, "CHILD: session ID mismatch: %q vs %q\n", sess.CurrentSessionID(), sessionID)
		return 1
	}
	fmt.Printf("Session restored: id=%s alive=%v\n", sess.CurrentSessionID(), sess.Alive())

	fmt.Fprintf(os.Stderr, "CHILD: sending message...\n")
	if err := sess.Send("hello from restored child", nil, nil); err != nil {
		fmt.Fprintf(os.Stderr, "CHILD: Send failed: %v\n", err)
		return 1
	}
	fmt.Println("Send succeeded on restored session")
	fmt.Fprintf(os.Stderr, "CHILD: done, exiting\n")
	return 0
}
