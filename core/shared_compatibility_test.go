package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

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
	as, err := a.sharedTestAgent.AttachSession(ctx, id)
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

func TestSharedCronWaitsForResultAndPreservesSideSessionIsolation(t *testing.T) {
	for _, mode := range []string{"reuse", "new_per_run"} {
		t.Run(mode, func(t *testing.T) {
			env, a := newSharedCompatEnv(t)
			active := env.e.sessions.GetOrCreateActive("test:user")
			active.AddHistory("user", "MAIN HISTORY")
			done := make(chan error, 1)
			go func() {
				done <- env.e.ExecuteCronJob(&CronJob{ID: "compat", SessionKey: "test:user", Prompt: "CRON TASK", SessionMode: mode})
			}()
			call := awaitSharedCompatCall(t, a)
			select {
			case err := <-done:
				t.Fatalf("returned before completion: %v", err)
			default:
			}
			if mode == "new_per_run" && active.HistoryLen() != 1 {
				t.Fatal("cron modified main conversation")
			}
			call.finish("CRON RESULT")
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cron did not complete")
			}
			env.await("CRON RESULT")
			if mode == "new_per_run" && active.HistoryLen() != 1 {
				t.Fatal("cron result entered main history")
			}
		})
	}
}

func TestSharedTimerWaitsForResultInSideSession(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	active := env.e.sessions.GetOrCreateActive("test:user")
	active.AddHistory("user", "MAIN HISTORY")
	done := make(chan error, 1)
	go func() {
		done <- env.e.ExecuteTimerJob(&TimerJob{ID: "compat", SessionKey: "test:user", Prompt: "TIMER TASK", SessionMode: "new_per_run"})
	}()
	call := awaitSharedCompatCall(t, a)
	select {
	case err := <-done:
		t.Fatalf("returned before completion: %v", err)
	default:
	}
	call.finish("TIMER RESULT")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timer did not complete")
	}
	env.await("TIMER RESULT")
	if active.HistoryLen() != 1 {
		t.Fatal("timer modified main history")
	}
}

func TestSharedManagementProviderPreservesThreadAndHistory(t *testing.T) {
	env, _, as := newSharedSettingsEnv(t)
	w := httptest.NewRecorder()
	(&ManagementServer{}).handleProjectProviders(w, httptest.NewRequest("POST", "/providers/new-provider/activate", nil), env.e, "new-provider/activate")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "new_threads") {
		t.Fatalf("response: %d %s", w.Code, w.Body)
	}
	assertSharedSettingsPreserved(t, env, as)
	as.emit(Event{Type: EventText, Content: "OBSERVATION STILL LIVE", Metadata: map[string]any{"phase": "commentary"}})
	env.await("OBSERVATION STILL LIVE")
}

func TestSharedManagementProviderSaveFailurePreservesDefaults(t *testing.T) {
	env, a, as := newSharedSettingsEnv(t)
	env.e.providerSaveFunc = func(string) error { return fmt.Errorf("disk full") }
	w := httptest.NewRecorder()
	(&ManagementServer{}).handleProjectProviders(w, httptest.NewRequest("POST", "/", nil), env.e, "new-provider/activate")
	if w.Code == 200 || a.GetActiveProvider().Name != "default-provider" {
		t.Fatal("failed save applied provider")
	}
	assertSharedSettingsPreserved(t, env, as)
}

func TestSharedForegroundAndExternalSilentReplies(t *testing.T) {
	for _, text := range []string{"NO_REPLY", "Visible answer\nNO_REPLY"} {
		t.Run(text, func(t *testing.T) {
			env, a := newSharedCompatEnv(t)
			env.send("question")
			call := awaitSharedCompatCall(t, a)
			call.finish(text)
			deadline := time.Now().Add(time.Second)
			for env.e.sessions.GetOrCreateActive("test:user").HistoryLen() < 2 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if strings.Contains(env.visible(), "NO_REPLY") {
				t.Fatalf("marker leaked: %s", env.visible())
			}
			if strings.HasPrefix(text, "Visible") {
				env.await("Visible answer")
			}
			call.session.emit(Event{Type: EventResult, Content: "NO_REPLY", Done: true})
			time.Sleep(10 * time.Millisecond)
			if strings.Contains(env.visible(), "NO_REPLY") {
				t.Fatal("external marker leaked")
			}
		})
	}
}

