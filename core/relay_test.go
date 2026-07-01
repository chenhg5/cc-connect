package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRelayManager_DefaultTimeout(t *testing.T) {
	rm := NewRelayManager("")

	if rm.timeout != relayTimeout {
		t.Fatalf("rm.timeout = %v, want %v", rm.timeout, relayTimeout)
	}
}

func TestRelayManager_RelayContextHonorsConfiguredTimeout(t *testing.T) {
	rm := NewRelayManager("")
	rm.SetTimeout(3 * time.Second)

	ctx, cancel := rm.relayContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected relay context deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > 3*time.Second {
		t.Fatalf("time until deadline = %v, want within (0, 3s]", remaining)
	}
}

func TestRelayManager_RelayContextDisablesTimeoutAtZero(t *testing.T) {
	rm := NewRelayManager("")
	rm.SetTimeout(0)

	baseCtx := context.Background()
	ctx, cancel := rm.relayContext(baseCtx)
	defer cancel()

	if ctx != baseCtx {
		t.Fatal("expected relayContext to return the original context when timeout is disabled")
	}
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("expected no deadline when timeout is disabled")
	}
}

func TestRelayManager_BurstLimit_RejectsAfterBudgetExhausted(t *testing.T) {
	// Loop-defense regression: if an agent misinterprets a reply as a relay
	// command, or two agents volley a "relay to me" instruction, the daemon
	// must drop the runaway calls inside the rolling window.
	rm := NewRelayManager("")
	rm.SetBurstLimit(1*time.Second, 3)

	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := rm.checkBurst("chat-A", "copilot-seat", now); err != nil {
			t.Fatalf("call %d unexpectedly rate-limited: %v", i+1, err)
		}
	}
	if err := rm.checkBurst("chat-A", "copilot-seat", now); err == nil {
		t.Fatal("4th call inside the window should be rejected, got nil error")
	}

	// After the window slides forward, the budget refills.
	if err := rm.checkBurst("chat-A", "copilot-seat", now.Add(2*time.Second)); err != nil {
		t.Fatalf("call after window expiry rejected: %v", err)
	}
}

func TestRelayManager_BurstLimit_DisabledWhenMaxZero(t *testing.T) {
	rm := NewRelayManager("")
	rm.SetBurstLimit(time.Second, 0)
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 100; i++ {
		if err := rm.checkBurst("chat-A", "copilot-seat", now); err != nil {
			t.Fatalf("burst disabled but call %d rejected: %v", i+1, err)
		}
	}
}

func TestRelayManager_BurstLimit_IsolatesSources(t *testing.T) {
	// One spammy seat must not starve other seats in the same chat.
	rm := NewRelayManager("")
	rm.SetBurstLimit(time.Second, 2)
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)

	_ = rm.checkBurst("chat-A", "copilot-seat", now)
	_ = rm.checkBurst("chat-A", "copilot-seat", now)
	if err := rm.checkBurst("chat-A", "copilot-seat", now); err == nil {
		t.Fatal("copilot-seat should be rate-limited after 2 calls")
	}
	if err := rm.checkBurst("chat-A", "reasonix-seat", now); err != nil {
		t.Fatalf("reasonix-seat should still be allowed (independent budget): %v", err)
	}
}

type relayVisibilityPlatform struct {
	stubPlatformEngine
	reconstructed []string
}

func (p *relayVisibilityPlatform) ReconstructReplyCtx(sessionKey string) (any, error) {
	p.mu.Lock()
	p.reconstructed = append(p.reconstructed, sessionKey)
	p.mu.Unlock()
	return sessionKey, nil
}

