package antigravity

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type agyMidTurnInputSender interface {
	SendMidTurn(string, string, []core.ImageAttachment, []core.FileAttachment) error
}

func startAgyACPFixture(t *testing.T) core.AgentSession {
	t.Helper()
	a := &Agent{
		cmd: os.Args[0], cliExtraArgs: []string{"-test.run=^TestAgyACPHelperProcess$", "--"},
		workDir: t.TempDir(), model: "fixture-model", effort: "high", mode: "default", activeIdx: -1,
		configEnv: []string{"CC_AGY_ACP_FIXTURE=1"},
	}
	s, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func nextAgyACPEvent(t *testing.T, s core.AgentSession, kind core.EventType) core.Event {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-s.Events():
			if event.Type == core.EventError {
				t.Fatal(event.Error)
			}
			if event.Type == kind {
				return event
			}
		case <-deadline.C:
			t.Fatalf("waiting for %s", kind)
		}
	}
}

func TestAgyACPFactoryAndMidTurnHandover(t *testing.T) {
	s := startAgyACPFixture(t)
	sender, ok := s.(agyMidTurnInputSender)
	if !ok {
		t.Fatal("agy 1.5.13 must use a persistent session supporting MidTurnInputSender")
	}
	if err := sender.SendMidTurn("idle", "", nil, nil); err == nil {
		t.Fatal("idle steering must fall back to the engine queue")
	}
	if err := s.Send("first", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	nextAgyACPEvent(t, s, core.EventToolUse)
	if err := sender.SendMidTurn("SECOND8767", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	var answer strings.Builder
	for {
		event := nextAgyACPEventAny(t, s)
		if event.Type == core.EventText {
			answer.WriteString(event.Content)
		}
		if event.Type != core.EventResult {
			continue
		}
		if answer.String() != "SECOND8767" {
			t.Fatalf("premature result or lost steer: answer %q", answer.String())
		}
		if event.InputTokens != 140 || event.CacheReadInputTokens != 240 || event.OutputTokens != 17 {
			t.Fatalf("handover must account for both prompt responses once: %+v", event)
		}
		usage := s.(core.ContextUsageReporter).GetContextUsage()
		if usage == nil || usage.InputTokens != 40 || usage.CachedInputTokens != 40 || usage.UsedTokens != 80 {
			t.Fatalf("ACP reports disjoint uncached/cache buckets: %+v", usage)
		}
		break
	}
	select {
	case event := <-s.Events():
		t.Fatalf("extra event after handover completion: %+v", event)
	case <-time.After(50 * time.Millisecond):
	}
	if err := s.Send("alive", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := nextAgyACPEvent(t, s, core.EventText).Content; got != "ALIVE8767" {
		t.Fatalf("next turn: %q", got)
	}
	nextAgyACPEvent(t, s, core.EventResult)
}

func TestAgyACPMinimumVersionKeepsCorrectSessionWorkingDirectory(t *testing.T) {
	for version, want := range map[string]bool{"1.4.0": false, "1.5.0": false, "1.5.12": false, "1.5.13": true, "1.5.14": true, "1.6.0": true, "2.0.0": true, "unknown": false} {
		t.Run(version, func(t *testing.T) {
			got := supportsAgyACP(context.Background(), os.Args[0], []string{"-test.run=^TestAgyACPHelperProcess$", "--"}, t.TempDir(), []string{"CC_AGY_ACP_FIXTURE=1", "CC_AGY_ACP_VERSION=" + version})
			if got != want {
				t.Fatalf("ACP support for %s = %t, want %t", version, got, want)
			}
		})
	}
}

func TestAgyACPMultipleSupplementsHaveOneResult(t *testing.T) {
	s := startAgyACPFixture(t)
	if err := s.Send("many", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	nextAgyACPEvent(t, s, core.EventToolUse)
	sender, ok := s.(agyMidTurnInputSender)
	if !ok {
		t.Fatal("missing mid-turn input capability")
	}
	second := make(chan error, 1)
	go func() { second <- sender.SendMidTurn("second", "", nil, nil) }()
	nextAgyACPEvent(t, s, core.EventToolUse)
	select {
	case event := <-s.Events():
		t.Fatalf("handover ended early: %+v", event)
	case <-time.After(30 * time.Millisecond):
	}
	if err := sender.SendMidTurn("last", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if got := nextAgyACPEvent(t, s, core.EventText).Content; got != "THIRD8767" {
		t.Fatalf("final answer %q", got)
	}
	result := nextAgyACPEvent(t, s, core.EventResult)
	if result.InputTokens != 240 || result.CacheReadInputTokens != 440 || result.OutputTokens != 27 {
		t.Fatalf("lost or duplicated usage: %+v", result)
	}
}

func TestAgyACPPlainSendSupplementsRunningTurn(t *testing.T) {
	s := startAgyACPFixture(t)
	if err := s.Send("first", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	nextAgyACPEvent(t, s, core.EventToolUse)
	if err := s.Send("SECOND8767", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := nextAgyACPEvent(t, s, core.EventText).Content; got != "SECOND8767" {
		t.Fatalf("/ps supplement lost: %q", got)
	}
	nextAgyACPEvent(t, s, core.EventResult)
}

func TestAgyACPCancelAfterHandoverKeepsSessionUsable(t *testing.T) {
	s := startAgyACPFixture(t)
	if err := s.Send("many", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	nextAgyACPEvent(t, s, core.EventToolUse)
	second := make(chan error, 1)
	go func() { second <- s.Send("second", "", nil, nil) }()
	nextAgyACPEvent(t, s, core.EventToolUse)
	canceller, ok := s.(interface{ CancelTurn() error })
	if !ok {
		t.Fatal("missing cancellation")
	}
	if err := canceller.CancelTurn(); err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	nextAgyACPEvent(t, s, core.EventResult)
	if err := s.Send("alive", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := nextAgyACPEvent(t, s, core.EventText).Content; got != "ALIVE8767" {
		t.Fatalf("cancelled session unusable: %q", got)
	}
	nextAgyACPEvent(t, s, core.EventResult)
}

func TestAgyACPReversedResponseOrderDoesNotFinishEarly(t *testing.T) {
	s := startAgyACPFixture(t)
	if err := s.Send("reverse", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	nextAgyACPEvent(t, s, core.EventToolUse)
	sender, ok := s.(agyMidTurnInputSender)
	if !ok {
		t.Fatal("missing mid-turn input capability")
	}
	if err := sender.SendMidTurn("SECOND8767", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := nextAgyACPEvent(t, s, core.EventText).Content; got != "SECOND8767" {
		t.Fatalf("final answer %q", got)
	}
	result := nextAgyACPEvent(t, s, core.EventResult)
	if result.InputTokens != 140 {
		t.Fatalf("lost superseded response usage: %+v", result)
	}
}

func TestAgyACPEOFReportsFailure(t *testing.T) {
	s := startAgyACPFixture(t)
	if err := s.Send("eof", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	nextAgyACPEvent(t, s, core.EventToolUse)
	select {
	case event := <-s.Events():
		if event.Type != core.EventError || event.Error == nil {
			t.Fatalf("unexpected EOF reported as success: %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EOF did not release the running turn")
	}
	nextAgyACPEvent(t, s, core.EventResult)
}

func TestAgyACPCloseUnblocksFullEventBuffer(t *testing.T) {
	s := startAgyACPFixture(t)
	if err := s.Send("flood", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	native := s.(*agyACPSession)
	deadline := time.Now().Add(5 * time.Second)
	for len(native.events) < cap(native.events) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close blocked behind an undrained event stream")
	}
}

func nextAgyACPEventAny(t *testing.T, s core.AgentSession) core.Event {
	t.Helper()
	select {
	case event := <-s.Events():
		if event.Type == core.EventError {
			t.Fatal(event.Error)
		}
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("waiting for ACP event")
		return core.Event{}
	}
}

func TestAgyACPPendingPermissionDeclinesSteerAndStaysAnswerable(t *testing.T) {
	s := startAgyACPFixture(t)
	sender, ok := s.(agyMidTurnInputSender)
	if !ok {
		t.Fatal("missing mid-turn input capability")
	}
	if err := s.Send("permission", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	permission := nextAgyACPEvent(t, s, core.EventPermissionRequest)
	if err := sender.SendMidTurn("SECOND8767", "", nil, nil); err == nil {
		t.Fatal("agy refuses mid-turn input while awaiting permission; return it to the engine queue")
	}
	if err := s.RespondPermission(permission.RequestID, core.PermissionResult{Behavior: "allow"}); err != nil {
		t.Fatal(err)
	}
	nextAgyACPEvent(t, s, core.EventResult)
}

func TestAgyACPRejectedPromptReturnsToQueueWithoutFailingOriginal(t *testing.T) {
	s := startAgyACPFixture(t)
	if err := s.Send("first", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	nextAgyACPEvent(t, s, core.EventToolUse)
	sender, ok := s.(agyMidTurnInputSender)
	if !ok {
		t.Fatal("missing mid-turn input capability")
	}
	if err := sender.SendMidTurn("refuse", "", nil, nil); err == nil {
		t.Fatal("a definite RPC rejection must be returned to the engine queue")
	}
	if got := nextAgyACPEvent(t, s, core.EventText).Content; got != "FIRST8767" {
		t.Fatalf("original answer %q", got)
	}
	nextAgyACPEvent(t, s, core.EventResult)
}

func TestAgyACPOptionalStartupCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, mode, model, effort, reply string
		succeeds                         bool
	}{
		{"unsupported defaults", "default", "fixture-model", "high", "unsupported", true},
		{"authorization is fatal", "default", "fixture-model", "high", "authorization", false},
		{"sandbox mode is required", "plan", "", "", "unsupported", false},
		{"transport error is fatal", "default", "fixture-model", "high", "disconnect", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s, err := newAgyACPSession(ctx, os.Args[0], []string{"-test.run=^TestAgyACPHelperProcess$", "--"}, t.TempDir(), tc.model, tc.effort, tc.mode, "", []string{"CC_AGY_ACP_FIXTURE=1", "CC_AGY_OPTION_REPLY=" + tc.reply}, 0, 0)
			if s != nil {
				defer func() { _ = s.Close() }()
			}
			if (err == nil) != tc.succeeds {
				t.Fatalf("unexpected start result: %v", err)
			}
		})
	}
}

func TestAgyACPRejectAfterOriginalResponse(t *testing.T) {
	s := startAgyACPFixture(t)
	if err := s.Send("first", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	nextAgyACPEvent(t, s, core.EventToolUse)
	if err := s.Send("refuse-late", "", nil, nil); err == nil || !strings.Contains(err.Error(), "input rejected") {
		t.Fatalf("missing definite rejection: %v", err)
	}
	if got := nextAgyACPEvent(t, s, core.EventText).Content; got != "FIRST8767" {
		t.Fatal(got)
	}
	if got := nextAgyACPEvent(t, s, core.EventResult); !got.Done || got.InputTokens != 100 {
		t.Fatalf("original result: %+v", got)
	}
}

func TestAgyACPNumericStringResponse(t *testing.T) {
	s := startAgyACPFixture(t)
	if err := s.Send("quoted", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := nextAgyACPEvent(t, s, core.EventResult); !got.Done || got.InputTokens != 1 {
		t.Fatalf("response was not correlated: %+v", got)
	}
}

func TestAgyACPStalledSupplementHasBoundedResult(t *testing.T) {
	s := startAgyACPFixture(t)
	if err := s.Send("first", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	nextAgyACPEvent(t, s, core.EventToolUse)
	result := make(chan error, 1)
	go func() { result <- s.Send("stall-late", "", nil, nil) }()
	acp := s.(*agyACPSession)
	deadline := time.Now().Add(2 * time.Second)
	for {
		acp.stateMu.Lock()
		settled := acp.usage.InputTokens == 100
		if settled {
			if remaining := time.Until(acp.turnDeadline); remaining <= 0 || remaining > agyACPSettlementTimeout {
				acp.stateMu.Unlock()
				t.Fatalf("settled predecessor left unbounded turn: %v", remaining)
			}
			acp.armACPTurnTimeout(time.Millisecond)
		}
		acp.stateMu.Unlock()
		if settled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("original did not settle")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("unexpected send response: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("send stayed blocked")
	}
	next := func() core.Event {
		t.Helper()
		select {
		case got := <-s.Events():
			return got
		case <-time.After(time.Second):
			t.Fatal("missing terminal sequence")
			return core.Event{}
		}
	}
	if got := next(); got.Type != core.EventText || got.Content != "FIRST8767" {
		t.Fatalf("original text lost: %+v", got)
	}
	if got := next(); got.Type != core.EventError {
		t.Fatalf("missing timeout error: %+v", got)
	}
	if got := next(); got.Type != core.EventResult || !got.Done || got.InputTokens != 100 {
		t.Fatalf("original result lost: %+v", got)
	}
	if s.Alive() {
		t.Fatal("timed-out process could emit into another turn")
	}
	select {
	case got := <-s.Events():
		t.Fatalf("unexpected extra event: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

// The helper models agy's measured prompt handover, including an old
// end_turn response followed by continued output on the new prompt.
func TestAgyACPHelperProcess(t *testing.T) {
	if os.Getenv("CC_AGY_ACP_FIXTURE") != "1" {
		return
	}
	for _, arg := range os.Args {
		if arg == "--version" {
			version := os.Getenv("CC_AGY_ACP_VERSION")
			if version == "" {
				version = "1.5.13"
			}
			fmt.Println(version)
			os.Exit(0)
		}
	}
	encoder := json.NewEncoder(os.Stdout)
	write := func(v any) { _ = encoder.Encode(v) }
	respond := func(id json.RawMessage, result any) {
		write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	update := func(value any) {
		write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "fixture-session", "update": value}})
	}
	end := func(id json.RawMessage, input, cache, output int) {
		respond(id, map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": input, "cachedReadTokens": cache, "outputTokens": output}})
	}
	var first, current json.RawMessage
	waitPermission := false
	many, reverse := false, false
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(3)
		}
		switch request.Method {
		case "initialize":
			respond(request.ID, map[string]any{"protocolVersion": 1, "agentInfo": map[string]any{"name": "Antigravity ACP Server", "version": "1.5.13"}})
		case "session/new", "session/resume":
			respond(request.ID, map[string]any{"sessionId": "fixture-session"})
		case "session/set_model", "session/set_config_option", "session/set_mode":
			switch os.Getenv("CC_AGY_OPTION_REPLY") {
			case "unsupported":
				write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32603, "message": "method not supported by this agent"}})
			case "authorization":
				write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32603, "message": "authorization denied"}})
			case "disconnect":
				os.Exit(2)
			default:
				respond(request.ID, map[string]any{})
			}
		case "session/prompt":
			text := request.Params.Prompt[0].Text
			if text == "refuse-late" || text == "stall-late" {
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "FIRST8767"}})
				end(first, 100, 200, 10)
				if text == "refuse-late" {
					write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32603, "message": "input rejected"}})
				}
			} else if text == "quoted" {
				id, _ := json.Marshal(string(request.ID))
				end(id, 1, 2, 3)
			} else if text == "refuse" {
				write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32603, "message": "running prompt is waiting for a question answer"}})
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "FIRST8767"}})
				end(first, 100, 200, 10)
			} else if text == "alive" {
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "ALIVE8767"}})
				end(request.ID, 1, 2, 3)
			} else if len(first) == 0 {
				first = request.ID
				many, reverse = text == "many", text == "reverse"
				update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "tool-1", "name": "run_command", "title": "sleep", "status": "in_progress"})
				if text == "eof" {
					os.Exit(0)
				}
				if text == "flood" {
					for i := 0; i < 200; i++ {
						update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": fmt.Sprint(i), "name": "run_command", "status": "in_progress"})
					}
				}
				if text == "permission" {
					waitPermission = true
					write(map[string]any{"jsonrpc": "2.0", "id": "permission-1", "method": "session/request_permission", "params": map[string]any{"sessionId": "fixture-session", "toolCall": map[string]any{"name": "run_command", "title": "sleep", "rawInput": map[string]any{"CommandLine": "sleep 1"}}, "options": []any{map[string]any{"optionId": "yes", "kind": "allow_once"}, map[string]any{"optionId": "no", "kind": "reject_once"}}}})
				}
			} else {
				current = request.ID
				if !reverse {
					end(first, 100, 200, 10)
				}
				if many && text != "last" {
					first = current
					update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "accepted", "name": "run_command", "status": "in_progress"})
					continue
				}
				if !waitPermission {
					answer := "SECOND8767"
					if many {
						answer = "THIRD8767"
					}
					update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": answer}})
					end(current, 40, 40, 7)
					if reverse {
						end(first, 100, 200, 10)
					}
				}
			}
		case "":
			if string(request.ID) == `"permission-1"` {
				end(first, 100, 200, 10)
			}
		case "session/cancel":
			end(first, 100, 200, 10)
		default:
			write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}})
		}
	}
	os.Exit(0)
}