func TestSharedMutedCronDoesNotMuteObserver(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	env.send("initial")
	first := awaitSharedCompatCall(t, a)
	first.finish("INITIAL RESULT")
	env.await("INITIAL RESULT")
	done := make(chan error, 1)
	go func() {
		done <- env.e.ExecuteCronJob(&CronJob{ID: "muted", SessionKey: "test:user", Prompt: "MUTED TASK", Mute: true})
	}()
	call := awaitSharedCompatCall(t, a)
	call.finish("MUTED RESULT")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(env.visible(), "MUTED RESULT") {
		t.Fatal("muted task delivered")
	}
	call.session.emit(Event{Type: EventText, Content: "EXTERNAL VISIBLE", Metadata: map[string]any{"phase": "commentary"}})
	env.await("EXTERNAL VISIBLE")
}

func TestSharedForegroundQueueDoesNotInjectInvocationContext(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	env.e.dataDir = "/tmp/bridge data"
	env.send("first question")
	first := awaitSharedCompatCall(t, a)
	if first.prompt != "first question" {
		t.Fatalf("application context leaked into user message: %s", first.prompt)
	}
	env.send("second question")
	first.finish("FIRST RESULT")
	second := awaitSharedCompatCall(t, a)
	if second.prompt != "second question" {
		t.Fatal("queued message contains unexpected context")
	}
	second.finish("SECOND RESULT")
	env.await("FIRST RESULT")
	env.await("SECOND RESULT")
	h := env.e.sessions.GetOrCreateActive("test:user").GetHistory(0)
	if len(h) != 4 || h[0].Content != "first question" || h[2].Content != "second question" {
		t.Fatalf("queue/history: %+v", h)
	}
}

func TestSharedForegroundRoutesOnlyAcknowledgedTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &sharedForeground{ctx: ctx, cancel: cancel, events: make(chan Event, 4)}
	f.route(Event{Type: EventResult, TurnID: "external", Content: "EXTERNAL"})
	f.route(Event{Type: EventResult, TurnID: "ours", Content: "OUR RESULT"})
	external := f.bind("ours")
	if len(external) != 1 || external[0].Content != "EXTERNAL" {
		t.Fatal("external result consumed")
	}
	if e := <-f.events; e.Content != "OUR RESULT" {
		t.Fatal("local result lost")
	}
	if f.route(Event{Type: EventText, TurnID: "external", Content: "LATER"}) {
		t.Fatal("external turn routed into local UI")
	}
}

type sharedCompatTTS struct{ calls chan string }

func (s sharedCompatTTS) Synthesize(_ context.Context, text string, _ TTSSynthesisOpts) ([]byte, string, error) {
	s.calls <- text
	return []byte("voice"), "mp3", nil
}

type sharedCompatAudioPlatform struct {
	*stubCardPlatform
	mu    sync.Mutex
	audio int
}

func (p *sharedCompatAudioPlatform) SendAudio(context.Context, any, []byte, string) error {
	p.mu.Lock()
	p.audio++
	p.mu.Unlock()
	return nil
}
func TestSharedForegroundVoiceReplyUsesExistingTTS(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	p := &sharedCompatAudioPlatform{stubCardPlatform: env.p}
	voice := sharedCompatTTS{make(chan string, 1)}
	env.e.tts = &TTSCfg{Enabled: true, TTS: voice}
	env.e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", UserID: "user", Content: "voice question", ReplyCtx: "reply", FromVoice: true})
	call := awaitSharedCompatCall(t, a)
	call.finish("VOICE ANSWER")
	select {
	case text := <-voice.calls:
		if !strings.Contains(text, "VOICE ANSWER") {
			t.Fatal(text)
		}
	case <-time.After(time.Second):
		t.Fatal("TTS not invoked")
	}
}

func TestSharedForegroundAndExternalTurnsUseStreamingCards(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(fmt.Sprint(external), func(t *testing.T) {
			env, a := newSharedCompatEnv(t)
			card := &recordingStreamCard{}
			p := &recordingStreamCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}, card: card}
			if external {
				env.e.agent = &sharedTestAgent{}
				env.e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", UserID: "user", Content: "/attach first", ReplyCtx: "reply"})
				as := env.e.agent.(*sharedTestAgent).connection("first")
				as.emit(Event{Type: EventTurnStarted, TurnID: "external"})
				as.emit(Event{Type: EventText, TurnID: "external", Content: "STREAMED RESULT"})
				as.emit(Event{Type: EventResult, TurnID: "external", Content: "STREAMED RESULT", Done: true})
			} else {
				env.e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", UserID: "user", Content: "question", ReplyCtx: "reply"})
				awaitSharedCompatCall(t, a).finish("STREAMED RESULT")
			}
			deadline := time.Now().Add(time.Second)
			for !card.finalized() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !strings.Contains(card.finalContent(), "STREAMED RESULT") {
				t.Fatalf("missing final card: %q", card.finalContent())
			}
			if strings.Contains(strings.Join(p.getSent(), "\n"), "STREAMED RESULT") {
				t.Fatal("duplicate plain reply alongside streaming card")
			}
		})
	}
}

