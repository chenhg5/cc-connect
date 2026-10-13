package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestAppServerPublicTextVisibleBeforeItemCompleted(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 32)}
	s.handleNotification("turn/started", json.RawMessage(`{"turn":{"id":"turn"}}`))
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "comment", "phase": "commentary"})
	s.handleNotification("item/agentMessage/delta", json.RawMessage(`{"turnId":"turn","itemId":"comment","delta":"正在检查"}`))
	select {
	case event := <-s.events:
		if event.Type != core.EventProgress || event.Content != "正在检查" {
			t.Fatalf("public delta = %#v", event)
		}
	default:
		t.Fatal("public text is buffered until item/tool/turn completion")
	}
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "comment", "phase": "commentary", "text": "正在检查代码。"})
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "final", "phase": "final_answer"})
	s.handleNotification("item/agentMessage/delta", json.RawMessage(`{"turnId":"turn","itemId":"final","delta":"已经"}`))
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "final", "phase": "final_answer", "text": "已经修复。"})
	s.completeTurn()
	var answer string
	for len(s.events) > 0 {
		e := <-s.events
		if e.Type == core.EventText {
			answer += e.Content
		}
		if e.Type == core.EventResult && e.Content != "已经修复。" {
			t.Fatalf("final includes progress or is missing: %#v", e)
		}
	}
	if answer != "已经修复。" {
		t.Fatalf("duplicated or lost final text: %q", answer)
	}
}

func TestAppServerInitializeDoesNotOptOutPublicDeltas(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "rpc.jsonl")
	a := &Agent{cmd: os.Args[0], backend: "app_server", mode: "suggest", appServerURL: "stdio://", activeIdx: -1, cliExtraArgs: []string{"-test.run=^TestDirectoryCodexHelper$", "--"}, configEnv: []string{"CC_DIRECTORY_HELPER=" + capture}}
	s, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"initialize"`) {
		t.Fatal("missing handshake")
	}
	if strings.Contains(string(b), `"item/agentMessage/delta"`) {
		t.Fatal("handshake disables public text deltas")
	}
}

func TestAppServerStreamingSurvivesTurnStartRaceAndLateChunks(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 32)}
	s.handleNotification("turn/started", json.RawMessage(`{"turn":{"id":"one"}}`))
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "a"})
	s.handlePublicDelta("one", "a", "Public ")
	s.stateMu.Lock()
	s.beginTurnLocked("one")
	s.stateMu.Unlock()
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "a", "text": "Public commentary", "phase": "commentary"})
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "b"})
	s.handlePublicDelta("one", "b", "Final")
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "b", "text": "Final"})
	s.completeTurn()
	before := len(s.events)
	s.stateMu.Lock()
	s.beginTurnLocked("one")
	s.stateMu.Unlock()
	s.handlePublicDelta("one", "b", "duplicated")
	s.completeTurn()
	if len(s.events) != before || s.currentTurn != "" {
		t.Fatal("late start response resurrected completed turn")
	}
	s.handleNotification("turn/started", json.RawMessage(`{"turn":{"id":"two"}}`))
	s.handlePublicDelta("one", "a", "stale")
	if len(s.events) != before {
		t.Fatal("old turn delta leaked into next turn")
	}
	var text string
	for len(s.events) > 0 {
		e := <-s.events
		if e.Type == core.EventText {
			text += e.Content
		}
		if e.Type == core.EventResult && e.Content != "Final" {
			t.Fatalf("bad final: %#v", e)
		}
	}
	if text != "Public Final" {
		t.Fatalf("turn start cleared or duplicated stream: %q", text)
	}
}

func TestAppServerEventBackpressureDoesNotDropText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &appServerSession{ctx: ctx, events: make(chan core.Event, 1)}
	s.emit(core.Event{Type: core.EventText, Content: "first"})
	done := make(chan struct{})
	go func() { s.emit(core.Event{Type: core.EventText, Content: "second"}); close(done) }()
	select {
	case <-done:
		t.Fatal("full queue silently dropped text")
	case <-time.After(20 * time.Millisecond):
	}
	<-s.events
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queue failed to resume")
	}
	if e := <-s.events; e.Content != "second" {
		t.Fatal("missing streamed text")
	}
	s.emit(core.Event{Type: core.EventText})
	cancelled := make(chan struct{})
	go func() { s.emit(core.Event{Type: core.EventText}); close(cancelled) }()
	cancel()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancel did not release blocked emitter")
	}
}

func TestAppServerCommandOutputStreamsAndKeepsOmittedFinalOutput(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8), currentTurn: "t"}
	s.handleNotification("item/commandExecution/outputDelta", json.RawMessage(`{"turnId":"t","itemId":"tool","delta":"partial"}`))
	e := <-s.events
	if !e.ToolResultDelta || e.ToolStatus != "in_progress" || e.ToolResult != "partial" {
		t.Fatalf("invalid live output: %#v", e)
	}
	s.handleItemCompleted(map[string]any{"type": "commandExecution", "id": "tool", "status": "completed"})
	if e := <-s.events; e.ToolResultPresent {
		t.Fatal("omitted final output clears streamed output")
	}
	s.handleItemCompleted(map[string]any{"type": "commandExecution", "id": "tool", "status": "completed", "aggregatedOutput": ""})
	if e := <-s.events; !e.ToolResultPresent {
		t.Fatal("explicit empty final output lost")
	}
}
