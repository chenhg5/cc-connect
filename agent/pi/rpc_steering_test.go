package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Run the test executable as a fake Pi peer, exercising the real subprocess,
// JSONL reader, and session lifecycle without installing Pi or calling a model.
// These are Pi's documented distinctions: prompt starts an idle agent, a busy
// prompt needs streamingBehavior, and raw steer only queues when idle.
func TestPiRPCSteeringHelperProcess(t *testing.T) {
	if os.Getenv("CC_CONNECT_PI_STEERING_HELPER") != "1" {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	emit := func(event map[string]any) {
		if err := encoder.Encode(event); err != nil {
			os.Exit(2)
		}
	}
	text := func(content string) {
		emit(map[string]any{
			"type":                  "message_update",
			"assistantMessageEvent": map[string]any{"type": "text_delta", "delta": content},
		})
	}
	busy := false
	var queued []string
	for {
		var command struct {
			ID                string `json:"id"`
			Type              string `json:"type"`
			Message           string `json:"message"`
			StreamingBehavior string `json:"streamingBehavior"`
		}
		if err := decoder.Decode(&command); err != nil {
			if err != io.EOF {
				os.Exit(2)
			}
			os.Exit(0)
		}
		response := map[string]any{"type": "response", "command": command.Type, "id": command.ID, "success": true}
		switch command.Type {
		case "get_state":
			response["data"] = map[string]any{"sessionId": "rpc-steering-session", "isStreaming": busy}
			emit(response)
		case "prompt", "steer":
			if command.Message == "exit-before-ack" {
				os.Exit(23)
			}
			if command.Message == "no-ack" {
				if err := os.WriteFile("prompt-unacknowledged", nil, 0o600); err != nil {
					os.Exit(2)
				}
				continue
			}
			if command.Message == "reject-prompt" || (busy && os.Getenv("CC_CONNECT_PI_LEGACY_RPC") == "1") {
				response["success"] = false
				// Pi v0.32.1 agent-session.ts ignores streamingBehavior and
				// rejects busy prompts before its first await.
				response["error"] = "Agent is already processing. Use steer() or followUp() to queue messages during streaming."
				if command.Message == "reject-prompt" {
					response["error"] = "preflight rejected: model is unavailable"
				}
				emit(response)
				// Pi 0.32.1 can emit a rejection followed by an optimistic
				// success for the same prompt. The first failure must win.
				if os.Getenv("CC_CONNECT_PI_LEGACY_RPC") == "1" {
					response["success"] = true
					delete(response, "error")
					emit(response)
				}
				continue
			}
			if command.Message == "include unit tests" {
				// The engine serializes replies behind final platform delivery.
				// A marker lets the parent release that delivery only once this
				// command has arrived, without relying on a sleep or its reply.
				if err := os.WriteFile("supplement-received", nil, 0o600); err != nil {
					os.Exit(2)
				}
			}
			if command.Type == "steer" && !busy {
				queued = append(queued, command.Message)
				emit(response)
				continue
			}
			if busy && command.Type == "prompt" && command.StreamingBehavior != "steer" {
				response["success"] = false
				response["error"] = "Agent is already processing. Specify streamingBehavior."
				emit(response)
				continue
			}
			emit(response)
			if !busy {
				emit(map[string]any{"type": "agent_start"})
				if command.Message == "hold-for-steering" {
					busy = true
					if err := os.WriteFile("task-started", nil, 0o600); err != nil {
						os.Exit(2)
					}
					text("waiting for supplement\n")
					continue
				}
			}
			queued = append(queued, command.Message)
			text("processed: " + strings.Join(queued, " | "))
			queued = nil
			busy = false
			emit(map[string]any{"type": "agent_end"})
		default:
			os.Exit(2)
		}
	}
}

func newRPCSteeringAgent(t *testing.T) *Agent {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &Agent{
		cmd: executable, cliExtraArgs: []string{"-test.run=^TestPiRPCSteeringHelperProcess$", "--"},
		configEnv: []string{"CC_CONNECT_PI_STEERING_HELPER=1"},
		workDir:   t.TempDir(), model: "fake-model", mode: "default", rpc: true,
	}
}

func newRPCSteeringSession(t *testing.T) core.AgentSession {
	t.Helper()
	session, err := newRPCSteeringAgent(t).StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	})
	return session
}

func awaitRPCSteeringEvent(t *testing.T, session core.AgentSession, want core.EventType) string {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	var output strings.Builder
	for {
		select {
		case event, ok := <-session.Events():
			if !ok {
				t.Fatalf("session closed before %v; output: %q", want, output.String())
			}
			if event.Type == core.EventError {
				t.Fatalf("agent error: %v", event.Error)
			}
			if event.Type == core.EventText {
				output.WriteString(event.Content)
			}
			if event.Type == want {
				return output.String()
			}
		case <-timer.C:
			t.Fatalf("no %v from RPC peer; output: %q", want, output.String())
		}
	}
}

