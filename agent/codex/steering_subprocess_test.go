package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// The test binary is the external JSON-RPC peer. Production connect/readLoop,
// Send/SteerUserInput and the real Engine are used; only the model is simulated.
func TestSteeringRPCPeer(t *testing.T) {
	if os.Getenv("CC_CONNECT_STEERING_PEER") != "1" {
		return
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	emit := func(v any) {
		if encoder.Encode(v) != nil {
			os.Exit(2)
		}
	}
	starts := 0
	for {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				ExpectedTurnID string `json:"expectedTurnId"`
				Input          []struct {
					Text string `json:"text"`
				} `json:"input"`
			} `json:"params"`
		}
		if decoder.Decode(&req) != nil {
			os.Exit(0)
		}
		response := map[string]any{"id": req.ID, "result": map[string]any{"turnId": "active"}}
		switch req.Method {
		case "turn/start":
			starts++
			response["result"] = map[string]any{"turn": map[string]any{"id": "active"}}
			emit(response)

		case "test/complete":
			emit(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread", "turn": map[string]any{"id": "active", "status": "completed"}}})
			emit(response)
		case "test/finish-queued":
			emit(response)
			emit(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread", "item": map[string]any{"type": "agentMessage", "id": "queued-answer", "text": fmt.Sprintf("Queued input used %d turn/start", starts)}}})
			emit(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread", "turn": map[string]any{"id": "active", "status": "completed"}}})

		case "turn/steer":
			if req.Params.ExpectedTurnID != "active" || strings.Contains(req.Params.Input[0].Text, "reject this") {
				emit(map[string]any{"id": req.ID, "error": map[string]any{"code": -32602, "message": "input rejected before acceptance"}})
				continue
			}
			emit(response)
			emit(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread", "item": map[string]any{"type": "agentMessage", "id": "answer", "text": fmt.Sprintf("Applied supplement with %d turn/start: %s", starts, req.Params.Input[0].Text)}}})
			emit(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread", "turn": map[string]any{"id": "active", "status": "completed"}}})
		default:
			os.Exit(3)
		}
	}
}

type steeringProcessAgent struct{ s *appServerSession }

func (*steeringProcessAgent) Name() string { return "test" }
func (a *steeringProcessAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	return a.s, nil
}
func (*steeringProcessAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}
func (*steeringProcessAgent) Stop() error { return nil }

type steeringProcessPlatform struct {
	mu       sync.Mutex
	messages []string
}

func (*steeringProcessPlatform) Name() string                    { return "test" }
func (*steeringProcessPlatform) Start(core.MessageHandler) error { return nil }
func (*steeringProcessPlatform) Stop() error                     { return nil }
func (p *steeringProcessPlatform) Reply(_ context.Context, _ any, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, text)
	return nil
}
func (p *steeringProcessPlatform) Send(c context.Context, r any, text string) error {
	return p.Reply(c, r, text)
}
func (p *steeringProcessPlatform) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.messages, "\n")
}

func TestSteeringSubprocess_EngineDeliversSupplementWithoutSecondTurn(t *testing.T) {
	for _, prefix := range []string{"", "/ps ", "/btw "} {
		t.Run(prefix, func(t *testing.T) {
			bin, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := &appServerSession{ctx: ctx, cancel: cancel, cliBin: bin, cliExtraArgs: []string{"-test.run=^TestSteeringRPCPeer$", "--"}, extraEnv: []string{"CC_CONNECT_STEERING_PEER=1"}, workDir: t.TempDir(), events: make(chan core.Event, 32), pending: make(map[int64]chan rpcResponseEnvelope)}
			s.alive.Store(true)
			s.threadID.Store("thread")
			if err := s.connect(); err != nil {
				t.Fatal(err)
			}
			p := &steeringProcessPlatform{}
			e := core.NewEngine("test", &steeringProcessAgent{s: s}, []core.Platform{p}, "", core.LangEnglish)
			e.SetBusyMessageMode("steer")
			e.SetStreamPreviewCfg(core.StreamPreviewCfg{Enabled: false})
			t.Cleanup(func() { _ = e.Stop(); _ = s.Close() })
			send := func(content string) {
				e.ReceiveMessage(p, &core.Message{SessionKey: "test:process", Platform: p.Name(), UserID: "user", Content: content, ReplyCtx: "ctx"})
			}
			wait := func(condition func() bool) {
				t.Helper()
				deadline := time.Now().Add(3 * time.Second)
				for !condition() {
					if time.Now().After(deadline) {
						t.Fatalf("timed out; visible messages: %s", p.output())
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			send("start held task")
			wait(func() bool { s.stateMu.Lock(); defer s.stateMu.Unlock(); return s.currentTurn == "active" })
			if prefix != "" {
				send(prefix + "reject this")
				wait(func() bool {
					return strings.Contains(p.output(), core.NewI18n(core.LangEnglish).T(core.MsgPsSendFailed))
				})
			}
			send(prefix + "include unit tests")
			wait(func() bool {
				return strings.Contains(p.output(), "Applied supplement with 1 turn/start: include unit tests")
			})
			send("/history")
			wait(func() bool { return strings.Contains(p.output(), "start held task") })
			if strings.Contains(p.output(), "with 2 turn/start") {
				t.Fatal("supplement started a second turn")
			}
		})
	}
}

func TestSteeringSubprocess_DefaultQueueStartsNextTurnAfterRemoteCompletion(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &appServerSession{ctx: ctx, cancel: cancel, cliBin: bin, cliExtraArgs: []string{"-test.run=^TestSteeringRPCPeer$", "--"}, extraEnv: []string{"CC_CONNECT_STEERING_PEER=1"}, workDir: t.TempDir(), events: make(chan core.Event, 32), pending: make(map[int64]chan rpcResponseEnvelope)}
	s.alive.Store(true)
	s.threadID.Store("thread")
	if err := s.connect(); err != nil {
		t.Fatal(err)
	}
	p := &steeringProcessPlatform{}
	e := core.NewEngine("test", &steeringProcessAgent{s: s}, []core.Platform{p}, "", core.LangEnglish)
	e.SetStreamPreviewCfg(core.StreamPreviewCfg{Enabled: false})
	t.Cleanup(func() { _ = e.Stop(); _ = s.Close() })
	send := func(text string) {
		e.ReceiveMessage(p, &core.Message{SessionKey: "test:queue", Platform: p.Name(), Content: text, ReplyCtx: "ctx"})
	}
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !check() {
			if time.Now().After(deadline) {
				t.Fatal("timeout", p.output())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	send("first task")
	wait(func() bool { s.stateMu.Lock(); defer s.stateMu.Unlock(); return s.currentTurn == "active" })
	send("next task")
	wait(func() bool {
		return strings.Contains(p.output(), core.NewI18n(core.LangEnglish).T(core.MsgMessageQueued))
	})
	if strings.Contains(p.output(), "Applied supplement") {
		t.Fatal("default queue became steer")
	}
	if err := s.request("test/complete", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	// Do not race synthetic completion ahead of the Send acknowledgement.
	wait(func() bool { s.stateMu.Lock(); defer s.stateMu.Unlock(); return s.currentTurn == "active" })
	if err := s.request("test/finish-queued", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return strings.Contains(p.output(), "Queued input used 2 turn/start") })
	send("/history")
	wait(func() bool { return strings.Contains(p.output(), "next task") })
}
