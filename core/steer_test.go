package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type steerTestCall struct{ turnID, content string }

type steerTestSession struct {
	steerImages []ImageAttachment
	steerFiles  []FileAttachment
	stubAgentSession
	mu           sync.Mutex
	turnID       string
	sends        []string
	steers       []steerTestCall
	steerErr     error
	steerStarted chan struct{}
	steerRelease <-chan struct{}
	events       chan Event
	closed       bool
}

func newSteerTestSession() *steerTestSession {
	return &steerTestSession{turnID: "turn-1", events: make(chan Event, 16)}
}
func (s *steerTestSession) CurrentTurnID() string { s.mu.Lock(); defer s.mu.Unlock(); return s.turnID }
func (s *steerTestSession) Send(content, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends = append(s.sends, content)
	s.turnID = fmt.Sprintf("turn-%d", len(s.sends))
	return nil
}
func (s *steerTestSession) Steer(ctx context.Context, expected, content string, images []ImageAttachment, files []FileAttachment) error {
	s.mu.Lock()
	s.steers = append(s.steers, steerTestCall{expected, content})
	s.steerImages = append([]ImageAttachment(nil), images...)
	s.steerFiles = append([]FileAttachment(nil), files...)
	err := s.steerErr
	if expected != s.turnID || expected == "" {
		err = ErrSteerNotActive
	}
	started, release := s.steerStarted, s.steerRelease
	s.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ErrSteerOutcomeUnknown
		}
	}
	return err
}

func (s *steerTestSession) Events() <-chan Event { return s.events }
func (s *steerTestSession) Alive() bool          { s.mu.Lock(); defer s.mu.Unlock(); return !s.closed }
func (s *steerTestSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
func (s *steerTestSession) snapshot() ([]string, []steerTestCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sends...), append([]steerTestCall(nil), s.steers...)
}

type steerTestPlatform struct {
	stubPlatformEngine
	cardMu      sync.Mutex
	cards       []*Card
	taskHandler CardTaskActionHandler
}

func newSteerTestPlatform() *steerTestPlatform {
	return &steerTestPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
}
func (p *steerTestPlatform) SetCardTaskActionHandler(h CardTaskActionHandler) { p.taskHandler = h }
func (p *steerTestPlatform) ReplyCard(ctx context.Context, replyCtx any, card *Card) error {
	p.cardMu.Lock()
	p.cards = append(p.cards, card)
	p.cardMu.Unlock()
	return p.Reply(ctx, replyCtx, card.RenderText())
}
func (p *steerTestPlatform) SendCard(ctx context.Context, replyCtx any, card *Card) error {
	return p.ReplyCard(ctx, replyCtx, card)
}
func (p *steerTestPlatform) action(t *testing.T, prefix string) string {
	t.Helper()
	p.cardMu.Lock()
	defer p.cardMu.Unlock()
	for i := len(p.cards) - 1; i >= 0; i-- {
		for _, element := range p.cards[i].Elements {
			if actions, ok := element.(CardActions); ok {
				for _, button := range actions.Buttons {
					if strings.HasPrefix(button.Value, prefix) {
						return button.Value
					}
				}
			}
		}
	}
	t.Fatalf("no %q action in %d cards", prefix, len(p.cards))
	return ""
}

type steerTestEnv struct {
	e     *Engine
	p     *steerTestPlatform
	s     *steerTestSession
	state *interactiveState
	key   string
}

