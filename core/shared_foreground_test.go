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

type sharedCompatAudioPlatform struct {
	*stubCardPlatform
	mu    sync.Mutex
	audio int
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

func TestSharedForegroundTerminalRejectsLateToolBeforeCancellation(t *testing.T) {
	f := &sharedForeground{ctx: context.Background(), events: make(chan Event, 3), bound: true, turnID: "old"}
	if !f.route(Event{Type: EventResult, TurnID: "old", Done: true}) {
		t.Fatal("terminal event refused")
	}
	if f.route(Event{Type: EventToolResult, TurnID: "old", ItemID: "late"}) {
		t.Fatal("tool result queued behind terminal event and lost")
	}
}

type delayedBindingAgent struct {
	*sharedCompatAgent
	release chan struct{}
}

func (a *delayedBindingAgent) StartSession(ctx context.Context, id string) (AgentSession, error) {
	as, err := a.sharedCompatAgent.StartSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return &delayedBindingSession{sharedCompatSession: as.(*sharedCompatSession), release: a.release}, nil
}

type delayedBindingSession struct {
	*sharedCompatSession
	release chan struct{}
}

func (s *delayedBindingSession) SendTurn(prompt, id string, images []ImageAttachment, files []FileAttachment) (string, error) {
	turn, err := s.sharedCompatSession.SendTurn(prompt, id, images, files)
	<-s.release
	return turn, err
}

func (s sharedCompatTTS) Synthesize(_ context.Context, text string, _ TTSSynthesisOpts) ([]byte, string, error) {
	s.calls <- text
	return []byte("voice"), "mp3", nil
}

func (p *sharedCompatAudioPlatform) SendAudio(context.Context, any, []byte, string) error {
	p.mu.Lock()
	p.audio++
	p.mu.Unlock()
	return nil
}
