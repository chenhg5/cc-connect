package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/chenhg5/cc-connect/core"
	"testing"
)

func TestManagedSubagent_RecognizesProtocolAndLegacySources(t *testing.T) {
	for _, source := range []string{`{"subAgent":{"thread_spawn":{"parent_thread_id":"parent","depth":1}}}`, `{"subagent":{}}`} {
		if !managedSubagent(json.RawMessage(source)) {
			t.Errorf("subagent accepted as root: %s", source)
		}
	}
	for _, source := range []string{`"cli"`, `"vscode"`, `null`, `{}`} {
		if managedSubagent(json.RawMessage(source)) {
			t.Errorf("root rejected: %s", source)
		}
	}
}

func TestManagedCompletedTurns_ReplayAfter1024TurnsDoesNotEmitAgain(t *testing.T) {
	s := &managedSession{ctx: context.Background(), thread: "target", completed: map[string]bool{}, events: make(chan core.Event, 4)}
	for i := 0; i < 1027; i++ {
		if err := s.completeManagedTurn(fmt.Sprint(i), "completed", "", nil); err != nil {
			t.Fatal(err)
		}
		<-s.events
	}
	if err := s.completeManagedTurn("0", "completed", "", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-s.events:
		t.Fatalf("replayed completed turn: %+v", event)
	default:
	}
}

func TestManagedRequests_IgnoreForeignAndDisabledApprovalsWithoutResponding(t *testing.T) {
	for _, method := range []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "item/tool/requestUserInput"} {
		for _, thread := range []string{"other", "target"} {
			t.Run(method+"/"+thread, func(t *testing.T) {
				enabled := thread == "other"
				s := &managedSession{ctx: context.Background(), thread: "target", agent: &managedAgent{daemon: managedOptions{approvals: enabled, questions: enabled}}, pending: map[string]*managedRequest{}, events: make(chan core.Event, 1)}
				// No RPC connection: attempting to answer would return an error. Foreign
				// requests and disabled capabilities must not mutate another client's work.
				params, _ := json.Marshal(map[string]any{"threadId": thread, "turnId": "turn", "questions": []any{}})
				if err := s.handleMessage(daemonMessage{ID: json.RawMessage(`17`), Method: method, Params: params}); err != nil {
					t.Fatal(err)
				}
				if len(s.pending) != 0 || len(s.events) != 0 {
					t.Fatal("ignored request became actionable locally")
				}
			})
		}
	}
}
