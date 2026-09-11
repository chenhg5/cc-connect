package core

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSteerHistory_LateAcceptancePrecedesAnswer(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprintf("queued_%t", queued), func(t *testing.T) {
			p := newSteerTestPlatform()
			s := newSteerTestSession()
			e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
			t.Cleanup(func() { stopSteerTestEngine(t, e) })
			key := "test:chat:user1"
			send := func(id, content string) {
				e.ReceiveMessage(p, &Message{SessionKey: key, ChannelID: "chat", UserID: "user1", MessageID: id, Content: content, Platform: "test", ReplyCtx: "ctx"})
			}
			send("original", "original")
			waitSteerTest(t, "original Send", func() bool { sends, _ := s.snapshot(); return len(sends) == 1 })
			send("early-correction", "/steer early correction")
			var action string
			if queued {
				send("correction", "correction")
				action = p.action(t, "steer:")
			}
			release, started := make(chan struct{}), make(chan struct{}, 1)
			s.mu.Lock()
			s.steerRelease = release
			s.steerStarted = started
			s.mu.Unlock()
			done := make(chan struct{})
			go func() {
				defer close(done)
				if queued {
					e.handleTaskAction(p, context.Background(), CardTaskAction{Action: action, SessionKey: key, UserID: "user1", ChatID: "chat", ReplyCtx: "ctx"})
				} else {
					send("correction", "/steer correction")
				}
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("steer never started")
			}
			s.events <- Event{Type: EventResult, Content: "answer incorporating correction", Done: true}
			waitSteerTest(t, "answer visible", func() bool {
				return strings.Contains(strings.Join(p.getSent(), "\n"), "answer incorporating correction")
			})
			close(release)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("steer never settled")
			}
			session := e.sessions.GetOrCreateActive(key)
			waitSteerTest(t, "turn complete", func() bool { return !session.Busy() })
			want := []string{"user:original", "user:early correction", "user:correction", "assistant:answer incorporating correction"}
			restored := NewSessionManager(e.sessions.StorePath()).GetOrCreateActive(key)
			for _, stored := range []*Session{session, restored} {
				var got []string
				for _, h := range stored.GetHistory(0) {
					got = append(got, h.Role+":"+h.Content)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("history after save/reload=%q want=%q", got, want)
				}
			}
		})
	}
}

func TestSteerCommand_NewlineAfterCommandUsesCurrentTurn(t *testing.T) {
	env := newSteerTestEnv(t)
	session := env.e.sessions.GetOrCreateActive(env.key)
	if !session.TryLock() {
		t.Fatal("busy")
	}
	defer session.Unlock()
	env.e.ReceiveMessage(env.p, &Message{SessionKey: env.key, ChannelID: "chat", UserID: "user1", MessageID: "correction", Content: "/steer\ncorrection", Platform: "test", ReplyCtx: "ctx"})
	sends, steers := env.s.snapshot()
	if !reflect.DeepEqual(steers, []steerTestCall{{"turn-1", "correction"}}) {
		t.Fatalf("steers=%v sends=%v pending=%q", steers, sends, env.pending())
	}
}

func TestSteerCommand_PsImageUsesCurrentTurn(t *testing.T) {
	env := newSteerTestEnv(t)
	session := env.e.sessions.GetOrCreateActive(env.key)
	if !session.TryLock() {
		t.Fatal("busy")
	}
	defer session.Unlock()
	env.e.ReceiveMessage(env.p, &Message{SessionKey: env.key, ChannelID: "chat", UserID: "user1", MessageID: "correction", Content: "/ps inspect image", Images: []ImageAttachment{{MimeType: "image/png", Data: []byte("image")}}, Platform: "test", ReplyCtx: "ctx"})
	sends, steers := env.s.snapshot()
	if !reflect.DeepEqual(steers, []steerTestCall{{"turn-1", "inspect image"}}) {
		t.Fatalf("steers=%v sends=%v pending=%q", steers, sends, env.pending())
	}
}

func TestSteerCommand_ConcurrentInputGetsExplicitRetryReceipt(t *testing.T) {
	env := newSteerTestEnv(t)
	release, started := make(chan struct{}), make(chan struct{}, 1)
	env.s.mu.Lock()
	env.s.steerRelease = release
	env.s.steerStarted = started
	env.s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.e.cmdSteer(env.p, &Message{SessionKey: env.key, UserID: "user1", ReplyCtx: "ctx"}, []string{"first correction"})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first did not start")
	}
	env.e.cmdSteer(env.p, &Message{SessionKey: env.key, UserID: "user1", ReplyCtx: "ctx"}, []string{"second correction"})
	before := strings.Join(env.p.getSent(), "\n")
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("first did not settle")
	}
	_, steers := env.s.snapshot()
	if len(steers) != 1 || before != env.e.i18n.T(MsgSteerBusy) || len(env.pending()) != 0 {
		t.Fatalf("second input never submitted or queued, but its receipt claims submission in progress: receipt=%q steers=%v pending=%v", before, steers, env.pending())
	}
}

