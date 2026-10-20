package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRelayManager_DefaultTimeout(t *testing.T) {
	rm := NewRelayManager("")

	if rm.timeout != relayTimeout {
		t.Fatalf("rm.timeout = %v, want %v", rm.timeout, relayTimeout)
	}
}

func TestPrepareRelaySessionPolicy(t *testing.T) {
	sessions := NewSessionManager("")
	session := sessions.GetOrCreateActive("relay:test")
	session.SetAgentSessionID("existing", "stub")
	agent := &stubAgent{}

	e := NewEngine("target", agent, nil, "", LangEnglish)
	e.SetRelaySessionPolicy("reuse", 0)
	e.prepareRelaySession(session, sessions, agent, "relay:key")
	if got := session.GetAgentSessionID(); got != "existing" {
		t.Fatalf("reuse cleared agent session ID: %q", got)
	}

	e.SetRelaySessionPolicy("fresh", 0)
	e.prepareRelaySession(session, sessions, agent, "relay:key")
	if got := session.GetAgentSessionID(); got != "" {
		t.Fatalf("fresh kept agent session ID: %q", got)
	}

	session.SetAgentSessionID("recent", "stub")
	session.mu.Lock()
	session.UpdatedAt = time.Now()
	session.mu.Unlock()
	e.SetRelaySessionPolicy("reuse", time.Hour)
	e.prepareRelaySession(session, sessions, agent, "relay:key")
	if got := session.GetAgentSessionID(); got != "recent" {
		t.Fatalf("non-expired reset cleared agent session ID: %q", got)
	}

	session.mu.Lock()
	session.UpdatedAt = time.Now().Add(-2 * time.Hour)
	session.mu.Unlock()
	e.prepareRelaySession(session, sessions, agent, "relay:key")
	if got := session.GetAgentSessionID(); got != "" {
		t.Fatalf("expired reset kept agent session ID: %q", got)
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

// delayedVisibilityPlatform blocks the request echo for a fixed delay (or
// until its context is canceled), so tests can prove the relay timeout covers
// the whole job rather than only the target turn.
type delayedVisibilityPlatform struct {
	relayVisibilityPlatform
	delay time.Duration
}

func (p *delayedVisibilityPlatform) Send(ctx context.Context, _ any, _ string) error {
	select {
	case <-time.After(p.delay):
	case <-ctx.Done():
	}
	return ctx.Err()
}

// TestRelayManager_TimeoutCoversWholeJob pins the fix: with a 30ms relay
// timeout and a 500ms request echo, Send must return in roughly the timeout,
// not after the echo. Previously the timeout was applied only after the echo,
// so the pre-send could block well past the bound.
func TestRelayManager_TimeoutCoversWholeJob(t *testing.T) {
	src := &delayedVisibilityPlatform{
		relayVisibilityPlatform: relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}},
		delay:                   500 * time.Millisecond,
	}
	sourceEngine := NewEngine("source", &stubAgent{}, []Platform{src}, "", LangEnglish)
	targetSession := newControllableSession("target-session")
	targetEngine := NewEngine("target", &controllableAgent{nextSession: targetSession},
		[]Platform{&relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}}, "", LangEnglish)

	rm := NewRelayManager("")
	rm.SetTimeout(30 * time.Millisecond)
	rm.Bind("feishu", "chat-1", map[string]string{"source": "s", "target": "t"})
	rm.RegisterEngine("source", sourceEngine)
	rm.RegisterEngine("target", targetEngine)

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := rm.Send(context.Background(), RelayRequest{
			From:       "source",
			To:         "target",
			SessionKey: "feishu:chat-1:user",
			Message:    "go",
		})
		done <- err
	}()

	// HandleRelay only observes ctx cancellation after an event arrives. With
	// the fix the 30ms timeout has already fired by the time the request echo
	// (which blocks up to 500ms) returns, so this single late event makes the
	// relay return promptly.
	time.Sleep(60 * time.Millisecond)
	targetSession.events <- Event{Type: EventThinking, Content: "late"}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected Send to fail once the whole-job timeout elapses")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return; is the timeout still applied only after the request echo?")
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Send took %v, want it bounded by the 30ms relay timeout", elapsed)
	}
}

