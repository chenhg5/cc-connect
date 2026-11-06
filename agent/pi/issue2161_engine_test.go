package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const issue2161Barrier = "cc-connect-issue2161-test-barrier:"

// The helper is only a transport peer: the parent controls every lifecycle
// event. Production Send, readLoopRPC, JSON decoding, and Engine routing run
// unchanged. A separate socket supplies explicit gates, not timing sleeps.
func TestPiIssue2161HelperProcess(t *testing.T) {
	for _, arg := range os.Args {
		if arg == "--version" {
			fmt.Println("0.99.0")
			os.Exit(0)
		}
	}
	address := os.Getenv("CC_CONNECT_ISSUE2161_PEER")
	if address == "" {
		return
	}
	conn, err := net.Dial("unix", address)
	if err != nil {
		os.Exit(2)
	}
	defer func() { _ = conn.Close() }()
	var stdoutMu sync.Mutex
	stdout := json.NewEncoder(os.Stdout)
	emit := func(event map[string]any) {
		stdoutMu.Lock()
		defer stdoutMu.Unlock()
		if stdout.Encode(event) != nil {
			os.Exit(2)
		}
	}
	go func() {
		stdin, control := json.NewDecoder(os.Stdin), json.NewEncoder(conn)
		promptNumber := 0
		for {
			var command map[string]any
			if err := stdin.Decode(&command); err != nil {
				if err == io.EOF {
					os.Exit(0)
				}
				os.Exit(2)
			}
			switch command["type"] {
			case "get_state":
				emit(map[string]any{"type": "response", "command": "get_state", "id": command["id"], "success": true,
					"data": map[string]any{"sessionId": "issue2161-peer", "isStreaming": false}})
			case "prompt":
				promptNumber++
				if (promptNumber != 1 || os.Getenv("CC_CONNECT_ISSUE2161_DEFER_ACK") != "1") && os.Getenv("CC_CONNECT_ISSUE2161_DEFER_ALL_ACKS") != "1" {
					emit(map[string]any{"type": "response", "command": "prompt", "id": command["id"], "success": true, "data": map[string]any{"disposition": "started"}})
				}
			}
			if control.Encode(command) != nil {
				os.Exit(2)
			}
		}
	}()
	decoder := json.NewDecoder(conn)
	for {
		var events []map[string]any
		if decoder.Decode(&events) != nil {
			os.Exit(0)
		}
		for _, event := range events {
			if event["type"] == "issue2161_exit" {
				os.Exit(0)
			}
			emit(event)
		}
	}
}

// This transparent observer only removes private test barriers from the
// stream. Unbuffered forwarding acknowledges when an Engine reader receives
// each real event; it never synthesizes terminal events or rewrites content.
type issue2161ObservedSession struct {
	core.AgentSession
	out       chan core.Event
	barriers  chan string
	done      chan struct{}
	finished  chan struct{}
	closeOnce sync.Once
	results   atomic.Int64
	errors    atomic.Int64
}

func (s *issue2161ObservedSession) Events() <-chan core.Event {
	return s.out
}

func (s *issue2161ObservedSession) forward() {
	defer close(s.finished)
	defer close(s.out)
	for {
		select {
		case event, ok := <-s.AgentSession.Events():
			if !ok {
				return
			}
			if event.Type == core.EventThinking && strings.HasPrefix(event.Content, issue2161Barrier) {
				select {
				case s.barriers <- event.Content:
				case <-s.done:
					return
				}
				continue
			}
			if event.Type == core.EventError {
				s.errors.Add(1)
			}
			if event.Type == core.EventResult && event.Done {
				s.results.Add(1)
			}
			select {
			case s.out <- event:
			case <-s.done:
				return
			}
		case <-s.done:
			return
		}
	}
}

func (s *issue2161ObservedSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		err = s.AgentSession.Close()
		<-s.finished
	})
	return err
}

type issue2161ObservedAgent struct {
	*Agent
	started chan *issue2161ObservedSession
}

func (a *issue2161ObservedAgent) StartSession(ctx context.Context, id string) (core.AgentSession, error) {
	underlying, err := a.Agent.StartSession(ctx, id)
	if err != nil {
		return nil, err
	}
	s := &issue2161ObservedSession{AgentSession: underlying, out: make(chan core.Event),
		barriers: make(chan string, 32), done: make(chan struct{}), finished: make(chan struct{})}
	go s.forward()
	a.started <- s
	return s, nil
}

type issue2161Delivery struct {
	Method  string
	ReplyTo string
	Text    string
}

