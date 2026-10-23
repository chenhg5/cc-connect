package codex

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func subagentNotification(s *appServerSession, method, params string) {
	s.handleNotification(method, json.RawMessage(params))
}

func subagentEvents(events chan core.Event) []core.Event {
	var out []core.Event
	for len(events) > 0 {
		out = append(out, <-events)
	}
	return out
}

func TestAppServerSubagents_SeparateProgressAndParentAnswer(t *testing.T) {
	s := &appServerSession{ctx: context.Background(), events: make(chan core.Event, 64), currentTurn: "parent-turn"}
	s.threadID.Store("parent")
	subagentNotification(s, "item/completed", `{"threadId":"parent","turnId":"parent-turn","item":{"type":"agentMessage","text":"Parent answer"}}`)
	subagentNotification(s, "thread/started", `{"thread":{"id":"child","agentNickname":"Scout","agentRole":"explorer","preview":"Find the bug","source":{"subAgent":{"thread_spawn":{"parent_thread_id":"parent","depth":1}}}}}`)
	subagentNotification(s, "turn/started", `{"threadId":"child","turn":{"id":"child-turn"}}`)
	subagentNotification(s, "item/completed", `{"threadId":"child","turnId":"child-turn","item":{"type":"reasoning","summary":["Inspecting code"]}}`)
	subagentNotification(s, "item/started", `{"threadId":"child","turnId":"child-turn","item":{"type":"commandExecution","command":"rg bug"}}`)
	subagentNotification(s, "item/completed", `{"threadId":"child","turnId":"child-turn","item":{"type":"agentMessage","text":"Found the bug"}}`)
	subagentNotification(s, "thread/tokenUsage/updated", `{"threadId":"child","tokenUsage":{"last":{"totalTokens":999},"total":{"totalTokens":999},"modelContextWindow":1000}}`)
	subagentNotification(s, "turn/completed", `{"threadId":"child","turn":{"id":"child-turn","status":"completed"}}`)
	subagentNotification(s, "thread/status/changed", `{"threadId":"child","status":{"type":"idle"}}`)
	if s.currentTurn != "parent-turn" || len(s.pendingMsgs) != 1 || s.pendingMsgs[0] != "Parent answer" {
		t.Fatalf("child changed parent turn: turn=%q messages=%v", s.currentTurn, s.pendingMsgs)
	}
	if s.context != nil {
		t.Fatalf("child replaced parent context: %+v", s.context)
	}
	subagentNotification(s, "turn/completed", `{"threadId":"parent","turn":{"id":"parent-turn","status":"completed"}}`)
	events := subagentEvents(s.events)
	if len(events) != 7 {
		t.Fatalf("got %d events, want 5 subagent events, parent text and result: %+v", len(events), events)
	}
	for i, phase := range []core.SubagentPhase{core.SubagentStarted, core.SubagentStep, core.SubagentStep, core.SubagentStep, core.SubagentFinished} {
		if events[i].Type != core.EventSubagent || events[i].Subagent.ID != "child" || events[i].Subagent.Phase != phase {
			t.Fatalf("event %d = %+v, want child %s", i, events[i], phase)
		}
	}
	if e := events[0].Subagent; e.Title != "Scout" || e.Kind != "explorer" || e.Prompt != "Find the bug" || !e.Background {
		t.Fatalf("start metadata = %+v", e)
	}
	if e := events[2].Subagent.Step; e.Tool != "Bash" || e.Text != "rg bug" {
		t.Fatalf("tool step = %+v", e)
	}
	if events[5].Type != core.EventText || events[5].Content != "Parent answer" || events[6].Type != core.EventResult {
		t.Fatalf("parent answer = %+v", events[5:])
	}
}

