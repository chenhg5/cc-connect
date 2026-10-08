package core

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type sharedTestAgent struct {
	mu            sync.Mutex
	connections   map[string]*sharedTestSession
	starts        []string
	failResume    bool
	failInterrupt bool
}

func (a *sharedTestAgent) Name() string { return "shared-test" }
func (a *sharedTestAgent) Stop() error  { return nil }
func (a *sharedTestAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	return []AgentSessionInfo{{ID: "first", Summary: "First"}, {ID: "second", Summary: "Second"}}, nil
}
func (a *sharedTestAgent) StartSession(ctx context.Context, id string) (AgentSession, error) {
	a.mu.Lock()
	a.starts = append(a.starts, id)
	a.mu.Unlock()
	if id == "" {
		id = "new"
	}
	return a.AttachSession(ctx, id)
}
func (a *sharedTestAgent) AttachSession(_ context.Context, id string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id == "bad" || a.failResume {
		return nil, fmt.Errorf("simulated attach failure")
	}
	as := &sharedTestSession{thread: id, turn: "active-" + id, events: make(chan Event, 64), pending: map[string]bool{}, interruptError: a.failInterrupt}
	if a.connections == nil {
		a.connections = map[string]*sharedTestSession{}
	}
	a.connections[id] = as
	return as, nil
}
func (a *sharedTestAgent) connection(id string) *sharedTestSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connections[id]
}

type sharedTestSession struct {
	mu               sync.Mutex
	thread, turn     string
	events           chan Event
	pending          map[string]bool
	closed           bool
	interruptError   bool
	terminalStopped  bool
	responses        []PermissionResult
	onSend           func()
	terminals        []BackgroundTerminal
	terminalErrors   map[string]error
	terminalListErr  error
	terminalAttempts []string
	stoppedTerminals map[string]bool
	settings         AgentRuntimeSettings
	settingsErr      error
	steerError       error
}

func (s *sharedTestSession) RuntimeState() AgentRuntimeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return AgentRuntimeState{SessionID: s.thread, TurnID: s.turn, Connected: !s.closed, CanSteer: true}
}
func (s *sharedTestSession) CurrentSessionID() string { return s.thread }

