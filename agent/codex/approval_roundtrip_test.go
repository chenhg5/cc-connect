package codex

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestAppServerApproval_WaitsForHumanAndReturnsDecision(t *testing.T) {
	for _, method := range []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval"} {
		for _, behavior := range []string{"allow", "deny"} {
			t.Run(method+"/"+behavior, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				stdin := &lockedWriteCloser{}
				s := &appServerSession{ctx: ctx, stdin: stdin, events: make(chan core.Event, 2), pendingApprovals: make(map[string]chan core.PermissionResult)}
				s.handleServerRequest(serverRequestProbe(t, `42`, method, map[string]any{
					"threadId": "test-thread", "command": "touch approval-test", "reason": "test edit",
					"permissions": map[string]any{"network": map[string]any{"enabled": true}},
				}))
				var event core.Event
				select {
				case event = <-s.events:
				case <-time.After(time.Second):
					t.Fatal("no approval event")
				}
				if event.Type != core.EventPermissionRequest || event.RequestID != "42" {
					t.Fatalf("event = %+v", event)
				}
				if stdin.String() != "" {
					t.Fatal("answered before human decision")
				}
				if err := s.RespondPermission(event.RequestID, core.PermissionResult{Behavior: behavior}); err != nil {
					t.Fatal(err)
				}
				var reply struct {
					ID     int            `json:"id"`
					Result map[string]any `json:"result"`
				}
				if err := json.Unmarshal([]byte(waitForWrittenJSONLine(t, stdin)), &reply); err != nil {
					t.Fatal(err)
				}
				if reply.ID != 42 {
					t.Fatal("response id mismatch")
				}
				if method == "item/permissions/requestApproval" {
					perms, ok := reply.Result["permissions"].(map[string]any)
					if !ok {
						t.Fatal("missing permissions")
					}
					if behavior == "allow" {
						if perms["network"] == nil || reply.Result["scope"] != "turn" {
							t.Fatal("allow did not grant turn permissions")
						}
					} else if len(perms) != 0 {
						t.Fatal("deny granted permissions")
					}
				} else {
					want := "decline"
					if behavior == "allow" {
						want = "accept"
					}
					if reply.Result["decision"] != want {
						t.Fatalf("decision = %v", reply.Result)
					}
				}
				if err := s.RespondPermission(event.RequestID, core.PermissionResult{Behavior: "allow"}); err == nil {
					t.Fatal("accepted expired approval")
				}
			})
		}
	}
}