func newSteerTestEnv(t *testing.T) *steerTestEnv {
	t.Helper()
	p := newSteerTestPlatform()
	s := newSteerTestSession()
	e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	state := &interactiveState{agentSession: s, platform: p, replyCtx: "ctx", turnUserID: "user1", stopCh: make(chan struct{})}
	key := "test:chat:user1"
	e.interactiveStates[key] = state
	return &steerTestEnv{e: e, p: p, s: s, state: state, key: key}
}
func (env *steerTestEnv) queue(t *testing.T, content string) string {
	t.Helper()
	msg := &Message{SessionKey: env.key, ChannelKey: "chat", ChannelID: "chat", UserID: "user1", MessageID: "msg-" + content, Content: content, ReplyCtx: "ctx", Platform: "test"}
	if !env.e.queueMessageForBusySession(env.p, msg, env.key) {
		t.Fatal("message was not queued")
	}
	return env.p.action(t, "steer:")
}
func (env *steerTestEnv) act(action string) CardTaskActionResult {
	return env.e.handleTaskAction(env.p, context.Background(), CardTaskAction{Action: action, SessionKey: env.key, UserID: "user1", ChatID: "chat", ReplyCtx: "ctx"})
}
func (env *steerTestEnv) pending() []string {
	env.state.mu.Lock()
	defer env.state.mu.Unlock()
	result := make([]string, 0, len(env.state.pendingMessages))
	for _, msg := range env.state.pendingMessages {
		result = append(result, msg.content)
	}
	return result
}

func TestSteerQueue_DefaultFIFOAndPromotionNeverStartsAnotherTurn(t *testing.T) {
	env := newSteerTestEnv(t)
	env.queue(t, "next task")
	action := env.queue(t, "correction")
	if got := env.pending(); !reflect.DeepEqual(got, []string{"next task", "correction"}) {
		t.Fatalf("queue=%v", got)
	}
	if sends, steers := env.s.snapshot(); len(sends) != 0 || len(steers) != 0 {
		t.Fatalf("ordinary queue delivered input: sends=%v steers=%v", sends, steers)
	}
	result := env.act(action)
	if result.Card == nil || result.Toast == "" {
		t.Fatalf("missing visible promotion receipt: %+v", result)
	}
	if got := env.pending(); !reflect.DeepEqual(got, []string{"next task"}) {
		t.Fatalf("promotion changed unrelated FIFO message: %v", got)
	}
	sends, steers := env.s.snapshot()
	if len(sends) != 0 || !reflect.DeepEqual(steers, []steerTestCall{{"turn-1", "correction"}}) {
		t.Fatalf("sends=%v steers=%v", sends, steers)
	}
	env.act(action)
	_, steers = env.s.snapshot()
	if len(steers) != 1 {
		t.Fatalf("duplicate callback resubmitted input: %v", steers)
	}
}

func TestSteerQueue_CallbackIdentityCannotCrossUserChatOrSession(t *testing.T) {
	for _, field := range []string{"user", "chat", "session"} {
		t.Run(field, func(t *testing.T) {
			env := newSteerTestEnv(t)
			action := env.queue(t, "correction")
			callback := CardTaskAction{Action: action, SessionKey: env.key, UserID: "user1", ChatID: "chat"}
			switch field {
			case "user":
				callback.UserID = "intruder"
			case "chat":
				callback.ChatID = "other-chat"
			case "session":
				callback.SessionKey = "test:other:user1"
			}
			result := env.e.handleTaskAction(env.p, context.Background(), callback)
			if result.Toast == "" {
				t.Fatal("foreign callback has no visible rejection")
			}
			if got := env.pending(); !reflect.DeepEqual(got, []string{"correction"}) {
				t.Fatalf("foreign callback changed queue: %v", got)
			}
			if _, steers := env.s.snapshot(); len(steers) != 0 {
				t.Fatalf("foreign callback steered: %v", steers)
			}
		})
	}
}

func TestSteerQueue_StaleTurnCannotTargetReplacementTurn(t *testing.T) {
	env := newSteerTestEnv(t)
	action := env.queue(t, "correction")
	env.s.mu.Lock()
	env.s.turnID = "turn-2"
	env.s.mu.Unlock()
	result := env.act(action)
	if result.Toast == "" {
		t.Fatal("missing stale-turn receipt")
	}
	if _, steers := env.s.snapshot(); len(steers) != 0 {
		t.Fatalf("stale callback reached replacement turn: %v", steers)
	}
	if got := env.pending(); !reflect.DeepEqual(got, []string{"correction"}) {
		t.Fatalf("stale action lost queued input: %v", got)
	}
}