func newUnitAgyACPSession() *agyACPSession {
	base := newAntigravitySessionState(context.Background(), "agy", nil, "/tmp", "gemini-3.8-flash-high", "yolo", "", nil, 0)
	s := &agyACPSession{
		antigravitySession: base,
		waiters:            make(map[string]chan agyACPReply),
		prompts:            make(map[string]*agyACPPrompt),
		permissions:        make(map[string]agyACPPermission),
		ready:              true,
	}
	s.chatID.Store("unit-session")
	return s
}

func drainACPEvents(ch <-chan core.Event) []core.Event {
	var out []core.Event
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestAgyACPThoughtChunksCoalesceAndFlushAtBoundaries(t *testing.T) {
	s := newUnitAgyACPSession()
	defer s.cancel()

	sendUpdate := func(upd map[string]any) {
		raw, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params":  map[string]any{"sessionId": "unit-session", "update": upd},
		})
		if err != nil {
			t.Fatal(err)
		}
		s.dispatchACP(raw)
	}

	s.stateMu.Lock()
	s.prompts["1"] = &agyACPPrompt{}
	s.stateMu.Unlock()

	sendUpdate(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "Step 1: "}})
	sendUpdate(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "read data.txt\n\n\n"}})
	sendUpdate(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Reading data.txt"}})
	sendUpdate(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "c1", "name": "read_file", "status": "in_progress", "rawInput": map[string]any{"Path": "/tmp/data.txt"}})
	sendUpdate(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "c1", "name": "read_file", "status": "completed"})

	sendUpdate(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Final answer: "}})
	sendUpdate(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "double check constraint\n\n\n"}})
	sendUpdate(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "52"}})

	resp, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result": map[string]any{
			"stopReason": "end_turn",
			"usage":      map[string]any{"inputTokens": 100, "cachedReadTokens": 50, "outputTokens": 20, "thoughtTokens": 12},
		},
	})
	s.dispatchACP(resp)

	events := drainACPEvents(s.events)
	wantTypes := []core.EventType{
		core.EventThinking,
		core.EventText,
		core.EventToolUse,
		core.EventToolResult,
		core.EventThinking,
		core.EventText,
		core.EventResult,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count = %d (%+v), want %d (%v)", len(events), events, len(wantTypes), wantTypes)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Fatalf("event[%d].Type = %s, want %s (all: %+v)", i, events[i].Type, want, events)
		}
	}
	if events[0].Content != "Step 1: read data.txt" {
		t.Fatalf("coalesced thinking = %q", events[0].Content)
	}
	if events[1].Content != "Reading data.txt" {
		t.Fatalf("pre-tool text = %q", events[1].Content)
	}
	if events[4].Content != "double check constraint" {
		t.Fatalf("trailing thinking = %q", events[4].Content)
	}
	if events[5].Content != "Final answer: 52" {
		t.Fatalf("final text = %q (must not split across thought)", events[5].Content)
	}
}

