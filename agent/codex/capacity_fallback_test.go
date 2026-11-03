package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAppServerCapacityFallbackOptions(t *testing.T) {
	cli, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	created, err := New(map[string]any{
		"cmd": cli, "backend": "app_server", "model": "gpt-6.1-sol",
		"capacity_fallback_from": " gpt-6.1-sol ", "capacity_fallback_model": " gpt-6.1-terra ",
	})
	if err != nil {
		t.Fatal(err)
	}
	options := created.(*Agent).WorkspaceAgentOptions()
	if options["capacity_fallback_from"] != "gpt-6.1-sol" || options["capacity_fallback_model"] != "gpt-6.1-terra" {
		t.Fatalf("capacity settings lost in config snapshot: %v", options)
	}
}

func TestAppServerCapacityFallbackClassification(t *testing.T) {
	// Codex CLI 0.166.0's generated v2/ErrorNotification.json defines
	// serverOverloaded as a string enum. This is a schema fixture, not a
	// captured provider failure; do not classify arbitrary nested text.
	for _, tc := range []struct {
		name, info, message string
		want                bool
	}{
		{"structured", `"serverOverloaded"`, "try later", true},
		{"structured-object", `{"serverOverloaded":{}}`, "try later", true},
		{"explicit", `null`, "The selected model is at capacity", true},
		{"rate-limit", `"usageLimitExceeded"`, "model is at capacity", false},
		{"auth", `"unauthorized"`, "model is at capacity", false},
		{"unknown-shape", `{"type":"serverOverloaded"}`, "model is at capacity", false},
		{"nested-auth", `{"unauthorized":{"detail":"serverOverloaded"}}`, "model is at capacity", false},
		{"http429", `{"httpConnectionFailed":{"httpStatusCode":429}}`, "rate limited", false},
		{"http503", `{"httpConnectionFailed":{"httpStatusCode":503}}`, "unavailable", false},
		{"stream", `"streamDisconnected"`, "model is at capacity", false},
		{"generic", `null`, "service unavailable", false},
		{"tool", `null`, "tool storage is at capacity", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTransportTestSession(t)
			s.model, s.turnModel = "gpt-6.1-sol", "gpt-6.1-sol"
			s.capacityFallbackFrom, s.capacityFallbackModel = "gpt-6.1-sol", "gpt-6.1-terra"
			err := &appServerTurnError{Message: tc.message, CodexErrorInfo: json.RawMessage(tc.info)}
			_, _, _, ok := s.TryCapacityFallback(fmt.Errorf("provider: %w", err))
			if ok != tc.want {
				t.Fatalf("fallback=%v, want %v", ok, tc.want)
			}
		})
	}
	for _, mode := range []string{"disabled", "different-model", "same-model", "generic-error", "dead"} {
		t.Run(mode, func(t *testing.T) {
			s := newTransportTestSession(t)
			s.turnModel = "gpt-6.1-sol"
			s.capacityFallbackFrom, s.capacityFallbackModel = "gpt-6.1-sol", "gpt-6.1-terra"
			var err error = &appServerTurnError{Message: "model is at capacity"}
			switch mode {
			case "disabled":
				s.capacityFallbackModel = ""
			case "different-model":
				s.turnModel = "another-model"
			case "same-model":
				s.capacityFallbackModel = s.turnModel
			case "generic-error":
				err = errors.New("model is at capacity")
			case "dead":
				s.alive.Store(false)
			}
			if _, _, _, ok := s.TryCapacityFallback(err); ok {
				t.Fatal("unexpected capacity fallback")
			}
		})
	}
}