// ctxErrRecordingPlatform records the context error observed by each Send and
// returns it, so tests can assert whether a visibility receipt was delivered on
// a live context or on an already-expired one.
type ctxErrRecordingPlatform struct {
	relayVisibilityPlatform
	ctxErrs []error
}

func (p *ctxErrRecordingPlatform) Send(ctx context.Context, _ any, content string) error {
	err := ctx.Err()
	p.mu.Lock()
	p.sent = append(p.sent, content)
	p.ctxErrs = append(p.ctxErrs, err)
	p.mu.Unlock()
	return err
}

func (p *ctxErrRecordingPlatform) getCtxErrs() []error {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]error, len(p.ctxErrs))
	copy(out, p.ctxErrs)
	return out
}

// TestRelayManager_TimeoutPartialResponseStillVisible pins the fix: when the
// relay deadline fires but HandleRelay still returns a partial response, the
// response visibility receipt must be delivered on a fresh context instead of
// being silently dropped with "context deadline exceeded".
func TestRelayManager_TimeoutPartialResponseStillVisible(t *testing.T) {
	src := &ctxErrRecordingPlatform{relayVisibilityPlatform: relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}}
	tgt := &ctxErrRecordingPlatform{relayVisibilityPlatform: relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}}
	sourceEngine := NewEngine("source", &stubAgent{}, []Platform{src}, "", LangEnglish)
	targetSession := newControllableSession("target-session")
	targetEngine := NewEngine("target", &controllableAgent{nextSession: targetSession}, []Platform{tgt}, "", LangEnglish)

	rm := NewRelayManager("")
	rm.SetTimeout(30 * time.Millisecond)
	rm.Bind("feishu", "chat-1", map[string]string{"source": "s", "target": "t"})
	rm.RegisterEngine("source", sourceEngine)
	rm.RegisterEngine("target", targetEngine)

	done := make(chan error, 1)
	go func() {
		_, err := rm.Send(context.Background(), RelayRequest{
			From:       "source",
			To:         "target",
			SessionKey: "feishu:chat-1:user",
			Message:    "go",
		})
		done <- err
	}()

	// Produce partial text, let the deadline fire, then unblock HandleRelay and
	// let its drain goroutine close the session.
	targetSession.events <- Event{Type: EventText, Content: "partial response", SessionID: "target-session"}
	time.Sleep(60 * time.Millisecond)
	targetSession.events <- Event{Type: EventThinking, Content: "late"}
	targetSession.events <- Event{Type: EventResult, Content: "done", Done: true}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Send() error = %v, want nil with a partial response", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return")
	}

	var sawPartial bool
	for _, s := range tgt.getSent() {
		if strings.Contains(s, "partial response") {
			sawPartial = true
		}
	}
	if !sawPartial {
		t.Fatalf("partial response visibility receipt missing; sent=%v", tgt.getSent())
	}
	errs := tgt.getCtxErrs()
	if len(errs) == 0 || errs[len(errs)-1] != nil {
		t.Fatalf("response visibility send used a dead context: %v", errs)
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

func (p *relayVisibilityPlatform) getReconstructed() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.reconstructed))
	copy(out, p.reconstructed)
	return out
}

// runRelayVisibilityScenarioWith is like runRelayVisibilityScenario but
// accepts a custom session key and returns the reconstructed session keys
// captured by the source and target platforms, so contract tests can pin
// exactly which group session key core computed.
func runRelayVisibilityScenarioWith(t *testing.T, visibility string, sessionKey string) (
	resp string, sourceSent []string, targetSent []string, sourceKeys []string, targetKeys []string,
) {
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

	done := make(chan error, 1)
	go func() {
		_, err := rm.Send(context.Background(), RelayRequest{
			From:       "source",
			To:         "target",
			SessionKey: sessionKey,
			Message:    "go",
		})
		done <- err
	}()
	targetSession.events <- Event{Type: EventResult, Content: "ok"}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Send() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send() did not return")
	}

	return "ok", sourcePlatform.getSent(), targetPlatform.getSent(), sourcePlatform.getReconstructed(), targetPlatform.getReconstructed()
}

// stubVisibilityTargetPlatform is a stubPlatformEngine that opts into
// core.RelayGroupVisibilityTarget, so core/relay tests can pin the exact
// contract: when the platform returns ok=true, core MUST use its key
// verbatim; when ok=false (or the interface isn't implemented), core MUST
// fall back to "<platform>:<chatID>:relay".
type stubVisibilityTargetPlatform struct {
	relayVisibilityPlatform
	overrideKey string
	overrideOK  bool
	seenCaller  []string
}

