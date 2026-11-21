package claudecode

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// The explicit success and HTTP-error shapes match Claude Code stream-json.
// In particular an API failure can have subtype=success and is_error=true.
const sdkResultSuccess = `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"Hello from the local fixture.","session_id":"sdk-result-fixture","usage":{"input_tokens":3,"output_tokens":5}}`
const sdkResultHTTPError = `{"type":"result","subtype":"success","is_error":true,"api_error_status":403,"num_turns":1,"result":"API Error: 403 (closed local fixture)","session_id":"sdk-result-fixture","usage":{"input_tokens":3,"output_tokens":5}}`

func newSDKResultSession(t *testing.T) *claudeSession {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &claudeSession{ctx: ctx, cancel: cancel, events: make(chan core.Event, 8), workDir: home}
	s.sessionID.Store("sdk-result-fixture")
	s.alive.Store(true)
	return s
}

func TestSDKTerminalResultRawContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		kind core.EventType
		done bool
	}{
		{"http_error_despite_success_subtype", sdkResultHTTPError, core.EventError, true},
		{"explicit_error_subtype_without_flag", `{"type":"result","subtype":"error_max_turns","result":"limit","usage":{"input_tokens":3,"output_tokens":5}}`, core.EventError, true},
		{"error_subtype_overrides_false_flag", `{"type":"result","subtype":"error_during_execution","is_error":false,"result":"failed","usage":{"input_tokens":3,"output_tokens":5}}`, core.EventError, true},
		{"success", sdkResultSuccess, core.EventResult, true},
		{"missing_flag", `{"type":"result","subtype":"success","result":"legacy","usage":{"input_tokens":3,"output_tokens":5}}`, core.EventResult, true},
		{"invalid_flag", `{"type":"result","subtype":"success","is_error":"false","result":"legacy","usage":{"input_tokens":3,"output_tokens":5}}`, core.EventResult, true},
		{"unknown_subtype", `{"type":"result","subtype":"future","is_error":false,"result":"future","usage":{"input_tokens":3,"output_tokens":5}}`, core.EventResult, true},
		{"compact", `{"type":"result","subtype":"compact","usage":{"input_tokens":3,"output_tokens":5}}`, core.EventResult, false},
		{"compaction", `{"type":"result","subtype":"compaction","usage":{"input_tokens":3,"output_tokens":5}}`, core.EventResult, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSDKResultSession(t)
			s.handleReadLoopLine(tc.raw)
			select {
			case ev := <-s.events:
				if ev.Type != tc.kind || ev.Done != tc.done {
					t.Fatalf("event type=%s done=%v, want %s/%v", ev.Type, ev.Done, tc.kind, tc.done)
				}
				if (ev.Error != nil) != (tc.kind == core.EventError) {
					t.Fatalf("error presence=%v for %s", ev.Error != nil, tc.kind)
				}
				wantStatus := core.ResultStatusUnknown
				if tc.kind == core.EventError {
					wantStatus = core.ResultStatusError
				} else if tc.name == "success" {
					wantStatus = core.ResultStatusSuccess
				}
				if ev.ResultStatus != wantStatus {
					t.Fatalf("explicit status=%q want %q", ev.ResultStatus, wantStatus)
				}
				if ev.InputTokens != 3 || ev.OutputTokens != 5 {
					t.Fatalf("terminal accounting lost: input=%d output=%d", ev.InputTokens, ev.OutputTokens)
				}
			case <-time.After(time.Second):
				t.Fatal("SDK result did not end or advance its stream")
			}
		})
	}
}