func TestSharedForegroundHooksAndExternalHistory(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	events := make(chan HookEvent, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event HookEvent
		_ = json.NewDecoder(r.Body).Decode(&event)
		events <- event
		w.WriteHeader(200)
	}))
	defer server.Close()
	async := false
	env.e.SetHooks(NewHookManager("shared", []HookConfig{{Event: "*", Type: "http", URL: server.URL, Async: &async}}, "sh", "-c", ""))
	env.send("question")
	call := awaitSharedCompatCall(t, a)
	call.finish("HOOK RESULT")
	env.await("HOOK RESULT")
	seen := map[HookEventType]bool{}
	deadline := time.After(time.Second)
	for !seen[HookEventMessageReceived] || !seen[HookEventSessionStarted] || !seen[HookEventMessageSent] {
		select {
		case event := <-events:
			seen[event.Event] = true
		case <-deadline:
			t.Fatalf("missing lifecycle hooks: %v", seen)
		}
	}
	call.session.emit(Event{Type: EventUserMessage, TurnID: "external", Content: "CLI QUESTION"})
	call.session.emit(Event{Type: EventTurnStarted, TurnID: "external"})
	call.session.emit(Event{Type: EventText, TurnID: "external", Content: "CLI ANSWER"})
	call.session.emit(Event{Type: EventResult, TurnID: "external", Content: "CLI ANSWER", Done: true})
	env.await("CLI ANSWER")
	env.send("/history 10")
	env.await("CLI QUESTION")
}

func TestSharedRelayQuestionsAreAnswerableFromSourceChat(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	done := make(chan error, 1)
	go func() {
		response, err := env.e.HandleRelay(context.Background(), "other", "test:user", "RELAY QUESTION")
		if err == nil && response != "RELAY RESULT" {
			err = fmt.Errorf("wrong relay response %q", response)
		}
		done <- err
	}()
	call := awaitSharedCompatCall(t, a)
	call.session.emit(Event{Type: EventPermissionRequest, TurnID: call.turn, RequestID: "relay-question", Questions: []UserQuestion{{ID: "q", Question: "Choose for relay", Options: []UserQuestionOption{{Label: "A"}}}}})
	env.await("Choose for relay")
	env.send(env.button("A"))
	call.session.mu.Lock()
	responses := append([]PermissionResult(nil), call.session.responses...)
	call.session.mu.Unlock()
	if len(responses) != 1 {
		t.Fatalf("source reply did not reach relay: %v", responses)
	}
	call.finish("RELAY RESULT")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("relay stuck")
	}
	if strings.Contains(env.visible(), "RELAY RESULT") {
		t.Fatal("relay sent duplicate final result instead of returning it")
	}
}

func TestSharedRelayTimeoutReturnsWithoutEventsAndPreservesPendingReader(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := env.e.HandleRelay(ctx, "other", "test:user", "RELAY TIMEOUT"); done <- err }()
	call := awaitSharedCompatCall(t, a)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("timeout: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("no-event relay failed to honor cancellation")
	}
	call.session.emit(Event{Type: EventPermissionRequest, TurnID: call.turn, RequestID: "after-timeout", ToolInput: "LATE APPROVAL", Decisions: []string{"cancel"}})
	env.await("LATE APPROVAL")
	env.send(env.button(env.e.i18n.T(MsgSharedCancelDecision)))
	call.finish("LATE RESULT")
}

type sharedCompatHistoryAgent struct {
	*sharedSettingsTestAgent
	history    []HistoryEntry
	historyErr error
}

func (a *sharedCompatHistoryAgent) GetSessionHistory(context.Context, string, int) ([]HistoryEntry, error) {
	return a.history, a.historyErr
}
func TestSharedHistoryUsesDaemonEvenWhenLocalHistoryExists(t *testing.T) {
	env, a, _ := newSharedSettingsEnv(t)
	env.e.agent = &sharedCompatHistoryAgent{sharedSettingsTestAgent: a, history: []HistoryEntry{{Role: "user", Content: "SERVER CLI QUESTION"}, {Role: "assistant", Content: "SERVER CLI ANSWER"}}}
	env.send("/history 10")
	env.await("SERVER CLI QUESTION")
	env.await("SERVER CLI ANSWER")
}