func (p *stubVisibilityTargetPlatform) RelayGroupVisibilityKey(callerSessionKey string) (string, bool) {
	p.mu.Lock()
	p.seenCaller = append(p.seenCaller, callerSessionKey)
	p.mu.Unlock()
	return p.overrideKey, p.overrideOK
}

// TestRelayGroupVisibility_DelegatesToPlatformInterface — when the
// platform implements RelayGroupVisibilityTarget AND returns ok=true,
// core MUST use the platform-supplied key for BOTH the request echo
// and the response echo.
func TestRelayGroupVisibility_DelegatesToPlatformInterface(t *testing.T) {
	src := &stubVisibilityTargetPlatform{
		relayVisibilityPlatform: relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}},
		overrideKey:             "feishu:chat-1:root:om_abc",
		overrideOK:              true,
	}
	tgt := &stubVisibilityTargetPlatform{
		relayVisibilityPlatform: relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}},
		overrideKey:             "feishu:chat-1:root:om_abc",
		overrideOK:              true,
	}
	sourceEngine := NewEngine("source", &stubAgent{}, []Platform{src}, "", LangEnglish)
	targetSession := newControllableSession("target-session")
	targetEngine := NewEngine("target", &controllableAgent{nextSession: targetSession}, []Platform{tgt}, "", LangEnglish)

	rm := NewRelayManager("")
	rm.Bind("feishu", "chat-1", map[string]string{"source": "source-bot", "target": "target-bot"})
	rm.RegisterEngine("source", sourceEngine)
	rm.RegisterEngine("target", targetEngine)

	done := make(chan error, 1)
	go func() {
		_, err := rm.Send(context.Background(), RelayRequest{
			From:       "source",
			To:         "target",
			SessionKey: "feishu:chat-1:root:om_abc",
			Message:    "go",
		})
		done <- err
	}()
	targetSession.events <- Event{Type: EventResult, Content: "ok"}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Send() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send() did not return")
	}

	const want = "feishu:chat-1:root:om_abc"
	if got := src.getReconstructed(); len(got) != 1 || got[0] != want {
		t.Fatalf("source reconstructed = %#v, want [%q]", got, want)
	}
	if got := tgt.getReconstructed(); len(got) != 1 || got[0] != want {
		t.Fatalf("target reconstructed = %#v, want [%q]", got, want)
	}
}

// TestRelayGroupVisibility_FallsBackWhenPlatformReturnsNotOK — even
// when the platform implements the interface, ok=false MUST cause core
// to use the legacy "<platform>:<chatID>:relay" key.
func TestRelayGroupVisibility_FallsBackWhenPlatformReturnsNotOK(t *testing.T) {
	src := &stubVisibilityTargetPlatform{
		relayVisibilityPlatform: relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}},
		overrideOK:              false, // returns ("", false)
	}
	tgt := &stubVisibilityTargetPlatform{
		relayVisibilityPlatform: relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}},
		overrideOK:              false,
	}
	sourceEngine := NewEngine("source", &stubAgent{}, []Platform{src}, "", LangEnglish)
	targetSession := newControllableSession("target-session")
	targetEngine := NewEngine("target", &controllableAgent{nextSession: targetSession}, []Platform{tgt}, "", LangEnglish)
	rm := NewRelayManager("")
	rm.Bind("feishu", "chat-1", map[string]string{"source": "source-bot", "target": "target-bot"})
	rm.RegisterEngine("source", sourceEngine)
	rm.RegisterEngine("target", targetEngine)

	done := make(chan error, 1)
	go func() {
		_, err := rm.Send(context.Background(), RelayRequest{
			From:       "source",
			To:         "target",
			SessionKey: "feishu:chat-1:root:om_xyz", // even thread-shaped
			Message:    "go",
		})
		done <- err
	}()
	targetSession.events <- Event{Type: EventResult, Content: "ok"}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Send() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send() did not return")
	}

	const want = "feishu:chat-1:relay"
	if got := src.getReconstructed(); len(got) != 1 || got[0] != want {
		t.Fatalf("source reconstructed = %#v, want [%q] (must fall back)", got, want)
	}
	if got := tgt.getReconstructed(); len(got) != 1 || got[0] != want {
		t.Fatalf("target reconstructed = %#v, want [%q] (must fall back)", got, want)
	}
}