func runRelayVisibilityScenario(t *testing.T, visibility string) (resp string, sourceSent []string, targetSent []string) {
	t.Helper()

	sourcePlatform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	targetPlatform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	sourceEngine := NewEngine("source", &stubAgent{}, []Platform{sourcePlatform}, "", LangEnglish)
	targetSession := newControllableSession("target-session")
	targetEngine := NewEngine("target", &controllableAgent{nextSession: targetSession}, []Platform{targetPlatform}, "", LangEnglish)

	rm := NewRelayManager("")
	rm.Bind("feishu", "chat-1", map[string]string{
		"source": "source-bot",
		"target": "target-bot",
	})
	rm.RegisterEngine("source", sourceEngine)
	rm.RegisterEngine("target", targetEngine)
	if visibility != "" {
		rm.SetVisibility(visibility)
	}

	type relayResult struct {
		resp string
		err  error
	}
	done := make(chan relayResult, 1)
	go func() {
		result, err := rm.Send(context.Background(), RelayRequest{
			From:       "source",
			To:         "target",
			SessionKey: "feishu:chat-1:user-1",
			Message:    "please ask target",
		})
		if result != nil {
			done <- relayResult{resp: result.Response, err: err}
			return
		}
		done <- relayResult{err: err}
	}()

	targetSession.events <- Event{Type: EventResult, Content: "target says long answer", Done: true}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("RelayManager.Send() error = %v", got.err)
		}
		resp = got.resp
	case <-time.After(2 * time.Second):
		t.Fatal("RelayManager.Send() did not return")
	}

	return resp, sourcePlatform.getSent(), targetPlatform.getSent()
}

func TestRelayManager_DefaultVisibilityEchoesFullMessages(t *testing.T) {
	resp, sourceSent, targetSent := runRelayVisibilityScenario(t, "")

	if resp != "target says long answer" {
		t.Fatalf("response = %q, want target response", resp)
	}
	if len(sourceSent) != 1 || sourceSent[0] != "[source-bot → target-bot] please ask target" {
		t.Fatalf("source sent = %#v, want full relay request", sourceSent)
	}
	wantTargetFull := []string{
		"[target-bot] target says long answer",
	}
	if len(targetSent) != 1 || targetSent[0] != wantTargetFull[0] {
		t.Fatalf("target sent = %#v, want full relay response", targetSent)
	}
}

func TestRelayManager_InjectsHandbackIntoSourceSession(t *testing.T) {
	sourcePlatform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	targetPlatform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	sourceEngine := NewEngine("source", &stubAgent{}, []Platform{sourcePlatform}, "", LangEnglish)
	targetSession := newControllableSession("target-session")
	targetEngine := NewEngine("target", &controllableAgent{nextSession: targetSession}, []Platform{targetPlatform}, "", LangEnglish)

	rm := NewRelayManager("")
	rm.Bind("feishu", "chat-1", map[string]string{
		"source": "source-bot",
		"target": "target-bot",
	})
	rm.RegisterEngine("source", sourceEngine)
	rm.RegisterEngine("target", targetEngine)

	sessionKey := "feishu:chat-1:user-1"
	done := make(chan error, 1)
	go func() {
		_, err := rm.Send(context.Background(), RelayRequest{
			From:       "source",
			To:         "target",
			SessionKey: sessionKey,
			Message:    "please ask target",
		})
		done <- err
	}()

	targetSession.events <- Event{Type: EventResult, Content: "target says long answer", Done: true}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RelayManager.Send() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RelayManager.Send() did not return")
	}

	var history []HistoryEntry
	for i := 0; i < 100; i++ {
		history = sourceEngine.sessions.GetOrCreateActive(sessionKey).GetHistory(0)
		if len(history) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(history) != 1 {
		t.Fatalf("source history len = %d, want 1", len(history))
	}
	if history[0].Role != "user" {
		t.Fatalf("source history role = %q, want user", history[0].Role)
	}
	if !strings.Contains(history[0].Content, "[CC-RELAY-HANDBACK]") ||
		!strings.Contains(history[0].Content, "From: target-bot") ||
		!strings.Contains(history[0].Content, "target says long answer") {
		t.Fatalf("source history missing handback content: %q", history[0].Content)
	}
}

func TestRelayManager_VisibilitySummarySuppressesBodies(t *testing.T) {
	resp, sourceSent, targetSent := runRelayVisibilityScenario(t, RelayVisibilitySummary)

	if resp != "target says long answer" {
		t.Fatalf("response = %q, want target response", resp)
	}
	if len(sourceSent) != 1 || sourceSent[0] != "[source-bot → target-bot] relay request sent" {
		t.Fatalf("source sent = %#v, want summary relay request", sourceSent)
	}
	wantTargetSummary := []string{
		"[target-bot] relay response ready (23 chars)",
	}
	if len(targetSent) != 1 || targetSent[0] != wantTargetSummary[0] {
		t.Fatalf("target sent = %#v, want summary relay response", targetSent)
	}
}

