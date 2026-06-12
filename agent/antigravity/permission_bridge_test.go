package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/agent/antigravityhook"
	"github.com/chenhg5/cc-connect/core"
)

func TestAgyPermissionBridgePreservesHooksAndRelaysDecisions(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	realConfigDir := filepath.Join(homeDir, ".gemini", "config")
	if err := os.MkdirAll(realConfigDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config: %v", err)
	}
	originalHooks := []byte(`{
  "existing-hook": {
    "PreToolUse": [{"matcher": "read_file", "hooks": [{"type": "command", "command": "existing"}]}]
  }
}`)
	realHooksPath := filepath.Join(realConfigDir, "hooks.json")
	if err := os.WriteFile(realHooksPath, originalHooks, 0o600); err != nil {
		t.Fatalf("WriteFile hooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realConfigDir, "keep.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile keep config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan core.Event, 4)
	bridge, err := newAgyPermissionBridge(ctx, events)
	if err != nil {
		t.Fatalf("newAgyPermissionBridge: %v", err)
	}
	defer bridge.Close()

	gotOriginal, err := os.ReadFile(realHooksPath)
	if err != nil {
		t.Fatalf("ReadFile original hooks: %v", err)
	}
	if !bytes.Equal(gotOriginal, originalHooks) {
		t.Fatalf("real hooks changed:\n%s", gotOriginal)
	}
	if target, err := os.Readlink(filepath.Join(bridge.AgyConfigDir(), "config", "keep.json")); err != nil {
		t.Fatalf("Readlink preserved config: %v", err)
	} else if target != filepath.Join(realConfigDir, "keep.json") {
		t.Fatalf("preserved config target = %q", target)
	}

	var overlayHooks map[string]json.RawMessage
	overlayData, err := os.ReadFile(filepath.Join(bridge.AgyConfigDir(), "config", "hooks.json"))
	if err != nil {
		t.Fatalf("ReadFile overlay hooks: %v", err)
	}
	if err := json.Unmarshal(overlayData, &overlayHooks); err != nil {
		t.Fatalf("Unmarshal overlay hooks: %v", err)
	}
	if _, ok := overlayHooks["existing-hook"]; !ok {
		t.Fatal("overlay does not preserve existing hook")
	}
	if _, ok := overlayHooks[agyPermissionHookName]; !ok {
		t.Fatal("overlay does not contain cc-connect permission hook")
	}

	testBridgeDecision(t, bridge, events, "allow", "", "allow", "")
	testBridgeDecision(t, bridge, events, "deny", "not now", "deny", "not now")
}

func testBridgeDecision(t *testing.T, bridge *agyPermissionBridge, events <-chan core.Event, behavior, message, wantDecision, wantReason string) {
	t.Helper()

	hookInput := `{
  "conversationId": "conversation-1",
  "stepIdx": 3,
  "toolCall": {
    "name": "run_command",
    "args": {"CommandLine": "touch /tmp/permission-test", "Cwd": "/tmp"}
  }
}`
	var output bytes.Buffer
	relayDone := make(chan error, 1)
	go func() {
		relayDone <- antigravityhook.Relay(strings.NewReader(hookInput), &output, bridge.address, bridge.token)
	}()

	var event core.Event
	select {
	case event = <-events:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for permission event")
	}
	if event.Type != core.EventPermissionRequest {
		t.Fatalf("event type = %q, want permission_request", event.Type)
	}
	if event.RequestID == "" || event.ToolName != "run_command" {
		t.Fatalf("event = %#v, want run_command permission request", event)
	}
	if event.ToolInput != "touch /tmp/permission-test" {
		t.Fatalf("ToolInput = %q", event.ToolInput)
	}

	if err := bridge.RespondPermission(event.RequestID, core.PermissionResult{Behavior: behavior, Message: message}); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}
	select {
	case err := <-relayDone:
		if err != nil {
			t.Fatalf("Relay: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for hook response")
	}

	var response antigravityhook.BridgeResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal hook response %q: %v", output.String(), err)
	}
	if response.Decision != wantDecision || response.Reason != wantReason {
		t.Fatalf("response = %#v, want decision=%q reason=%q", response, wantDecision, wantReason)
	}
}

func TestAgyPermissionBridgeRejectsInvalidBehavior(t *testing.T) {
	bridge := &agyPermissionBridge{pending: make(map[string]chan core.PermissionResult)}
	if err := bridge.RespondPermission("request-1", core.PermissionResult{Behavior: "maybe"}); err == nil {
		t.Fatal("RespondPermission() error = nil, want invalid behavior error")
	}
}
