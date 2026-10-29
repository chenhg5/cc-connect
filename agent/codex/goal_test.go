package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type goalRPCWriter struct{ writes chan []byte }

func (w *goalRPCWriter) Write(p []byte) (int, error) {
	w.writes <- append([]byte(nil), p...)
	return len(p), nil
}
func (*goalRPCWriter) Close() error { return nil }

func TestAppServerGoalNativeRPC(t *testing.T) {
	w := &goalRPCWriter{writes: make(chan []byte, 8)}
	s := &appServerSession{ctx: context.Background(), stdin: w, pending: make(map[int64]chan rpcResponseEnvelope), events: make(chan core.Event, 8)}
	s.threadID.Store("goal-thread")
	s.alive.Store(true)
	m, ok := any(s).(core.GoalManager)
	if !ok {
		t.Fatal("app-server session has no native GoalManager")
	}
	objective := "Finish the migration"
	budget := int64(40000)
	for _, tc := range []struct {
		method string
		params map[string]any
		run    func() error
		result string
	}{
		{"thread/goal/set", map[string]any{"threadId": "goal-thread", "origin": "user", "objective": objective, "status": "active", "tokenBudget": float64(40000)}, func() error {
			g, err := m.SetGoal(core.GoalUpdate{Objective: &objective, Status: "active", TokenBudget: &budget})
			if err == nil && (g == nil || g.Status != "active" || g.TokensUsed != 17) {
				t.Errorf("set returned %+v", g)
			}
			return err
		}, `{"goal":{"threadId":"goal-thread","objective":"Finish the migration","status":"active","tokenBudget":40000,"tokensUsed":17,"timeUsedSeconds":2}}`},
		{"thread/goal/get", map[string]any{"threadId": "goal-thread"}, func() error { _, err := m.GetGoal(); return err }, `{"goal":{"threadId":"goal-thread","objective":"Finish the migration","status":"active","tokensUsed":17}}`},
		{"thread/goal/set", map[string]any{"threadId": "goal-thread", "origin": "user", "tokenBudget": nil}, func() error { _, err := m.SetGoal(core.GoalUpdate{ClearTokenBudget: true}); return err }, `{"goal":{"threadId":"goal-thread","status":"active"}}`},
		{"thread/goal/clear", map[string]any{"threadId": "goal-thread", "origin": "user"}, m.ClearGoal, `{"cleared":true}`},
	} {
		t.Run(tc.method, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- tc.run() }()
			var req struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			select {
			case b := <-w.writes:
				if err := json.Unmarshal(b, &req); err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("no native Goal RPC")
			}
			want, _ := json.Marshal(tc.params)
			got, _ := json.Marshal(req.Params)
			if req.Method != tc.method || string(got) != string(want) {
				t.Errorf("request = %s %s, want %s %s", req.Method, got, tc.method, want)
			}
			s.handleResponse(rpcResponseEnvelope{ID: req.ID, Result: json.RawMessage(tc.result)})
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("RPC did not return")
			}
		})
	}
	if m.GoalActive() {
		t.Fatal("cleared goal is still active")
	}
}

func TestAppServerGoalNotifications(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8)}
	s.threadID.Store("parent")
	m, ok := any(s).(core.GoalManager)
	if !ok {
		t.Fatal("native goals unsupported")
	}
	for _, thread := range []string{"child", "parent"} {
		for _, used := range []int{1, 2, 3} {
			s.handleNotification("thread/goal/updated", json.RawMessage(fmt.Sprintf(`{"threadId":%q,"goal":{"threadId":%q,"objective":"Finish","status":"active","tokensUsed":%d}}`, thread, thread, used)))
		}
	}
	if len(s.events) != 1 || !m.GoalActive() {
		t.Fatalf("events=%d active=%v, want one parent transition", len(s.events), m.GoalActive())
	}
	s.handleNotification("thread/goal/updated", json.RawMessage(`{"threadId":"parent","goal":{"threadId":"parent","objective":"Finish","status":"budgetLimited","tokensUsed":100}}`))
	if len(s.events) != 2 || m.GoalActive() {
		t.Fatalf("terminal state events=%d active=%v", len(s.events), m.GoalActive())
	}
	s.handleNotification("thread/goal/cleared", json.RawMessage(`{"threadId":"child"}`))
	if len(s.events) != 2 {
		t.Fatal("child goal cleared parent")
	}
}

