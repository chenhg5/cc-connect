package dsh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type enginePlatform struct {
	mu       sync.Mutex
	messages []string
}

func (p *enginePlatform) Name() string                    { return "test" }
func (p *enginePlatform) Start(core.MessageHandler) error { return nil }
func (p *enginePlatform) Stop() error                     { return nil }
func (p *enginePlatform) Reply(_ context.Context, _ any, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, text)
	return nil
}
func (p *enginePlatform) Send(ctx context.Context, reply any, text string) error {
	return p.Reply(ctx, reply, text)
}
func (p *enginePlatform) contains(text string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, message := range p.messages {
		if strings.Contains(message, text) {
			return true
		}
	}
	return false
}

func engineMessage(e *core.Engine, p core.Platform, id, text string, permission bool) {
	e.ReceiveMessage(p, &core.Message{SessionKey: "test:user", Platform: p.Name(), UserID: "user", MessageID: id, Content: text, ReplyCtx: "reply", IsPermissionResponse: permission})
}

func (f *remoteFixture) sendInteraction(sessionID, kind string, request map[string]any) {
	f.mu.Lock()
	peers := make([]*remoteFixturePeer, 0, len(f.peers))
	for _, peer := range f.peers {
		peers = append(peers, peer)
	}
	f.mu.Unlock()
	for _, peer := range peers {
		peer.mu.Lock()
		var streams []string
		for id, sid := range peer.streams {
			if sid == "" {
				streams = append(streams, id)
			}
		}
		peer.mu.Unlock()
		for _, id := range streams {
			peer.send(id, map[string]any{"type": "waterfall", "event": kind, "eventId": "pending", "agentId": sessionID, "request": request})
		}
	}
}

func (f *remoteFixture) sendEngineEvent(sessionID string, event map[string]any) {
	f.mu.Lock()
	peers := make([]*remoteFixturePeer, 0, len(f.peers))
	for _, peer := range f.peers {
		peers = append(peers, peer)
	}
	f.mu.Unlock()
	for _, peer := range peers {
		peer.mu.Lock()
		var streams []string
		for id, sid := range peer.streams {
			if sid == sessionID {
				streams = append(streams, id)
			}
		}
		peer.mu.Unlock()
		for _, id := range streams {
			peer.send(id, map[string]any{"type": "event", "event": event})
		}
	}
}

func TestRemote_EngineInteractionSubmissionCanRetry(t *testing.T) {
	for _, decision := range []string{"allow", "deny", "allow all", "question"} {
		t.Run(decision, func(t *testing.T) {
			f := newRemoteFixture(t)
			var attempts atomic.Int32
			started := make(chan string, 1)
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/session/prompt" {
					var req struct {
						RPCID   string
						Payload struct {
							Args struct{ Request struct{ SessionID string } }
						}
					}
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
						return
					}
					writeServerValue(w, req.RPCID, map[string]any{"accepted": true})
					f.sendEngineEvent(req.Payload.Args.Request.SessionID, evTurnStart(1))
					started <- req.Payload.Args.Request.SessionID
					return
				}
				if r.URL.Path == "/api/$events/result" && attempts.Add(1) == 1 {
					http.Error(w, "temporary failure", http.StatusServiceUnavailable)
					return
				}
				f.handle(w, r)
			}))
			t.Cleanup(proxy.Close)
			a, err := New(map[string]any{"base_url": proxy.URL, "token": "fixture-token", "work_dir": t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			p := &enginePlatform{}
			e := core.NewEngine("test", a, []core.Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			engineMessage(e, p, "task", "run the task", false)
			var sid string
			select {
			case sid = <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("prompt not submitted")
			}
			kind := "approval/request"
			request := map[string]any{"toolName": "bash", "reason": "run ls"}
			answer := decision
			if decision == "question" {
				kind = "user-questions/request"
				request = map[string]any{"questions": []any{map[string]any{"id": "one", "question": "Choose first", "options": []any{map[string]any{"label": "A"}}}, map[string]any{"id": "two", "question": "Choose second", "options": []any{map[string]any{"label": "B"}}}}}
				answer = "B"
			}
			f.sendInteraction(sid, kind, request)
			if decision == "question" {
				waitFor(t, 3*time.Second, func() bool { return p.contains("Choose first") }, "first question")
				engineMessage(e, p, "first", "A", true)
				waitFor(t, 3*time.Second, func() bool { return p.contains("Choose second") }, "second question")
			} else {
				waitFor(t, 3*time.Second, func() bool { return p.contains("bash") }, "approval prompt")
			}
			engineMessage(e, p, "attempt", answer, true)
			waitFor(t, 3*time.Second, func() bool { return p.contains("temporary failure") }, "submission error visible to user")
			engineMessage(e, p, "retry", answer, true)
			waitFor(t, 3*time.Second, func() bool { return attempts.Load() == 2 }, "second click reaches Remote transport")
			f.sendEngineEvent(sid, ev("assistant/message", map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "recovered reply"}}}}))
			f.sendEngineEvent(sid, evTurnEnd(1, "completed"))
			waitFor(t, 3*time.Second, func() bool { return p.contains("recovered reply") }, "reply after successful retry")
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.replies) != 1 {
				t.Fatalf("accepted replies = %d, want 1", len(f.replies))
			}
			value := f.replies[0]["outcome"].(map[string]any)["value"]
			if decision == "question" {
				answers := value.(map[string]any)["answers"].([]any)
				if len(answers) != 2 || fmt.Sprint(answers[0].(map[string]any)["selected"]) != "[A]" || fmt.Sprint(answers[1].(map[string]any)["selected"]) != "[B]" {
					t.Fatalf("lost question answers: %+v", answers)
				}
			} else {
				want := "allowed-once"
				if decision == "deny" {
					want = "rejected"
				}
				if value != want {
					t.Fatalf("decision = %v, want %s", value, want)
				}
			}
		})
	}
}