func TestSteerCommand_SupplementAttachmentRouting(t *testing.T) {
	for _, command := range []string{"/ps", "/btw"} {
		for _, backend := range []string{"native", "exec", "legacy", "no-session"} {
			for _, attachment := range []string{"text", "image", "image-only", "file-only"} {
				t.Run(command+"/"+backend+"/"+attachment, func(t *testing.T) {
					env := newSteerTestEnv(t)
					session := env.e.sessions.GetOrCreateActive(env.key)
					if !session.TryLock() {
						t.Fatal("session busy")
					}
					defer session.Unlock()
					ordinary := newQueuingSession("ordinary")
					switch backend {
					case "exec":
						env.state.agentSession = &steerUnsupportedSupplementSession{ordinary}
					case "legacy":
						env.state.agentSession = ordinary
					case "no-session":
						delete(env.e.interactiveStates, env.key)
					}
					content := command
					if attachment == "image" || attachment == "text" {
						content += " inspect input"
					}
					msg := &Message{SessionKey: env.key, ChannelID: "chat", UserID: "user1", MessageID: "supplement", Content: content, Platform: "test", ReplyCtx: "ctx"}
					if attachment == "file-only" {
						msg.Files = []FileAttachment{{FileName: "notes.txt", Data: []byte("notes")}}
					} else if attachment != "text" {
						msg.Images = []ImageAttachment{{MimeType: "image/png", Data: []byte("image")}}
					}
					env.e.ReceiveMessage(env.p, msg)
					_, steers := env.s.snapshot()
					queued := env.pending()
					ordinary.sendMu.Lock()
					sends := append([]string(nil), ordinary.sendCalls...)
					ordinary.sendMu.Unlock()
					wantSteers, wantQueued, wantSends := 0, 0, 0
					if backend == "native" {
						wantSteers = 1
					}
					if backend == "legacy" && (attachment == "image" || attachment == "image-only") {
						wantQueued = 1
					}
					if backend == "legacy" && attachment == "text" {
						wantSends = 1
					}
					if len(steers) != wantSteers || len(queued) != wantQueued || len(sends) != wantSends {
						t.Fatalf("steers=%v queued=%v legacy sends=%v", steers, queued, sends)
					}
					visible := strings.Join(env.p.getSent(), "\n")
					if backend == "exec" && !strings.Contains(visible, env.e.i18n.T(MsgSteerUnsupported)) {
						t.Fatalf("missing unsupported receipt: %q", visible)
					}
					if backend == "no-session" && !strings.Contains(visible, env.e.i18n.T(MsgPsNoSession)) {
						t.Fatalf("missing no-session receipt: %q", visible)
					}
					if backend == "native" {
						env.s.mu.Lock()
						if len(env.s.steerImages) != len(msg.Images) || len(env.s.steerFiles) != len(msg.Files) {
							t.Error("native attachments lost")
						}
						env.s.mu.Unlock()
					}
				})
			}
		}
	}
}

func TestSteerQueue_NonterminalResponsesCannotOverwriteFinalCard(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprintf("rejected_%t", rejected), func(t *testing.T) {
			env := newSteerTestEnv(t)
			first := env.queue(t, "first correction")
			second := env.queue(t, "second correction")
			release, started := make(chan struct{}), make(chan struct{}, 1)
			env.s.mu.Lock()
			env.s.steerRelease = release
			env.s.steerStarted = started
			if rejected {
				env.s.steerErr = fmt.Errorf("explicit rejection")
			}
			env.s.mu.Unlock()
			done := make(chan CardTaskActionResult, 1)
			go func() { done <- env.act(first) }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("not started")
			}
			duplicate := env.act(first)
			busy := env.act(second)
			close(release)
			settled := <-done
			if duplicate.Card != nil || duplicate.Toast != env.e.i18n.T(MsgSteerSubmitting) {
				t.Fatalf("duplicate response=%+v", duplicate)
			}
			if busy.Card != nil || busy.Toast != env.e.i18n.T(MsgSteerBusy) {
				t.Fatalf("second response=%+v", busy)
			}
			if rejected {
				if settled.Card != nil || settled.Toast != env.e.i18n.T(MsgSteerFailed) {
					t.Fatalf("rejection response=%+v", settled)
				}
				env.s.mu.Lock()
				env.s.steerRelease = nil
				env.s.steerStarted = nil
				env.s.steerErr = nil
				env.s.mu.Unlock()
				settled = env.act(first)
			}
			if settled.Card == nil || settled.Toast != env.e.i18n.T(MsgSteerAccepted) {
				t.Fatalf("terminal response=%+v", settled)
			}
			// An older reply carries no card body with which to overwrite this final card.
			replay := env.act(first)
			if replay.Card == nil || replay.Toast != settled.Toast {
				t.Fatalf("terminal replay=%+v", replay)
			}
			if !reflect.DeepEqual(env.pending(), []string{"second correction"}) {
				t.Fatalf("queue=%v", env.pending())
			}
		})
	}
}

