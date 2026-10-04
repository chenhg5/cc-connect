package codex

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func TestManagedUsageNoticesMergeSparseDeduplicateAndRecover(t *testing.T) {
	s := &managedSession{ctx: context.Background(), events: make(chan core.Event, 20), decoder: &appServerSession{}}
	updates := []string{
		`{"rateLimits":{"limitId":"model-quota","normalModelSlug":"test-model","primary":{"usedPercent":100,"resetsAt":2000000000,"windowDurationMins":300},"rateLimitReachedType":"rate_limit_reached"}}`,
		`{"rateLimits":{"limitId":"model-quota","primary":null,"normalModelSlug":null}}`,
		`{"rateLimits":{"limitId":"model-quota","primary":{"usedPercent":101,"resetsAt":2000000000,"windowDurationMins":300}}}`,
		`{"rateLimits":{"limitId":"model-quota","primary":{"usedPercent":5,"resetsAt":2000000000,"windowDurationMins":300}}}`,
		`{"rateLimits":{"limitId":"model-quota","primary":{"usedPercent":100,"resetsAt":2000000000,"windowDurationMins":300}}}`,
	}
	for _, update := range updates {
		if err := s.handleUsageUpdate(json.RawMessage(update)); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.events) != 2 {
		t.Fatalf("alerts should be once per exhaustion: %d", len(s.events))
	}
	event := <-s.events
	if event.Type != core.EventNotice || event.Notice.Usage.Buckets[0].Name != "test-model" || event.Notice.Usage.Buckets[0].Windows[0].ResetAtUnix != 2000000000 {
		t.Fatalf("lost scope/reset: %+v", event)
	}
	if !s.decoder.cachedUsage().Buckets[0].LimitReached {
		t.Fatal("lost cached quota")
	}
}

func TestManagedSpendControlNoticeWithoutQuotaWindows(t *testing.T) {
	s := &managedSession{ctx: context.Background(), events: make(chan core.Event, 4), decoder: &appServerSession{}}
	for _, update := range []string{
		`{"rateLimits":{"limitId":"spend","spendControlReached":true,"rateLimitReachedType":"workspace_member_usage_limit_reached"}}`,
		`{"rateLimits":{"limitId":"spend","spendControlReached":null}}`,
	} {
		if err := s.handleUsageUpdate(json.RawMessage(update)); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.events) != 1 {
		t.Fatalf("bad sparse spend notice: %d", len(s.events))
	}
	event := <-s.events
	if event.Notice.Code != "workspace_member_usage_limit_reached" || !event.Notice.Usage.Buckets[0].LimitReached {
		t.Fatalf("lost explicit limit: %+v", event.Notice)
	}
}

func TestManagedDaemonWarningsAndRetryNoticesDoNotCompleteTurn(t *testing.T) {
	cwd := t.TempDir()
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method != "thread/resume" {
				t.Errorf("unexpected RPC: %s", m.Method)
				return nil, false
			}
			_ = ws.WriteJSON(map[string]any{"id": m.ID, "result": fixtureSnapshot(cwd, "target", "turn")})
			notifications := []struct{ method, params string }{
				{"warning", `{"message":"GLOBAL WARNING"}`},
				{"warning", `{"threadId":"other","message":"OTHER THREAD WARNING"}`},
				{"error", `{"threadId":"target","turnId":"turn","willRetry":true,"error":{"message":"LIMIT RETRY"}}`},
				{"error", `{"threadId":"target","turnId":"turn","willRetry":true,"error":{"message":"LIMIT RETRY"}}`},
				{"model/rerouted", `{"threadId":"target","turnId":"turn","fromModel":"old","toModel":"new","reason":"highRiskCyberActivity"}`},
				{"item/completed", `{"threadId":"target","turnId":"turn","item":{"id":"text","type":"agentMessage","text":"TASK STILL RUNNING","phase":"commentary"}}`},
			}
			for _, n := range notifications {
				_ = ws.WriteJSON(map[string]any{"method": n.method, "params": json.RawMessage(n.params)})
			}
			return nil, false
		})
	})
	as, err := fixtureManagedAgent(t, socket, cwd, nil).AttachSession(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	for _, kind := range []string{"warning", "retry", "model_rerouted"} {
		event := awaitManagedEvent(t, as, core.EventNotice)
		if event.Notice.Kind != kind {
			t.Fatalf("unexpected notice: %+v", event.Notice)
		}
	}
	event := awaitManagedEvent(t, as, core.EventText)
	if event.Content != "TASK STILL RUNNING" || as.(core.SharedAgentSession).RuntimeState().TurnID != "turn" {
		t.Fatal("notice ended task")
	}
}

func TestManagedUsageWarningThresholdsDoNotSpam(t *testing.T) {
	s := &managedSession{ctx: context.Background(), events: make(chan core.Event, 20), decoder: &appServerSession{}}
	for _, percent := range []int{20, 50, 51, 75, 76, 90, 91, 95, 96, 91, 96, 100, 100} {
		raw, _ := json.Marshal(map[string]any{"rateLimits": map[string]any{"limitId": "codex", "primary": map[string]any{"usedPercent": percent, "resetsAt": 2000000000, "windowDurationMins": 300}}})
		if err := s.handleUsageUpdate(raw); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.events) != 5 {
		t.Fatalf("want four warnings and one exhaustion, got %d", len(s.events))
	}
	for _, threshold := range []int{50, 75, 90, 95} {
		event := <-s.events
		if event.Notice.Kind != "usage_warning" || event.Notice.UsageThreshold != threshold {
			t.Fatalf("wrong warning: %+v", event.Notice)
		}
	}
}