func TestAgyACPToolCallAndResultExtraction(t *testing.T) {
	s := newUnitAgyACPSession()
	defer s.cancel()

	s.stateMu.Lock()
	s.prompts["1"] = &agyACPPrompt{}
	s.stateMu.Unlock()

	sendUpdate := func(upd map[string]any) {
		raw, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params":  map[string]any{"sessionId": "unit-session", "update": upd},
		})
		s.dispatchACP(raw)
	}

	sendUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    "c-read",
		"name":          "read_file",
		"status":        "in_progress",
		"kind":          "read",
		"title":         "File data.txt",
		"rawInput":      map[string]any{"Path": "/workspace/data.txt"},
	})
	sendUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    "c-read",
		"name":          "read_file",
		"status":        "in_progress",
		"content":       []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "partial"}}},
	})
	sendUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    "c-read",
		"name":          "read_file",
		"status":        "completed",
		"kind":          "read",
		"title":         "File data.txt",
		"rawInput":      map[string]any{"Path": "/workspace/data.txt"},
	})

	sendUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    "c-cmd",
		"name":          "run_command",
		"status":        "in_progress",
		"kind":          "execute",
		"title":         "wc -l data.txt",
		"rawInput":      map[string]any{"CommandLine": "wc -l data.txt", "WaitMsBeforeAsync": 5000},
	})
	sendUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    "c-cmd",
		"name":          "run_command",
		"status":        "completed",
		"kind":          "execute",
		"title":         "wc -l data.txt",
		"rawInput":      map[string]any{"CommandLine": "wc -l data.txt"},
		"rawOutput":     map[string]any{"output": "3 data.txt\n", "exit_code": 0},
		"content": []any{
			map[string]any{
				"type":    "content",
				"content": map[string]any{"type": "text", "text": "3 data.txt\n"},
			},
		},
	})

	sendUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    "c-fail",
		"title":         "Run check",
		"kind":          "execute",
		"status":        "in_progress",
	})
	sendUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    "c-fail",
		"title":         "Run check",
		"status":        "failed",
		"rawOutput":     map[string]any{"output": "command failed", "exit_code": 2},
	})

	events := drainACPEvents(s.events)
	if len(events) != 6 {
		t.Fatalf("got %d events (%+v), want 6 (in_progress update must be ignored)", len(events), events)
	}

	if events[0].Type != core.EventToolUse || events[0].ToolName != "read_file" || events[0].ToolInput != "/workspace/data.txt" {
		t.Fatalf("read_file tool_use = %+v", events[0])
	}
	if events[1].Type != core.EventToolResult || events[1].ToolResult != "" || events[1].ToolSuccess == nil || !*events[1].ToolSuccess {
		t.Fatalf("read_file tool_result = %+v", events[1])
	}

	if events[2].Type != core.EventToolUse || events[2].ToolName != "run_command" || events[2].ToolInput != "wc -l data.txt" {
		t.Fatalf("run_command tool_use = %+v", events[2])
	}
	if events[3].Type != core.EventToolResult || strings.Contains(events[3].ToolResult, `"type":"content"`) || events[3].ToolResult != "3 data.txt\n" {
		t.Fatalf("run_command tool_result unexpected: %+v", events[3])
	}
	if events[3].ToolExitCode == nil || *events[3].ToolExitCode != 0 || events[3].ToolSuccess == nil || !*events[3].ToolSuccess {
		t.Fatalf("run_command exit/success = %+v", events[3])
	}

	if events[4].ToolName != "Run check" || events[4].ToolInput != "" || events[4].ToolInput == "null" {
		t.Fatalf("no-input tool_use = %+v", events[4])
	}
	if events[5].ToolResult != "command failed" || events[5].ToolExitCode == nil || *events[5].ToolExitCode != 2 || events[5].ToolSuccess == nil || *events[5].ToolSuccess {
		t.Fatalf("failed tool_result = %+v", events[5])
	}

	sendUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    "c-grep",
		"name":          "grep_search",
		"status":        "in_progress",
		"rawInput":      map[string]any{"SearchPath": "/repo", "Query": "needle"},
	})
	sendUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    "c-str-in",
		"name":          "custom_tool",
		"status":        "in_progress",
		"rawInput":      "raw string input",
	})
	more := drainACPEvents(s.events)
	if len(more) != 2 || more[0].ToolInput != "needle @ /repo" || more[1].ToolInput != "raw string input" {
		t.Fatalf("grep/rawInput batch = %+v", more)
	}
}