func (s *sharedTestSession) ReadRuntimeSettings(context.Context) (AgentRuntimeSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings, s.settingsErr
}
func (s *sharedTestSession) Events() <-chan Event { return s.events }
func (s *sharedTestSession) Alive() bool          { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed }
func (s *sharedTestSession) Close() error         { s.mu.Lock(); s.closed = true; s.mu.Unlock(); return nil }
func (s *sharedTestSession) Send(_ string, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.mu.Lock()
	if s.closed || s.turn != "" {
		s.mu.Unlock()
		return fmt.Errorf("busy or detached")
	}
	s.events <- Event{Type: EventResult, Content: "RECOVERY on " + s.thread, Done: true}
	hook := s.onSend
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}
func (s *sharedTestSession) Steer(expected, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.steerError != nil {
		return s.steerError
	}
	if s.closed || expected == "" || expected != s.turn {
		return fmt.Errorf("stale turn")
	}
	s.events <- Event{Type: EventText, Content: "STEER observed: " + text, Metadata: map[string]any{"phase": "commentary"}}
	return nil
}
func (s *sharedTestSession) CancelTurn() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.interruptError {
		return fmt.Errorf("simulated interrupt failure")
	}
	if s.turn == "" {
		return fmt.Errorf("no active turn")
	}
	turn := s.turn
	s.turn = ""
	s.events <- Event{Type: EventResult, TurnID: turn, Done: true, Metadata: map[string]any{"turn_status": "interrupted"}}
	return nil
}
func (s *sharedTestSession) RespondPermission(id string, result PermissionResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.pending[id] {
		return fmt.Errorf("request no longer pending")
	}
	delete(s.pending, id)
	s.responses = append(s.responses, result)
	text := "decision: " + result.Behavior
	if answers, ok := result.UpdatedInput["answers"].(map[string]any); ok {
		text = fmt.Sprintf("answers by stable ID: %v", answers)
	}
	s.events <- Event{Type: EventText, Content: text, Metadata: map[string]any{"phase": "commentary"}}
	return nil
}
func (s *sharedTestSession) ListBackgroundTerminals(context.Context) ([]BackgroundTerminal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminalListErr != nil {
		return nil, s.terminalListErr
	}
	if s.terminals != nil {
		list := []BackgroundTerminal{}
		for _, terminal := range s.terminals {
			if !s.stoppedTerminals[terminal.ID] {
				list = append(list, terminal)
			}
		}
		return list, nil
	}
	if s.terminalStopped {
		return nil, nil
	}
	return []BackgroundTerminal{{ID: "terminal-first", Command: "print ticks"}}, nil
}
func (s *sharedTestSession) TerminateBackgroundTerminal(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.terminalAttempts = append(s.terminalAttempts, id)
	if s.terminals != nil {
		found := false
		for _, terminal := range s.terminals {
			if terminal.ID == id {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("not owned")
		}
		if err := s.terminalErrors[id]; err != nil {
			return err
		}
		if s.stoppedTerminals == nil {
			s.stoppedTerminals = map[string]bool{}
		}
		s.stoppedTerminals[id] = true
		return nil
	}
	if id != "terminal-first" {
		return fmt.Errorf("not owned")
	}
	s.terminalStopped = true
	return nil
}
func (s *sharedTestSession) emit(event Event) {
	s.mu.Lock()
	if event.Type == EventPermissionRequest {
		s.pending[event.RequestID] = true
	}
	if event.Type == EventPermissionResolved {
		delete(s.pending, event.RequestID)
	}
	s.mu.Unlock()
	s.events <- event
}

type sharedTestEnv struct {
	t *testing.T
	e *Engine
	p *stubCardPlatform
	a *sharedTestAgent
}

func newSharedTestEnv(t *testing.T) *sharedTestEnv {
	t.Helper()
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	a := &sharedTestAgent{}
	e := NewEngine("shared", a, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	e.display.ToolMessages = true
	t.Cleanup(func() { _ = e.Stop() })
	return &sharedTestEnv{t: t, e: e, p: p, a: a}
}
func (env *sharedTestEnv) send(text string) {
	env.e.ReceiveMessage(env.p, &Message{SessionKey: "test:user", Platform: "test", UserID: "user", MessageID: "msg-" + text, Content: text, ReplyCtx: "reply"})
}
func (env *sharedTestEnv) visible() string {
	sent := env.p.getSent()
	env.p.mu.Lock()
	defer env.p.mu.Unlock()
	cards := append(append([]*Card(nil), env.p.sentCards...), env.p.repliedCards...)
	for _, card := range cards {
		for _, element := range card.Elements {
			switch text := element.(type) {
			case CardMarkdown:
				sent = append(sent, text.Content)
			case CardListItem:
				sent = append(sent, text.Text)
			}
		}
	}
	return strings.Join(sent, "\n")
}
func (env *sharedTestEnv) await(text string) {
	env.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(env.visible(), text) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	env.t.Fatalf("missing user-visible %q; got %s", text, env.visible())
}
func (env *sharedTestEnv) button(text string) string {
	env.t.Helper()
	env.p.mu.Lock()
	defer env.p.mu.Unlock()
	cards := append(append([]*Card(nil), env.p.repliedCards...), env.p.sentCards...)
	for i := len(cards) - 1; i >= 0; i-- {
		for _, element := range cards[i].Elements {
			if item, ok := element.(CardListItem); ok && item.BtnText == text {
				return strings.TrimPrefix(item.BtnValue, "cmd:")
			}
			if actions, ok := element.(CardActions); ok {
				for _, button := range actions.Buttons {
					if button.Text == text {
						return strings.TrimPrefix(button.Value, "cmd:")
					}
				}
			}
		}
	}
	env.t.Fatalf("no visible button %q", text)
	return ""
}

func TestSharedResumeFailurePreservesIDAndNeverStartsFresh(t *testing.T) {
	env := newSharedTestEnv(t)
	session := env.e.sessions.GetOrCreateActive("test:user")
	session.SetAgentSessionID("first", env.a.Name())
	env.a.failResume = true
	env.send("continue")
	env.await(env.e.i18n.T(MsgFailedToStartAgentSession))
	env.a.mu.Lock()
	starts := append([]string(nil), env.a.starts...)
	env.a.mu.Unlock()
	if len(starts) != 1 || starts[0] != "first" || session.GetAgentSessionID() != "first" {
		t.Fatalf("shared resume silently replaced session: starts=%v id=%s", starts, session.GetAgentSessionID())
	}
}

func TestSharedCommandsDoNotChangeLegacyPrefixMatching(t *testing.T) {
	for _, prefix := range []string{"sw", "de", "st", "a"} {
		id := matchPrefix(prefix, builtinCommands)
		if id == "attach" || id == "detach" || id == "decision" || id == "steer" {
			t.Fatalf("legacy prefix changed: %s => %s", prefix, id)
		}
	}
}

func TestSharedImmediateResultPreservesUserBeforeAssistantHistory(t *testing.T) {
	env := newSharedTestEnv(t)
	env.send("/attach first")
	env.await("first")
	as := env.a.connection("first")
	as.mu.Lock()
	as.turn = ""
	as.onSend = func() { env.await("RECOVERY on first") }
	as.mu.Unlock()
	env.send("instant response")
	env.await("RECOVERY on first")
	session := env.e.sessions.GetOrCreateActive("test:user")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		history := session.GetHistory(0)
		if len(history) >= 2 {
			if history[0].Role != "user" || history[0].Content != "instant response" || history[1].Role != "assistant" {
				t.Fatalf("instant response reordered history: %+v", history)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("history never recorded both messages")
}

func TestSharedTerminalsStopAllReportsFailuresAndContinues(t *testing.T) {
	for _, tc := range []struct {
		name     string
		list     []BackgroundTerminal
		listErr  error
		failures map[string]error
		want     string
		attempts []string
	}{
		{name: "empty", list: []BackgroundTerminal{}, want: "No background commands"},
		{name: "list failure", listErr: fmt.Errorf("simulated list failure"), want: "simulated list failure"},
		{name: "partial failure", list: []BackgroundTerminal{{ID: "one"}, {ID: "failed"}, {ID: "three"}}, failures: map[string]error{"failed": fmt.Errorf("simulated termination failure")}, want: "Stopped 2 background commands; 1 could not be stopped", attempts: []string{"one", "failed", "three"}},
		{name: "deduplicated IDs", list: []BackgroundTerminal{{ID: "one"}, {ID: "one"}, {ID: "two"}}, want: "Stopped 2 background commands in the current thread", attempts: []string{"one", "two"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSharedTestEnv(t)
			env.send("/attach first")
			env.await("Attached to session first")
			as := env.a.connection("first")
			as.mu.Lock()
			as.terminals, as.terminalListErr, as.terminalErrors = tc.list, tc.listErr, tc.failures
			as.mu.Unlock()
			env.send("/terminals stop all")
			env.await(tc.want)
			as.mu.Lock()
			attempts := append([]string(nil), as.terminalAttempts...)
			turn, closed := as.turn, as.closed
			as.mu.Unlock()
			if !reflect.DeepEqual(attempts, tc.attempts) {
				t.Fatalf("termination attempts=%v want %v", attempts, tc.attempts)
			}
			if turn != "active-first" || closed {
				t.Fatalf("batch termination changed active turn or connection: turn=%s closed=%v", turn, closed)
			}
			if len(tc.failures) > 0 {
				env.await("failed: simulated termination failure")
			}
		})
	}
}

func TestSharedInterruptedMessageReportsBackgroundCountAndPreservesUnknown(t *testing.T) {
	env := newSharedTestEnv(t)
	for _, tc := range []struct {
		name  string
		list  []BackgroundTerminal
		err   error
		count int
	}{
		{name: "none", list: []BackgroundTerminal{}, count: 0},
		{name: "two", list: []BackgroundTerminal{{ID: "a"}, {ID: "b"}}, count: 2},
		{name: "duplicate IDs", list: []BackgroundTerminal{{ID: "a"}, {ID: "a"}}, count: 1},
		{name: "lookup failed", err: fmt.Errorf("unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			as := &sharedTestSession{terminals: tc.list, terminalListErr: tc.err}
			got := env.e.sharedInterruptedMessage(as)
			want := env.e.i18n.Tf(MsgSharedInterruptedCount, tc.count)
			if tc.err != nil {
				want = env.e.i18n.T(MsgSharedInterrupted)
			}
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

func TestSharedPending_TextAcceptsOfferedDecisionsBeforeAliases(t *testing.T) {
	for _, tc := range []struct{ reply, decision string }{
		{"allow_session", "allow_session"}, {"allow_similar", "allow_similar"},
		{"network_allow", "network_allow"}, {"network_deny", "network_deny"},
		{"cancel", "cancel"}, {"取消", "cancel"}, {" ALLOW_SESSION ", "allow_session"},
		{"yes", "allow"}, {"no", "deny"},
	} {
		t.Run(tc.reply, func(t *testing.T) {
			env := newSharedTestEnv(t)
			plain := &stubPlatformEngine{n: "test"}
			send := func(text string) {
				env.e.ReceiveMessage(plain, &Message{SessionKey: "test:user", Platform: "test", UserID: "user", Content: text, ReplyCtx: "reply"})
			}
			send("/attach first")
			as := env.a.connection("first")
			as.emit(Event{Type: EventPermissionRequest, RequestID: "approval", ToolName: "Bash", ToolInput: "TEXT APPROVAL", Decisions: []string{"allow", "deny", "allow_session", "allow_similar", "network_allow", "network_deny", "cancel"}})
			waitSharedPlainText(t, plain, "TEXT APPROVAL")
			send(tc.reply)
			waitSharedPlainText(t, plain, env.e.i18n.T(MsgSharedResponseSent))
			as.mu.Lock()
			defer as.mu.Unlock()
			if len(as.responses) != 1 || as.responses[0].Behavior != tc.decision {
				t.Fatalf("reply %q submitted %+v; want %q", tc.reply, as.responses, tc.decision)
			}
		})
	}
}

func TestSharedPending_CancelWithoutOfferedCancelDoesNotDeny(t *testing.T) {
	env := newSharedTestEnv(t)
	env.send("/attach first")
	as := env.a.connection("first")
	as.emit(Event{Type: EventPermissionRequest, RequestID: "approval", ToolName: "Bash", ToolInput: "NO CANCEL", Decisions: []string{"allow", "deny"}})
	env.await("NO CANCEL")
	env.send("cancel")
	env.await(env.e.i18n.Tf(MsgSharedDecisionHint, "allow / deny"))
	as.mu.Lock()
	defer as.mu.Unlock()
	if len(as.responses) != 0 || !as.pending["approval"] {
		t.Fatalf("unsupported cancellation submitted a decision: %+v", as.responses)
	}
}

func waitSharedPlainText(t *testing.T, p *stubPlatformEngine, text string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(p.getSent(), "\n"), text) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("missing text %q in %v", text, p.getSent())
}

type sharedCompatCall struct {
	session      *sharedCompatSession
	turn, prompt string
	images       []ImageAttachment
	files        []FileAttachment
}

type sharedCompatAgent struct {
	sharedTestAgent
	calls chan sharedCompatCall
	seq   int
}

func (a *sharedCompatAgent) StartSession(ctx context.Context, id string) (AgentSession, error) {
	a.mu.Lock()
	a.seq++
	if id == "" {
		id = fmt.Sprintf("thread-%d", a.seq)
	}
	a.mu.Unlock()
	as, err := a.AttachSession(ctx, id)
	if err != nil {
		return nil, err
	}
	s := as.(*sharedTestSession)
	s.mu.Lock()
	s.turn = ""
	s.mu.Unlock()
	return &sharedCompatSession{sharedTestSession: s, calls: a.calls}, nil
}

type sharedCompatSession struct {
	*sharedTestSession
	calls chan sharedCompatCall
	seq   int
}

func (s *sharedCompatSession) SendTurn(prompt, _ string, images []ImageAttachment, files []FileAttachment) (string, error) {
	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("turn-%d", s.seq)
	s.turn = id
	s.mu.Unlock()
	s.calls <- sharedCompatCall{session: s, turn: id, prompt: prompt, images: images, files: files}
	return id, nil
}

func (c sharedCompatCall) finish(text string) {
	c.session.mu.Lock()
	c.session.turn = ""
	c.session.mu.Unlock()
	c.session.emit(Event{Type: EventText, TurnID: c.turn, Content: text, Metadata: map[string]any{"phase": "final_answer"}})
	c.session.emit(Event{Type: EventResult, TurnID: c.turn, Content: text, Done: true})
}

func newSharedCompatEnv(t *testing.T) (*sharedTestEnv, *sharedCompatAgent) {
	env := newSharedTestEnv(t)
	a := &sharedCompatAgent{calls: make(chan sharedCompatCall, 16)}
	env.e.agent = a
	return env, a
}

func awaitSharedCompatCall(t *testing.T, a *sharedCompatAgent) sharedCompatCall {
	t.Helper()
	select {
	case c := <-a.calls:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("turn not started")
		return sharedCompatCall{}
	}
}

func TestSharedPendingQuestionPausesIdleTimeout(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	env.e.eventIdleTimeout = 100 * time.Millisecond
	env.send("question")
	call := awaitSharedCompatCall(t, a)
	call.session.emit(Event{Type: EventPermissionRequest, TurnID: call.turn, RequestID: "wait", Questions: []UserQuestion{{ID: "q", Question: "WAIT FOR ANSWER", Options: []UserQuestionOption{{Label: "A"}}}}})
	env.await("WAIT FOR ANSWER")
	time.Sleep(150 * time.Millisecond)
	if strings.Contains(env.visible(), "timed out") {
		t.Fatalf("human wait triggered idle timeout: %s", env.visible())
	}
	env.send(env.button("A"))
	call.finish("AFTER HUMAN ANSWER")
	env.await("AFTER HUMAN ANSWER")
}

func TestSharedIdleResetDoesNotDetachExternallyActiveThread(t *testing.T) {
	env, _, as := newSharedSettingsEnv(t)
	env.e.resetOnIdle = time.Minute
	s := env.e.sessions.GetOrCreateActive("test:user")
	s.mu.Lock()
	s.LastUserActivity = time.Now().Add(-time.Hour)
	s.ExplicitActivatedAt = time.Time{}
	s.mu.Unlock()
	env.send("ordinary input during CLI turn")
	env.await(env.e.i18n.T(MsgSharedBusy))
	assertSharedSettingsPreserved(t, env, as)
}

func TestSharedLateToolNeverInterruptsAnotherTurn(t *testing.T) {
	env := newSharedTestEnv(t)
	env.e.eventIdleTimeout = 100 * time.Millisecond
	env.e.maxTurnTime = 150 * time.Millisecond
	env.send("/attach first")
	env.await("Attached to session first")
	as := env.a.connection("first")
	as.emit(Event{Type: EventResult, TurnID: "active-first", Content: "OLD TURN FINISHED", Done: true})
	env.await("OLD TURN FINISHED")
	as.mu.Lock()
	as.turn = "next-turn"
	as.mu.Unlock()
	as.emit(Event{Type: EventToolResult, TurnID: "active-first", ItemID: "late", ToolName: "Bash", ToolResult: "LATE TOOL FINISHED", ToolStatus: "completed"})
	env.await("LATE TOOL FINISHED")
	time.Sleep(250 * time.Millisecond)
	env.send("/steer after late tool")
	env.await("STEER observed: after late tool")
	if strings.Contains(env.visible(), "timed out") || strings.Contains(env.visible(), "maximum time") {
		t.Fatalf("late tool restarted task timers: %s", env.visible())
	}
	env.send("/detach")
	env.await(env.e.i18n.T(MsgSharedDetached))
}