func TestRelayManager_VisibilityNoneSuppressesGroupEcho(t *testing.T) {
	resp, sourceSent, targetSent := runRelayVisibilityScenario(t, RelayVisibilityNone)

	if resp != "target says long answer" {
		t.Fatalf("response = %q, want target response", resp)
	}
	if len(sourceSent) != 0 {
		t.Fatalf("source sent = %#v, want no relay request echo", sourceSent)
	}
	if len(targetSent) != 0 {
		t.Fatalf("target sent = %#v, want no relay response echo", targetSent)
	}
}

func TestHandleRelay_ReturnsPartialOnTimeout(t *testing.T) {
	e := newTestEngine()
	session := newControllableSession("relay-session")
	e.agent = &controllableAgent{nextSession: session}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	type relayResult struct {
		resp string
		err  error
	}
	done := make(chan relayResult, 1)
	go func() {
		resp, err := e.HandleRelay(ctx, "source", "test:chat-1:user", "hello")
		done <- relayResult{resp: resp, err: err}
	}()

	session.events <- Event{Type: EventText, Content: "partial response", SessionID: "relay-session"}
	time.Sleep(40 * time.Millisecond)
	// After timeout, HandleRelay consumes the next event from the channel to
	// unblock the for-range loop, then checks ctx.Err() and spawns the drain
	// goroutine. We need two events: one to unblock HandleRelay, and one
	// EventResult for the drain goroutine to close the session cleanly.
	session.events <- Event{Type: EventThinking, Content: "still working"}
	session.events <- Event{Type: EventResult, Content: "done", Done: true}

	got := <-done
	if got.err != nil {
		t.Fatalf("HandleRelay() error = %v, want nil", got.err)
	}
	if got.resp != "partial response" {
		t.Fatalf("HandleRelay() response = %q, want %q", got.resp, "partial response")
	}

	// Wait for the background drain goroutine to close the session.
	select {
	case <-session.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("background drain goroutine did not close the session")
	}
}

func TestHandleRelay_TimeoutWithoutTextReturnsContextError(t *testing.T) {
	e := newTestEngine()
	session := newControllableSession("relay-session")
	e.agent = &controllableAgent{nextSession: session}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	type relayResult struct {
		resp string
		err  error
	}
	done := make(chan relayResult, 1)
	go func() {
		resp, err := e.HandleRelay(ctx, "source", "test:chat-1:user", "hello")
		done <- relayResult{resp: resp, err: err}
	}()

	time.Sleep(40 * time.Millisecond)
	// One event to unblock HandleRelay's for-range, one for the drain goroutine.
	session.events <- Event{Type: EventThinking, Content: "still working"}
	session.events <- Event{Type: EventResult, Content: "done", Done: true}

	got := <-done
	if got.resp != "" {
		t.Fatalf("HandleRelay() response = %q, want empty", got.resp)
	}
	if !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("HandleRelay() error = %v, want context deadline exceeded", got.err)
	}

	select {
	case <-session.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("background drain goroutine did not close the session")
	}
}

func TestHandleRelay_SuppressesToolResultsWhenToolMessagesDisabled(t *testing.T) {
	e := newTestEngine()
	e.display.ToolMessages = false
	session := newControllableSession("relay-session")
	e.agent = &controllableAgent{nextSession: session}

	type relayResult struct {
		resp string
		err  error
	}
	done := make(chan relayResult, 1)
	go func() {
		resp, err := e.HandleRelay(context.Background(), "source", "test:chat-1:user", "hello")
		done <- relayResult{resp: resp, err: err}
	}()

	session.events <- Event{Type: EventToolResult, ToolName: "read", ToolResult: "raw file output"}
	session.events <- Event{Type: EventResult, Done: true}

	got := <-done
	if got.err != nil {
		t.Fatalf("HandleRelay() error = %v, want nil", got.err)
	}
	if strings.Contains(got.resp, "raw file output") {
		t.Fatalf("HandleRelay() response leaked tool output: %q", got.resp)
	}
	if got.resp != "(empty response)" {
		t.Fatalf("HandleRelay() response = %q, want empty-response placeholder", got.resp)
	}
}