func TestAgyACPToolCompletionWithNonObjectRawOutput(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want string
	}{
		{"string", `"3 lines"`, "3 lines"},
		{"array", `["line one", "line two"]`, ""},
		{"number", "3", ""},
		{"boolean", "true", ""},
		{"null", "null", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newUnitAgyACPSession()
			defer s.cancel()
			s.prompts["1"] = &agyACPPrompt{}
			s.dispatchACP([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"unit-session","update":{"sessionUpdate":"tool_call","toolCallId":"c-read","name":"read_file","status":"in_progress"}}}`))
			s.dispatchACP([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"unit-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"c-read","name":"read_file","status":"completed","rawOutput":` + tt.raw + `}}}`))
			events := drainACPEvents(s.events)
			if len(events) != 2 || events[0].Type != core.EventToolUse {
				t.Fatalf("want tool start and completion, got %+v", events)
			}
			got := events[1]
			if got.Type != core.EventToolResult || got.ToolResult != tt.want || got.ToolSuccess == nil || !*got.ToolSuccess || got.ToolExitCode != nil {
				t.Fatalf("unexpected completion: %+v", got)
			}
		})
	}
}

func TestAgyACPUsageUpdateAndPlan(t *testing.T) {
	s := newUnitAgyACPSession()
	defer s.cancel()

	s.stateMu.Lock()
	s.prompts["1"] = &agyACPPrompt{}
	s.stateMu.Unlock()

	sendUpdate := func(upd map[string]any) {
		raw, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params":  map[string]any{"sessionId": "unit-session", "update": upd},
		})
		s.dispatchACP(raw)
	}

	sendUpdate(map[string]any{"sessionUpdate": "usage_update", "used": 16786.0, "size": 1048576.0})
	usage := s.GetContextUsage()
	if usage == nil || usage.UsedTokens != 16786 || usage.ContextWindow != 1048576 || usage.InputTokens != 0 {
		t.Fatalf("usage after usage_update = %+v", usage)
	}
	sendUpdate(map[string]any{"sessionUpdate": "usage_update", "used": 0, "size": 1048576})
	usage = s.GetContextUsage()
	if usage == nil || usage.UsedTokens != 16786 {
		t.Fatalf("usage overwritten by zero usage_update = %+v", usage)
	}

	replyRaw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result": map[string]any{
			"stopReason": "end_turn",
			"usage":      map[string]any{"inputTokens": 100, "cachedReadTokens": 900, "outputTokens": 50, "thoughtTokens": 12},
		},
	})
	s.dispatchACP(replyRaw)
	_ = drainACPEvents(s.events)
	// Next turn starts before late usage_update arrives (matching len(s.prompts) > 0 guard).
	s.stateMu.Lock()
	s.prompts["2"] = &agyACPPrompt{}
	s.stateMu.Unlock()
	sendUpdate(map[string]any{"sessionUpdate": "usage_update", "used": 1200, "size": 2097152})
	usage = s.GetContextUsage()
	if usage == nil || usage.UsedTokens != 1200 || usage.ContextWindow != 2097152 ||
		usage.InputTokens != 100 || usage.CachedInputTokens != 900 || usage.OutputTokens != 50 || usage.ReasoningOutputTokens != 12 ||
		usage.TotalTokens != 1200 {
		t.Fatalf("late usage_update clobbered reply breakdown: %+v", usage)
	}

	sendUpdate(map[string]any{
		"sessionUpdate": "plan",
		"entries": []any{
			map[string]any{"content": "Read file", "status": "completed"},
			map[string]any{"content": "Count lines", "status": "in_progress"},
		},
	})
	events := drainACPEvents(s.events)
	if len(events) != 1 || events[0].Type != core.EventThinking || events[0].Content != "[completed] Read file\n[in_progress] Count lines" {
		t.Fatalf("plan events = %+v", events)
	}
}

