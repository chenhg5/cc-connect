package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type approvalWriteCloser struct{ bytes.Buffer }

func (*approvalWriteCloser) Close() error { return nil }

func TestClaudeApproval_DefaultWaitsForHumanAndReturnsDecision(t *testing.T) {
	for _, behavior := range []string{"allow", "deny"} {
		t.Run(behavior, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out := &approvalWriteCloser{}
			s := &claudeSession{ctx: ctx, stdin: out, events: make(chan core.Event, 2), ccHooks: newCCPermissionHookRunner(t.TempDir())}
			// Unit tests must not execute hooks from the developer's home.
			s.ccHooks.once.Do(func() {})
			s.alive.Store(true)
			s.setPermissionMode("default")
			input := map[string]any{"command": "touch approval-test"}
			s.handleControlRequest(map[string]any{"request_id": "req-1", "request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "input": input}})
			var event core.Event
			select {
			case event = <-s.events:
			case <-time.After(time.Second):
				t.Fatal("no approval event")
			}
			if event.Type != core.EventPermissionRequest || event.RequestID != "req-1" {
				t.Fatalf("event = %+v", event)
			}
			if out.Len() != 0 {
				t.Fatal("answered before human decision")
			}
			if err := s.RespondPermission(event.RequestID, core.PermissionResult{Behavior: behavior, UpdatedInput: event.ToolInputRaw}); err != nil {
				t.Fatal(err)
			}
			var reply struct {
				Type     string `json:"type"`
				Response struct {
					RequestID string         `json:"request_id"`
					Response  map[string]any `json:"response"`
				} `json:"response"`
			}
			if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			if reply.Type != "control_response" || reply.Response.RequestID != "req-1" || reply.Response.Response["behavior"] != behavior {
				t.Fatalf("wrong response: %s", out.String())
			}
			if behavior == "allow" {
				updated, ok := reply.Response.Response["updatedInput"].(map[string]any)
				if !ok || updated["command"] != input["command"] {
					t.Fatal("lost approved tool arguments")
				}
			} else if reply.Response.Response["message"] == nil {
				t.Fatal("denial message missing")
			}
		})
	}
}