type issue2161Platform struct {
	mu      sync.Mutex
	sent    []issue2161Delivery
	changed chan struct{}
}

func (*issue2161Platform) Name() string                    { return "issue2161" }
func (*issue2161Platform) Start(core.MessageHandler) error { return nil }
func (*issue2161Platform) Stop() error                     { return nil }
func (p *issue2161Platform) Reply(_ context.Context, ctx any, text string) error {
	return p.record("reply", ctx, text)
}
func (p *issue2161Platform) Send(_ context.Context, ctx any, text string) error {
	return p.record("send", ctx, text)
}
func (p *issue2161Platform) record(method string, ctx any, text string) error {
	p.mu.Lock()
	p.sent = append(p.sent, issue2161Delivery{method, fmt.Sprint(ctx), text})
	p.mu.Unlock()
	select {
	case p.changed <- struct{}{}:
	default:
	}
	return nil
}
func (p *issue2161Platform) snapshot() []issue2161Delivery {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]issue2161Delivery(nil), p.sent...)
}
func (p *issue2161Platform) answers() []issue2161Delivery {
	var results []issue2161Delivery
	for _, message := range p.snapshot() {
		if strings.HasPrefix(message.Text, "answer:") || message.Text == "(空响应)" {
			results = append(results, message)
		}
	}
	return results
}

type issue2161Fixture struct {
	t        *testing.T
	engine   *core.Engine
	platform *issue2161Platform
	agent    *issue2161ObservedAgent
	session  *issue2161ObservedSession
	peer     net.Conn
	accepted chan net.Conn
	commands chan map[string]any
	serial   int
}

func newIssue2161Fixture(t *testing.T, bot string, deferFirstAck ...bool) *issue2161Fixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the deterministic subprocess control channel uses Unix domain sockets")
	}
	// Keep Unix socket paths below the OS limit regardless of test-name length.
	dir, err := os.MkdirTemp("", "pi2161-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	listener, err := net.Listen("unix", filepath.Join(dir, "peer.sock"))
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"CC_CONNECT_ISSUE2161_PEER": listener.Addr().String()}
	if len(deferFirstAck) > 0 && deferFirstAck[0] {
		env["CC_CONNECT_ISSUE2161_DEFER_ACK"] = "1"
	}
	if len(deferFirstAck) > 1 && deferFirstAck[1] {
		env["CC_CONNECT_ISSUE2161_DEFER_ALL_ACKS"] = "1"
	}
	base, err := New(map[string]any{
		"cmd": []string{executable, "-test.run=^TestPiIssue2161HelperProcess$", "--"},
		"rpc": true, "model": "fake-model", "work_dir": dir,
		"env": env,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := &issue2161ObservedAgent{Agent: base.(*Agent), started: make(chan *issue2161ObservedSession, 1)}
	p := &issue2161Platform{changed: make(chan struct{}, 128)}
	f := &issue2161Fixture{t: t, platform: p, agent: agent, accepted: make(chan net.Conn, 1), commands: make(chan map[string]any, 64)}
	f.engine = core.NewEngine(bot, agent, []core.Platform{p}, "", core.LangChinese)
	f.engine.SetStreamPreviewCfg(core.StreamPreviewCfg{Enabled: false})
	f.engine.SetInstantReply(core.InstantReplyCfg{Enabled: false})
	f.engine.SetDisplayConfig(core.DisplayCfg{Mode: "quiet", ThinkingMessages: false, ToolMessages: false})
	f.engine.SetReplyFooterEnabled(false)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		f.accepted <- conn
		decoder := json.NewDecoder(conn)
		for {
			var command map[string]any
			if decoder.Decode(&command) != nil {
				return
			}
			f.commands <- command
		}
	}()
	t.Cleanup(func() {
		if err := f.engine.Stop(); err != nil {
			t.Error(err)
		}
		_ = listener.Close()
		if f.peer != nil {
			_ = f.peer.Close()
		}
	})
	return f
}

func (f *issue2161Fixture) send(id, text string) {
	f.engine.ReceiveMessage(f.platform, &core.Message{
		SessionKey: "issue2161:same-user", Platform: f.platform.Name(), UserID: "same-user",
		MessageID: id, Content: text, ReplyCtx: id,
	})
}