type acpCategoryCardPlatform struct {
	sent     chan string
	payloads chan core.ProgressCardPayload
}

func (p *acpCategoryCardPlatform) Name() string                    { return "test" }
func (p *acpCategoryCardPlatform) Start(core.MessageHandler) error { return nil }
func (p *acpCategoryCardPlatform) Stop() error                     { return nil }
func (p *acpCategoryCardPlatform) Reply(ctx context.Context, replyCtx any, text string) error {
	return p.Send(ctx, replyCtx, text)
}
func (p *acpCategoryCardPlatform) Send(_ context.Context, _ any, text string) error {
	p.sent <- text
	return nil
}
func (p *acpCategoryCardPlatform) ProgressStyle() string             { return "card" }
func (p *acpCategoryCardPlatform) SupportsProgressCardPayload() bool { return true }
func (p *acpCategoryCardPlatform) SendPreviewStart(_ context.Context, _ any, content string) (any, error) {
	if payload, ok := core.ParseProgressCardPayload(content); ok {
		p.payloads <- *payload
	}
	return "card-handle", nil
}
func (p *acpCategoryCardPlatform) UpdateMessage(_ context.Context, _ any, content string) error {
	if payload, ok := core.ParseProgressCardPayload(content); ok {
		p.payloads <- *payload
	}
	return nil
}