func TestSteerQueue_EndedToastStillAllowsCancellation(t *testing.T) {
	env := newSteerTestEnv(t)
	action := env.queue(t, "correction")
	env.s.mu.Lock()
	env.s.turnID = "turn-2"
	env.s.mu.Unlock()
	for range 2 {
		ended := env.act(action)
		if ended.Card != nil || ended.Toast != env.e.i18n.T(MsgSteerEnded) {
			t.Fatalf("ended response=%+v", ended)
		}
	}
	cancelled := env.act(strings.Replace(action, "steer:", "unqueue:", 1))
	if cancelled.Card == nil || cancelled.Toast != env.e.i18n.T(MsgSteerCancelled) || len(env.pending()) != 0 {
		t.Fatalf("cancel=%+v queue=%v", cancelled, env.pending())
	}
	if _, steers := env.s.snapshot(); len(steers) != 0 {
		t.Fatalf("ended input submitted: %v", steers)
	}
}

func TestSteerQueue_OldControlsExpireAfterStopNewOrRestart(t *testing.T) {
	for _, lifecycle := range []string{"stop", "new", "restart"} {
		t.Run(lifecycle, func(t *testing.T) {
			env := newSteerTestEnv(t)
			action := env.queue(t, "correction")
			engine := env.e
			if lifecycle == "restart" {
				engine = NewEngine("test", &controllableAgent{nextSession: env.s}, []Platform{env.p}, t.TempDir()+"/sessions.json", LangEnglish)
				engine.sessions.GetOrCreateActive(env.key)
			} else {
				env.e.ReceiveMessage(env.p, &Message{SessionKey: env.key, ChannelID: "chat", UserID: "user1", MessageID: "lifecycle", Content: "/" + lifecycle, Platform: "test", ReplyCtx: "ctx"})
			}
			for _, kind := range []string{"steer:", "unqueue:"} {
				result := engine.handleTaskAction(env.p, context.Background(), CardTaskAction{Action: strings.Replace(action, "steer:", kind, 1), SessionKey: env.key, UserID: "user1", ChatID: "chat"})
				if result.Toast != engine.i18n.T(MsgSteerExpired) {
					t.Fatalf("old control response=%+v", result)
				}
			}
			if sends, steers := env.s.snapshot(); len(sends) != 0 || len(steers) != 0 {
				t.Fatalf("old control submitted sends=%v steers=%v", sends, steers)
			}
		})
	}
}

func TestSteerQueue_DrainBeforeClickCannotSubmitTwice(t *testing.T) {
	p, s := newSteerTestPlatform(), newSteerTestSession()
	e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	t.Cleanup(func() { stopSteerTestEngine(t, e) })
	key := "test:chat:user1"
	send := func(id, content string) {
		e.ReceiveMessage(p, &Message{SessionKey: key, ChannelID: "chat", UserID: "user1", MessageID: id, Content: content, Platform: "test", ReplyCtx: "ctx"})
	}
	send("original", "original")
	waitSteerTest(t, "original", func() bool { sends, _ := s.snapshot(); return len(sends) == 1 })
	send("queued", "next task")
	action := p.action(t, "steer:")
	s.events <- Event{Type: EventResult, Content: "original answer", Done: true}
	waitSteerTest(t, "queued turn", func() bool { sends, _ := s.snapshot(); return len(sends) == 2 })
	for range 2 {
		result := e.handleTaskAction(p, context.Background(), CardTaskAction{Action: action, SessionKey: key, UserID: "user1", ChatID: "chat"})
		if result.Card == nil || result.Toast != e.i18n.T(MsgSteerExpired) {
			t.Fatalf("consumed control=%+v", result)
		}
	}
	if sends, steers := s.snapshot(); !reflect.DeepEqual(sends, []string{"original", "next task"}) || len(steers) != 0 {
		t.Fatalf("sends=%v steers=%v", sends, steers)
	}
	send("next-correction", "/steer correction for next")
	s.events <- Event{Type: EventResult, Content: "next answer", Done: true}
	waitSteerTest(t, "turn complete", func() bool { return !e.sessions.GetOrCreateActive(key).Busy() })
	var history []string
	for _, entry := range e.sessions.GetOrCreateActive(key).GetHistory(0) {
		history = append(history, entry.Role+":"+entry.Content)
	}
	want := []string{"user:original", "assistant:original answer", "user:next task", "user:correction for next", "assistant:next answer"}
	if !reflect.DeepEqual(history, want) {
		t.Fatalf("queued turn history=%q want=%q", history, want)
	}
}