func TestSteerQueue_RejectionKeepsFIFOButUnknownOutcomePreventsReplay(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want []string
	}{
		{"definite rejection", errors.New("input rejected"), []string{"first", "correction"}},
		{"unknown outcome", fmt.Errorf("transport lost: %w", ErrSteerOutcomeUnknown), []string{"first"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSteerTestEnv(t)
			env.queue(t, "first")
			action := env.queue(t, "correction")
			env.s.mu.Lock()
			env.s.steerErr = tc.err
			env.s.mu.Unlock()
			result := env.act(action)
			if result.Toast == "" {
				t.Fatal("missing failure receipt")
			}
			if got := env.pending(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("queue=%v want=%v", got, tc.want)
			}
			if sends, _ := env.s.snapshot(); len(sends) != 0 {
				t.Fatalf("steer failure silently became Send: %v", sends)
			}
			if errors.Is(tc.err, ErrSteerOutcomeUnknown) {
				env.act(action)
				_, steers := env.s.snapshot()
				if len(steers) != 1 {
					t.Fatalf("unknown outcome was retried: %v", steers)
				}
			}
		})
	}
}

func TestSteerQueue_CancelRemovesOnlySelectedMessage(t *testing.T) {
	env := newSteerTestEnv(t)
	env.queue(t, "first")
	env.queue(t, "cancel me")
	action := env.p.action(t, "unqueue:")
	result := env.act(action)
	if result.Card == nil || result.Toast == "" {
		t.Fatalf("missing cancellation receipt: %+v", result)
	}
	if got := env.pending(); !reflect.DeepEqual(got, []string{"first"}) {
		t.Fatalf("cancel changed unrelated input: %v", got)
	}
	env.act(action)
	if sends, steers := env.s.snapshot(); len(sends) != 0 || len(steers) != 0 {
		t.Fatalf("cancel delivered input: sends=%v steers=%v", sends, steers)
	}
}

func waitSteerTest(t *testing.T, reason string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", reason)
}

func TestSteerQueue_ConcurrentDuplicateCallbacksSubmitOnce(t *testing.T) {
	env := newSteerTestEnv(t)
	action := env.queue(t, "correction")
	var callers sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		callers.Add(1)
		go func() { defer callers.Done(); <-start; env.act(action) }()
	}
	close(start)
	callers.Wait()
	if sends, steers := env.s.snapshot(); len(sends) != 0 || len(steers) != 1 {
		t.Fatalf("duplicate race delivered sends=%v steers=%v", sends, steers)
	}
	if got := env.pending(); len(got) != 0 {
		t.Fatalf("accepted correction remains queued: %v", got)
	}
}

func TestSteerCommand_CannotSteerAnotherUsersActiveTurn(t *testing.T) {
	env := newSteerTestEnv(t)
	env.e.cmdSteer(env.p, &Message{SessionKey: env.key, ChannelID: "chat", UserID: "intruder", ReplyCtx: "ctx"}, []string{"change", "the", "task"})
	if sends, steers := env.s.snapshot(); len(sends) != 0 || len(steers) != 0 {
		t.Fatalf("foreign command delivered sends=%v steers=%v", sends, steers)
	}
	if len(env.p.getSent()) == 0 {
		t.Fatal("foreign command has no visible rejection")
	}
}

func TestSteerCommand_UnsupportedSessionDoesNotFallbackToSend(t *testing.T) {
	env := newSteerTestEnv(t)
	ordinary := newQueuingSession("exec-session")
	env.state.agentSession = ordinary
	env.e.cmdSteer(env.p, &Message{SessionKey: env.key, ChannelID: "chat", UserID: "user1", ReplyCtx: "ctx"}, []string{"change", "the", "task"})
	ordinary.sendMu.Lock()
	defer ordinary.sendMu.Unlock()
	if len(ordinary.sendCalls) != 0 {
		t.Fatalf("unsupported steer fell back to Send: %v", ordinary.sendCalls)
	}
	if len(env.p.getSent()) == 0 {
		t.Fatal("unsupported command has no visible explanation")
	}
}