// relayFallbackAgent fails the first StartSession call (simulating a corrupt
// resume) and returns freshSession on the second call (fresh start).
type relayFallbackAgent struct {
	callCount    int
	freshSession AgentSession
}

func (a *relayFallbackAgent) Name() string { return "fallback" }
func (a *relayFallbackAgent) StartSession(_ context.Context, sessionID string) (AgentSession, error) {
	a.callCount++
	if a.callCount == 1 && sessionID != "" {
		return nil, fmt.Errorf("simulated resume failure")
	}
	return a.freshSession, nil
}
func (a *relayFallbackAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) {
	return nil, nil
}
func (a *relayFallbackAgent) Stop() error { return nil }

func TestHandleRelay_ResumeFailureFallsBackToFreshSession(t *testing.T) {
	e := newTestEngine()
	freshSession := newControllableSession("fresh-session")

	e.agent = &relayFallbackAgent{freshSession: freshSession}

	// Pre-set a stale session ID so that the first StartSession tries to resume.
	sourceSessionKey := "test:chat-1:user"
	relaySessionKey := "relay:source:test:chat-1"
	sess := e.sessions.GetOrCreateActive(relaySessionKey)
	sess.SetAgentSessionID("stale-id", "fallback")
	e.sessions.Save()

	ctx := context.Background()
	done := make(chan string, 1)
	go func() {
		resp, err := e.HandleRelay(ctx, "source", sourceSessionKey, "hello")
		if err != nil {
			done <- "error: " + err.Error()
			return
		}
		done <- resp
	}()

	// The fresh session should receive the message and respond.
	freshSession.events <- Event{Type: EventResult, Content: "recovered", SessionID: "fresh-session", Done: true}

	got := <-done
	if got != "recovered" {
		t.Fatalf("HandleRelay() = %q, want %q", got, "recovered")
	}

	// Session should be closed after EventResult.
	select {
	case <-freshSession.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("session was not closed after EventResult")
	}
}

func TestHandleRelay_SingleWorkspaceUsesGlobalAgentAndSourceSessionKey(t *testing.T) {
	e := newTestEngine()
	agent := &sessionEnvRecordingAgent{session: newResultAgentSession("global")}
	e.agent = agent

	sourceSessionKey := "discord:C1:U1"
	resp, err := e.HandleRelay(context.Background(), "source", sourceSessionKey, "hello")
	if err != nil {
		t.Fatalf("HandleRelay() error = %v", err)
	}
	if resp != "global" {
		t.Fatalf("HandleRelay() response = %q, want %q", resp, "global")
	}
	if got := agent.EnvValue("CC_SESSION_KEY"); got != sourceSessionKey {
		t.Fatalf("CC_SESSION_KEY = %q, want %q", got, sourceSessionKey)
	}
	if got := e.sessions.ActiveSessionID("relay:source:discord:C1"); got == "" {
		t.Fatal("expected relay session to be stored under platform-qualified relay key")
	}
}

func TestHandleRelay_MultiWorkspaceRoutesBySourceSessionKey(t *testing.T) {
	baseDir := t.TempDir()
	channelID := "C42"
	wsDir := filepath.Join(baseDir, "relay-ws")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	p := &mockChannelResolver{name: "mock", names: map[string]string{channelID: "relay-ws"}}
	globalAgent := &sessionEnvRecordingAgent{session: newResultAgentSession("global")}
	e := NewEngine("test", globalAgent, []Platform{p}, "", LangEnglish)
	e.SetMultiWorkspace(baseDir, filepath.Join(t.TempDir(), "bindings.json"))

	normalizedWsDir := normalizeWorkspacePath(wsDir)
	workspaceAgent := &sessionEnvRecordingAgent{session: newResultAgentSession("workspace")}
	ws := e.workspacePool.GetOrCreate(normalizedWsDir)
	ws.agent = workspaceAgent
	ws.sessions = NewSessionManager("")

	sourceSessionKey := "mock:" + channelID + ":U1"
	resp, err := e.HandleRelay(context.Background(), "source", sourceSessionKey, "hello")
	if err != nil {
		t.Fatalf("HandleRelay() error = %v", err)
	}
	if resp != "workspace" {
		t.Fatalf("HandleRelay() response = %q, want %q", resp, "workspace")
	}
	if got := workspaceAgent.EnvValue("CC_SESSION_KEY"); got != sourceSessionKey {
		t.Fatalf("workspace CC_SESSION_KEY = %q, want %q", got, sourceSessionKey)
	}
	if got := globalAgent.EnvValue("CC_SESSION_KEY"); got != "" {
		t.Fatalf("global agent should not receive relay env, got %q", got)
	}
	if got := e.sessions.ActiveSessionID("relay:source:mock:" + channelID); got != "" {
		t.Fatalf("expected no global relay session, got %q", got)
	}
	if got := ws.sessions.ActiveSessionID("relay:source:mock:" + channelID); got == "" {
		t.Fatal("expected relay session in workspace session manager")
	}
	if b := e.workspaceBindings.Lookup("project:test", workspaceChannelKey("mock", channelID)); b == nil || b.Workspace != normalizedWsDir {
		t.Fatalf("expected convention binding to be created for %q", normalizedWsDir)
	}
}