func (f *issue2161Fixture) command(want string) map[string]any {
	f.t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case command := <-f.commands:
			switch command["type"] {
			case "get_state":
				continue
			}
			if command["type"] != want {
				f.t.Fatalf("RPC command = %v, want %q; deliveries=%v", command, want, f.platform.snapshot())
			}
			return command
		case <-timer.C:
			f.t.Fatalf("no RPC %s; deliveries=%v", want, f.platform.snapshot())
		}
	}
}

func issue2161Text(text string) map[string]any {
	return map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": text}}
}

func (f *issue2161Fixture) emit(events ...map[string]any) {
	f.t.Helper()
	if f.peer == nil {
		select {
		case f.peer = <-f.accepted:
		case <-time.After(5 * time.Second):
			f.t.Fatal("fake RPC peer did not connect")
		}
	}
	if f.session == nil {
		select {
		case f.session = <-f.agent.started:
		case <-time.After(5 * time.Second):
			f.t.Fatal("Pi session did not start")
		}
	}
	f.serial++
	barrier := fmt.Sprintf("%s%d", issue2161Barrier, f.serial)
	events = append(events,
		map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "thinking_delta", "delta": barrier}},
		map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "thinking_end"}})
	if err := json.NewEncoder(f.peer).Encode(events); err != nil {
		f.t.Fatal(err)
	}
	select {
	case got := <-f.session.barriers:
		if got != barrier {
			f.t.Fatalf("parser barrier = %q, want %q", got, barrier)
		}
	case <-time.After(5 * time.Second):
		f.t.Fatalf("parser/Engine did not reach barrier %q; deliveries=%v", barrier, f.platform.snapshot())
	}
}

func (f *issue2161Fixture) awaitAnswers(n int) {
	f.t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for len(f.platform.answers()) < n {
		select {
		case <-f.platform.changed:
		case <-timer.C:
			f.t.Fatalf("wanted %d final answers; deliveries=%v", n, f.platform.snapshot())
		}
	}
}

func (f *issue2161Fixture) assertAnswers(want ...issue2161Delivery) {
	f.t.Helper()
	f.awaitAnswers(len(want))
	if got := f.platform.answers(); !reflect.DeepEqual(got, want) {
		f.t.Fatalf("final platform deliveries = %+v, want %+v", got, want)
	}
}

func (f *issue2161Fixture) finish(text string) {
	f.emit(issue2161Text(text), map[string]any{"type": "agent_end"}, map[string]any{"type": "agent_settled"})
}

func (f *issue2161Fixture) awaitIdle() {
	f.t.Helper()
	// This polls the actual lock state; it does not create an event-ordering
	// window. All RPC event interleavings are controlled by explicit barriers.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for f.engine.GetSessions().GetOrCreateActive("issue2161:same-user").Busy() {
		select {
		case <-ticker.C:
		case <-timer.C:
			f.t.Fatalf("Engine stayed busy; deliveries=%v", f.platform.snapshot())
		}
	}
}

func TestPiIssue2161Engine_AutoCompactionOneReply(t *testing.T) {
	f := newIssue2161Fixture(t, "bot-a", true)
	f.send("m1", "long task")
	command := f.command("prompt")
	// Current Pi can compact during prompt preflight, before the prompt ACK
	// and before agent_start. willRetry=false does not end that user request.
	f.emit(map[string]any{"type": "compaction_start", "reason": "threshold"},
		map[string]any{"type": "compaction_end", "reason": "threshold", "willRetry": false})
	premature := f.session.results.Load()
	if premature != 0 {
		// On the unmodified baseline, let the premature reply and idle
		// handoff finish before releasing the actual answer. This reproduces
		// the reporter's two outbound messages without a timing delay.
		f.awaitAnswers(1)
		f.awaitIdle()
	}
	f.emit(map[string]any{"type": "response", "command": "prompt", "id": command["id"], "success": true,
		"data": map[string]any{"disposition": "started"}}, map[string]any{"type": "agent_start"})
	f.finish("answer:real")
	f.awaitAnswers(1 + int(premature))
	t.Logf("premature terminal count=%d; platform deliveries=%+v", premature, f.platform.snapshot())
	f.assertAnswers(issue2161Delivery{"send", "m1", "answer:real"})
	if premature != 0 {
		t.Fatalf("compaction closed the interactive turn %d times", premature)
	}
}

