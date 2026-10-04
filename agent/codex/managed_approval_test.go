package codex

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func TestManagedApprovalPreservesServerPolicyChoicesAndRejectsUnlistedGrant(t *testing.T) {
	policy := map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []any{"git", "status"}}}
	network := map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": map[string]any{"host": "example.test", "action": "allow"}}}
	r := &managedRequest{method: "item/commandExecution/requestApproval"}
	choices := managedApprovalChoices(map[string]any{"availableDecisions": []any{"accept", policy, "acceptForSession", network, "cancel"}}, r)
	if !reflect.DeepEqual(choices, []string{"allow", "allow_similar", "allow_session", "network_allow", "cancel"}) {
		t.Fatal(choices)
	}
	if managedApprovalChoiceDetails(policy) != `["git","status"]` || managedApprovalChoiceDetails(network) != "example.test" {
		t.Fatal("approval details expose protocol wrappers or lose proposed scope")
	}
	for choice, original := range map[string]any{"allow_similar": policy, "allow_session": "acceptForSession", "network_allow": network} {
		payload, err := managedPermissionPayload(r, core.PermissionResult{Behavior: choice, UpdatedInput: map[string]any{"execpolicy_amendment": []any{"sh"}}})
		if err != nil || !reflect.DeepEqual(payload, map[string]any{"decision": original}) {
			t.Fatalf("choice=%s payload=%v err=%v", choice, payload, err)
		}
	}
	if _, err := managedPermissionPayload(r, core.PermissionResult{Behavior: "deny"}); err == nil {
		t.Fatal("unoffered choice accepted")
	}
	r = &managedRequest{method: "item/commandExecution/requestApproval"}
	managedApprovalChoices(map[string]any{"availableDecisions": []any{"accept", "cancel"}, "proposedExecpolicyAmendment": []any{"git"}}, r)
	if _, err := managedPermissionPayload(r, core.PermissionResult{Behavior: "allow_similar"}); err == nil {
		t.Fatal("proposal broadened explicit choices")
	}
}

func TestManagedPermissionsExposeSessionGrantAndReason(t *testing.T) {
	s := &managedSession{ctx: context.Background(), agent: &managedAgent{daemon: managedOptions{approvals: true}}, thread: "target", pending: map[string]*managedRequest{}, events: make(chan core.Event, 2)}
	params, _ := json.Marshal(map[string]any{"threadId": "target", "turnId": "turn", "reason": "Need project files", "cwd": "/project", "permissions": map[string]any{"fileSystem": map[string]any{"read": []any{"/project"}}}})
	if err := s.handleRequest(daemonMessage{ID: json.RawMessage("12"), Method: "item/permissions/requestApproval", Params: params}); err != nil {
		t.Fatal(err)
	}
	event := <-s.events
	if !reflect.DeepEqual(event.Decisions, []string{"allow", "allow_session", "deny"}) || event.ToolInputRaw["reason"] != "Need project files" {
		t.Fatal(event)
	}
	for _, test := range []struct{ behavior, scope string }{{"allow", "turn"}, {"allow_session", "session"}} {
		payload, err := managedPermissionPayload(s.pending["n:12"], core.PermissionResult{Behavior: test.behavior})
		if err != nil || payload.(map[string]any)["scope"] != test.scope {
			t.Fatalf("payload=%v err=%v", payload, err)
		}
	}
}

func TestManagedSendDoesNotInjectApplicationContextOrOverrideThread(t *testing.T) {
	cwd := t.TempDir()
	paramsCh := make(chan map[string]any, 1)
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method == "thread/resume" {
				return fixtureSnapshot(cwd, "target", ""), true
			}
			if m.Method == "turn/start" {
				var params map[string]any
				_ = json.Unmarshal(m.Params, &params)
				paramsCh <- params
				return map[string]any{"turn": map[string]any{"id": "turn"}}, true
			}
			return map[string]any{}, true
		})
	})
	as, err := fixtureManagedAgent(t, socket, cwd, nil).AttachSession(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := as.Close(); err != nil {
			t.Errorf("Close cleanup: %v", err)
		}
	}()
	sender := as.(core.AgentTurnSender)
	if _, err := sender.SendTurn("hello", "message", nil, nil); err != nil {
		t.Fatal(err)
	}
	params := <-paramsCh
	if params["input"].([]any)[0].(map[string]any)["text"] != "hello" {
		t.Fatal(params)
	}
	for _, key := range []string{"additionalContext", "developerInstructions", "collaborationMode", "model", "approvalPolicy", "sandboxPolicy"} {
		if _, exists := params[key]; exists {
			t.Fatalf("overrides thread setting: %s", key)
		}
	}
}
