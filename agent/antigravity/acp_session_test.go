package antigravity

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

func TestAgyACPResumeKeepsEffectiveReasoningSettings(t *testing.T) {
	for _, tc := range []struct {
		name, model, wantEffort string
	}{
		{name: "ordinary model", model: "gemini-4-neon", wantEffort: "high"},
		{name: "provider overrides ordinary model with orchestrator", model: "gemini-pro-orchestrator-neon"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trace := filepath.Join(t.TempDir(), "trace")
			a := &Agent{
				cmd: os.Args[0], cliExtraArgs: []string{"-test.run=^TestAgyACPHelperProcess$", "--"},
				workDir: t.TempDir(), model: "gemini-4-neon", effort: "high", mode: "default",
				planner: "Gemini 4 Neon (High)", executor: "Gemini 4 Hydrogen (High)",
				activeIdx: 0, providers: []core.ProviderConfig{{Model: tc.model}},
				configEnv: []string{"CC_AGY_ACP_FIXTURE=1", "CC_AGY_SETTINGS_TRACE=" + trace},
			}
			s, err := a.StartSession(context.Background(), "previous-session")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			if _, ok := s.(agyMidTurnInputSender); !ok {
				t.Fatal("session-specific flags must not make the ACP version probe fail")
			}
			raw, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var entry string
				if err := json.Unmarshal([]byte(line), &entry); err != nil {
					t.Fatal(err)
				}
				got = append(got, entry)
			}
			want := []string{
				`{"orchestrator":{"executor":"Gemini 4 Hydrogen (High)","planner":"Gemini 4 Neon (High)"}}`,
				"resume:previous-session", "model:" + tc.model,
			}
			if tc.wantEffort != "" {
				want = append(want, "thought_level:"+tc.wantEffort)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("ACP startup trace = %q; want %q", got, want)
			}
		})
	}
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
	var trace *json.Encoder
	if path := os.Getenv("CC_AGY_SETTINGS_TRACE"); path != "" {
		for i, arg := range os.Args {
			switch arg {
			case "--version":
				if i < 1 || os.Args[i-1] != "--" {
					os.Exit(3)
				}
			case "--effort":
				os.Exit(3)
			case "--settings":
				if i+2 >= len(os.Args) || os.Args[i+2] != "acp" {
					os.Exit(3)
				}
				file, err := os.Create(path)
				if err != nil {
					os.Exit(3)
				}
				trace = json.NewEncoder(file)
				_ = trace.Encode(os.Args[i+1])
			}
		}
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
				SessionID string `json:"sessionId"`
				ModelID   string `json:"modelId"`
				ConfigID  string `json:"configId"`
				Value     string `json:"value"`
				Prompt    []struct {
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
			if trace != nil && request.Method == "session/resume" {
				_ = trace.Encode("resume:" + request.Params.SessionID)
			}
			respond(request.ID, map[string]any{"sessionId": "fixture-session"})
		case "session/set_model", "session/set_config_option", "session/set_mode":
			if trace != nil {
				switch request.Method {
				case "session/set_model":
					_ = trace.Encode("model:" + request.Params.ModelID)
				case "session/set_config_option":
					_ = trace.Encode(request.Params.ConfigID + ":" + request.Params.Value)
				}
			}
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