func TestSteerQueue_RecallDuringSubmissionNeverReplaysRecalledInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"accepted", nil}, {"rejected", errors.New("rejected")}, {"uncertain", ErrSteerOutcomeUnknown}} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSteerTestEnv(t)
			action := env.queue(t, "correction")
			release := make(chan struct{})
			started := make(chan struct{}, 1)
			env.s.mu.Lock()
			env.s.steerRelease = release
			env.s.steerStarted = started
			env.s.steerErr = tc.err
			env.s.mu.Unlock()
			result := make(chan CardTaskActionResult, 1)
			go func() { result <- env.act(action) }()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("steer never started")
			}
			env.e.ReceiveMessage(env.p, &Message{SessionKey: env.key, UserID: "user1", MessageID: "msg-correction", Recalled: true})
			// Cancellation while submission is unresolved must not claim it was undone.
			cancellation := env.act(strings.Replace(action, "steer:", "unqueue:", 1))
			if cancellation.Toast == env.e.i18n.T(MsgSteerCancelled) {
				t.Fatal("in-flight input falsely reported cancelled")
			}
			close(release)
			select {
			case settled := <-result:
				if tc.err != nil && !errors.Is(tc.err, ErrSteerOutcomeUnknown) && settled.Toast == env.e.i18n.T(MsgSteerFailed) {
					t.Fatal("recalled message falsely reported as still queued")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("steer never settled")
			}
			if got := env.pending(); len(got) != 0 {
				t.Fatalf("recalled input returned to queue: %v", got)
			}
			env.act(action)
			if sends, steers := env.s.snapshot(); len(sends) != 0 || len(steers) != 1 {
				t.Fatalf("recalled input replayed: sends=%v steers=%v", sends, steers)
			}
		})
	}
}

type steerUnsupportedSupplementSession struct{ *queuingAgentSession }

func (*steerUnsupportedSupplementSession) SupportsLegacySupplement() bool { return false }

func TestSteerCommand_PsRespectsOneShotSessionOptOut(t *testing.T) {
	env := newSteerTestEnv(t)
	ordinary := &steerUnsupportedSupplementSession{newQueuingSession("exec-session")}
	env.state.agentSession = ordinary
	session := env.e.sessions.GetOrCreateActive(env.key)
	if !session.TryLock() {
		t.Fatal("session already busy")
	}
	defer session.Unlock()
	env.e.cmdPs(env.p, &Message{SessionKey: env.key, ChannelID: "chat", UserID: "user1", ReplyCtx: "ctx"}, []string{"correction"})
	ordinary.sendMu.Lock()
	defer ordinary.sendMu.Unlock()
	if len(ordinary.sendCalls) != 0 {
		t.Fatalf("one-shot /ps started another process: %v", ordinary.sendCalls)
	}
	if !strings.Contains(strings.Join(env.p.getSent(), "\n"), env.e.i18n.T(MsgSteerUnsupported)) {
		t.Fatalf("unsupported explanation missing: %v", env.p.getSent())
	}
}

func TestSteerMessages_AllSupportedLanguagesPresent(t *testing.T) {
	keys := []MsgKey{MsgSteerSubmitting, MsgSteerExpired, MsgSteerQueued, MsgSteerButton, MsgSteerCancelButton, MsgSteerAccepted, MsgSteerCancelled, MsgSteerUnknown, MsgSteerFailed, MsgSteerEnded, MsgSteerNoTurn, MsgSteerUnsupported, MsgSteerUsage, MsgSteerRejected, MsgBuiltinCmdSteer, MsgSteerQueueHint}
	for _, key := range keys {
		for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
			if strings.TrimSpace(messages[key][lang]) == "" {
				t.Errorf("missing %s translation for %s", lang, key)
			}
		}
	}
}