func TestAppServerSubagents_BackgroundWakeAndUnknownThread(t *testing.T) {
	s := &appServerSession{ctx: context.Background(), events: make(chan core.Event, 64)}
	s.threadID.Store("parent")
	// Current Codex emits subAgentActivity items on the parent thread.
	subagentNotification(s, "item/started", `{"threadId":"parent","item":{"type":"subAgentActivity","kind":"started","agentThreadId":"child","agentPath":"/scout"}}`)
	subagentNotification(s, "item/completed", `{"threadId":"parent","item":{"type":"subAgentActivity","kind":"started","agentThreadId":"child","agentPath":"/scout"}}`)
	subagentNotification(s, "item/completed", `{"threadId":"child","item":{"type":"agentMessage","text":"Background report"}}`)
	subagentNotification(s, "turn/completed", `{"threadId":"child","turn":{"id":"one","status":"completed"}}`)
	subagentNotification(s, "turn/started", `{"threadId":"child","turn":{"id":"two"}}`)
	subagentNotification(s, "turn/completed", `{"threadId":"child","turn":{"id":"two","status":"failed","error":{"message":"Tool failed"}}}`)
	subagentNotification(s, "item/completed", `{"threadId":"unrelated","item":{"type":"agentMessage","text":"MUST NOT LEAK"}}`)
	subagentNotification(s, "turn/started", `{"threadId":"unrelated","turn":{"id":"foreign"}}`)
	events := subagentEvents(s.events)
	if len(events) != 5 || s.currentTurn != "" || len(s.pendingMsgs) != 0 {
		t.Fatalf("events=%+v turn=%q messages=%v", events, s.currentTurn, s.pendingMsgs)
	}
	for _, e := range events {
		if e.Type != core.EventSubagent || e.Subagent.ID != "child" {
			t.Fatalf("background event leaked to parent: %+v", e)
		}
	}
	if events[1].Subagent.Step.Text != "Background report" || events[3].Subagent.Phase != core.SubagentStarted || events[4].Subagent.Status != "failed" || events[4].Subagent.Summary != "Tool failed" {
		t.Fatalf("background lifecycle = %+v", events)
	}
}

func TestExecSubagents_CollabStatesPerThread(t *testing.T) {
	s := &codexSession{ctx: context.Background(), events: make(chan core.Event, 32)}
	for _, raw := range []string{
		`{"item":{"type":"collab_tool_call","tool":"spawn_agent","receiver_thread_ids":["a"],"prompt":"Search A","agents_states":{"a":{"status":"running"}}}}`,
		`{"item":{"type":"collab_tool_call","tool":"spawn_agent","receiver_thread_ids":["b"],"prompt":"Search B","agents_states":{"b":{"status":"running"}}}}`,
		`{"item":{"type":"collab_tool_call","tool":"wait","receiver_thread_ids":["a","b"],"agents_states":{"a":{"status":"completed","message":"A report"},"b":{"status":"errored","message":"B failed"}}}}`,
	} {
		var item map[string]any
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			t.Fatal(err)
		}
		s.handleItemCompleted(item)
	}
	events := subagentEvents(s.events)
	if len(events) != 4 {
		t.Fatalf("got %d events, want two cards started and finished: %+v", len(events), events)
	}
	finished := map[string]*core.SubagentEvent{}
	for _, e := range events {
		if e.Type != core.EventSubagent {
			t.Fatalf("non-subagent event: %+v", e)
		}
		if e.Subagent.Phase == core.SubagentFinished {
			finished[e.Subagent.ID] = e.Subagent
		}
	}
	if finished["a"] == nil || finished["a"].Summary != "A report" || finished["b"] == nil || finished["b"].Status != "failed" {
		t.Fatalf("finished cards = %+v", finished)
	}
}

func TestAppServerSubagents_CollabDoesNotFinishOnSpawnAndTracksNestedThreads(t *testing.T) {
	s := &appServerSession{ctx: context.Background(), events: make(chan core.Event, 64)}
	s.threadID.Store("parent")
	subagentNotification(s, "item/completed", `{"threadId":"parent","item":{"type":"collabAgentToolCall","tool":"spawnAgent","status":"completed","receiverThreadIds":["child"],"prompt":"Inspect","agentsStates":{"child":{"status":"running"}}}}`)
	subagentNotification(s, "thread/started", `{"thread":{"id":"grandchild","parentThreadId":"child","agentNickname":"Nested"}}`)
	subagentNotification(s, "item/completed", `{"threadId":"grandchild","item":{"type":"agentMessage","text":"Nested report"}}`)
	subagentNotification(s, "item/completed", `{"threadId":"parent","item":{"type":"collabAgentToolCall","tool":"wait","receiverThreadIds":["child"],"agentsStates":{"child":{"status":"completed","message":"Report"}}}}`)
	events := subagentEvents(s.events)
	if len(events) != 4 || events[0].Subagent.Phase != core.SubagentStarted || events[1].Subagent.ID != "grandchild" || events[2].Subagent.Step.Text != "Nested report" || events[3].Subagent.Summary != "Report" {
		t.Fatalf("collaboration events = %+v", events)
	}
}