func TestPiRPCSend_StartsIdlePrompt(t *testing.T) {
	session := newRPCSteeringSession(t)
	if err := session.Send("first task", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := awaitRPCSteeringEvent(t, session, core.EventResult); got != "processed: first task" {
		t.Fatalf("reply = %q", got)
	}
}

func TestPiRPCSend_SteersWhileStreaming(t *testing.T) {
	session := newRPCSteeringSession(t)
	if err := session.Send("hold-for-steering", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := awaitRPCSteeringEvent(t, session, core.EventText); !strings.Contains(got, "waiting for supplement") {
		t.Fatalf("initial response = %q", got)
	}
	if err := session.Send("include unit tests", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := awaitRPCSteeringEvent(t, session, core.EventResult); got != "processed: include unit tests" {
		t.Fatalf("supplement response = %q", got)
	}
}

func TestPiRPCSend_PropagatesPromptRejection(t *testing.T) {
	session := newRPCSteeringSession(t)
	if err := session.Send("reject-prompt", "", nil, nil); err == nil || !strings.Contains(err.Error(), "preflight rejected") {
		t.Fatalf("Send() = %v; a rejected prompt must not report delivery", err)
	}
}

func TestPiRPCSend_ProcessExitReleasesWaiter(t *testing.T) {
	session := newRPCSteeringSession(t).(*piSession)
	done := make(chan error, 1)
	go func() { done <- session.Send("exit-before-ack", "", nil, nil) }()
	if err := receiveRPCSendResult(t, done); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("Send = %v, want process exit error", err)
	}
	assertNoRPCWaiters(t, session)
}

func TestPiRPCSend_CancelAndCloseReleaseWaiter(t *testing.T) {
	for _, action := range []string{"cancel", "close"} {
		t.Run(action, func(t *testing.T) {
			agent := newRPCSteeringAgent(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			session, err := agent.StartSession(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			s := session.(*piSession)
			t.Cleanup(func() { _ = s.Close() })
			done := make(chan error, 1)
			go func() { done <- s.Send("no-ack", "", nil, nil) }()
			waitRPCMarker(t, filepath.Join(agent.workDir, "prompt-unacknowledged"))
			if action == "cancel" {
				cancel()
			} else {
				_ = s.Close()
			}
			if err := receiveRPCSendResult(t, done); !errors.Is(err, context.Canceled) {
				t.Fatalf("Send = %v, want context cancellation", err)
			}
			assertNoRPCWaiters(t, s)
			_ = s.Close()
			if s.rpcCmd.ProcessState == nil {
				t.Fatal("RPC child was not reaped")
			}
		})
	}
}

func TestPiRPCPs_RejectionIsVisible(t *testing.T) {
	for _, command := range []string{"/ps", "/btw"} {
		for _, legacy := range []bool{false, true} {
			t.Run(command+"/legacy="+fmt.Sprint(legacy), func(t *testing.T) {
				agent := newRPCSteeringAgent(t)
				if legacy {
					agent.configEnv = append(agent.configEnv, "CC_CONNECT_PI_LEGACY_RPC=1")
				}
				p := &rpcSteeringPlatform{messages: make(chan string, 32)}
				engine := core.NewEngine("rpc-rejection", agent, []core.Platform{p}, "", core.LangEnglish)
				engine.SetStreamPreviewCfg(core.StreamPreviewCfg{Enabled: false})
				t.Cleanup(func() { _ = engine.Stop() })
				send := func(content string) {
					engine.ReceiveMessage(p, &core.Message{
						SessionKey: "rpc-rejection:user", Platform: p.Name(), UserID: "user", Content: content, ReplyCtx: "ctx",
					})
				}
				send("hold-for-steering")
				waitRPCMarker(t, filepath.Join(agent.workDir, "task-started"))
				message := "reject-prompt"
				if legacy {
					message = "include unit tests"
				}
				send(command + " " + message)
				i18n := core.NewI18n(core.LangEnglish)
				awaitRPCPlatformMessage(t, p, i18n.T(core.MsgPsSendFailed), i18n.T(core.MsgPsSent))
				// A second rejected supplement must still reach the running
				// session; a prompt rejection must not finish the main turn.
				send(command + " " + message)
				awaitRPCPlatformMessage(t, p, i18n.T(core.MsgPsSendFailed), i18n.T(core.MsgPsSent))
				if !legacy {
					send(command + " include unit tests")
					awaitRPCPlatformMessage(t, p, "processed: include unit tests", "")
				}
			})
		}
	}
}

func waitRPCMarker(t *testing.T, path string) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("RPC peer did not create %s", filepath.Base(path))
		}
	}
}

func awaitRPCPlatformMessage(t *testing.T, p *rpcSteeringPlatform, want, forbidden string) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	var messages []string
	for {
		select {
		case message := <-p.messages:
			messages = append(messages, message)
			if forbidden != "" && strings.Contains(message, forbidden) {
				t.Fatalf("unexpected confirmation: %s", message)
			}
			if strings.Contains(message, want) {
				return
			}
		case <-deadline.C:
			t.Fatalf("missing %q; visible replies: %q", want, messages)
		}
	}
}

