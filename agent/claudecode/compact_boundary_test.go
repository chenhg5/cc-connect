package claudecode

import (
	"reflect"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// compactBoundaryLine is the shape Claude Code 2.1.28x writes to stream-json
// when it compacts the conversation mid-turn (values are synthetic). The
// compaction is reported as a system message, never as a result subtype.
const compactBoundaryLine = `{"type":"system","subtype":"compact_boundary","session_id":"compact-sess","uuid":"6f1d2c3b-0000-4000-8000-000000000010","compact_metadata":{"trigger":"auto","pre_tokens":167012,"post_tokens":9120,"duration_ms":41000,"messages_summarized":88}}`

// TestCompactBoundary_EmitsEventCompactAndTurnContinues is the regression test
// for compaction detection that only ever looked at result subtypes: Claude
// Code's compact_boundary system message must surface as EventCompact with
// its metadata, must not end the turn, and the turn must still finish on the
// result event that follows.
func TestCompactBoundary_EmitsEventCompactAndTurnContinues(t *testing.T) {
	cs := newWindowTestSession(t, "claude-sonnet-4-6")

	for _, line := range []string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"before compaction"}]},"session_id":"compact-sess"}`,
		compactBoundaryLine,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"after compaction"}]},"session_id":"compact-sess"}`,
		`{"type":"result","subtype":"success","result":"after compaction","session_id":"compact-sess"}`,
	} {
		cs.handleReadLoopLine(line)
	}

	var compacts, results []core.Event
	var texts []string
	for len(cs.events) > 0 {
		evt := <-cs.events
		switch evt.Type {
		case core.EventCompact:
			if len(results) > 0 {
				t.Fatalf("EventCompact arrived after the turn's result")
			}
			compacts = append(compacts, evt)
		case core.EventResult:
			results = append(results, evt)
		case core.EventText:
			if evt.Content != "" {
				texts = append(texts, evt.Content)
			}
		}
	}

	if len(compacts) != 1 {
		t.Fatalf("EventCompact count = %d, want 1", len(compacts))
	}
	if compacts[0].Done {
		t.Fatalf("EventCompact Done = true; a compaction must not end the turn")
	}
	want := map[string]any{
		"trigger":     "auto",
		"pre_tokens":  float64(167012),
		"post_tokens": float64(9120),
		"duration_ms": float64(41000),
	}
	if !reflect.DeepEqual(compacts[0].Metadata, want) {
		t.Fatalf("EventCompact metadata = %v, want %v", compacts[0].Metadata, want)
	}

	if len(results) != 1 || !results[0].Done {
		t.Fatalf("results = %+v, want exactly one terminal EventResult", results)
	}
	if !reflect.DeepEqual(texts, []string{"before compaction", "after compaction"}) {
		t.Fatalf("texts = %q, want both sides of the compaction", texts)
	}
}

// TestCompactBoundary_MinimalMetadata covers older CLIs that only report the
// trigger and pre-compaction size, and a boundary without compact_metadata.
func TestCompactBoundary_MinimalMetadata(t *testing.T) {
	cases := []struct {
		name string
		line string
		want map[string]any
	}{
		{
			name: "trigger and pre_tokens only",
			line: `{"type":"system","subtype":"compact_boundary","session_id":"s","compact_metadata":{"trigger":"manual","pre_tokens":52000}}`,
			want: map[string]any{"trigger": "manual", "pre_tokens": float64(52000)},
		},
		{
			name: "no compact_metadata",
			line: `{"type":"system","subtype":"compact_boundary","session_id":"s"}`,
			want: map[string]any{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newWindowTestSession(t, "")
			cs.handleReadLoopLine(tc.line)

			var got []core.Event
			for len(cs.events) > 0 {
				if evt := <-cs.events; evt.Type == core.EventCompact {
					got = append(got, evt)
				}
			}
			if len(got) != 1 {
				t.Fatalf("EventCompact count = %d, want 1", len(got))
			}
			if !reflect.DeepEqual(got[0].Metadata, tc.want) {
				t.Fatalf("metadata = %v, want %v", got[0].Metadata, tc.want)
			}
		})
	}
}