func TestSteerQueue_PromotionSettlesBeforeCompletedTurnDrainsFIFO(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want []string
	}{
		{"accepted", nil, []string{"original", "first queued"}},
		{"rejected", errors.New("rejected"), []string{"original", "first queued", "correction"}},
		{"uncertain", ErrSteerOutcomeUnknown, []string{"original", "first queued"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newSteerTestPlatform()
			s := newSteerTestSession()
			e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
			t.Cleanup(func() { stopSteerTestEngine(t, e) })
			send := func(id, content string) {
				e.ReceiveMessage(p, &Message{SessionKey: "test:chat:user1", ChannelID: "chat", UserID: "user1", MessageID: id, Content: content, Platform: "test", ReplyCtx: "ctx"})
			}
			send("original", "original")
			waitSteerTest(t, "original Send", func() bool { sends, _ := s.snapshot(); return len(sends) == 1 })
			send("first", "first queued")
			send("correction", "correction")
			action := p.action(t, "steer:")
			release := make(chan struct{})
			started := make(chan struct{}, 1)
			s.mu.Lock()
			s.steerRelease = release
			s.steerStarted = started
			s.steerErr = tc.err
			s.mu.Unlock()
			done := make(chan CardTaskActionResult, 1)
			go func() {
				done <- e.handleTaskAction(p, context.Background(), CardTaskAction{Action: action, SessionKey: "test:chat:user1", UserID: "user1", ChatID: "chat", ReplyCtx: "ctx"})
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("submission did not start")
			}
			s.events <- Event{Type: EventResult, Content: "original answer", Done: true}
			waitSteerTest(t, "original answer is visible", func() bool { return strings.Contains(strings.Join(p.getSent(), "\n"), "original answer") })
			if sends, _ := s.snapshot(); len(sends) != 1 {
				t.Fatalf("queue drained during unresolved submission: %v", sends)
			}
			close(release)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("submission did not settle")
			}
			waitSteerTest(t, "first queued turn", func() bool { sends, _ := s.snapshot(); return len(sends) >= 2 })
			s.events <- Event{Type: EventResult, Content: "first queued answer", Done: true}
			if tc.err != nil && !errors.Is(tc.err, ErrSteerOutcomeUnknown) {
				waitSteerTest(t, "rejected correction resumes in FIFO order", func() bool { sends, _ := s.snapshot(); return len(sends) >= 3 })
				s.events <- Event{Type: EventResult, Content: "correction answer", Done: true}
			}
			session := e.sessions.GetOrCreateActive("test:chat:user1")
			waitSteerTest(t, "queue finished", func() bool { return !session.Busy() })
			sends, _ := s.snapshot()
			if !reflect.DeepEqual(sends, tc.want) {
				t.Fatalf("delivered prompts=%v want=%v", sends, tc.want)
			}
		})
	}
}

func TestSteerQueue_DequeuedTurnTransfersSteeringOwnership(t *testing.T) {
	p := newSteerTestPlatform()
	s := newSteerTestSession()
	e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	t.Cleanup(func() { stopSteerTestEngine(t, e) })
	send := func(user, id, content string) {
		e.ReceiveMessage(p, &Message{SessionKey: "test:chat", ChannelID: "chat", UserID: user, MessageID: id, Content: content, Platform: "test", ReplyCtx: "ctx"})
	}
	send("user1", "original", "original")
	waitSteerTest(t, "first turn", func() bool { sends, _ := s.snapshot(); return len(sends) == 1 })
	send("user2", "queued", "user2 task")
	s.events <- Event{Type: EventResult, Content: "user1 done", Done: true}
	waitSteerTest(t, "second owner's turn", func() bool { sends, _ := s.snapshot(); return len(sends) == 2 })
	send("user1", "wrong-owner", "/steer wrong correction")
	if _, steers := s.snapshot(); len(steers) != 0 {
		t.Fatalf("previous owner steered replacement task: %v", steers)
	}
	send("user2", "correct-owner", "/steer correct correction")
	waitSteerTest(t, "new owner accepted", func() bool { return strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(MsgSteerAccepted)) })
	_, steers := s.snapshot()
	if !reflect.DeepEqual(steers, []steerTestCall{{"turn-2", "correct correction"}}) {
		t.Fatalf("steers=%v", steers)
	}
	s.events <- Event{Type: EventResult, Content: "user2 done", Done: true}
	waitSteerTest(t, "second result", func() bool { return strings.Contains(strings.Join(p.getSent(), "\n"), "user2 done") })
}

