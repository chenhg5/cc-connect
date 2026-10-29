package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// newWindowTestSession returns a session with just enough state for
// handleAssistant / handleResult. workDir stays empty, so no transcript is
// read unless the test sets transcriptOverride.
func newWindowTestSession(t *testing.T, model string) *claudeSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cs := &claudeSession{events: make(chan core.Event, 64), ctx: ctx}
	cs.sessionID.Store("window-sess")
	cs.alive.Store(true)
	if model != "" {
		cs.activeModel.Store(model)
	}
	return cs
}

func windowAssistantEvent(input, cacheCreation, cacheRead int) map[string]any {
	return map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{},
			"usage": map[string]any{
				"input_tokens":                float64(input),
				"output_tokens":               float64(1),
				"cache_creation_input_tokens": float64(cacheCreation),
				"cache_read_input_tokens":     float64(cacheRead),
			},
		},
	}
}

// windowResultEvent mirrors the result event of `claude -p --output-format
// stream-json`. modelUsage is omitted when nil, as older CLIs do.
func windowResultEvent(modelUsage map[string]any) map[string]any {
	raw := map[string]any{
		"type":       "result",
		"subtype":    "success",
		"result":     "done",
		"session_id": "window-sess",
		"usage": map[string]any{
			"input_tokens":                float64(4),
			"output_tokens":               float64(203),
			"cache_creation_input_tokens": float64(45208),
			"cache_read_input_tokens":     float64(74277),
		},
	}
	if modelUsage != nil {
		raw["modelUsage"] = modelUsage
	}
	return raw
}

// modelUsageEntry is one value of the modelUsage map, trimmed to the fields
// that matter here. The token counts let a test tell "the session model's
// entry" apart from "the entry with the biggest prompt".
func modelUsageEntry(window, cacheCreation int) map[string]any {
	return map[string]any{
		"inputTokens":              float64(4),
		"outputTokens":             float64(203),
		"cacheReadInputTokens":     float64(0),
		"cacheCreationInputTokens": float64(cacheCreation),
		"contextWindow":            float64(window),
		"maxOutputTokens":          float64(128000),
	}
}

// TestHandleResultUsesReportedContextWindow: claude-opus-5-5 has a native 1M
// window and no "[1m]" suffix, so the model-name heuristic says 200k. The
// prompt size is from a live session, where 293,640 tokens rendered as
// "ctx 100%" (147% of 200k, clamped) instead of 29%.
//
// The turn also used a subagent model, as any turn with an Agent tool call
// does. That entry is given a different window and a bigger prompt here so
// the assertion can tell which entry was used.
func TestHandleResultUsesReportedContextWindow(t *testing.T) {
	cs := newWindowTestSession(t, "claude-opus-5-5")

	cs.handleAssistant(windowAssistantEvent(2, 695, 292_943))
	if u := cs.GetContextUsage(); u == nil || u.ContextWindow != 200_000 {
		t.Fatalf("before any result: usage = %+v, want ContextWindow 200000 (name heuristic)", u)
	}

	cs.handleResult(windowResultEvent(map[string]any{
		"claude-opus-5-5":  modelUsageEntry(1_000_000, 45_208),
		"claude-haiku-5-5": modelUsageEntry(200_000, 352_131),
	}))
	u := cs.GetContextUsage()
	if u.ContextWindow != 1_000_000 {
		t.Fatalf("after result: ContextWindow = %d, want 1000000 (modelUsage[claude-opus-5-5])", u.ContextWindow)
	}
	if pct := u.UsedTokens * 100 / u.ContextWindow; pct != 29 {
		t.Errorf("ctx = %d%%, want 29%% (%d of 1M)", pct, u.UsedTokens)
	}

	// Next turn, before its result arrives: only the cached value can say 1M.
	cs.handleAssistant(windowAssistantEvent(2, 1_200, 294_100))
	if u := cs.GetContextUsage(); u.ContextWindow != 1_000_000 {
		t.Errorf("next assistant event: ContextWindow = %d, want the cached 1000000", u.ContextWindow)
	}
}