// TestRelayGroupVisibility_FallsBackWhenPlatformDoesNotImplement — a
// platform that does NOT implement RelayGroupVisibilityTarget (the
// vanilla relayVisibilityPlatform) MUST get the legacy ":relay" key.
// This pins the default for the 13 non-feishu platforms in production.
func TestRelayGroupVisibility_FallsBackWhenPlatformDoesNotImplement(t *testing.T) {
	_, _, _, sourceKeys, targetKeys :=
		runRelayVisibilityScenarioWith(t, "", "feishu:chat-1:root:om_does_not_matter")

	const want = "feishu:chat-1:relay"
	if len(sourceKeys) != 1 || sourceKeys[0] != want {
		t.Fatalf("source reconstructed = %#v, want [%q]", sourceKeys, want)
	}
	if len(targetKeys) != 1 || targetKeys[0] != want {
		t.Fatalf("target reconstructed = %#v, want [%q]", targetKeys, want)
	}
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
	if len(targetSent) != 1 || targetSent[0] != "[target-bot] target says long answer" {
		t.Fatalf("target sent = %#v, want full relay response", targetSent)
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
	if len(targetSent) != 1 || targetSent[0] != "[target-bot] relay response ready (23 chars)" {
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

func TestRelayManager_LiveUsesInteractivePathWithoutResponseEcho(t *testing.T) {
	sourcePlatform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	targetPlatform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	sourceEngine := NewEngine("source", &stubAgent{}, []Platform{sourcePlatform}, "", LangEnglish)
	targetAgent := &sessionEnvRecordingAgent{session: newResultAgentSession("live response")}
	targetEngine := NewEngine("target", targetAgent, []Platform{targetPlatform}, "", LangEnglish)

	rm := NewRelayManager("")
	rm.Bind("feishu", "chat-1", map[string]string{"source": "source-bot", "target": "target-bot"})
	rm.RegisterEngine("source", sourceEngine)
	rm.RegisterEngine("target", targetEngine)

	result, err := rm.Send(context.Background(), RelayRequest{
		From:       "source",
		To:         "target",
		SessionKey: "feishu:chat-1:user-1",
		Message:    "run live",
		Live:       true,
	})
	if err != nil {
		t.Fatalf("RelayManager.Send() error = %v", err)
	}
	if result == nil || result.Response != "live response" {
		t.Fatalf("response = %#v, want live response", result)
	}
	if got := sourcePlatform.getSent(); len(got) != 1 {
		t.Fatalf("source visibility = %#v, want request only", got)
	}
	for _, got := range targetPlatform.getSent() {
		if strings.Contains(got, "[target-bot]") {
			t.Fatalf("live relay sent duplicate response visibility: %#v", targetPlatform.getSent())
		}
	}
	if got := targetAgent.EnvValue("CC_SESSION_KEY"); got != "feishu:chat-1:user-1" {
		t.Fatalf("CC_SESSION_KEY = %q, want source session key", got)
	}
}

func TestRelayManager_LiveIgnoresConfiguredRelayTimeout(t *testing.T) {
	sourcePlatform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	targetPlatform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	sourceEngine := NewEngine("source", &stubAgent{}, []Platform{sourcePlatform}, "", LangEnglish)
	targetSession := newControllableSession("live-timeout-session")
	targetEngine := NewEngine("target", &controllableAgent{nextSession: targetSession}, []Platform{targetPlatform}, "", LangEnglish)
	rm := NewRelayManager("")
	rm.SetTimeout(20 * time.Millisecond)
	rm.Bind("feishu", "chat-1", map[string]string{"source": "s", "target": "t"})
	rm.RegisterEngine("source", sourceEngine)
	rm.RegisterEngine("target", targetEngine)

	done := make(chan error, 1)
	go func() {
		_, err := rm.Send(context.Background(), RelayRequest{
			From: "source", To: "target", SessionKey: "feishu:chat-1:user", Message: "slow", Live: true,
		})
		done <- err
	}()

	time.Sleep(60 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("live relay returned before agent response: %v", err)
	default:
	}
	targetSession.events <- Event{Type: EventResult, Content: "after timeout", Done: true}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("live relay error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("live relay did not finish after response")
	}
}

func TestRelayManager_RelayWaitsForLiveRelaySession(t *testing.T) {
	sourcePlatform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	targetPlatform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	sourceEngine := NewEngine("source", &stubAgent{}, []Platform{sourcePlatform}, "", LangEnglish)
	liveSession := newControllableSession("live-session")
	plainSession := newControllableSession("plain-session")
	var startsMu sync.Mutex
	starts := 0
	targetAgent := &controllableAgent{startSessionFn: func(context.Context, string) (AgentSession, error) {
		startsMu.Lock()
		defer startsMu.Unlock()
		starts++
		if starts == 1 {
			return liveSession, nil
		}
		return plainSession, nil
	}}
	targetEngine := NewEngine("target", targetAgent, []Platform{targetPlatform}, "", LangEnglish)
	rm := NewRelayManager("")
	rm.SetTimeout(time.Second)
	rm.Bind("feishu", "chat-1", map[string]string{"source": "s", "target": "t"})
	rm.RegisterEngine("source", sourceEngine)
	rm.RegisterEngine("target", targetEngine)

	liveDone := make(chan error, 1)
	go func() {
		_, err := rm.Send(context.Background(), RelayRequest{
			From: "source", To: "target", SessionKey: "feishu:chat-1:user", Message: "live", Live: true,
		})
		liveDone <- err
	}()
	time.Sleep(20 * time.Millisecond)

	plainDone := make(chan error, 1)
	go func() {
		_, err := rm.Send(context.Background(), RelayRequest{
			From: "source", To: "target", SessionKey: "feishu:chat-1:user", Message: "plain",
		})
		plainDone <- err
	}()
	select {
	case err := <-plainDone:
		t.Fatalf("plain relay ran while live relay held session: %v", err)
	case <-time.After(80 * time.Millisecond):
	}

	liveSession.events <- Event{Type: EventResult, Content: "live done", Done: true}
	select {
	case err := <-liveDone:
		if err != nil {
			t.Fatalf("live relay error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("live relay did not finish")
	}

	plainSession.events <- Event{Type: EventResult, Content: "plain done", Done: true}
	select {
	case err := <-plainDone:
		if err != nil {
			t.Fatalf("plain relay error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("plain relay did not start after live relay")
	}
}

func TestHandleRelayLockTimeoutExplainsBusySession(t *testing.T) {
	e := newTestEngine()
	session := e.sessions.GetOrCreateActive("relay:source:feishu:chat-1")
	lockGen, ok := session.TryLock()
	if !ok {
		t.Fatal("failed to occupy relay session")
	}
	defer session.Unlock(lockGen)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := e.HandleRelay(ctx, "source", "feishu:chat-1:user", "hello")
	if err == nil || !strings.Contains(err.Error(), "occupied by another task") {
		t.Fatalf("HandleRelay() error = %v, want busy-session explanation", err)
	}
}

func TestHandleRelayLive_MultiWorkspaceUsesBoundWorkspace(t *testing.T) {
	baseDir := t.TempDir()
	wsDir := filepath.Join(baseDir, "relay-ws")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	platform := &relayWorkspacePlatform{
		relayVisibilityPlatform: relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "mock"}},
		names:                   map[string]string{"C42": "relay-ws"},
	}
	e := NewEngine("target", &sessionEnvRecordingAgent{session: newResultAgentSession("workspace response")}, []Platform{platform}, "", LangEnglish)
	e.SetMultiWorkspace(baseDir, filepath.Join(t.TempDir(), "bindings.json"))
	normalized := normalizeWorkspacePath(wsDir)
	ws := e.workspacePool.GetOrCreate(normalized)
	ws.agent = &sessionEnvRecordingAgent{session: newResultAgentSession("workspace response")}
	ws.sessions = NewSessionManager("")

	resp, err := e.HandleRelayLive(context.Background(), "source", "mock:C42:U1", "hello")
	if err != nil {
		t.Fatalf("HandleRelayLive() error = %v", err)
	}
	if resp != "workspace response" {
		t.Fatalf("response = %q, want workspace response", resp)
	}
	workspaceAgent := ws.agent.(*sessionEnvRecordingAgent)
	if got := workspaceAgent.EnvValue("CC_SESSION_KEY"); got != "mock:C42:U1" {
		t.Fatalf("workspace CC_SESSION_KEY = %q", got)
	}
	if got := e.sessions.ActiveSessionID("relay:source:mock:C42"); got != "" {
		t.Fatalf("global relay session should be unused, got %q", got)
	}
}

func TestHandleRelayLive_MultiWorkspaceRequiresWorkspaceBinding(t *testing.T) {
	e := NewEngine("target", &stubAgent{}, nil, "", LangEnglish)
	e.SetMultiWorkspace(t.TempDir(), filepath.Join(t.TempDir(), "bindings.json"))

	_, err := e.HandleRelayLive(context.Background(), "source", "mock:missing:user", "hello")
	if err == nil || !strings.Contains(err.Error(), "no workspace binding") {
		t.Fatalf("HandleRelayLive() error = %v, want missing workspace binding", err)
	}
}

func TestStopRelayTurnsOnlyStopsMatchingChatIncludingWorkspacePrefix(t *testing.T) {
	e := NewEngine("target", &stubAgent{}, nil, "", LangEnglish)
	matching := newControllableSession("matching")
	workspaceMatching := newControllableSession("workspace-matching")
	otherChat := newControllableSession("other-chat")
	e.interactiveMu.Lock()
	e.interactiveStates["feishu:chat-1#relay:source"] = &interactiveState{agentSession: matching}
	workspaceKey := "/tmp/workspace:feishu:chat-1#relay:other"
	e.interactiveStates[workspaceKey] = &interactiveState{agentSession: workspaceMatching}
	e.interactiveStates["feishu:chat-2#relay:source"] = &interactiveState{agentSession: otherChat}
	e.interactiveMu.Unlock()

	if got := e.stopRelayTurns("feishu", "chat-1"); got != 2 {
		t.Fatalf("stopped = %d, want 2", got)
	}
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()
	if e.interactiveStates["feishu:chat-1#relay:source"] != nil || e.interactiveStates[workspaceKey] != nil {
		t.Fatal("matching relay states were not removed")
	}
	if e.interactiveStates["feishu:chat-2#relay:source"] == nil {
		t.Fatal("relay state for another chat was stopped")
	}
}

func TestHandleRelayLiveDistinguishesAgentFailureFromStop(t *testing.T) {
	platform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	session := &relayErrorAfterSendSession{controllableAgentSession: newControllableSession("failed-live-session")}
	e := NewEngine("target", &controllableAgent{nextSession: session}, []Platform{platform}, "", LangEnglish)
	e.SetEventIdleTimeout(100 * time.Millisecond)

	_, err := e.HandleRelayLive(context.Background(), "source", "feishu:chat-1:user", "hello")
	if err == nil || !strings.Contains(err.Error(), "execution failed") {
		t.Fatalf("HandleRelayLive() error = %v, want execution failure", err)
	}
	if strings.Contains(err.Error(), "stopped") {
		t.Fatalf("agent failure was misreported as stopped: %v", err)
	}
}

type relayErrorAfterSendSession struct {
	*controllableAgentSession
}

func (s *relayErrorAfterSendSession) Send(_ string, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.events <- Event{Type: EventError, Error: errors.New("upstream unavailable")}
	return nil
}

func TestCmdStopStopsRelayTaskAfterOwnAndCronFallbacks(t *testing.T) {
	platform := &relayVisibilityPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("target", &stubAgent{}, []Platform{platform}, "", LangEnglish)
	session := newControllableSession("relay-stop-session")
	e.interactiveMu.Lock()
	e.interactiveStates["feishu:chat-1#relay:source"] = &interactiveState{agentSession: session}
	e.interactiveMu.Unlock()

	e.cmdStop(platform, &Message{SessionKey: "feishu:chat-1:user", ReplyCtx: "ctx"})
	sent := platform.getSent()
	if len(sent) != 1 || sent[0] != e.i18n.T(MsgRelayExecutionStopped) {
		t.Fatalf("stop response = %v, want relay stop message", sent)
	}
}

type relayWorkspacePlatform struct {
	relayVisibilityPlatform
	names map[string]string
}

func (p *relayWorkspacePlatform) ResolveChannelName(channelID string) (string, error) {
	name, ok := p.names[channelID]
	if !ok {
		return "", fmt.Errorf("unknown channel %s", channelID)
	}
	return name, nil
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