func TestExecSubagents_StartedUpdatedAndWake(t *testing.T) {
	s := &codexSession{ctx: context.Background(), events: make(chan core.Event, 32)}
	for _, line := range []string{
		`{"type":"item.started","item":{"type":"collab_tool_call","tool":"spawn_agent","receiver_thread_ids":["child"],"prompt":"Task"}}`,
		`{"type":"item.updated","item":{"type":"collab_tool_call","tool":"wait","receiver_thread_ids":["child"],"agents_states":{"child":{"status":"completed","message":"Report"}}}}`,
		`{"type":"item.completed","item":{"type":"collab_tool_call","tool":"wait","receiver_thread_ids":["child"],"agents_states":{"child":{"status":"completed","message":"Report"}}}}`,
		`{"type":"item.completed","item":{"type":"collab_tool_call","tool":"send_input","receiver_thread_ids":["child"],"prompt":"More work","agents_states":{"child":{"status":"running"}}}}`,
	} {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		s.handleEvent(event)
	}
	events := subagentEvents(s.events)
	if len(events) != 3 || events[0].Subagent.Prompt != "Task" || events[1].Subagent.Summary != "Report" || events[2].Subagent.Phase != core.SubagentStarted || events[2].Subagent.Prompt != "More work" {
		t.Fatalf("exec lifecycle = %+v", events)
	}
}

func TestAppServerSubagents_CollaborationFlushesParentCommentary(t *testing.T) {
	for _, tc := range []struct{ name, item string }{
		{"collabAgentToolCall", `{"type":"collabAgentToolCall","tool":"spawnAgent","receiverThreadIds":["child"],"prompt":"Inspect"}`},
		{"subAgentActivity", `{"type":"subAgentActivity","kind":"started","agentThreadId":"child","agentPath":"/scout"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &appServerSession{ctx: context.Background(), events: make(chan core.Event, 32), currentTurn: "parent-turn"}
			s.threadID.Store("parent")
			subagentNotification(s, "item/completed", `{"threadId":"parent","item":{"type":"agentMessage","text":"I will delegate"}}`)
			subagentNotification(s, "item/started", `{"threadId":"parent","item":`+tc.item+`}`)
			subagentNotification(s, "item/completed", `{"threadId":"parent","item":{"type":"agentMessage","text":"Final answer"}}`)
			subagentNotification(s, "turn/completed", `{"threadId":"parent","turn":{"id":"parent-turn","status":"completed"}}`)
			events := subagentEvents(s.events)
			if len(events) != 4 || events[0].Type != core.EventThinking || events[0].Content != "I will delegate" || events[1].Type != core.EventSubagent || events[2].Type != core.EventText || events[2].Content != "Final answer" || events[3].Type != core.EventResult {
				t.Fatalf("parent commentary must stay out of the answer: %+v", events)
			}
		})
	}
}

func TestAppServerSubagents_IdleBeforeAuthoritativeCompletion(t *testing.T) {
	for _, status := range []string{"completed", "interrupted", "failed"} {
		t.Run(status, func(t *testing.T) {
			s := &appServerSession{ctx: context.Background(), events: make(chan core.Event, 32)}
			s.threadID.Store("parent")
			subagentNotification(s, "thread/started", `{"thread":{"id":"child","parentThreadId":"parent"}}`)
			subagentNotification(s, "thread/status/changed", `{"threadId":"child","status":{"type":"idle"}}`)
			if events := subagentEvents(s.events); len(events) != 1 || events[0].Subagent.Phase != core.SubagentStarted {
				t.Fatalf("idle must not finalize a child before its actual result: %+v", events)
			}
			turn := map[string]any{"id": "child-turn", "status": status}
			if status == "failed" {
				turn["error"] = map[string]any{"message": "Expected failure"}
			}
			raw, err := json.Marshal(map[string]any{"threadId": "child", "turn": turn})
			if err != nil {
				t.Fatal(err)
			}
			s.handleNotification("turn/completed", raw)
			events := subagentEvents(s.events)
			if len(events) != 1 || events[0].Type != core.EventSubagent || events[0].Subagent.Phase != core.SubagentFinished || events[0].Subagent.Status != status {
				t.Fatalf("child must finish with authoritative status %q: %+v", status, events)
			}
			if status == "failed" && events[0].Subagent.Summary != "Expected failure" {
				t.Fatalf("failure summary = %q", events[0].Subagent.Summary)
			}
		})
	}
}