// TestClaudeModelUsageContextWindow pins which modelUsage entry is the
// session's. Each case runs repeatedly because a selection that depends on
// map iteration order passes about half the time.
func TestClaudeModelUsageContextWindow(t *testing.T) {
	cases := []struct {
		name       string
		modelUsage any // nil = field absent
		model      string
		want       int
	}{
		{"no modelUsage (older CLI)", nil, "claude-opus-5-5", 0},
		{"empty map", map[string]any{}, "claude-opus-5-5", 0},
		{
			"session model next to a subagent model with a bigger prompt",
			map[string]any{
				"claude-opus-5-5":  modelUsageEntry(1_000_000, 45_208),
				"claude-haiku-5-5": modelUsageEntry(200_000, 352_131),
			},
			"claude-opus-5-5", 1_000_000,
		},
		{
			"subagent model has the larger window",
			map[string]any{
				"claude-sonnet-4-6": modelUsageEntry(200_000, 45_208),
				"claude-haiku-5-5":  modelUsageEntry(1_000_000, 1_000),
			},
			"claude-sonnet-4-6", 200_000,
		},
		{
			"[1m] id is the key, as the init event reports it",
			map[string]any{"claude-sonnet-5-5[1m]": modelUsageEntry(1_000_000, 100)},
			"claude-sonnet-5-5[1m]", 1_000_000,
		},
		{
			"key differs only in case",
			map[string]any{"Claude-Opus-5-5": modelUsageEntry(1_000_000, 100)},
			"claude-opus-5-5", 1_000_000,
		},
		{
			"session model absent: a lone helper entry is not the session's",
			map[string]any{"claude-haiku-5-5": modelUsageEntry(200_000, 100)},
			"claude-opus-5-5", 0,
		},
		{
			"session model unknown: a sole entry is taken",
			map[string]any{"claude-opus-5-5": modelUsageEntry(1_000_000, 100)},
			"", 1_000_000,
		},
		{
			"session model unknown: several entries are ambiguous",
			map[string]any{
				"claude-opus-5-5":  modelUsageEntry(1_000_000, 45_208),
				"claude-haiku-5-5": modelUsageEntry(200_000, 352_131),
			},
			"", 0,
		},
		{
			"zero window",
			map[string]any{"claude-opus-5-5": modelUsageEntry(0, 100)},
			"claude-opus-5-5", 0,
		},
		{
			"contextWindow missing",
			map[string]any{"claude-opus-5-5": map[string]any{"inputTokens": float64(4)}},
			"claude-opus-5-5", 0,
		},
		{
			"json.Number (UseNumber path)",
			map[string]any{"claude-opus-5-5": map[string]any{"contextWindow": json.Number("1000000")}},
			"claude-opus-5-5", 1_000_000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]any{"type": "result"}
			if tc.modelUsage != nil {
				raw["modelUsage"] = tc.modelUsage
			}
			for i := 0; i < 32; i++ {
				if got := claudeModelUsageContextWindow(raw, tc.model); got != tc.want {
					t.Fatalf("claudeModelUsageContextWindow(model=%q) = %d, want %d (call %d)", tc.model, got, tc.want, i+1)
				}
			}
		})
	}
}

// TestHandleResultWithoutReportedWindow: without a reported window the name
// heuristic stays in charge, and once a window has been reported a later
// result that does not repeat it must not undo it.
func TestHandleResultWithoutReportedWindow(t *testing.T) {
	t.Run("never reported: name heuristic", func(t *testing.T) {
		for model, want := range map[string]int{
			"claude-opus-5-5":     200_000,
			"claude-opus-4-7[1m]": 1_000_000,
		} {
			cs := newWindowTestSession(t, model)
			cs.handleAssistant(windowAssistantEvent(2, 695, 50_000))
			cs.handleResult(windowResultEvent(nil))
			if u := cs.GetContextUsage(); u.ContextWindow != want {
				t.Errorf("%s: ContextWindow = %d, want %d", model, u.ContextWindow, want)
			}
		}
	})

	t.Run("reported earlier: cached value survives", func(t *testing.T) {
		cs := newWindowTestSession(t, "claude-opus-5-5")
		cs.handleAssistant(windowAssistantEvent(2, 695, 50_000))
		cs.handleResult(windowResultEvent(map[string]any{"claude-opus-5-5": modelUsageEntry(1_000_000, 695)}))

		cs.handleAssistant(windowAssistantEvent(2, 700, 60_000))
		cs.handleResult(windowResultEvent(nil))
		if u := cs.GetContextUsage(); u.ContextWindow != 1_000_000 {
			t.Errorf("result without modelUsage: ContextWindow = %d, want the cached 1000000", u.ContextWindow)
		}

		cs.handleAssistant(windowAssistantEvent(2, 710, 70_000))
		cs.handleResult(windowResultEvent(map[string]any{"claude-haiku-5-5": modelUsageEntry(200_000, 710)}))
		if u := cs.GetContextUsage(); u.ContextWindow != 1_000_000 {
			t.Errorf("result listing only a helper model: ContextWindow = %d, want the cached 1000000", u.ContextWindow)
		}
	})
}