func TestAppServerCapacityFallbackSameThreadProtocol(t *testing.T) {
	for _, terminal := range []string{"error", "turn/completed"} {
		t.Run(terminal, func(t *testing.T) {
			s := newTransportTestSession(t)
			s.currentTurn = ""
			s.model, s.effort, s.mode = "gpt-6.1-sol", "high", "suggest"
			s.capacityFallbackFrom, s.capacityFallbackModel = "gpt-6.1-sol", "gpt-6.1-terra"
			s.promptPreamble = "Test system instructions"
			type turnRequest struct {
				ThreadID       string           `json:"threadId"`
				Model          string           `json:"model"`
				Effort         string           `json:"effort"`
				ApprovalPolicy string           `json:"approvalPolicy"`
				Input          []map[string]any `json:"input"`
			}
			var requests []turnRequest
			s.stdin = transportTestWriter(func(p []byte) (int, error) {
				var req struct {
					ID     int64           `json:"id"`
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				if err := json.Unmarshal(p, &req); err != nil {
					return 0, err
				}
				if req.Method != "turn/start" {
					t.Fatalf("unexpected RPC: %s", req.Method)
				}
				requests = append(requests, turnRequest{})
				if err := json.Unmarshal(req.Params, &requests[len(requests)-1]); err != nil {
					return 0, err
				}
				id := fmt.Sprintf("turn-%d", len(requests))
				s.handleNotification("turn/started", notificationProbe(t, map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": id}}))
				s.handleResponse(rpcResponseEnvelope{ID: req.ID, Result: notificationProbe(t, map[string]any{"turn": map[string]any{"id": id}})})
				return len(p), nil
			})
			if err := s.Send("original request", "m1", nil, nil); err != nil {
				t.Fatal(err)
			}
			failure := map[string]any{"message": "model is at capacity", "codexErrorInfo": "serverOverloaded"}
			notifyError := func(turn string, retry bool) {
				s.handleNotification("error", notificationProbe(t, map[string]any{"threadId": "thread-1", "turnId": turn, "error": failure, "willRetry": retry}))
			}
			notifyError("turn-1", true)
			notifyError("another-turn", false)
			if len(s.events) != 0 || s.currentTurn != "turn-1" {
				t.Fatal("provider retry or stale notification ended the turn")
			}
			if terminal == "error" {
				notifyError("turn-1", false)
			} else {
				s.handleNotification(terminal, notificationProbe(t, map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "status": "failed", "error": failure}}))
			}
			if len(s.events) != 1 {
				t.Fatalf("terminal events=%d", len(s.events))
			}
			event := <-s.events
			if event.Type != core.EventError {
				t.Fatalf("terminal=%#v", event)
			}
			from, to, prompt, ok := s.TryCapacityFallback(event.Error)
			if !ok || from != "gpt-6.1-sol" || to != "gpt-6.1-terra" {
				t.Fatalf("fallback=%s,%s,%v", from, to, ok)
			}
			notifyError("turn-1", false) // duplicate before fallback starts
			if err := s.Send(prompt, "m1", nil, nil); err != nil {
				t.Fatal(err)
			}
			notifyError("turn-1", false) // duplicate after fallback starts
			if len(s.events) != 0 || s.currentTurn != "turn-2" {
				t.Fatal("late terminal error interrupted the continuation")
			}
			if got := s.GetModel(); got != "gpt-6.1-terra" {
				t.Fatalf("actual model=%s", got)
			}
			notifyError("turn-2", false)
			if _, _, _, ok := s.TryCapacityFallback((<-s.events).Error); ok {
				t.Fatal("fallback model recursively fell back")
			}
			s.RestoreCapacityFallback()
			if err := s.Send("next request", "m2", nil, nil); err != nil {
				t.Fatal(err)
			}
			for i, wantModel := range []string{"gpt-6.1-sol", "gpt-6.1-terra", "gpt-6.1-sol"} {
				r := requests[i]
				if r.ThreadID != "thread-1" || r.Model != wantModel || r.Effort != "high" || r.ApprovalPolicy != requests[0].ApprovalPolicy {
					t.Fatalf("request %d changed context/settings: %+v", i, r)
				}
			}
			if len(requests[1].Input) != 1 || requests[1].Input[0]["text"] != prompt || strings.Contains(prompt, "original request") || strings.Contains(prompt, "Test system instructions") {
				t.Fatalf("continuation replayed original input: %+v", requests[1])
			}
		})
	}
}

func TestAppServerCapacityTerminalErrorBeforeStartResponse(t *testing.T) {
	for _, tc := range []struct {
		method  string
		enabled bool
	}{
		{"error", false},
		{"error", true},
		{"turn/completed", false},
		{"turn/completed", true},
	} {
		t.Run(fmt.Sprintf("%s/fallback=%v", tc.method, tc.enabled), func(t *testing.T) {
			s := newTransportTestSession(t)
			s.currentTurn = ""
			s.model = "gpt-6.1-sol"
			if tc.enabled {
				s.capacityFallbackFrom, s.capacityFallbackModel = "gpt-6.1-sol", "gpt-6.1-terra"
			}
			notifyError := func(id string) {
				failure := map[string]any{"message": "model is at capacity", "codexErrorInfo": "serverOverloaded"}
				if tc.method == "turn/completed" {
					s.handleNotification(tc.method, notificationProbe(t, map[string]any{
						"threadId": "thread-1", "turn": map[string]any{"id": id, "status": "failed", "error": failure},
					}))
					return
				}
				s.handleNotification("error", notificationProbe(t, map[string]any{
					"threadId": "thread-1", "turnId": id,
					"error": failure,
				}))
			}
			turns := 0
			s.stdin = transportTestWriter(func(p []byte) (int, error) {
				var req struct {
					ID int64 `json:"id"`
				}
				if err := json.Unmarshal(p, &req); err != nil {
					t.Fatal(err)
				}
				turns++
				id := fmt.Sprintf("turn-%d", turns)
				if turns == 1 {
					notifyError(id)
				}
				s.handleResponse(rpcResponseEnvelope{ID: req.ID, Result: notificationProbe(t, map[string]any{"turn": map[string]any{"id": id}})})
				return len(p), nil
			})
			if err := s.Send("hello", "m1", nil, nil); err != nil {
				t.Fatal(err)
			}
			if len(s.events) != 1 {
				t.Fatalf("early terminal error events=%d, want 1", len(s.events))
			}
			event := <-s.events
			var turnErr *appServerTurnError
			if event.Type != core.EventError || !errors.As(event.Error, &turnErr) {
				t.Fatalf("early terminal error lost its type: %#v", event)
			}
			if s.currentTurn != "" || s.turnRevision == 0 {
				t.Fatal("late start response revived the failed turn")
			}
			if _, _, _, ok := s.TryCapacityFallback(event.Error); ok != tc.enabled {
				t.Fatalf("fallback=%v, want %v", ok, tc.enabled)
			}
			notifyError("turn-1")
			if len(s.events) != 0 {
				t.Fatal("duplicate terminal error was emitted again")
			}
			if err := s.Send("next", "m2", nil, nil); err != nil {
				t.Fatal(err)
			}
			notifyError("turn-1")
			if s.currentTurn != "turn-2" || len(s.events) != 0 {
				t.Fatal("old terminal error interrupted the next turn")
			}
			s.handleNotification("turn/completed", notificationProbe(t, map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-2", "status": "completed"}}))
			if event := <-s.events; event.Type != core.EventResult {
				t.Fatalf("next turn did not complete: %#v", event)
			}
			notifyError("turn-2")
			if len(s.events) != 0 {
				t.Fatal("completed turn's duplicate error was emitted again")
			}
		})
	}
}