func TestPiIssue2161Engine_MidRunCompactionOneReply(t *testing.T) {
	f := newIssue2161Fixture(t, "bot-midrun")
	f.send("m1", "long task")
	f.command("prompt")
	f.emit(map[string]any{"type": "agent_start"}, map[string]any{"type": "compaction_start"}, map[string]any{"type": "compaction_end"})
	premature := f.session.results.Load()
	if premature != 0 {
		f.awaitAnswers(1)
		f.awaitIdle()
	}
	f.finish("answer:midrun")
	f.awaitAnswers(1 + int(premature))
	t.Logf("premature terminal count=%d; platform deliveries=%+v", premature, f.platform.snapshot())
	f.assertAnswers(issue2161Delivery{"send", "m1", "answer:midrun"})
}

func TestPiIssue2161Engine_NormalEndWaitsForSettled(t *testing.T) {
	f := newIssue2161Fixture(t, "bot-normal")
	f.send("m1", "normal task")
	f.command("prompt")
	f.emit(map[string]any{"type": "agent_start"}, issue2161Text("answer:normal"), map[string]any{"type": "agent_end"})
	premature := f.session.results.Load()
	f.emit(map[string]any{"type": "agent_settled"})
	f.assertAnswers(issue2161Delivery{"send", "m1", "answer:normal"})
	if premature != 0 || f.session.results.Load() != 1 {
		t.Fatalf("terminal events: before settled=%d total=%d, want 0 then 1", premature, f.session.results.Load())
	}
}

func TestPiIssue2161Engine_ThreeMessagesKeepReplyContexts(t *testing.T) {
	f := newIssue2161Fixture(t, "bot-queue")
	f.send("m1", "first task")
	f.command("prompt")
	f.emit(map[string]any{"type": "agent_start"}, map[string]any{"type": "compaction_start"})
	f.send("m2", "second task")
	f.send("m3", "third task")
	f.emit(map[string]any{"type": "compaction_end"})
	if f.session.results.Load() != 0 {
		t.Fatalf("compaction prematurely released a queued turn; deliveries=%v", f.platform.snapshot())
	}
	f.finish("answer:first")
	if command := f.command("prompt"); command["message"] != "second task" {
		t.Fatalf("second RPC prompt=%v", command)
	}
	f.emit(map[string]any{"type": "agent_start"})
	f.finish("answer:second")
	if command := f.command("prompt"); command["message"] != "third task" {
		t.Fatalf("third RPC prompt=%v", command)
	}
	f.emit(map[string]any{"type": "agent_start"})
	f.finish("answer:third")
	f.assertAnswers(issue2161Delivery{"send", "m1", "answer:first"}, issue2161Delivery{"send", "m2", "answer:second"}, issue2161Delivery{"send", "m3", "answer:third"})
	f.awaitIdle()
}

func TestPiIssue2161Engine_TwoBotsDoNotBleed(t *testing.T) {
	a, b := newIssue2161Fixture(t, "bot-a"), newIssue2161Fixture(t, "bot-b")
	a.send("same-id", "task A")
	b.send("same-id", "task B")
	a.command("prompt")
	b.command("prompt")
	a.emit(map[string]any{"type": "agent_start"}, map[string]any{"type": "compaction_start"}, map[string]any{"type": "compaction_end"})
	b.emit(map[string]any{"type": "agent_start"})
	b.finish("answer:BOT-B")
	a.finish("answer:BOT-A")
	a.assertAnswers(issue2161Delivery{"send", "same-id", "answer:BOT-A"})
	b.assertAnswers(issue2161Delivery{"send", "same-id", "answer:BOT-B"})
}

// Recovery can continue after agent_end even when willRetry is false: the
// following compaction is a separate step within the same accepted prompt.
func TestPiIssue2161Engine_RecoveryStaysInOriginalTurn(t *testing.T) {
	for _, scenario := range []string{"overflow", "compaction-failure", "retry-success"} {
		t.Run(scenario, func(t *testing.T) {
			f := newIssue2161Fixture(t, scenario)
			f.send("m1", "recover this task")
			f.command("prompt")
			f.emit(map[string]any{"type": "agent_start"})
			switch scenario {
			case "overflow":
				f.emit(issue2161AssistantError("context overflow"), map[string]any{"type": "agent_end", "willRetry": false},
					map[string]any{"type": "compaction_start", "reason": "overflow"},
					map[string]any{"type": "compaction_end", "reason": "overflow", "willRetry": true})
			case "compaction-failure":
				f.emit(map[string]any{"type": "compaction_start"},
					map[string]any{"type": "compaction_end", "errorMessage": "temporary compaction failure", "willRetry": false})
			case "retry-success":
				f.emit(issue2161AssistantError("temporary provider failure"),
					map[string]any{"type": "agent_end", "willRetry": true},
					map[string]any{"type": "auto_retry_start", "attempt": 1},
					map[string]any{"type": "auto_retry_end", "success": true})
			}
			if f.session.results.Load() != 0 || f.session.errors.Load() != 0 {
				t.Fatalf("recovery ended turn: results=%d errors=%d deliveries=%v", f.session.results.Load(), f.session.errors.Load(), f.platform.snapshot())
			}
			f.emit(map[string]any{"type": "agent_start"}, map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant"}})
			f.finish("answer:recovered")
			f.assertAnswers(issue2161Delivery{"send", "m1", "answer:recovered"})
			f.awaitIdle()
			if f.session.results.Load() != 1 || f.session.errors.Load() != 0 {
				t.Fatalf("recovery emitted duplicate terminal or error")
			}
		})
	}
}

func issue2161AssistantError(message string) map[string]any {
	return map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "errorMessage": message}}
}

