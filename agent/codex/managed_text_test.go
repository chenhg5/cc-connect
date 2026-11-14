package codex

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func TestManagedAgentTextStreamsBeforeCompletionWithoutDuplicating(t *testing.T) {
	cwd := t.TempDir()
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method != "thread/resume" {
				t.Errorf("unexpected RPC %s", m.Method)
				return nil, false
			}
			_ = ws.WriteJSON(map[string]any{"id": m.ID, "result": fixtureSnapshot(cwd, "target", "turn")})
			send := func(method string, item any) { _ = ws.WriteJSON(map[string]any{"method": method, "params": item}) }
			send("item/started", map[string]any{"threadId": "target", "turnId": "turn", "item": map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer"}})
			for _, chunk := range []string{"中文开头", "...", "后续正文"} {
				send("item/agentMessage/delta", map[string]any{"threadId": "target", "turnId": "turn", "itemId": "answer", "delta": chunk})
			}
			<-release // Completion cannot arrive before the live-stream assertions.
			item := map[string]any{"threadId": "target", "turnId": "turn", "item": map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer", "text": "中文开头...后续正文结尾"}}
			send("item/completed", item)
			send("item/completed", item)
			send("item/agentMessage/delta", map[string]any{"threadId": "target", "turnId": "turn", "itemId": "answer", "delta": "STALE"})
			send("turn/completed", map[string]any{"threadId": "target", "turn": map[string]any{"id": "turn", "status": "completed"}})
			return nil, false
		})
	})
	as, err := fixtureManagedAgent(t, socket, cwd, nil).AttachSession(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := as.Close(); err != nil {
			t.Errorf("Close cleanup: %v", err)
		}
	}()
	var preview strings.Builder
	for _, chunk := range []string{"中文开头", "...", "后续正文"} {
		event := awaitManagedEvent(t, as, core.EventText)
		if event.Content != chunk || event.ItemID != "answer" || event.TurnID != "turn" || event.Metadata["phase"] != "final_answer" || event.Metadata["text_delta"] != true {
			t.Fatalf("bad scoped delta: %+v", event)
		}
		preview.WriteString(event.Content)
	}
	finish()
	event := awaitManagedEvent(t, as, core.EventText)
	if event.Content != "结尾" {
		t.Fatalf("completion re-appended full response: %+v", event)
	}
	preview.WriteString(event.Content)
	result := awaitManagedEvent(t, as, core.EventResult)
	if preview.String() != result.Content || result.Content != "中文开头...后续正文结尾" {
		t.Fatalf("preview/final mismatch: %q / %q", preview.String(), result.Content)
	}
}

func TestManagedTextCompletionReconcilesItemsAndKeepsAsyncQuestions(t *testing.T) {
	s := &managedSession{agent: &managedAgent{daemon: managedOptions{asyncQuestions: true}}, ctx: context.Background(), thread: "target", turn: "turn", events: make(chan core.Event, 32), seenItems: map[string]bool{}, completed: map[string]bool{}, decoder: &appServerSession{}}
	send := func(method, params string) {
		t.Helper()
		if err := s.handleMessage(daemonMessage{Method: method, Params: json.RawMessage(params)}); err != nil {
			t.Fatal(err)
		}
	}
	send("item/agentMessage/delta", `{"threadId":"other","turnId":"turn","itemId":"answer","delta":"OTHER THREAD"}`)
	send("item/agentMessage/delta", `{"threadId":"target","turnId":"turn","itemId":"a","delta":"MISSED PREFIX SUFFIX"}`)
	send("item/agentMessage/delta", `{"threadId":"target","turnId":"turn","itemId":"b","delta":"Question text"}`)
	send("item/completed", `{"threadId":"target","turnId":"turn","item":{"type":"agentMessage","id":"a","phase":"final_answer","text":"AUTHORITATIVE ANSWER"}}`)
	send("item/completed", `{"threadId":"target","turnId":"turn","item":{"type":"agentMessage","id":"b","phase":"commentary","text":"Question text","delivery":"async","questions":[{"title":"Preference?"}]}}`)
	if len(s.events) != 4 {
		t.Fatalf("lost async metadata, mixed threads, or appended duplicate: %d", len(s.events))
	}
	<-s.events
	<-s.events
	corrected := <-s.events
	if corrected.Content != "AUTHORITATIVE ANSWER" || corrected.Metadata["replace_item_text"] != true {
		t.Fatalf("no authoritative correction: %+v", corrected)
	}
	question := <-s.events
	if question.Content != "" || len(question.Questions) != 1 || question.Metadata["message_text"] != "Question text" {
		t.Fatalf("async question lost or duplicated: %+v", question)
	}
	if len(s.textStreams) != 0 {
		t.Fatal("completed item buffers retained")
	}
}

func TestManagedFullyStreamedFinalDoesNotDisappearOnCompletion(t *testing.T) {
	s := &managedSession{agent: &managedAgent{daemon: managedOptions{asyncQuestions: true}}, ctx: context.Background(), thread: "target", turn: "turn", events: make(chan core.Event, 8), seenItems: map[string]bool{}, completed: map[string]bool{}, decoder: &appServerSession{}}
	messages := []daemonMessage{
		{Method: "item/agentMessage/delta", Params: json.RawMessage(`{"threadId":"target","turnId":"turn","itemId":"answer","delta":"FULL FINAL ANSWER"}`)},
		{Method: "item/completed", Params: json.RawMessage(`{"threadId":"target","turnId":"turn","item":{"type":"agentMessage","id":"answer","phase":"final_answer","text":"FULL FINAL ANSWER"}}`)},
		{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"target","turn":{"id":"turn","status":"completed"}}`)},
	}
	for _, m := range messages {
		if err := s.handleMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.events) != 2 {
		t.Fatalf("duplicated full completion: %d", len(s.events))
	}
	preview := <-s.events
	final := <-s.events
	if preview.Type != core.EventText || final.Type != core.EventResult || final.Content != preview.Content {
		t.Fatalf("lost completed answer: %+v / %+v", preview, final)
	}
}