func TestHandleRelay_MultiWorkspaceRequiresWorkspaceBinding(t *testing.T) {
	baseDir := t.TempDir()
	globalAgent := &sessionEnvRecordingAgent{session: newResultAgentSession("global")}
	e := NewEngine("test", globalAgent, nil, "", LangEnglish)
	e.SetMultiWorkspace(baseDir, filepath.Join(t.TempDir(), "bindings.json"))

	resp, err := e.HandleRelay(context.Background(), "source", "mock:C404:U1", "hello")
	if err == nil {
		t.Fatal("expected error for unbound relay workspace")
	}
	if resp != "" {
		t.Fatalf("HandleRelay() response = %q, want empty", resp)
	}
	if !strings.Contains(err.Error(), "no workspace binding") {
		t.Fatalf("HandleRelay() error = %v, want missing workspace binding", err)
	}
	if got := e.sessions.ActiveSessionID("relay:source:mock:C404"); got != "" {
		t.Fatalf("expected no global relay session, got %q", got)
	}
}

type msgRecordingAgentSession struct {
	stubAgentSession
	receivedPrompts []string
	events          chan Event
}

func (s *msgRecordingAgentSession) Send(prompt string, _ []ImageAttachment, _ []FileAttachment) error {
	s.receivedPrompts = append(s.receivedPrompts, prompt)
	go func() {
		s.events <- Event{Type: EventResult, Content: "ok", Done: true}
	}()
	return nil
}

func (s *msgRecordingAgentSession) Events() <-chan Event {
	return s.events
}

type msgRecordingAgent struct {
	stubAgent
	nextSession *msgRecordingAgentSession
}

func (a *msgRecordingAgent) StartSession(ctx context.Context, sessionID string) (AgentSession, error) {
	return a.nextSession, nil
}