func TestAppServerGoalStopPausesBeforeInterrupt(t *testing.T) {
	w := &goalRPCWriter{writes: make(chan []byte, 4)}
	s := &appServerSession{ctx: context.Background(), stdin: w, pending: make(map[int64]chan rpcResponseEnvelope), events: make(chan core.Event, 8), currentTurn: "goal-turn"}
	s.threadID.Store("goal-thread")
	s.goal = &core.Goal{ThreadID: "goal-thread", Status: "active"}
	m, ok := any(s).(interface{ CancelTurn() error })
	if !ok {
		t.Fatal("native goal cannot be cancelled")
	}
	done := make(chan error, 1)
	go func() { done <- m.CancelTurn() }()
	for _, method := range []string{"thread/goal/set", "turn/interrupt"} {
		var req struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		select {
		case b := <-w.writes:
			if err := json.Unmarshal(b, &req); err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing %s", method)
		}
		if req.Method != method || req.Params["threadId"] != "goal-thread" {
			t.Fatalf("unexpected request %+v", req)
		}
		result := `{}`
		if method == "thread/goal/set" {
			if req.Params["status"] != "paused" || req.Params["origin"] != "user" {
				t.Fatalf("stop did not explicitly pause: %+v", req.Params)
			}
			result = `{"goal":{"threadId":"goal-thread","status":"paused"}}`
		} else if req.Params["turnId"] != "goal-turn" {
			t.Fatal("wrong interrupted turn")
		}
		s.handleResponse(rpcResponseEnvelope{ID: req.ID, Result: json.RawMessage(result)})
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop did not complete")
	}
	if s.GoalActive() {
		t.Fatal("stopped goal remained active")
	}
}

// The real Codex binary owns both persistence and the continuation loop. The
// local Responses fixture avoids inference accounts and external model traffic.
func TestAppServerGoalRealCLI(t *testing.T) {
	cli := os.Getenv("CODEX_GOALS_TEST_CLI")
	if cli == "" {
		t.Skip("set CODEX_GOALS_TEST_CLI to test the installed native Codex protocol")
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		n := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		item := map[string]any{"id": fmt.Sprintf("msg-%d", n), "type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "Another step toward the goal.", "annotations": []any{}}}}
		for _, event := range []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("resp-%d", n)}},
			{"type": "response.output_item.done", "output_index": 0, "item": item},
			{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("resp-%d", n), "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 100, "output_tokens": 1, "total_tokens": 101}}},
		} {
			b, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], b)
		}
	}))
	defer server.Close()
	home := t.TempDir()
	config := fmt.Sprintf("model = \"gpt-5.4\"\nmodel_provider = \"goal_fixture\"\n[model_providers.goal_fixture]\nname = \"Local Goal fixture\"\nbase_url = %q\nwire_api = \"responses\"\n", server.URL)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	start := func(id string) *appServerSession {
		s, err := newAppServerSession(ctx, cli, nil, "stdio://", home, "", "", "suggest", id, "", "", nil, home, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := start("")
	defer func() { _ = s.Close() }()
	objective := "Work toward the goal without using tools."
	budget := int64(320)
	g, err := s.SetGoal(core.GoalUpdate{Objective: &objective, Status: "paused", TokenBudget: &budget})
	if err != nil || g == nil || g.Status != "paused" {
		t.Fatalf("set paused = %+v, %v", g, err)
	}
	id := s.CurrentSessionID()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = start(id)
	g, err = s.GetGoal()
	if err != nil || g == nil || g.Objective != objective || g.Status != "paused" || g.TokenBudget == nil || *g.TokenBudget != budget {
		t.Fatalf("restored goal = %+v, %v", g, err)
	}
	_, err = s.SetGoal(core.GoalUpdate{Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(25 * time.Second)
	for {
		select {
		case event := <-s.Events():
			if event.Type == core.EventError {
				t.Fatalf("native goal failed: %s", event.Content)
			}
			if event.Type != core.EventGoal || event.Goal == nil || event.Goal.Status != "budgetLimited" {
				continue
			}
			if requests.Load() < 2 || event.Goal.TokensUsed < budget {
				t.Fatalf("no native continuation/accounting: requests=%d goal=%+v", requests.Load(), event.Goal)
			}
			if err := s.ClearGoal(); err != nil {
				t.Fatal(err)
			}
			g, err := s.GetGoal()
			if err != nil || g != nil {
				t.Fatalf("clear = %+v, %v", g, err)
			}
			return
		case <-deadline:
			t.Fatalf("native continuation did not reach budget limit, model requests=%d", requests.Load())
		}
	}
}