func TestAgyACPEndToEndEngineCategories(t *testing.T) {
	if os.Getenv("CC_AGY_ACP_CAT_HELPER") == "1" {
		for _, arg := range os.Args {
			if arg == "--version" {
				fmt.Println("1.5.16")
				os.Exit(0)
			}
		}
		encoder := json.NewEncoder(os.Stdout)
		write := func(v any) { _ = encoder.Encode(v) }
		update := func(value any) {
			write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "cat-session", "update": value}})
		}
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(scanner.Bytes(), &req) != nil {
				os.Exit(3)
			}
			switch req.Method {
			case "initialize":
				write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": 1}})
			case "session/new":
				write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"sessionId": "cat-session"}})
			case "session/set_model", "session/set_config_option", "session/set_mode":
				write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
			case "session/prompt":
				update(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "Plan step 1"}})
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Checking lines now"}})
				update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "name": "run_command", "status": "in_progress", "rawInput": map[string]any{"CommandLine": "wc -l data.txt"}})
				update(map[string]any{
					"sessionUpdate": "tool_call_update",
					"toolCallId":    "t1",
					"name":          "run_command",
					"status":        "completed",
					"rawOutput":     map[string]any{"output": "3 data.txt\n", "exit_code": 0},
					"content":       []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "3 data.txt"}}},
				})
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "All done: 3 lines"}})
				write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": 50, "cachedReadTokens": 10, "outputTokens": 15, "thoughtTokens": 5}}})
			}
		}
		os.Exit(0)
	}

	a := &Agent{
		cmd:          os.Args[0],
		cliExtraArgs: []string{"-test.run=^TestAgyACPEndToEndEngineCategories$", "--"},
		workDir:      t.TempDir(),
		model:        "gemini-3.8-flash-high",
		effort:       "high",
		mode:         "yolo",
		activeIdx:    -1,
		configEnv:    []string{"CC_AGY_ACP_CAT_HELPER=1"},
	}
	p := &acpCategoryCardPlatform{sent: make(chan string, 16), payloads: make(chan core.ProgressCardPayload, 16)}
	e := core.NewEngine("acp-cat", a, []core.Platform{p}, "", core.LangEnglish)
	e.SetDisplayConfig(core.DisplayCfg{
		Mode:             "full",
		CardMode:         "legacy",
		ThinkingMessages: true,
		ToolMessages:     true,
	})
	e.SetStreamPreviewCfg(core.StreamPreviewCfg{Enabled: false})
	t.Cleanup(func() { _ = e.Stop() })

	e.ReceiveMessage(p, &core.Message{SessionKey: "test:cat:user", Platform: "test", UserID: "user", Content: "check", ReplyCtx: "ctx"})

	var sent []string
	deadline := time.After(5 * time.Second)
	for len(sent) < 2 {
		select {
		case msg := <-p.sent:
			sent = append(sent, msg)
		case <-deadline:
			t.Fatalf("timed out waiting for sent messages, got %v", sent)
		}
	}

	var latest core.ProgressCardPayload
	for {
		select {
		case pl := <-p.payloads:
			latest = pl
		case <-time.After(100 * time.Millisecond):
			goto checkPayload
		}
	}
