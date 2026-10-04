package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type autoSteerTestAgent struct {
	*sharedTestAgent
	enabled bool
}

func (a *autoSteerTestAgent) AutoSteerBusyMessages() bool { return a.enabled }

type autoSteerCompatAgent struct{ *sharedCompatAgent }

func (*autoSteerCompatAgent) AutoSteerBusyMessages() bool { return true }

type queuedExternalAgent struct{ *sharedCompatAgent }

func (*queuedExternalAgent) AutoSteerBusyMessages() bool { return false }
func (a *queuedExternalAgent) AttachSession(ctx context.Context, id string) (AgentSession, error) {
	as, err := a.sharedTestAgent.AttachSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return &sharedCompatSession{sharedTestSession: as.(*sharedTestSession), calls: a.calls}, nil
}

type blockedInterruptAgent struct {
	*queuedExternalAgent
	cancelled, release chan struct{}
}

func (a *blockedInterruptAgent) AttachSession(ctx context.Context, id string) (AgentSession, error) {
	as, err := a.queuedExternalAgent.AttachSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return &blockedInterruptSession{sharedCompatSession: as.(*sharedCompatSession), cancelled: a.cancelled, release: a.release}, nil
}

type blockedInterruptSession struct {
	*sharedCompatSession
	cancelled, release chan struct{}
}

func (s *blockedInterruptSession) CancelTurn() error {
	if err := s.sharedCompatSession.CancelTurn(); err != nil {
		return err
	}
	close(s.cancelled)
	<-s.release
	return nil
}

func TestSharedStop_ClearsQueueBeforeInterruptCompletes(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	agent := &blockedInterruptAgent{queuedExternalAgent: &queuedExternalAgent{a}, cancelled: make(chan struct{}), release: make(chan struct{})}
	env.e.agent = agent
	defer close(agent.release)
	env.send("/attach first")
	env.await("Attached to session first")
	env.send("MUST NOT RUN AFTER STOP")
	env.await(env.e.i18n.T(MsgMessageQueued))
	go env.send("/stop")
	select {
	case <-agent.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("interrupt was not called")
	}
	// The runtime is idle while interrupt's response is withheld. The queue
	// must already be discarded, rather than executing in that interval.
	select {
	case c := <-a.calls:
		c.finish("UNWANTED QUEUED RESULT")
		t.Fatalf("queued input executed during stop: %q", c.prompt)
	case <-time.After(120 * time.Millisecond):
	}
	env.await("session stopped")
}

func TestSharedExternalQueueRetainsFIFOAndMedia(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	env.e.agent = &queuedExternalAgent{a}
	env.send("/attach first")
	env.await("Attached to session first")
	as := a.connection("first")
	env.send("FIRST QUEUED TASK")
	env.e.ReceiveMessage(env.p, &Message{SessionKey: "test:user", Platform: "test", UserID: "user", ReplyCtx: "reply", Content: "SECOND QUEUED TASK", Images: []ImageAttachment{{}}, Files: []FileAttachment{{}}})
	select {
	case <-a.calls:
		t.Fatal("started a turn before the external turn finished")
	case <-time.After(80 * time.Millisecond):
	}
	as.mu.Lock()
	as.turn = ""
	as.mu.Unlock()
	as.emit(Event{Type: EventResult, Content: "CLI RESULT", Done: true})
	first := awaitSharedCompatCall(t, a)
	if first.prompt != "FIRST QUEUED TASK" {
		t.Fatalf("wrong first queued prompt: %q", first.prompt)
	}
	first.finish("FIRST RESULT")
	second := awaitSharedCompatCall(t, a)
	if second.prompt != "SECOND QUEUED TASK" || len(second.images) != 1 || len(second.files) != 1 {
		t.Fatalf("queue lost order or media: %+v", second)
	}
	second.finish("SECOND RESULT")
	env.await("SECOND RESULT")
}

func TestSharedExternalQueueHonorsLimit(t *testing.T) {
	env := newSharedTestEnv(t)
	env.e.agent = &autoSteerTestAgent{env.a, false}
	env.e.maxQueuedMessages = 1
	env.send("/attach first")
	env.await("Attached to session first")
	env.send("FIRST QUEUED TASK")
	env.await(env.e.i18n.T(MsgMessageQueued))
	env.send("SECOND QUEUED TASK")
	env.await(env.e.i18n.Tf(MsgQueueFull, 1))
	env.send("/stop")
	env.await("session stopped")
}

func TestSharedAutoSteerFailureDoesNotQueueOrStartTurn(t *testing.T) {
	env := newSharedTestEnv(t)
	env.e.agent = &autoSteerTestAgent{env.a, true}
	env.send("/attach first")
	env.await("Attached to session first")
	as := env.a.connection("first")
	as.mu.Lock()
	as.steerError = fmt.Errorf("turn completed before steer")
	as.mu.Unlock()
	env.send("do not start this as another turn")
	env.await("turn completed before steer")
	env.send("/stop")
	env.await("interrupted")
	if strings.Contains(env.visible(), "RECOVERY") {
		t.Fatal("failed steer became a queued turn")
	}
}

func TestSharedAutoSteerAttachmentsAreNotSilentlyDropped(t *testing.T) {
	for _, kind := range []string{"image", "file"} {
		t.Run(kind, func(t *testing.T) {
			env := newSharedTestEnv(t)
			env.e.agent = &autoSteerTestAgent{env.a, true}
			env.send("/attach first")
			env.await("Attached to session first")
			msg := &Message{SessionKey: "test:user", Platform: "test", UserID: "user", Content: "with attachment", ReplyCtx: "reply"}
			if kind == "image" {
				msg.Images = []ImageAttachment{{}}
			} else {
				msg.Files = []FileAttachment{{}}
			}
			env.e.ReceiveMessage(env.p, msg)
			env.await(env.e.i18n.T(MsgSharedSteerTextOnly))
			if strings.Contains(env.visible(), "STEER observed") {
				t.Fatal("steered text while losing attachment")
			}
		})
	}
}