func TestSteerCommand_QuotedContextAndAttachmentsReachSameTurn(t *testing.T) {
	env := newSteerTestEnv(t)
	images := []ImageAttachment{{MimeType: "image/png", FileName: "example.png", Data: []byte("image")}}
	files := []FileAttachment{{MimeType: "text/plain", FileName: "notes.txt", Data: []byte("notes")}}
	env.e.ReceiveMessage(env.p, &Message{SessionKey: env.key, ChannelID: "chat", UserID: "user1", MessageID: "supplement", Content: "/补充 inspect this", ExtraContent: "Quoted original task", Images: images, Files: files, ReplyCtx: "ctx"})
	env.s.mu.Lock()
	defer env.s.mu.Unlock()
	if !reflect.DeepEqual(env.s.steers, []steerTestCall{{"turn-1", "Quoted original task\ninspect this"}}) {
		t.Fatalf("steered prompt=%v", env.s.steers)
	}
	if !reflect.DeepEqual(env.s.steerImages, images) || !reflect.DeepEqual(env.s.steerFiles, files) {
		t.Fatalf("attachments lost: images=%v files=%v", env.s.steerImages, env.s.steerFiles)
	}
	if len(env.s.sends) != 0 {
		t.Fatalf("image command became ordinary Send: %v", env.s.sends)
	}
}

func TestSteerCommand_EffectiveRoleGateAppliesToCommandsAndExistingCards(t *testing.T) {
	for _, tc := range []struct {
		name    string
		project []string
		roles   []RoleInput
		allowed bool
	}{
		{name: "project disables steer", project: []string{"steer"}},
		{name: "project disables all", project: []string{"*"}},
		{name: "role disables steer", roles: []RoleInput{{Name: "member", UserIDs: []string{"*"}, DisabledCommands: []string{"steer"}}}},
		{name: "role disables all", roles: []RoleInput{{Name: "member", UserIDs: []string{"*"}, DisabledCommands: []string{"*"}}}},
		{name: "role permission overrides project ban", project: []string{"steer"}, roles: []RoleInput{{Name: "member", UserIDs: []string{"*"}, DisabledCommands: []string{}}}, allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSteerTestEnv(t)
			action := env.queue(t, "previously queued")
			env.e.SetDisabledCommands(tc.project)
			if tc.roles != nil {
				roles := NewUserRoleManager()
				roles.Configure("member", tc.roles)
				env.e.SetUserRoles(roles)
				t.Cleanup(roles.Stop)
			}
			env.act(action)
			env.e.cmdSteer(env.p, &Message{SessionKey: env.key, UserID: "user1", ChannelID: "chat", ReplyCtx: "ctx"}, []string{"correction"})
			_, steers := env.s.snapshot()
			want := 0
			if tc.allowed {
				want = 2
			}
			if len(steers) != want {
				t.Fatalf("effective permission allowed %d submissions, want %d", len(steers), want)
			}
			env.p.cardMu.Lock()
			before := len(env.p.cards)
			env.p.cardMu.Unlock()
			if !env.e.queueMessageForBusySession(env.p, &Message{SessionKey: env.key, ChannelID: "chat", UserID: "user1", MessageID: "new", Content: "new task", ReplyCtx: "ctx"}, env.key) {
				t.Fatal("ordinary message was not queued")
			}
			env.p.cardMu.Lock()
			after := len(env.p.cards)
			env.p.cardMu.Unlock()
			if !tc.allowed && after != before {
				t.Fatal("disabled steer was advertised on a new card")
			}
			if tc.allowed && after != before+1 {
				t.Fatal("permitted role did not receive task actions")
			}
		})
	}
}