// TestContextWindowOverrideWinsOverReportedWindow: context_window_tokens is an
// explicit operator choice and outranks what the CLI reports.
func TestContextWindowOverrideWinsOverReportedWindow(t *testing.T) {
	cs := newWindowTestSession(t, "claude-opus-5-5")
	cs.ctxWindowOverride = 500_000

	cs.handleAssistant(windowAssistantEvent(2, 695, 292_943))
	cs.handleResult(windowResultEvent(map[string]any{"claude-opus-5-5": modelUsageEntry(1_000_000, 45_208)}))
	if u := cs.GetContextUsage(); u.ContextWindow != 500_000 {
		t.Errorf("after result: ContextWindow = %d, want the 500000 override", u.ContextWindow)
	}

	cs.handleAssistant(windowAssistantEvent(2, 700, 293_500))
	if u := cs.GetContextUsage(); u.ContextWindow != 500_000 {
		t.Errorf("next assistant event: ContextWindow = %d, want the 500000 override", u.ContextWindow)
	}
}

// TestTranscriptSnapshotUsesReportedContextWindow covers the snapshot read from
// the transcript tail, which is what a provider sending empty assistant usage
// blocks gets (see TestHandleResultUsesTranscriptTailWhenAssistantEmpty). It is
// built from the model id in the transcript line, and for a "[1m]" session that
// id has no suffix (measured: "claude-sonnet-5-5" in the transcript of a
// "claude-sonnet-5-5[1m]" session), so on its own it guesses 200k. It must take
// the reported window on the turn that reports it and on later turns whose
// result does not repeat it.
func TestTranscriptSnapshotUsesReportedContextWindow(t *testing.T) {
	path := writeFixture(t, []string{
		`{"type":"assistant","message":{"model":"claude-sonnet-5-5","usage":{"input_tokens":12,"cache_creation_input_tokens":800,"cache_read_input_tokens":240000}}}`,
	})
	cs := newWindowTestSession(t, "claude-sonnet-5-5[1m]")
	cs.transcriptOverride = path
	emptyUsage := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{},
			"usage":   map[string]any{"input_tokens": float64(0), "output_tokens": float64(0)},
		},
	}

	cs.handleAssistant(emptyUsage)
	cs.handleResult(windowResultEvent(map[string]any{"claude-sonnet-5-5[1m]": modelUsageEntry(1_000_000, 800)}))
	u := cs.GetContextUsage()
	if u == nil || cs.GetUsageSource() != "transcript" {
		t.Fatalf("precondition: want a transcript snapshot, got %+v (source %q)", u, cs.GetUsageSource())
	}
	if u.ContextWindow != 1_000_000 {
		t.Errorf("turn 1: ContextWindow = %d, want the reported 1000000", u.ContextWindow)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	_, err = f.WriteString(`{"type":"assistant","message":{"model":"claude-sonnet-5-5","usage":{"input_tokens":9,"cache_creation_input_tokens":500,"cache_read_input_tokens":260000}}}` + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("append fixture: %v", err)
	}

	cs.handleAssistant(emptyUsage)
	cs.handleResult(windowResultEvent(nil))
	u = cs.GetContextUsage()
	if want := 9 + 500 + 260_000; u.UsedTokens != want {
		t.Fatalf("precondition: turn 2 UsedTokens = %d, want %d from the new transcript line", u.UsedTokens, want)
	}
	if u.ContextWindow != 1_000_000 {
		t.Errorf("turn 2: ContextWindow = %d, want the cached 1000000", u.ContextWindow)
	}
}