checkPayload:
	if latest.State != core.ProgressCardStateCompleted {
		t.Fatalf("card state = %q, want completed", latest.State)
	}
	var hasThinking, hasToolUse, hasToolResult bool
	for _, item := range latest.Items {
		switch item.Kind {
		case core.ProgressEntryThinking:
			if strings.Contains(item.Text, "Plan step 1") {
				hasThinking = true
			}
		case core.ProgressEntryToolUse:
			if item.Tool == "run_command" && item.Text == "wc -l data.txt" {
				hasToolUse = true
			}
		case core.ProgressEntryToolResult:
			if item.Tool == "run_command" && item.Text == "3 data.txt" && item.ExitCode != nil && *item.ExitCode == 0 && item.Success != nil && *item.Success && !strings.Contains(item.Text, `"type"`) {
				hasToolResult = true
			}
		}
	}
	if !hasThinking {
		t.Fatalf("progress card missing thinking item: %+v", latest.Items)
	}
	if !hasToolUse {
		t.Fatalf("progress card missing clean tool use input: %+v", latest.Items)
	}
	if !hasToolResult {
		t.Fatalf("progress card missing clean tool result: %+v", latest.Items)
	}
	if sent[0] != "Checking lines now" || !strings.Contains(sent[1], "All done: 3 lines") || strings.Contains(sent[1], "Checking lines now") {
		t.Fatalf("sent messages = %+v", sent)
	}
}
