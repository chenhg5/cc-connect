package codex

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAppServerSession_AsyncUserInputSurvivesToolBoundary(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8)}
	s.handleItemCompleted(map[string]any{
		"type": "agentMessage", "id": "question-1", "delivery": "async",
		"text":      "Which database?\n- PostgreSQL\n- SQLite",
		"questions": []any{map[string]any{"title": "Which database?", "options": []any{"PostgreSQL", "SQLite"}}},
	})
	s.handleItemStarted(map[string]any{"type": "commandExecution", "command": "pwd"})
	question := <-s.events
	if question.Type != core.EventUserInputRequest || question.RequestID != "question-1" {
		t.Fatalf("first event = %#v, want immediate async question", question)
	}
	if len(question.Questions) != 1 || question.Questions[0].Options[1].Label != "SQLite" {
		t.Fatalf("questions = %#v", question.Questions)
	}
	if event := <-s.events; event.Type != core.EventToolUse {
		t.Fatalf("next event = %#v, question must not become thinking", event)
	}
	if len(s.pendingMsgs) != 0 {
		t.Fatalf("async question was buffered as ordinary text: %v", s.pendingMsgs)
	}
}

func TestAppServerSession_AsyncUserInputFreeTextAndFallback(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 4)}
	s.handleItemCompleted(map[string]any{
		"type": "agentMessage", "id": "free", "delivery": "async", "text": "Your preference?",
		"questions": []any{map[string]any{"title": "Your preference?"}},
	})
	if event := <-s.events; event.Type != core.EventUserInputRequest || len(event.Questions[0].Options) != 0 {
		t.Fatalf("free-text question = %#v", event)
	}
	s.handleItemCompleted(map[string]any{
		"type": "agentMessage", "delivery": "async", "text": "Please reply in chat.", "questions": "invalid",
	})
	if event := <-s.events; event.Type != core.EventText || event.Content != "Please reply in chat." {
		t.Fatalf("fallback = %#v", event)
	}
}

func TestAppServerSession_AsyncUserInputIgnoresChildThread(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 4)}
	s.threadID.Store("parent")
	s.handleNotification("item/completed", json.RawMessage(`{"threadId":"child","item":{"type":"agentMessage","id":"child-question","delivery":"async","text":"Child question","questions":[{"title":"Child question"}]}}`))
	if len(s.events) != 0 {
		t.Fatal("child question leaked to parent")
	}
}

type asyncSteerWriter struct {
	s       *appServerSession
	request struct {
		ID     int64          `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
}

func (w *asyncSteerWriter) Write(p []byte) (int, error) {
	if err := json.Unmarshal(p, &w.request); err != nil {
		return 0, err
	}
	w.s.handleResponse(rpcResponseEnvelope{ID: w.request.ID, Result: json.RawMessage(`{"turnId":"active"}`)})
	return len(p), nil
}
func (*asyncSteerWriter) Close() error { return nil }

func TestAppServerSession_SteerUserInputUsesActiveTurn(t *testing.T) {
	s := &appServerSession{ctx: context.Background(), currentTurn: "active", pending: make(map[int64]chan rpcResponseEnvelope)}
	s.threadID.Store("parent")
	w := &asyncSteerWriter{s: s}
	s.stdin = w
	steered, err := s.SteerUserInput("Which database?: SQLite")
	if err != nil || !steered {
		t.Fatalf("SteerUserInput = %v, %v", steered, err)
	}
	if w.request.Method != "turn/steer" || w.request.Params["threadId"] != "parent" || w.request.Params["expectedTurnId"] != "active" {
		t.Fatalf("request = %#v", w.request)
	}
	input := w.request.Params["input"].([]any)[0].(map[string]any)
	if input["text"] != "Which database?: SQLite" {
		t.Fatalf("answer input = %#v", input)
	}
	s.currentTurn = ""
	if steered, err := s.SteerUserInput("late answer"); err != nil || steered {
		t.Fatalf("idle answer = %v, %v; want ordinary Send path", steered, err)
	}
}
