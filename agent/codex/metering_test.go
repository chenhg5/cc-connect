package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestMeteredAppServerTurnIncludesDescendantsWithoutChangingParent(t *testing.T) {
	s := &appServerSession{ctx: context.Background(), events: make(chan core.Event, 8), currentTurn: "one"}
	s.threadID.Store("parent")
	s.turnTokens.newThread()
	s.turnTokens.beginTurn()
	root := t.TempDir()
	s.sidechain.begin(root, "parent", false)
	writeChildRollout(t, root, "child", "parent", 3000, 2000, 50)
	s.handleNotification("thread/tokenUsage/updated", json.RawMessage(`{"threadId":"parent","turnId":"one","tokenUsage":{"total":{"inputTokens":20000,"cachedInputTokens":12000,"outputTokens":100},"last":{"inputTokens":20000,"cachedInputTokens":12000,"outputTokens":100}}}`))
	s.completeTurn()
	event := <-s.events
	if event.InputTokens != 8000 || event.OutputTokens != 100 || event.CacheReadInputTokens != 12000 {
		t.Fatalf("parent usage: %+v", event)
	}
	usage := event.BillableUsage()
	if usage.InputTokens != 9000 || usage.OutputTokens != 150 || usage.CacheReadInputTokens != 14000 {
		t.Fatalf("family usage: %+v", usage)
	}
	s.currentTurn = "two"
	s.turnTokens.beginTurn()
	s.completeTurn()
	event = <-s.events
	if event.Sidechain != nil {
		t.Fatalf("child history billed twice: %+v", event)
	}
}
func TestMeteredAppServerResumeExcludesHistoricalTokens(t *testing.T) {
	s := &appServerSession{ctx: context.Background(), events: make(chan core.Event, 8), currentTurn: "one"}
	s.threadID.Store("parent")
	s.handleNotification("thread/tokenUsage/updated", json.RawMessage(`{"threadId":"parent","turnId":"one","tokenUsage":{"total":{"inputTokens":20000,"cachedInputTokens":12000,"outputTokens":100},"last":{"inputTokens":5000,"cachedInputTokens":3000,"outputTokens":20}}}`))
	s.completeTurn()
	event := <-s.events
	if event.InputTokens != 2000 || event.OutputTokens != 20 || event.CacheReadInputTokens != 3000 {
		t.Fatalf("history billed to resumed turn: %+v", event)
	}
}

func TestMeteredAppServerTransportLossReportsTurnUsage(t *testing.T) {
	s := newTransportTestSession(t)
	s.turnTokens.newThread()
	s.turnTokens.beginTurn()
	s.sidechain.begin(t.TempDir(), "thread-1", false)
	stream := `{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"inputTokens":20000,"cachedInputTokens":12000,"outputTokens":100},"last":{"inputTokens":20000,"cachedInputTokens":12000,"outputTokens":100}}}}` + "\n"
	s.wg.Add(1)
	s.readLoop(strings.NewReader(stream))
	var event core.Event
	for found := false; !found; {
		select {
		case event = <-s.events:
			found = event.Type == core.EventError
		default:
			t.Fatal("transport loss did not finish the turn")
		}
	}
	if !errors.Is(event.Error, io.EOF) {
		t.Fatalf("transport loss error = %v", event.Error)
	}
	if event.InputTokens != 8000 || event.OutputTokens != 100 || event.CacheReadInputTokens != 12000 {
		t.Fatalf("turn usage lost with the transport: %+v", event)
	}
}
