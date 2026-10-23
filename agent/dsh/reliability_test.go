package dsh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestSession_SlowConsumerPreservesCompleteAnswer(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, t.TempDir())
	id := m.addSession(agent.GetWorkDir())
	const chunks = 4096
	var want strings.Builder
	m.frames = append(m.frames, mockFrame{body: frameEvent(id, evTurnStart(1))})
	for i := 0; i < chunks; i++ {
		text := fmt.Sprintf("%04d.", i)
		want.WriteString(text)
		m.frames = append(m.frames, mockFrame{body: frameEvent(id, evTextDelta(1, 1, text))})
	}
	m.frames = append(m.frames, mockFrame{body: frameEvent(id, evTurnEnd(1, "completed"))})
	s, err := agent.StartSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	ds := s.(*dshSession)
	t.Cleanup(func() { _ = ds.Close() })
	waitFor(t, time.Second, func() bool { return len(ds.events) == cap(ds.events) }, "full output queue")
	// Let the producer reach the overflow path before consuming any output.
	time.Sleep(50 * time.Millisecond)
	var got strings.Builder
	for _, event := range drainEvents(t, ds, 5*time.Second) {
		if event.Type == core.EventText {
			got.WriteString(event.Content)
		}
	}
	if got.String() != want.String() {
		t.Fatalf("slow consumer lost output: got %d bytes, want %d", got.Len(), want.Len())
	}
}

func TestSession_CloseUnblocksFullOutputQueue(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, t.TempDir())
	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	ds := s.(*dshSession)
	for i := 0; i < cap(ds.events); i++ {
		ds.events <- core.Event{Type: core.EventText, Content: "queued"}
	}
	emitted := make(chan struct{})
	go func() {
		ds.emit(core.Event{Type: core.EventText, Content: "blocked"})
		close(emitted)
	}()
	select {
	case <-emitted:
		t.Fatal("a full output queue dropped the pending event")
	case <-time.After(30 * time.Millisecond):
	}
	done := make(chan error, 1)
	go func() { done <- ds.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close blocked behind a slow consumer")
	}
	select {
	case <-emitted:
	case <-time.After(time.Second):
		t.Fatal("close did not release the blocked event producer")
	}
}

func TestStartSession_AppliesConfiguredMode(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, t.TempDir())
	agent.SetMode("plan")
	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if m.commandCount() != 1 || m.commandAt(0) != "/permission read-only" {
		t.Fatal("configured mode was not applied before session startup returned")
	}
}

func TestAgent_SetModeDoesNotMutateAnotherSession(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, t.TempDir())
	for i := 0; i < 2; i++ {
		s, err := agent.StartSession(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
	}
	agent.SetMode("yolo")
	time.Sleep(30 * time.Millisecond)
	if m.commandCount() != 0 || m.settingsUpdateCount() != 0 {
		t.Fatal("changing the constructor default mutated a live session")
	}
}

func TestSession_LiveModeTargetsOnlyItsOwnSession(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, t.TempDir())
	s1, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s1.Close() })
	s2, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	switcher, ok := s1.(core.LiveModeSwitcher)
	if !ok || !switcher.SetLiveMode("yolo") {
		t.Fatal("session does not support acknowledged live mode changes")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.commandSessions) != 1 || m.commandSessions[0] != s1.CurrentSessionID() {
		t.Fatalf("mode changed the wrong session: %v", m.commandSessions)
	}
}

func TestStartSession_ModeFailureNeverChangesGlobalDefaults(t *testing.T) {
	m := newMockDshServer()
	m.failCommandExecute = true
	agent := newTestAgent(t, m, t.TempDir())
	agent.SetMode("yolo")
	s, err := agent.StartSession(context.Background(), "")
	if s != nil {
		_ = s.Close()
	}
	if err == nil {
		t.Fatal("session started without applying its explicit permission mode")
	}
	if m.settingsUpdateCount() != 0 {
		t.Fatal("mode failure changed server-wide permission defaults")
	}
}

func TestRespondPermission_TransientFailureCanRetry(t *testing.T) {
	for _, question := range []bool{false, true} {
		t.Run(fmt.Sprintf("question=%v", question), func(t *testing.T) {
			m := newMockDshServer()
			agent := newTestAgent(t, m, t.TempDir())
			s, err := agent.StartSession(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			ds := s.(*dshSession)
			t.Cleanup(func() { _ = ds.Close() })
			if question {
				ds.pendingQuestions["retry"] = pendingQuestion{sessionID: ds.sessionID}
			} else {
				ds.pendingApprovals["retry"] = pendingApproval{sessionID: ds.sessionID, approvalID: "approval"}
			}
			m.mu.Lock()
			m.failNext["respond"] = 1
			m.mu.Unlock()
			result := core.PermissionResult{Behavior: "allow"}
			if err := ds.RespondPermission("retry", result); err == nil {
				t.Fatal("expected first HTTP response to fail")
			}
			if err := ds.RespondPermission("retry", result); err != nil {
				t.Fatalf("retry failed: %v", err)
			}
			if m.respondCount() != 1 {
				t.Fatalf("expected exactly one accepted response, got %d", m.respondCount())
			}
		})
	}
}

func TestAgent_WorkspaceOptionsPreserveDSHConfiguration(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, t.TempDir())
	agent.agentPreset = "custom"
	agent.timeout = 90 * time.Second
	agent.SetModel("mock/mock-2")
	agent.SetReasoningEffort("high")
	agent.SetMode("plan")
	snapshotter, ok := any(agent).(core.WorkspaceAgentOptionSnapshotter)
	if !ok {
		t.Fatal("workspace agents cannot inherit DSH configuration")
	}
	opts := snapshotter.WorkspaceAgentOptions()
	if _, exists := opts["work_dir"]; exists {
		t.Fatal("snapshot must let the caller choose its workspace")
	}
	opts["work_dir"] = t.TempDir()
	clone, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	got := clone.(*Agent)
	if got.baseURL != agent.baseURL || got.agentPreset != "custom" || got.timeout != agent.timeout ||
		got.GetModel() != "mock/mock-2" || got.GetMode() != "plan" || got.GetReasoningEffort() != "high" {
		t.Fatalf("workspace agent lost its configuration: %#v", opts)
	}
}

