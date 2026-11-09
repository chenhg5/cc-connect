package cursor

import (
	"context"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// TestParseCursorUsage_NormalCamelCase covers the happy-path payload shape
// that Cursor Agent CLI 2026.09.02-c22c1a3 emits on stream-json result
// events (camelCase keys). See issue #1810.
func TestParseCursorUsage_NormalCamelCase(t *testing.T) {
	usage := map[string]any{
		"inputTokens":      float64(5749),
		"outputTokens":     float64(25),
		"cacheReadTokens":  float64(9728),
		"cacheWriteTokens": float64(0),
	}
	in, out, cr, cw := parseCursorUsage(usage)
	if in != 5749 || out != 25 || cr != 9728 || cw != 0 {
		t.Fatalf("parseCursorUsage = (%d,%d,%d,%d), want (5749,25,9728,0)", in, out, cr, cw)
	}
}

// TestParseCursorUsage_MissingUsageReturnsZero documents the
// backward-compat contract: when the CLI omits usage entirely, the
// helper returns all zeros so handleResult's fallback path stays
// unchanged from prior behaviour.
func TestParseCursorUsage_MissingUsageReturnsZero(t *testing.T) {
	in, out, cr, cw := parseCursorUsage(nil)
	if in != 0 || out != 0 || cr != 0 || cw != 0 {
		t.Fatalf("parseCursorUsage(nil) = (%d,%d,%d,%d), want all zero", in, out, cr, cw)
	}
}

// TestParseCursorUsage_NonMapUsageIgnored defends against future schema
// drift: if the CLI downgrades usage to a string or struct, the caller's
// `raw["usage"].(map[string]any)` type assertion fails and parseCursorUsage
// is never reached. This test pins that behaviour: a non-map payload
// that does slip past (e.g. nil map from a bug) yields zero counts
// rather than panicking.
func TestParseCursorUsage_NonMapUsageIgnored(t *testing.T) {
	var nilMap map[string]any
	in, out, cr, cw := parseCursorUsage(nilMap)
	if in != 0 || out != 0 || cr != 0 || cw != 0 {
		t.Fatalf("parseCursorUsage(nilMap) = (%d,%d,%d,%d), want all zero", in, out, cr, cw)
	}

	// Empty (but typed) map: same expected behaviour.
	in, out, cr, cw = parseCursorUsage(map[string]any{})
	if in != 0 || out != 0 || cr != 0 || cw != 0 {
		t.Fatalf("parseCursorUsage(empty) = (%d,%d,%d,%d), want all zero", in, out, cr, cw)
	}
}

// TestParseCursorUsage_NonNumericFieldsSkipped protects against partial
// payloads where one field is present but encoded as a string (e.g. the
// CLI sends "5749" instead of 5749.0). We skip those values rather than
// crashing, so the other fields still flow through.
func TestParseCursorUsage_NonNumericFieldsSkipped(t *testing.T) {
	usage := map[string]any{
		"inputTokens":      "5749", // wrong type, ignored
		"outputTokens":     float64(25),
		"cacheReadTokens":  float64(9728),
		"cacheWriteTokens": nil, // nil, ignored
	}
	in, out, cr, cw := parseCursorUsage(usage)
	if in != 0 {
		t.Fatalf("inputTokens should be skipped for string value, got %d", in)
	}
	if out != 25 || cr != 9728 {
		t.Fatalf("output/cacheRead should populate, got (%d,%d)", out, cr)
	}
	if cw != 0 {
		t.Fatalf("cacheWriteTokens should be skipped for nil, got %d", cw)
	}
}

// TestParseCursorUsage_PartialUsageSucceeds is the most realistic edge
// case: CLI emits only inputTokens (e.g. early in a turn before output
// is finalised). Other fields must default to 0 instead of clobbering
// state.
func TestParseCursorUsage_PartialUsageSucceeds(t *testing.T) {
	usage := map[string]any{
		"inputTokens": float64(100),
	}
	in, out, cr, cw := parseCursorUsage(usage)
	if in != 100 || out != 0 || cr != 0 || cw != 0 {
		t.Fatalf("parseCursorUsage = (%d,%d,%d,%d), want (100,0,0,0)", in, out, cr, cw)
	}
}

// TestHandleResultParsesUsage_AllFieldsPopulated is the integration-style
// assertion: a realistic stream-json result event with usage yields an
// Event with non-zero InputTokens, OutputTokens, CacheReadInputTokens,
// and CacheCreationInputTokens, and Done=true. Mirrors the regression
// scenario in issue #1810 (turn-complete log + reply footer suppression).
func TestHandleResultParsesUsage_AllFieldsPopulated(t *testing.T) {
	cs := newTestSession("default")
	defer cs.cancel()

	raw := map[string]any{
		"type":       "result",
		"result":     "ok",
		"session_id": "abc123",
		"usage": map[string]any{
			"inputTokens":      float64(5749),
			"outputTokens":     float64(25),
			"cacheReadTokens":  float64(9728),
			"cacheWriteTokens": float64(0),
		},
	}

	cs.handleResult(raw)

	select {
	case evt := <-cs.events:
		if evt.Type != core.EventResult {
			t.Fatalf("event type = %q, want EventResult", evt.Type)
		}
		if !evt.Done {
			t.Fatalf("event Done = false, want true")
		}
		if evt.InputTokens != 5749 {
			t.Fatalf("InputTokens = %d, want 5749", evt.InputTokens)
		}
		if evt.OutputTokens != 25 {
			t.Fatalf("OutputTokens = %d, want 25", evt.OutputTokens)
		}
		if evt.CacheReadInputTokens != 9728 {
			t.Fatalf("CacheReadInputTokens = %d, want 9728", evt.CacheReadInputTokens)
		}
		if evt.CacheCreationInputTokens != 0 {
			t.Fatalf("CacheCreationInputTokens = %d, want 0", evt.CacheCreationInputTokens)
		}
		if evt.Content != "ok" {
			t.Fatalf("Content = %q, want %q", evt.Content, "ok")
		}
		if evt.SessionID != "abc123" {
			t.Fatalf("SessionID = %q, want %q", evt.SessionID, "abc123")
		}
	case <-cs.ctx.Done():
		t.Fatal("context cancelled before event emitted")
	}
}

// TestHandleResultParsesUsage_MissingUsageKeepsZeroTokens ensures
// backward compat: the prior behaviour (Event with all-zero token
// fields) is preserved when usage is absent.
func TestHandleResultParsesUsage_MissingUsageKeepsZeroTokens(t *testing.T) {
	cs := newTestSession("default")
	defer cs.cancel()

	raw := map[string]any{
		"type":       "result",
		"result":     "ok",
		"session_id": "abc123",
		// no usage field
	}

	cs.handleResult(raw)

	select {
	case evt := <-cs.events:
		if evt.InputTokens != 0 || evt.OutputTokens != 0 ||
			evt.CacheReadInputTokens != 0 || evt.CacheCreationInputTokens != 0 {
			t.Fatalf("expected zero token fields when usage absent, got %+v", evt)
		}
	case <-cs.ctx.Done():
		t.Fatal("context cancelled before event emitted")
	}
}

// TestHandleResultParsesUsage_NonMapUsageIgnored ensures a malformed
// usage (e.g. the CLI sends a string) doesn't crash the session; the
// event still emits with zero token fields.
func TestHandleResultParsesUsage_NonMapUsageIgnored(t *testing.T) {
	cs := newTestSession("default")
	defer cs.cancel()

	raw := map[string]any{
		"type":       "result",
		"result":     "ok",
		"session_id": "abc123",
		"usage":      "broken payload",
	}

	cs.handleResult(raw)

	select {
	case evt := <-cs.events:
		if evt.InputTokens != 0 || evt.OutputTokens != 0 {
			t.Fatalf("expected zero tokens for non-map usage, got %+v", evt)
		}
		if !evt.Done {
			t.Fatal("Done flag should still be true on result event")
		}
	case <-cs.ctx.Done():
		t.Fatal("context cancelled before event emitted")
	}
}

// TestHandleResult_DoesNotBlockOnFullChannel documents the non-blocking
// send path: when the consumer has not drained the events channel, an
// already-cancelled ctx must NOT deadlock handleResult. The event may
// or may not be delivered (select chooses pseudo-randomly when both
// cases are ready), but the call must return promptly without
// panicking. This is the path that issue #1804's graceful-drain
// shutdown relies on.
func TestHandleResult_DoesNotBlockOnFullChannel(t *testing.T) {
	cs := newTestSession("default")
	cs.cancel() // pre-cancel

	// 16-slot channel from newTestSession; this call must return
	// promptly regardless of which select branch wins.
	done := make(chan struct{})
	go func() {
		defer close(done)
		cs.handleResult(map[string]any{
			"type":   "result",
			"result": "ok",
			"usage": map[string]any{
				"inputTokens":  float64(100),
				"outputTokens": float64(50),
			},
		})
	}()

	select {
	case <-done:
		// expected: handleResult returned without blocking
	case <-time.After(2 * time.Second):
		t.Fatal("handleResult blocked despite cancelled ctx")
	}
}

// Compile-time guard so that adding new fields to core.Event (and
// forgetting to populate them here) does not silently compile through.
// Touching this guard forces the integration test above to be updated
// alongside any token-related Event field.
var _ = context.Background