func TestRelayManager_ProactiveDeathNotifications(t *testing.T) {
	// 1. Process Crash Case
	sourcePlatform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	targetPlatform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	sourceSession := &msgRecordingAgentSession{events: make(chan Event, 10)}
	sourceEngine := NewEngine("source", &msgRecordingAgent{nextSession: sourceSession}, []Platform{sourcePlatform}, "", LangEnglish)

	targetSession := newControllableSession("target-session")
	targetEngine := NewEngine("target", &controllableAgent{nextSession: targetSession}, []Platform{targetPlatform}, "", LangEnglish)

	rm := NewRelayManager("")
	rm.Bind("feishu", "chat-1", map[string]string{
		"source": "source-bot",
		"target": "target-bot",
	})
	rm.RegisterEngine("source", sourceEngine)
	rm.RegisterEngine("target", targetEngine)

	done := make(chan error, 1)
	go func() {
		_, err := rm.Send(context.Background(), RelayRequest{
			From:       "source",
			To:         "target",
			SessionKey: "feishu:chat-1:user-1",
			Message:    "crash me",
		})
		done <- err
	}()

	targetSession.events <- Event{Type: EventError, Error: fmt.Errorf("process terminated unexpectedly")}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from crashed target")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RelayManager.Send() did not return")
	}

	time.Sleep(150 * time.Millisecond)

	if len(sourceSession.receivedPrompts) != 1 {
		t.Fatalf("expected 1 proactive notification injected, got %d", len(sourceSession.receivedPrompts))
	}
	wantMsg := "[CC-RELAY-HANDBACK]\nFrom: target-bot\n\ntarget-bot process has crashed. It will be restarted on next message, but context was lost."
	if sourceSession.receivedPrompts[0] != wantMsg {
		t.Errorf("got prompt %q, want %q", sourceSession.receivedPrompts[0], wantMsg)
	}

	// 2. Timeout Case
	sourceSession2 := &msgRecordingAgentSession{events: make(chan Event, 10)}
	sourceEngine2 := NewEngine("source", &msgRecordingAgent{nextSession: sourceSession2}, []Platform{sourcePlatform}, "", LangEnglish)

	targetSession2 := newControllableSession("target-session2")
	targetEngine2 := NewEngine("target", &controllableAgent{nextSession: targetSession2}, []Platform{targetPlatform}, "", LangEnglish)

	rm2 := NewRelayManager("")
	rm2.SetTimeout(10 * time.Millisecond) // short timeout
	rm2.Bind("feishu", "chat-1", map[string]string{
		"source": "source-bot",
		"target": "target-bot",
	})
	rm2.RegisterEngine("source", sourceEngine2)
	rm2.RegisterEngine("target", targetEngine2)

	done2 := make(chan error, 1)
	go func() {
		_, err := rm2.Send(context.Background(), RelayRequest{
			From:       "source",
			To:         "target",
			SessionKey: "feishu:chat-1:user-1",
			Message:    "timeout me",
		})
		done2 <- err
	}()

	select {
	case err := <-done2:
		if err == nil {
			t.Fatal("expected timeout error")
		}
	case <-time.After(50 * time.Millisecond):
		targetSession2.events <- Event{Type: EventThinking, Content: "unblock"}
		targetSession2.events <- Event{Type: EventResult, Content: "unblock", Done: true}

		select {
		case err := <-done2:
			if err == nil {
				t.Fatal("expected timeout error")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Relay2 did not return even after unblocking")
		}
	}

	time.Sleep(150 * time.Millisecond)

	if len(sourceSession2.receivedPrompts) != 1 {
		t.Fatalf("expected 1 proactive notification injected for timeout, got %d", len(sourceSession2.receivedPrompts))
	}
	wantMsgTimeout := "[CC-RELAY-HANDBACK]\nFrom: target-bot\n\ntarget-bot appears to be hung (session locked beyond timeout). You can decide to wait, retry, or escalate."
	if sourceSession2.receivedPrompts[0] != wantMsgTimeout {
		t.Errorf("got timeout prompt %q, want %q", sourceSession2.receivedPrompts[0], wantMsgTimeout)
	}
}

func TestParseSessionKeyParts_TopicThreadSupport(t *testing.T) {
	tests := []struct {
		name         string
		sessionKey   string
		wantPlatform string
		wantChatID   string
		wantErr      bool
	}{
		{
			name:         "Standard 3-part key (no topic)",
			sessionKey:   "telegram:-1003917051393:7664413698",
			wantPlatform: "telegram",
			wantChatID:   "-1003917051393",
			wantErr:      false,
		},
		{
			name:         "4-part key with topic thread",
			sessionKey:   "telegram:-1003917051393:553:7664413698",
			wantPlatform: "telegram",
			wantChatID:   "-1003917051393:553",
			wantErr:      false,
		},
		{
			name:         "Relay session key (no topic)",
			sessionKey:   "relay:chef-seat:telegram:-1003917051393",
			wantPlatform: "relay",
			wantChatID:   "telegram:-1003917051393",
			wantErr:      false,
		},
		{
			name:         "Relay session key with topic",
			sessionKey:   "relay:chef-seat:telegram:-1003917051393:553",
			wantPlatform: "relay",
			wantChatID:   "telegram:-1003917051393:553",
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plat, chatID, err := parseSessionKeyParts(tt.sessionKey)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseSessionKeyParts() error = %v, wantErr %v", err, tt.wantErr)
			}
			if plat != tt.wantPlatform {
				t.Errorf("platform = %q, want %q", plat, tt.wantPlatform)
			}
			if chatID != tt.wantChatID {
				t.Errorf("chatID = %q, want %q", chatID, tt.wantChatID)
			}
		})
	}
}