func TestSharedManagementSwitchValidatesBeforeReplacingObserver(t *testing.T) {
	env, _, first := newSharedSettingsEnv(t)
	second := env.e.sessions.NewSideSession("test:user", "Second")
	second.SetAgentSessionID("second", "shared-test")
	second.AddHistory("user", "SECOND HISTORY")
	w := httptest.NewRecorder()
	body := fmt.Sprintf(`{"session_key":"test:user","session_id":%q}`, second.ID)
	(&ManagementServer{}).handleProjectSessionSwitch(w, httptest.NewRequest("POST", "/switch", strings.NewReader(body)), env.e)
	if w.Code != 200 {
		t.Fatalf("switch: %d %s", w.Code, w.Body)
	}
	if first.Alive() {
		t.Fatal("old observer still alive")
	}
	as := env.e.agent.(*sharedSettingsTestAgent).connection("second")
	as.emit(Event{Type: EventText, Content: "SECOND OBSERVER LIVE", Metadata: map[string]any{"phase": "commentary"}})
	env.await("SECOND OBSERVER LIVE")
	bad := env.e.sessions.NewSideSession("test:user", "Bad")
	bad.SetAgentSessionID("bad", "shared-test")
	w = httptest.NewRecorder()
	body = fmt.Sprintf(`{"session_key":"test:user","session_id":%q}`, bad.ID)
	(&ManagementServer{}).handleProjectSessionSwitch(w, httptest.NewRequest("POST", "/switch", strings.NewReader(body)), env.e)
	if w.Code == 200 || !as.Alive() || env.e.sessions.GetOrCreateActive("test:user").GetAgentSessionID() != "second" {
		t.Fatal("failed switch replaced current observer")
	}
}

func TestSharedCancelFailureDoesNotClearHistoryOrCreateSession(t *testing.T) {
	env, _, as := newSharedSettingsEnv(t)
	as.mu.Lock()
	as.interruptError = true
	as.mu.Unlock()
	env.send("/cancel")
	env.await("simulated interrupt failure")
	assertSharedSettingsPreserved(t, env, as)
}

func TestSharedDetachDuringForegroundLeavesTurnAndDropsQueuedInput(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	env.send("FIRST QUESTION")
	call := awaitSharedCompatCall(t, a)
	env.send("QUEUED QUESTION")
	env.send("/detach")
	env.await(env.e.i18n.T(MsgSharedDetached))
	if call.session.RuntimeState().TurnID != call.turn {
		t.Fatal("detach interrupted daemon turn")
	}
	call.finish("AFTER DETACH")
	time.Sleep(10 * time.Millisecond)
	if strings.Contains(env.visible(), "AFTER DETACH") {
		t.Fatal("detached observer delivered result")
	}
	select {
	case extra := <-a.calls:
		t.Fatalf("queued turn started after detach: %v", extra)
	default:
	}
}

func TestSharedDaemonHistoryDoesNotExposeBridgeInvocationInstructions(t *testing.T) {
	env, _ := newSharedCompatEnv(t)
	env.e.dataDir = "/tmp/bridge data"
	prompt := "[cc-connect invocation context]\n{\"project\":\"shared\",\"session_key\":\"test:user\"}\nLegacy invocation instructions\n[/cc-connect invocation context]\n[cc-connect sender_id=user]\nORIGINAL USER INPUT"
	if got := StripSharedBridgePrompt("Configured first-turn preamble\n" + prompt); got != "ORIGINAL USER INPUT" {
		t.Fatalf("internal instructions visible: %s", got)
	}
	plain := "CLI USER INPUT"
	if StripSharedBridgePrompt(plain) != plain {
		t.Fatal("ordinary CLI input changed")
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

func TestHistoryTimestampUsesLocalTimeAndMarksMissing(t *testing.T) {
	e := &Engine{i18n: NewI18n(LangEnglish)}
	timestamp := time.Date(2026, 10, 2, 12, 34, 56, 0, time.FixedZone("source", 7*60*60+13))
	if got, want := e.historyTimestamp(timestamp), timestamp.Local().Format("15:04:05"); got != want {
		t.Fatalf("got %q want local %q", got, want)
	}
	if got := e.historyTimestamp(time.Time{}); got != e.i18n.T(MsgHistoryTimeUnknown) {
		t.Fatalf("missing timestamp=%q", got)
	}
}