func TestPiIssue2161Engine_TerminalFailureWaitsForSettled(t *testing.T) {
	f := newIssue2161Fixture(t, "bot-failure")
	f.send("m1", "failing task")
	f.command("prompt")
	f.emit(map[string]any{"type": "agent_start"}, issue2161AssistantError("provider retries exhausted"),
		map[string]any{"type": "agent_end", "willRetry": false}, map[string]any{"type": "auto_retry_end", "success": false, "finalError": "provider retries exhausted"})
	if f.session.results.Load() != 0 || f.session.errors.Load() != 0 {
		t.Fatal("failure surfaced before agent_settled")
	}
	f.emit(map[string]any{"type": "agent_settled"})
	f.awaitIdle()
	failures := 0
	for _, delivery := range f.platform.snapshot() {
		if strings.Contains(delivery.Text, "provider retries exhausted") {
			failures++
			if delivery.ReplyTo != "m1" {
				t.Fatalf("failure lost reply context: %+v", delivery)
			}
		}
	}
	if failures != 1 || f.session.errors.Load() != 1 || f.session.results.Load() != 0 {
		t.Fatalf("want one terminal error and completed turn, got failures=%d errors=%d results=%d deliveries=%v", failures, f.session.errors.Load(), f.session.results.Load(), f.platform.snapshot())
	}
}

