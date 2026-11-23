package claudecode

import (
	"github.com/chenhg5/cc-connect/core"
	"testing"
)

func TestConfirmedQuotaFailureUsesControlCodeRatherThanTextOrHTTP429(t *testing.T) {
	for _, code := range []string{"usage_limit_reached", ""} {
		cs := newWindowTestSession(t, "")
		cs.handleReadLoopLine(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":100,"unknown":"synthetic-secret"}}`)
		cs.handleResult(map[string]any{"is_error": true, "api_error": code, "api_error_status": float64(429), "result": "rate limit: synthetic-secret"})
		ev := <-cs.events
		if code == "" {
			if ev.Metadata["quota_exhausted"] == true {
				t.Fatal("generic HTTP429 classified as exhausted")
			}
		} else if ev.Type != core.EventError || ev.Metadata["quota_exhausted"] != true || ev.Error.Error() != "quota exhausted" {
			t.Fatalf("confirmed event = %+v", ev)
		}
	}
}
