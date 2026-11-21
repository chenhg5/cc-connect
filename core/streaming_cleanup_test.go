package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type closingTestCard struct {
	recordingStreamCard
	closed bool
}

type closingTestSession struct {
	*controllableAgentSession
	once sync.Once
}

func (s *closingTestSession) Close() error {
	s.once.Do(func() { _ = s.controllableAgentSession.Close() })
	return nil
}

func (c *closingTestCard) Close(context.Context) error { c.closed = true; return nil }

type closingTestPlatform struct {
	stubPlatformEngine
	card *closingTestCard
}

func (p *closingTestPlatform) CreateStreamingCard(context.Context, any) (StreamingCard, error) {
	return p.card, nil
}

func TestStreamingCardCleanup_OnSuccessErrorAndExit(t *testing.T) {
	for _, outcome := range []string{"success", "error", "exit", "stop"} {
		t.Run(outcome, func(t *testing.T) {
			card := &closingTestCard{}
			p := &closingTestPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}, card: card}
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			key := "test:stream-cleanup"
			session := e.sessions.GetOrCreateActive(key)
			agentSession := &closingTestSession{controllableAgentSession: newControllableSession("stream-cleanup")}
			state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx"}
			e.interactiveStates[key] = state
			agentSession.events <- Event{Type: EventText, Content: "partial"}
			switch outcome {
			case "success":
				agentSession.events <- Event{Type: EventResult, Done: true}
			case "error":
				agentSession.events <- Event{Type: EventError, Error: errors.New("test failure")}
			case "exit":
				_ = agentSession.Close()
			case "stop":
				state.stopCh = make(chan struct{})
				close(state.stopCh)
			}
			e.processInteractiveEvents(state, session, e.sessions, key, "message", time.Now(), nil, nil, state.replyCtx, 0)
			if !card.closed {
				t.Fatal("protocol stream was left unfinished")
			}
		})
	}
}

// lifecycleStreamPlatform records what a user sees: snapshots within one reply
// and its final state. Closing an already finished stream is intentionally silent.
type lifecycleStreamFrame struct {
	id       int
	text     string
	finished bool
}

type lifecycleStreamPlatform struct {
	stubPlatformEngine
	streamMu sync.Mutex
	nextID   int
	frames   []lifecycleStreamFrame
}

type lifecycleStreamCard struct {
	platform *lifecycleStreamPlatform
	id       int
	last     string
	finished bool
}

func (p *lifecycleStreamPlatform) CreateStreamingCard(context.Context, any) (StreamingCard, error) {
	p.streamMu.Lock()
	defer p.streamMu.Unlock()
	p.nextID++
	return &lifecycleStreamCard{platform: p, id: p.nextID}, nil
}
func (p *lifecycleStreamPlatform) snapshot() []lifecycleStreamFrame {
	p.streamMu.Lock()
	defer p.streamMu.Unlock()
	return append([]lifecycleStreamFrame(nil), p.frames...)
}
func (c *lifecycleStreamCard) Update(_ context.Context, text string) error {
	c.platform.streamMu.Lock()
	defer c.platform.streamMu.Unlock()
	c.last = text
	c.platform.frames = append(c.platform.frames, lifecycleStreamFrame{c.id, text, false})
	return nil
}
func (c *lifecycleStreamCard) Finalize(_ context.Context, text string) error {
	c.platform.streamMu.Lock()
	defer c.platform.streamMu.Unlock()
	c.finished = true
	c.last = text
	c.platform.frames = append(c.platform.frames, lifecycleStreamFrame{c.id, text, true})
	return nil
}
func (c *lifecycleStreamCard) Close(context.Context) error {
	c.platform.streamMu.Lock()
	defer c.platform.streamMu.Unlock()
	if !c.finished && c.last != "" {
		c.finished = true
		c.platform.frames = append(c.platform.frames, lifecycleStreamFrame{c.id, c.last, true})
	}
	return nil
}
func (*lifecycleStreamCard) Failed() bool { return false }