func TestPiIssue2161Engine_EOFAndCancelReleaseBusyTurn(t *testing.T) {
	for _, shutdown := range []string{"EOF", "cancel"} {
		t.Run(shutdown, func(t *testing.T) {
			f := newIssue2161Fixture(t, "bot-"+shutdown)
			f.send("m1", "interrupted task")
			f.command("prompt")
			f.emit(map[string]any{"type": "agent_start"})
			if shutdown == "EOF" {
				if err := json.NewEncoder(f.peer).Encode([]map[string]any{{"type": "issue2161_exit"}}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := f.session.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if shutdown == "cancel" {
				select {
				case <-f.session.finished:
				case <-time.After(5 * time.Second):
					t.Fatal("canceled RPC event stream did not close")
				}
			}
			f.awaitIdle()
			if shutdown == "EOF" && (f.session.Alive() || f.session.errors.Load() != 1) {
				t.Fatalf("EOF did not emit one terminal failure: alive=%v errors=%d", f.session.Alive(), f.session.errors.Load())
			}
			if f.session.results.Load() > 1 {
				t.Fatalf("shutdown emitted duplicate terminal events")
			}
		})
	}
}

func TestPiIssue2161Engine_HandledExtensionCompletesWithoutAgentStart(t *testing.T) {
	f := newIssue2161Fixture(t, "bot-extension", true)
	f.send("extension", "extension command")
	command := f.command("prompt")
	f.send("next", "follow-up task")
	// Synchronous extension handling returns a correlated ACK without running
	// the agent loop, so neither agent_start nor agent_settled follows it.
	f.emit(issue2161Text("answer:extension"), map[string]any{
		"type": "response", "command": "prompt", "id": command["id"], "success": true,
		"data": map[string]any{"disposition": "handled"},
	})
	if next := f.command("prompt"); next["message"] != "follow-up task" {
		t.Fatalf("extension follow-up prompt=%v", next)
	}
	f.emit(map[string]any{"type": "agent_start"})
	f.finish("answer:follow-up")
	f.assertAnswers(issue2161Delivery{"send", "extension", "answer:extension"}, issue2161Delivery{"send", "next", "answer:follow-up"})
	f.awaitIdle()
	if got := f.session.results.Load(); got != 2 {
		t.Fatalf("handled extension and follow-up terminal count=%d, want 2", got)
	}
}

func (s *issue2161ObservedSession) SupportsBusySteer() bool {
	return s.AgentSession.(core.BusySteerSender).SupportsBusySteer()
}

func (s *issue2161ObservedSession) SendBusySteer(ctx context.Context, text, messageID string, images []core.ImageAttachment, files []core.FileAttachment) (core.SteerResult, error) {
	return s.AgentSession.(core.BusySteerSender).SendBusySteer(ctx, text, messageID, images, files)
}

func TestPiIssue2161Engine_ManualCompressThenQueuedMessage(t *testing.T) {
	for _, command := range []string{"/compress", "/compact"} {
		t.Run(strings.TrimPrefix(command, "/"), func(t *testing.T) {
			runIssue2161ManualCompressThenQueuedMessage(t, command)
		})
	}
}

func runIssue2161ManualCompressThenQueuedMessage(t *testing.T, slashCommand string) {
	f := newIssue2161Fixture(t, "bot-manual")
	f.send("warmup", "create session")
	f.command("prompt")
	f.emit(map[string]any{"type": "agent_start"})
	f.finish("answer:warmup")
	f.awaitAnswers(1)
	f.awaitIdle()
	f.send("compact", slashCommand)
	command := f.command("compact")
	if command["id"] == nil || command["id"] == "" {
		t.Fatalf("compact RPC lacks correlation ID: %v", command)
	}
	f.send("after", "queued after compact")
	before := f.session.results.Load()
	f.emit(map[string]any{"type": "compaction_start"}, map[string]any{"type": "compaction_end"}, map[string]any{"type": "agent_end"})
	if got := f.session.results.Load(); got != before {
		t.Fatalf("manual compact completed before its correlated response: %d -> %d", before, got)
	}
	f.emit(map[string]any{"type": "response", "command": "compact", "id": command["id"], "success": true, "data": map[string]any{}})
	if next := f.command("prompt"); next["message"] != "queued after compact" {
		t.Fatalf("queued compact follow-up=%v", next)
	}
	f.emit(map[string]any{"type": "agent_start"})
	f.finish("answer:after-compact")
	f.assertAnswers(issue2161Delivery{"send", "warmup", "answer:warmup"}, issue2161Delivery{"send", "after", "answer:after-compact"})
	f.awaitIdle()
	completed := 0
	for _, message := range f.platform.snapshot() {
		if message.ReplyTo == "compact" && strings.Contains(message.Text, "上下文压缩完成") {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("manual compact completion count=%d; deliveries=%v", completed, f.platform.snapshot())
	}
}

// The peer deliberately supplies a supported synthetic interleaving. This
// checks adapter/core completion ownership, not stock Pi event scheduling.

func TestPiIssue2161Engine_StartedACKCoalescesWithoutExtraTerminalWait(t *testing.T) {
	f := newIssue2161Fixture(t, "bot-coalesced", true, true)
	f.engine.SetBusyMessageMode("steer")
	f.send("m1", "old task")
	first := f.command("prompt")
	f.emit(issue2161UnitResponse(first, true, "started"), map[string]any{"type": "agent_start"})
	f.emit(map[string]any{"type": "agent_end"})
	steered := make(chan struct{})
	go func() { f.send("m2", "new native run"); close(steered) }()
	second := f.command("prompt")
	f.emit(issue2161UnitResponse(second, true, "started"), map[string]any{"type": "agent_start"}, map[string]any{"type": "agent_settled"})
	if got := f.session.results.Load(); got != 0 {
		t.Fatalf("old settled emitted %d terminal events", got)
	}
	select {
	case <-steered:
	case <-time.After(5 * time.Second):
		t.Fatal("busy steer did not return after ACK")
	}
	f.finish("answer:coalesced")
	f.assertAnswers(issue2161Delivery{"send", "m1", "answer:coalesced"})
	f.awaitIdle()
	// A third user action must start a new ordinary turn after the single
	// terminal event; a phantom already-sent turn would keep this queued.
	f.send("m3", "next task")
	third := f.command("prompt")
	f.emit(issue2161UnitResponse(third, true, "started"), map[string]any{"type": "agent_start"})
	f.finish("answer:next")
	f.assertAnswers(issue2161Delivery{"send", "m1", "answer:coalesced"}, issue2161Delivery{"send", "m3", "answer:next"})
	f.awaitIdle()
	if got := f.session.results.Load(); got != 2 {
		t.Fatalf("terminal events=%d, want 2", got)
	}
}