func TestSteerQueue_PreviewEscapesMarkupButSubmissionPreservesOriginal(t *testing.T) {
	env := newSteerTestEnv(t)
	content := "<at id=all></at> [Open](https://example.com) **important** `code`"
	action := env.queue(t, content)
	env.p.cardMu.Lock()
	card := env.p.cards[len(env.p.cards)-1]
	env.p.cardMu.Unlock()
	var preview string
	for _, element := range card.Elements {
		if markdown, ok := element.(CardMarkdown); ok {
			preview = markdown.Content
			break
		}
	}
	if strings.Contains(preview, "<at") || strings.Contains(preview, "[Open]") || strings.Contains(preview, "**important**") {
		t.Fatalf("preview contains active user markup: %q", preview)
	}
	for _, literal := range []string{"&lt;at", `\[Open\]`, `\*\*important\*\*`, `\` + "`code" + `\` + "`"} {
		if !strings.Contains(preview, literal) {
			t.Errorf("preview missing escaped literal %q: %q", literal, preview)
		}
	}
	env.act(action)
	_, steers := env.s.snapshot()
	if len(steers) != 1 || steers[0].content != content {
		t.Fatalf("preview escaping changed actual input: %v", steers)
	}
}

func TestSteerQueue_ConsumedActionCapacityDoesNotDisableFutureCards(t *testing.T) {
	for _, retainEnded := range []bool{false, true} {
		t.Run(fmt.Sprintf("retain_pending_ended_%t", retainEnded), func(t *testing.T) {
			env := newSteerTestEnv(t)
			var oldAction string
			consumed := 256
			if retainEnded {
				oldAction = env.queue(t, "still queued from old turn")
				env.s.mu.Lock()
				env.s.turnID = "turn-2"
				env.s.mu.Unlock()
				env.act(oldAction)
				consumed--
			}
			env.state.mu.Lock()
			if env.state.queueActions == nil {
				env.state.queueActions = make(map[string]*queuedTaskAction)
			}
			for i := range consumed {
				env.state.queueActions[fmt.Sprintf("consumed-%d", i)] = &queuedTaskAction{status: MsgSteerQueued, turnID: "old-turn", queued: queuedMessage{actionToken: fmt.Sprintf("consumed-%d", i), content: "already consumed by FIFO"}}
			}
			env.state.mu.Unlock()
			freshAction := env.queue(t, "fresh queued task")
			if freshAction == oldAction {
				t.Fatal("fresh queued message did not receive a new card action")
			}
			if retainEnded {
				result := env.act(strings.Replace(oldAction, "steer:", "unqueue:", 1))
				if result.Toast != env.e.i18n.T(MsgSteerCancelled) {
					t.Fatalf("pending ended action lost cancellation: %+v", result)
				}
			}
			if got := env.pending(); !reflect.DeepEqual(got, []string{"fresh queued task"}) {
				t.Fatalf("capacity cleanup changed pending queue: %v", got)
			}
			if result := env.act(freshAction); result.Toast != env.e.i18n.T(MsgSteerAccepted) {
				t.Fatalf("fresh action not usable after capacity cleanup: %+v", result)
			}
		})
	}
}

func TestSteerCommand_LateAcceptanceAfterNewCannotRestoreClearedHistory(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprintf("queued_%t", queued), func(t *testing.T) {
			env := newSteerTestEnv(t)
			old := env.e.sessions.GetOrCreateActive(env.key)
			old.AddHistory("user", "original task")
			env.state.turnSession = old
			release := make(chan struct{})
			started := make(chan struct{}, 1)
			env.s.mu.Lock()
			env.s.steerRelease = release
			env.s.steerStarted = started
			env.s.mu.Unlock()
			done := make(chan struct{})
			if queued {
				action := env.queue(t, "late correction")
				go func() { defer close(done); env.act(action) }()
			} else {
				go func() {
					defer close(done)
					env.e.cmdSteer(env.p, &Message{SessionKey: env.key, ChannelID: "chat", UserID: "user1", ReplyCtx: "ctx"}, []string{"late correction"})
				}()
			}
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("submission did not start")
			}
			env.e.ReceiveMessage(env.p, &Message{SessionKey: env.key, ChannelID: "chat", UserID: "user1", MessageID: "new-session", Content: "/new", ReplyCtx: "ctx"})
			current := env.e.sessions.GetOrCreateActive(env.key)
			if current.ID == old.ID {
				t.Fatal("/new did not create a replacement session")
			}
			if old.HistoryLen() != 0 {
				t.Fatal("/new did not clear original history")
			}
			close(release)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("submission did not settle")
			}
			if old.HistoryLen() != 0 || current.HistoryLen() != 0 {
				t.Fatalf("late acceptance repopulated history: old=%v current=%v", old.GetHistory(0), current.GetHistory(0))
			}
		})
	}
}

// Await the processor's completion lock after cancellation before TempDir can
// remove the session store. Engine.Stop alone does not join those goroutines.
func stopSteerTestEngine(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.Stop(); err != nil {
		t.Errorf("stop engine: %v", err)
	}
	waitSteerTest(t, "processor shutdown", func() bool {
		for _, session := range e.sessions.AllSessions() {
			if session.Busy() {
				return false
			}
		}
		return true
	})
}

func TestSteerQueue_HistoricalReceiptsReleasePayloadAndClearedQueueSlots(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		err        error
	}{
		{name: "accepted", kind: "steer"},
		{name: "uncertain", kind: "steer", err: ErrSteerOutcomeUnknown},
		{name: "cancelled", kind: "unqueue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSteerTestEnv(t)
			// Keep the backing array observable after removing the final entry, so a
			// shortened slice cannot hide references that would keep payloads alive.
			env.state.pendingMessages = make([]queuedMessage, 0, 2)
			env.queue(t, "unrelated next task")
			content := strings.Repeat("补充说明", 400) + " preserve this full-prompt tail"
			images := []ImageAttachment{{MimeType: "image/png", FileName: "diagram.png", Data: []byte("image payload")}}
			files := []FileAttachment{{MimeType: "application/pdf", FileName: "spec.pdf", Data: []byte("file payload")}}
			msg := &Message{SessionKey: env.key, ChannelID: "chat", UserID: "user1", MessageID: "long-supplement", Content: content, Images: images, Files: files, ReplyCtx: "ctx", Platform: "test"}
			if !env.e.queueMessageForBusySession(env.p, msg, env.key) {
				t.Fatal("supplement was not queued")
			}
			action := env.p.action(t, "steer:")
			token := strings.TrimPrefix(action, "steer:")
			env.state.mu.Lock()
			receipt := env.state.queueActions[token]
			backing := env.state.pendingMessages[:cap(env.state.pendingMessages)]
			env.state.mu.Unlock()
			assertReceiptHasNoPayload := func() {
				t.Helper()
				q := receipt.queued
				if q.images != nil || q.files != nil || q.replyCtx != nil {
					t.Fatalf("historical receipt retains payload-bearing fields: images=%v files=%v replyCtx=%v", q.images, q.files, q.replyCtx)
				}
				if len([]rune(q.content)) > 503 || strings.Contains(q.content, "full-prompt tail") {
					t.Fatalf("historical receipt retained full prompt (%d runes)", len([]rune(q.content)))
				}
				if !strings.HasPrefix(q.content, "补充说明") {
					t.Fatalf("receipt lost preview: %q", q.content)
				}
			}
			assertReceiptHasNoPayload()
			env.s.mu.Lock()
			env.s.steerErr = tc.err
			env.s.mu.Unlock()
			env.act(strings.Replace(action, "steer:", tc.kind+":", 1))
			assertReceiptHasNoPayload()
			if !reflect.DeepEqual(backing[1], queuedMessage{}) {
				t.Fatal("removed queue backing slot still retains the prompt or attachments")
			}
			if got := env.pending(); !reflect.DeepEqual(got, []string{"unrelated next task"}) {
				t.Fatalf("unrelated queue changed: %v", got)
			}
			env.s.mu.Lock()
			defer env.s.mu.Unlock()
			if tc.kind == "steer" {
				if len(env.s.steers) != 1 || env.s.steers[0].content != content {
					t.Fatal("steering submitted the truncated receipt instead of the full prompt")
				}
				if !reflect.DeepEqual(env.s.steerImages, images) || !reflect.DeepEqual(env.s.steerFiles, files) {
					t.Fatal("steering did not receive the original attachments")
				}
			} else if len(env.s.steers) != 0 {
				t.Fatal("cancelling a receipt submitted its payload")
			}
		})
	}
}