func TestPiRPCSend_LegacyPiRejectsStreamingPrompt(t *testing.T) {
	agent := newRPCSteeringAgent(t)
	agent.configEnv = append(agent.configEnv, "CC_CONNECT_PI_LEGACY_RPC=1")
	session, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if err := session.Send("hold-for-steering", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	awaitRPCSteeringEvent(t, session, core.EventText)
	if err := session.Send("include unit tests", "", nil, nil); err == nil || !strings.Contains(err.Error(), "already processing") {
		t.Fatalf("Send() = %v; Pi 0.32.1 rejection must not report delivery", err)
	}
}

// Block the first final delivery after Pi has emitted agent_end. The engine
// still considers this turn busy, so /ps must also handle Pi already being idle.
type rpcSteeringPlatform struct {
	blocked     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
	messages    chan string
}

func (p *rpcSteeringPlatform) Name() string                    { return "rpc-steering-test" }
func (p *rpcSteeringPlatform) Start(core.MessageHandler) error { return nil }
func (p *rpcSteeringPlatform) Stop() error                     { return nil }
func (p *rpcSteeringPlatform) Reply(ctx context.Context, _ any, content string) error {
	return p.record(ctx, content)
}
func (p *rpcSteeringPlatform) Send(ctx context.Context, _ any, content string) error {
	if strings.Contains(content, "processed: first task") {
		close(p.blocked)
		select {
		case <-p.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return p.record(ctx, content)
}
func (p *rpcSteeringPlatform) record(ctx context.Context, content string) error {
	select {
	case p.messages <- content:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *rpcSteeringPlatform) unblock() { p.releaseOnce.Do(func() { close(p.release) }) }

func TestPiRPCPs_AfterRemoteTurnEnds(t *testing.T) {
	p := &rpcSteeringPlatform{
		blocked: make(chan struct{}), release: make(chan struct{}), messages: make(chan string, 16),
	}
	// No persistent store: this test concerns transport and routing, not disk writes.
	agent := newRPCSteeringAgent(t)
	engine := core.NewEngine("rpc-steering-test", agent, []core.Platform{p}, "", core.LangEnglish)
	engine.SetStreamPreviewCfg(core.StreamPreviewCfg{Enabled: false})
	engine.SetShowContextIndicator(false)
	engine.SetShowWorkdirIndicator(false)
	engine.SetReplyFooterEnabled(false)
	t.Cleanup(func() {
		p.unblock()
		if err := engine.Stop(); err != nil {
			t.Error(err)
		}
	})
	send := func(id, content string) {
		engine.ReceiveMessage(p, &core.Message{
			SessionKey: "rpc-steering-test:user", Platform: p.Name(), UserID: "user",
			MessageID: id, Content: content, ReplyCtx: id,
		})
	}
	send("first", "first task")
	select {
	case <-p.blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("first response never reached platform delivery")
	}
	supplementDone := make(chan struct{})
	go func() {
		defer close(supplementDone)
		send("supplement", "/ps include unit tests")
	}()
	t.Cleanup(func() {
		p.unblock()
		select {
		case <-supplementDone:
		case <-time.After(3 * time.Second):
			t.Error("/ps goroutine did not finish during cleanup")
		}
	})
	marker := filepath.Join(agent.workDir, "supplement-received")
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	arrivalDeadline := time.NewTimer(3 * time.Second)
	defer arrivalDeadline.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-arrivalDeadline.C:
			t.Fatal("/ps never reached the RPC peer during final platform delivery")
		}
	}
	p.unblock()
	select {
	case <-supplementDone:
	case <-time.After(3 * time.Second):
		t.Fatal("/ps did not return after final delivery was released")
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	var messages []string
	for {
		select {
		case message := <-p.messages:
			messages = append(messages, message)
			if strings.Contains(message, "processed: include unit tests") {
				return
			}
		case <-deadline.C:
			t.Fatalf("supplement was not processed after remote completion; visible messages: %q", messages)
		}
	}
}
