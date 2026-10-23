package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCompactProgressWriter_RecordsStableStepTimestamps(t *testing.T) {
	p := &previewCapturePlatform{}
	w := newCompactProgressWriter(context.Background(), p, progressHintReplyCtx{
		style: progressStyleCard, payload: true,
	}, "codex", LangEnglish, nil)
	w.maxEntries = 2
	before := time.Now()
	w.Append("first")
	w.Append("second")
	readTimes := func(content string) []time.Time {
		t.Helper()
		var payload struct {
			Items []struct {
				Timestamp time.Time `json:"timestamp"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(content, ProgressCardPayloadPrefix)), &payload); err != nil {
			t.Fatal(err)
		}
		var times []time.Time
		for _, item := range payload.Items {
			if item.Timestamp.Before(before) || item.Timestamp.After(time.Now()) {
				t.Fatalf("step timestamp %v is outside the event interval", item.Timestamp)
			}
			times = append(times, item.Timestamp)
		}
		return times
	}
	initial := readTimes(w.content)
	if len(initial) != 2 {
		t.Fatalf("got %d steps, want 2", len(initial))
	}
	w.Append("third")
	w.Finalize(ProgressCardStateCompleted)
	final := readTimes(p.updated[len(p.updated)-1])
	if len(final) != 2 || !final[0].Equal(initial[1]) {
		t.Fatalf("timestamp changed after truncation/finalization: %v -> %v", initial, final)
	}
}

func TestCompactProgressWriter_PreservesProvidedTimestamp(t *testing.T) {
	for _, payload := range []bool{true, false} {
		p := &previewCapturePlatform{}
		w := newCompactProgressWriter(context.Background(), p, progressHintReplyCtx{
			style: progressStyleCard, payload: payload,
		}, "codex", LangEnglish, nil)
		at := time.Date(2026, 10, 23, 14, 3, 7, 0, time.FixedZone("UTC+8", 8*60*60))
		if !w.AppendStructured(ProgressCardEntry{Text: "step", Timestamp: at}, "step") {
			t.Fatal("AppendStructured failed")
		}
		if payload {
			parsed, ok := ParseProgressCardPayload(w.content)
			if !ok || len(parsed.Items) != 1 || !parsed.Items[0].Timestamp.Equal(at) {
				t.Fatalf("timestamp lost in payload: %s", w.content)
			}
		} else if !strings.Contains(w.content, "14:03:07 · step") {
			t.Fatalf("plain card fallback is missing time: %s", w.content)
		}
	}
	content := BuildProgressCardPayloadV2([]ProgressCardEntry{{Text: "legacy"}}, false, "", LangEnglish, "")
	if strings.Contains(content, "timestamp") {
		t.Fatalf("zero timestamp should be omitted: %s", content)
	}
}

type timestampRichPlatform struct {
	stubCompactProgressPlatform
	rows [][]ToolStep
}

func (p *timestampRichPlatform) BuildRichCard(_ CardStatus, _ string, steps []ToolStep, _ string, _ bool, _ string) string {
	p.rows = append(p.rows, append([]ToolStep(nil), steps...))
	return "rich card"
}

func TestProcessInteractiveEvents_RecordsStableRichStepTimestamps(t *testing.T) {
	p := &timestampRichPlatform{}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{ThinkingMessages: true, ToolMessages: true, CardMode: "rich"})
	key := "test:timestamps"
	session := e.sessions.GetOrCreateActive(key)
	as := newControllableSession("timestamps")
	state := &interactiveState{agentSession: as, platform: p, replyCtx: "ctx"}
	e.interactiveStates[key] = state
	as.events <- Event{Type: EventThinking, Content: "planning"}
	as.events <- Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"}
	as.events <- Event{Type: EventToolResult, ToolName: "Bash", ToolResult: "ok"}
	as.events <- Event{Type: EventResult, Content: "done", Done: true}
	before := time.Now()
	e.processInteractiveEvents(state, session, e.sessions, key, "m", before, nil, nil, state.replyCtx, 0)
	if len(p.rows) < 3 || len(p.rows[0]) != 1 || len(p.rows[1]) != 2 {
		t.Fatalf("unexpected card updates: %+v", p.rows)
	}
	final := p.rows[len(p.rows)-1]
	for i, step := range final {
		if step.Timestamp.Before(before) || step.Timestamp.After(time.Now()) {
			t.Fatalf("step %d has invalid occurrence time: %v", i, step.Timestamp)
		}
		if !step.Timestamp.Equal(p.rows[i][i].Timestamp) {
			t.Fatalf("step %d occurrence time changed on refresh", i)
		}
	}
	if len(final) != 2 || !final[1].Done {
		t.Fatalf("tool result should update the original row: %+v", final)
	}
	orphan := mergeRichToolResult(nil, Event{ToolName: "Bash"}, "ok", 0)
	if len(orphan) != 1 || orphan[0].Timestamp.Before(before) {
		t.Fatalf("result without a tool-use row needs its own occurrence time: %+v", orphan)
	}
}
