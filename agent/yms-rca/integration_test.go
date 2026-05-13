//go:build integration

// Integration tests for the yms-rca adapter — drives a real `yms-rca rpc`
// subprocess and verifies the adapter's startup, stderr capture, JSONL
// read loop, get_state round-trip, and Close path against the actual CLI.
//
// Run with:
//
//	go test -tags=integration -v ./agent/yms-rca/...
//
// Skipped automatically when:
//   - the `yms-rca` binary is not on $PATH
//   - $YMS_RCA_INTEGRATION is unset (opt-in)
//
// The test does NOT require a working LLM backend: it only asserts that
// the adapter either receives a successful `response command=get_state`
// (when yms-rca's startup succeeds) OR cleanly surfaces an `EventError`
// + `EventResult{Done:true}` (when the subprocess fails to start — e.g.
// missing provider, no network).
package ymsagent

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func requireRealCLI(t *testing.T) {
	t.Helper()
	if os.Getenv("YMS_RCA_INTEGRATION") == "" {
		t.Skip("set YMS_RCA_INTEGRATION=1 to enable real-CLI integration tests")
	}
	if _, err := exec.LookPath("yms-rca"); err != nil {
		t.Skipf("yms-rca not on PATH: %v", err)
	}
}

// TestIntegration_StartSessionAgainstRealCLI spawns the real `yms-rca rpc`
// subprocess and asserts the adapter sees EITHER a healthy get_state
// response OR a clean error path. Both outcomes prove the wiring (spawn,
// stdin/stdout/stderr pipes, JSONL decoding, event channel, Close) works
// end-to-end against the actual binary.
func TestIntegration_StartSessionAgainstRealCLI(t *testing.T) {
	requireRealCLI(t)

	a, err := New(map[string]any{
		"cmd":      "yms-rca",
		"work_dir": t.TempDir(),
		"offline":  true, // skip startup network ops where possible
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sess, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() {
		if cerr := sess.Close(); cerr != nil {
			t.Logf("Close returned: %v", cerr)
		}
	}()

	// Two valid outcomes:
	//   A. subprocess starts → adapter learns sessionID via get_state response
	//      (success — no EventResult/EventError, child stays alive)
	//   B. subprocess fails to start → adapter emits EventError + EventResult{Done:true}
	deadline := time.After(20 * time.Second)
	gotSessionInfo := false
	gotError := false
	gotResult := false

	// Poll CurrentSessionID (success signal) every 100ms while draining events.
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()

loop:
	for {
		select {
		case evt, ok := <-sess.Events():
			if !ok {
				break loop
			}
			t.Logf("event: type=%s sid=%s err=%v done=%v",
				evt.Type, evt.SessionID, evt.Error, evt.Done)
			switch evt.Type {
			case core.EventError:
				gotError = true
			case core.EventResult:
				gotResult = true
				if evt.Done {
					break loop
				}
			}
		case <-poll.C:
			if sid := sess.CurrentSessionID(); sid != "" {
				gotSessionInfo = true
				t.Logf("get_state landed: sessionId=%s", sid)
				break loop // success — long-running rpc, no further events expected
			}
		case <-deadline:
			t.Fatalf("timeout: no sessionID, no EventError, no EventResult; adapter wiring broken")
		}
	}

	if !gotSessionInfo && !gotError {
		t.Error("neither get_state response nor EventError observed; adapter wiring broken")
	}
	t.Logf("outcome: sessionInfo=%v error=%v result=%v", gotSessionInfo, gotError, gotResult)
}

// TestIntegration_DebugRPCConfirmRoundTrip uses yms-rca's built-in
// `/debug-rpc-confirm` slash command (enabled by YMS_RCA_DEBUG_RPC_COMMANDS=1)
// to drive a real high-risk confirm round-trip WITHOUT touching an LLM.
// The adapter must:
//
//  1. spawn the child with the debug env var
//  2. send a "prompt" frame
//  3. observe EventPermissionRequest
//  4. answer via RespondPermission("deny")
//  5. observe EventResult{Done:true} for the turn
//
// This mirrors the upstream smoke-rpc.mjs "decline" half but runs through
// the cc-connect adapter end-to-end.
func TestIntegration_DebugRPCConfirmRoundTrip(t *testing.T) {
	requireRealCLI(t)

	a, err := New(map[string]any{
		"cmd":      "yms-rca",
		"work_dir": t.TempDir(),
		"offline":  true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	agent := a.(*Agent)
	agent.SetSessionEnv([]string{"YMS_RCA_DEBUG_RPC_COMMANDS=1"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sess, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	// Wait for get_state to land.
	deadline := time.Now().Add(15 * time.Second)
	for sess.CurrentSessionID() == "" {
		if time.Now().After(deadline) {
			t.Fatal("get_state never landed; cannot start prompt round-trip")
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("session ready: %s", sess.CurrentSessionID())

	// Drive a debug confirm prompt.
	if err := sess.Send("/debug-rpc-confirm", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// /debug-rpc-confirm is a slash command (no LLM call). Expected flow:
	//   1. EventPermissionRequest (title="High-risk operation")
	//   2. (our deny goes back over stdin)
	//   3. EventText "confirmed=false auto_approve=false"
	//   4. turn_end → EventResult{Done:true}; busy cleared
	// The Result-on-turn_end is the regression for the code-review HIGH
	// finding: without it, busy stays set and the NEXT Send is refused.
	gotPermission := false
	gotAck := false
	gotResult := false
	roundDeadline := time.After(20 * time.Second)
loop:
	for {
		select {
		case evt, ok := <-sess.Events():
			if !ok {
				break loop
			}
			t.Logf("event: type=%s tool=%q rid=%s content=%q done=%v",
				evt.Type, evt.ToolName, evt.RequestID,
				truncStr(evt.Content, 80), evt.Done)
			switch evt.Type {
			case core.EventPermissionRequest:
				gotPermission = true
				if evt.ToolName != "High-risk operation" {
					t.Errorf("unexpected confirm title: %q", evt.ToolName)
				}
				if err := sess.RespondPermission(evt.RequestID,
					core.PermissionResult{Behavior: "deny"}); err != nil {
					t.Errorf("RespondPermission(deny): %v", err)
				}
			case core.EventText:
				if gotPermission {
					gotAck = true
				}
			case core.EventResult:
				if evt.Done {
					gotResult = true
					break loop
				}
			case core.EventError:
				t.Logf("error event (tolerable for slash command): %v", evt.Error)
			}
		case <-roundDeadline:
			t.Fatalf("timeout; perm=%v ack=%v result=%v",
				gotPermission, gotAck, gotResult)
		}
	}

	if !gotPermission {
		t.Error("never received EventPermissionRequest from /debug-rpc-confirm")
	}
	if !gotAck {
		t.Error("never received follow-up EventText after deny — round-trip broken")
	}
	if !gotResult {
		t.Error("never received EventResult{Done:true} on turn_end — regression: busy would stay set, next Send would be refused")
	}

	// Verify the next Send is NOT refused — direct regression check for
	// "previous turn still running" on slash-command turns.
	if s, ok := sess.(*session); ok && s.busy.Load() {
		t.Error("busy still set after slash-command turn — regression for code-review HIGH finding")
	}
}

// TestIntegration_CloseTearsDownRealCLI verifies that Close on a healthy,
// long-running `yms-rca rpc` subprocess:
//
//  1. terminates the child within 8s
//  2. closes the Events() channel without panicking
//  3. emits a final EventResult{Done:true} from readStdout cleanup
//
// This is the integration counterpart to TestClose_WriteFramePermanentlyBlocked
// which uses a mock encoder.
func TestIntegration_CloseTearsDownRealCLI(t *testing.T) {
	requireRealCLI(t)

	a, err := New(map[string]any{
		"cmd":      "yms-rca",
		"work_dir": t.TempDir(),
		"offline":  true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sess, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// Wait for the session to actually be alive (get_state landed).
	deadline := time.Now().Add(10 * time.Second)
	for sess.CurrentSessionID() == "" {
		if time.Now().After(deadline) {
			t.Fatal("session never became ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !sess.Alive() {
		t.Fatal("session not alive after get_state")
	}

	// Close should return promptly.
	closeStart := time.Now()
	done := make(chan error, 1)
	go func() { done <- sess.Close() }()
	select {
	case cerr := <-done:
		dur := time.Since(closeStart)
		t.Logf("Close returned in %v (err=%v)", dur, cerr)
		if dur > 12*time.Second {
			t.Errorf("Close took too long: %v", dur)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Close did not return within 15s on healthy subprocess")
	}

	if sess.Alive() {
		t.Error("session still reports alive after Close")
	}

	// Events channel must drain to completion.
	drained := false
	gotDone := false
	drainDeadline := time.After(3 * time.Second)
	for !drained {
		select {
		case evt, ok := <-sess.Events():
			if !ok {
				drained = true
				continue
			}
			if evt.Type == core.EventResult && evt.Done {
				gotDone = true
			}
		case <-drainDeadline:
			t.Fatal("events channel did not close after Close()")
		}
	}
	if !gotDone {
		t.Error("no final EventResult{Done:true} emitted on Close")
	}
}