type startupReplayAgent struct {
	core.Agent
	t *testing.T
}

func (a startupReplayAgent) StartSession(ctx context.Context, id string) (core.AgentSession, error) {
	session, err := a.Agent.StartSession(ctx, id)
	if err == nil {
		s := session.(*dshSession)
		waitFor(a.t, 3*time.Second, func() bool { return len(s.events) > 0 }, "replayed approval buffered before startup returns")
	}
	return session, err
}

func TestRemote_EngineResumedApprovalSurvivesStartupDrain(t *testing.T) {
	f := newRemoteFixture(t)
	dir := t.TempDir()
	f.sessions["remote-1"] = dir
	f.replayApprovalSession = "remote-1"
	store := filepath.Join(t.TempDir(), "sessions.json")
	saved := core.NewSessionManager(store)
	saved.GetOrCreateActive("test:user").SetAgentSessionID("remote-1", "dsh")
	saved.Save()
	a, err := New(map[string]any{"base_url": f.server.URL, "token": "fixture-token", "work_dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	p := &enginePlatform{}
	e := core.NewEngine("test", startupReplayAgent{Agent: a, t: t}, []core.Platform{p}, store, core.LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	engineMessage(e, p, "continue", "continue", false)
	waitFor(t, 3*time.Second, func() bool { return p.contains("bash") }, "original approval visible after resume")
	engineMessage(e, p, "allow", "allow", true)
	waitFor(t, 3*time.Second, func() bool { return p.contains("reply-1") }, "resumed conversation completes")
	engineMessage(e, p, "next", "next task", false)
	waitFor(t, 3*time.Second, func() bool { return p.contains("reply-2") }, "next user turn completes")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sessions) != 1 || len(f.replies) != 1 {
		t.Fatalf("resume forked or lost approval: sessions=%d replies=%d", len(f.sessions), len(f.replies))
	}
}

func TestSession_StopDisarmsWatchdog(t *testing.T) {
	m := newMockDshServer()
	a := newTestAgent(t, m, t.TempDir())
	a.timeout = time.Second
	session, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	s := session.(*dshSession)
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Send("first", "one", nil, nil); err != nil {
		t.Fatal(err)
	}
	done := s.turnDone
	if err := s.CancelTurn(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	default:
		t.Fatal("cancel left the old watchdog armed")
	}
}

func TestSession_LateTurnEndDoesNotFinishNewTurn(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprint(started), func(t *testing.T) {
			m := newMockDshServer()
			a := newTestAgent(t, m, t.TempDir())
			session, err := a.StartSession(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			s := session.(*dshSession)
			t.Cleanup(func() { _ = s.Close() })
			if err := s.Send("first", "one", nil, nil); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(evTurnStart(1))
			s.handleSessionEvent(raw)
			if err := s.CancelTurn(); err != nil {
				t.Fatal(err)
			}
			if err := s.Send("second", "two", nil, nil); err != nil {
				t.Fatal(err)
			}
			if started {
				raw, _ = json.Marshal(evTurnStart(2))
				s.handleSessionEvent(raw)
			}
			s.finishTurn(sessionEventData{Turn: 1})
			select {
			case <-s.turnDone:
				t.Fatal("old turn/end stopped the new watchdog")
			default:
			}
			select {
			case event := <-s.events:
				if event.Type != core.EventResult {
					t.Fatalf("unexpected terminal event: %+v", event)
				}
			default:
				t.Fatal("cancelled turn did not deliver its terminal event")
			}
			if !started {
				raw, _ = json.Marshal(evTurnStart(2))
				s.handleSessionEvent(raw)
			}
			done := s.turnDone
			s.finishTurn(sessionEventData{Turn: 2})
			select {
			case <-done:
			default:
				t.Fatal("new turn did not stop its own watchdog")
			}
		})
	}
}

func TestSession_ResultDisarmsWatchdogBeforePublishing(t *testing.T) {
	m := newMockDshServer()
	a := newTestAgent(t, m, t.TempDir())
	session, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	s := session.(*dshSession)
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Send("work", "one", nil, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(evTurnStart(1))
	s.handleSessionEvent(raw)
	done := s.turnDone
	for i := 0; i < cap(s.events); i++ {
		s.events <- core.Event{Type: core.EventText, Content: "queued"}
	}
	go s.finishTurn(sessionEventData{Turn: 1})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchdog remained armed while final result delivery blocked")
	}
}

func TestSession_ResumedTurnWithoutStartFlushesReply(t *testing.T) {
	m := newMockDshServer()
	a := newTestAgent(t, m, t.TempDir())
	session, err := a.StartSession(context.Background(), "")
	if err != nil { t.Fatal(err) }
	s := session.(*dshSession)
	t.Cleanup(func() { _ = s.Close() })
	// DSH can replay an approval while the assistant itself is idle, so the
	// reconnect snapshot has no active attempt from which to replay turn/start.
	s.handleChunk(&streamChunk{Type: "text-delta", Text: "resumed reply"})
	s.finishTurn(sessionEventData{Turn: 9})
	var text strings.Builder
	for _, event := range drainEvents(t, s, time.Second) {
		if event.Type == core.EventText { text.WriteString(event.Content) }
	}
	if text.String() != "resumed reply" { t.Fatalf("resumed text = %q", text.String()) }
}
