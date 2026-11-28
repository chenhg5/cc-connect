package claudecode

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestConfirmedQuotaFailureUsesControlCodeRatherThanTextOrHTTP429(t *testing.T) {
	const secret = "synthetic-secret"
	rejected := `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":100,"errorCode":"credits_required","unknown":"` + secret + `"}}`

	t.Run("confirmed", func(t *testing.T) {
		cs := newWindowTestSession(t, "")
		cs.handleReadLoopLine(rejected)
		cs.handleReadLoopLine(`{"type":"result","subtype":"success","is_error":true,"api_error":"usage_limit_reached","api_error_status":429,"result":"You've hit your limit: ` + secret + `"}`)
		ev := <-cs.events
		if ev.Type != core.EventError || ev.Error == nil || ev.Error.Error() != "quota exhausted" {
			t.Fatalf("confirmed event = %+v", ev)
		}
		if ev.Content != "" || strings.Contains(ev.Error.Error(), secret) {
			t.Fatalf("provider text leaked: %+v", ev)
		}
		if ev.Metadata["quota_exhausted"] != true || ev.Metadata["quota_resets_at"] != int64(100) || ev.Metadata["quota_credits_required"] != true {
			t.Fatalf("metadata = %+v", ev.Metadata)
		}
		for _, v := range ev.Metadata {
			if s, ok := v.(string); ok && strings.Contains(s, secret) {
				t.Fatalf("metadata leaked unknown field: %+v", ev.Metadata)
			}
		}

		// The rate-limit observation belongs to the turn that consumed it.
		cs.handleReadLoopLine(`{"type":"result","subtype":"success","is_error":true,"api_error":"usage_limit_reached","result":"again"}`)
		ev = <-cs.events
		if ev.Type != core.EventError || ev.Metadata["quota_resets_at"] != int64(0) || ev.Metadata["quota_credits_required"] != false {
			t.Fatalf("stale reset observation carried over: %+v", ev)
		}
	})

	for _, tc := range []struct{ name, result string }{
		{"generic_http_429", `{"type":"result","subtype":"success","is_error":true,"api_error":"rate_limit","api_error_status":429,"result":"rate limit"}`},
		{"no_typed_code", `{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"You've hit your limit"}`},
		{"code_without_error", `{"type":"result","subtype":"success","is_error":false,"api_error":"usage_limit_reached","result":"done"}`},
		{"code_with_other_status", `{"type":"result","subtype":"success","is_error":true,"api_error":"usage_limit_reached","api_error_status":500,"result":"oops"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := newWindowTestSession(t, "")
			cs.handleReadLoopLine(rejected)
			cs.handleReadLoopLine(tc.result)
			ev := <-cs.events
			if ev.Type != core.EventResult || ev.Metadata["quota_exhausted"] == true {
				t.Fatalf("classified as exhausted: %+v", ev)
			}
		})
	}

	t.Run("allowed_snapshot_clears_rejection", func(t *testing.T) {
		cs := newWindowTestSession(t, "")
		cs.handleReadLoopLine(rejected)
		cs.handleReadLoopLine(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`)
		cs.handleReadLoopLine(`{"type":"result","subtype":"success","is_error":true,"api_error":"usage_limit_reached","result":"x"}`)
		ev := <-cs.events
		if ev.Metadata["quota_resets_at"] != int64(0) || ev.Metadata["quota_credits_required"] != false {
			t.Fatalf("allowed snapshot did not clear rejection: %+v", ev.Metadata)
		}
	})
}