func TestStartSession_ModelSelectionPreservesRoute(t *testing.T) {
	for _, model := range []string{"mock-2", "mock/mock-2", "mock/vendor/model", "vendor/model"} {
		t.Run(model, func(t *testing.T) {
			m := newMockDshServer()
			m.modelCatalog = map[string]any{
				"current": map[string]any{"provider": "mock", "model": "mock-1"},
				"groups": []map[string]any{{
					"id": "mock", "name": "Mock Provider",
					"models": []map[string]any{{"id": "mock-1"}, {"id": "mock-2"}, {"id": "vendor/model"}},
				}},
			}
			agent := newTestAgent(t, m, t.TempDir())
			agent.SetModel(model)
			s, err := agent.StartSession(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			if m.selectModelCount() != 1 {
				t.Fatalf("model %q was not selected", model)
			}
			provider, selected, _ := m.selectModelAt(0)
			want := "mock-2"
			if strings.Contains(model, "vendor/model") {
				want = "vendor/model"
			}
			if provider != "mock" || selected != want {
				t.Fatalf("wrong route: %s/%s; want mock/%s", provider, selected, want)
			}
		})
	}
}

func TestStartSession_ModelSelectionFailureIsReported(t *testing.T) {
	for _, model := range []string{"mock/mock-2", "missing"} {
		t.Run(model, func(t *testing.T) {
			m := newMockDshServer()
			agent := newTestAgent(t, m, t.TempDir())
			agent.SetModel(model)
			m.failNext["session.selectModel"] = 1
			s, err := agent.StartSession(context.Background(), "")
			if s != nil {
				_ = s.Close()
			}
			if err == nil {
				t.Fatal("failed model selection silently started a different model")
			}
		})
	}
}

func TestAgent_ReasoningEffortMatchesCurrentProvider(t *testing.T) {
	m := newMockDshServer()
	m.modelCatalog = map[string]any{
		"current": map[string]any{"provider": "second", "model": "shared"},
		"groups": []map[string]any{
			{"id": "first", "models": []map[string]any{{"id": "shared", "reasoning": map[string]any{"efforts": []map[string]any{{"id": "low"}}}}}},
			{"id": "second", "models": []map[string]any{{"id": "shared", "reasoning": map[string]any{"efforts": []map[string]any{{"id": "high"}}}}}},
		},
	}
	agent := newTestAgent(t, m, t.TempDir())
	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if got := agent.AvailableReasoningEfforts(); len(got) != 1 || got[0] != "high" {
		t.Fatalf("efforts came from the wrong provider: %v", got)
	}
}

func TestRespondPermission_InvalidReceiptKeepsPendingRequest(t *testing.T) {
	for _, body := range []string{"not-json", `{}`, `{"accepted":false,"reason":"retry"}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			s := &dshSession{
				client: newRPCClient(server.URL), ctx: context.Background(),
				pendingApprovals: map[string]pendingApproval{"pending": {sessionID: "session", approvalID: "approval"}},
			}
			if err := s.RespondPermission("pending", core.PermissionResult{Behavior: "allow"}); err == nil {
				t.Fatal("invalid or rejected receipt reported success")
			}
			if _, ok := s.pendingApprovals["pending"]; !ok {
				t.Fatal("unacknowledged approval was discarded")
			}
		})
	}
}

func TestResolveModelRoute_DuplicateBareIDIsRejected(t *testing.T) {
	var catalog sessionModels
	if err := json.Unmarshal([]byte(`{"groups":[{"id":"first","models":[{"id":"same"}]},{"id":"second","models":[{"id":"same"}]}]}`), &catalog); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveModelRoute(catalog, "same"); err == nil {
		t.Fatal("ambiguous bare ID silently picked a provider")
	}
	provider, model, err := resolveModelRoute(catalog, "second/same")
	if err != nil || provider != "second" || model != "same" {
		t.Fatalf("qualified route did not disambiguate: %s/%s, %v", provider, model, err)
	}
}

func TestSession_StreamErrorWithoutSessionIDIsDelivered(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, t.TempDir())
	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	ds := s.(*dshSession)
	t.Cleanup(func() { _ = ds.Close() })
	m.broadcast("global-error", map[string]any{"type": "stream/error", "error": map[string]any{"message": "stream failed"}})
	select {
	case event := <-ds.events:
		if event.Type != core.EventError || event.Error == nil || !strings.Contains(event.Error.Error(), "stream failed") {
			t.Fatalf("unexpected stream error: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("global stream error was filtered out")
	}
}
