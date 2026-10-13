package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestACPApproval_WaitsForHumanAndReturnsOption(t *testing.T) {
	for _, behavior := range []string{"allow", "deny"} {
		t.Run(behavior, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var out bytes.Buffer
			s := &acpSession{ctx: ctx, events: make(chan core.Event, 2), permByID: make(map[string]permState), toolInputByID: map[string]string{"call-1": "touch approval-test"}}
			s.alive.Store(true)
			s.tr = newTransport(bytes.NewReader(nil), &out, nil, s.onServerRequest)
			s.tr.dispatchLine([]byte(`{"jsonrpc":"2.0","id":42,"method":"session/request_permission","params":{"sessionId":"test-session","toolCall":{"toolCallId":"call-1","title":"Shell","kind":"execute"},"options":[{"optionId":"yes","kind":"allow_once"},{"optionId":"no","kind":"reject_once"}]}}`))
			var event core.Event
			select {
			case event = <-s.events:
			case <-time.After(time.Second):
				t.Fatal("no approval event")
			}
			s.wg.Wait()
			if event.Type != core.EventPermissionRequest {
				t.Fatalf("event = %+v", event)
			}
			if out.Len() != 0 {
				t.Fatal("answered before human decision")
			}
			if err := s.RespondPermission(event.RequestID, core.PermissionResult{Behavior: behavior}); err != nil {
				t.Fatal(err)
			}
			var reply struct {
				ID     int `json:"id"`
				Result struct {
					Outcome struct {
						Outcome  string `json:"outcome"`
						OptionID string `json:"optionId"`
					} `json:"outcome"`
				} `json:"result"`
			}
			if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			want := "no"
			if behavior == "allow" {
				want = "yes"
			}
			if reply.ID != 42 || reply.Result.Outcome.Outcome != "selected" || reply.Result.Outcome.OptionID != want {
				t.Fatalf("wrong approval response: %s", out.String())
			}
			if err := s.RespondPermission(event.RequestID, core.PermissionResult{Behavior: "allow"}); err == nil {
				t.Fatal("accepted expired approval")
			}
		})
	}
}
