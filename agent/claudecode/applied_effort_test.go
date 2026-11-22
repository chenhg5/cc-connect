package claudecode

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func settingsResponse(requestID, subtype string, applied map[string]any) map[string]any {
	return map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    subtype,
			"request_id": requestID,
			"response": map[string]any{
				"effective": map[string]any{
					"env": map[string]any{"ANTHROPIC_AUTH_TOKEN": "test-credential-must-not-leak"},
				},
				"applied": applied,
			},
		},
	}
}

func TestGetReasoningEffort_PrefersAppliedOverConfigured(t *testing.T) {
	cs := &claudeSession{configuredEffort: "medium", settingsRequestID: getSettingsRequestID}
	if got := cs.GetReasoningEffort(); got != "medium" {
		t.Fatalf("before get_settings: got %q, want configured %q", got, "medium")
	}
	cs.handleControlResponse(settingsResponse(getSettingsRequestID, "success", map[string]any{"effort": "high"}))
	if got := cs.GetReasoningEffort(); got != "high" {
		t.Fatalf("after get_settings: got %q, want applied %q", got, "high")
	}
}

func TestGetReasoningEffort_AppliedWithoutConfigured(t *testing.T) {
	cs := &claudeSession{settingsRequestID: getSettingsRequestID}
	if got := cs.GetReasoningEffort(); got != "" {
		t.Fatalf("got %q, want empty before any response", got)
	}
	cs.handleControlResponse(settingsResponse(getSettingsRequestID, "success", map[string]any{"effort": " xhigh "}))
	if got := cs.GetReasoningEffort(); got != "xhigh" {
		t.Fatalf("got %q, want %q", got, "xhigh")
	}
}

func TestHandleControlResponse_IgnoresOtherAndFailedResponses(t *testing.T) {
	cs := &claudeSession{configuredEffort: "low", settingsRequestID: getSettingsRequestID}
	cs.handleControlResponse(settingsResponse("some-other-id", "success", map[string]any{"effort": "max"}))
	cs.handleControlResponse(settingsResponse(getSettingsRequestID, "error", map[string]any{"effort": "max"}))
	cs.handleControlResponse(settingsResponse(getSettingsRequestID, "success", map[string]any{"effort": nil}))
	cs.handleControlResponse(map[string]any{"type": "control_response"})
	if got := cs.GetReasoningEffort(); got != "low" {
		t.Fatalf("got %q, want configured %q to survive ignored responses", got, "low")
	}
}

// The get_settings payload carries the effective env, including provider
// credentials; handling it must never write any of that to the log.
func TestControlResponse_DoesNotLogCredentials(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cs := &claudeSession{settingsRequestID: getSettingsRequestID}
	line := `{"type":"control_response","response":{"subtype":"success","request_id":"` + getSettingsRequestID +
		`","response":{"effective":{"env":{"ANTHROPIC_AUTH_TOKEN":"test-credential-must-not-leak"}},"applied":{"effort":"high"}}}}`
	cs.handleReadLoopLine(line)

	if strings.Contains(buf.String(), "test-credential-must-not-leak") {
		t.Fatalf("credential leaked into log output:\n%s", buf.String())
	}
	if got := cs.GetReasoningEffort(); got != "high" {
		t.Fatalf("got %q, want %q", got, "high")
	}
}

func TestControlResponse_ErrorDoesNotLogPayload(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	cs := &claudeSession{settingsRequestID: getSettingsRequestID}
	cs.handleReadLoopLine(`{"type":"control_response","response":{"subtype":"error","request_id":"cc-connect-get-settings","error":"test-credential-must-not-leak"}}`)
	if strings.Contains(buf.String(), "test-credential-must-not-leak") {
		t.Fatal("error payload reached debug log")
	}
}

func TestControlResponse_UnexpectedShapeDoesNotLogPayload(t *testing.T) {
	for _, line := range []string{
		`{"type":"settings_response","response":{"request_id":"cc-connect-get-settings","effective":{"env":{"TOKEN":"test-credential-must-not-leak"}}}}`,
		`{"type":"control_response","response":{"effective":{"env":{"TOKEN":"test-credential-must-not-leak"`,
	} {
		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		cs := &claudeSession{settingsRequestID: getSettingsRequestID}
		cs.handleReadLoopLine(line)
		slog.SetDefault(prev)
		if strings.Contains(buf.String(), "test-credential-must-not-leak") {
			t.Error("unexpected settings response reached debug log")
		}
	}
}

func TestAppliedEffort_ModelChangeRejectsLateResponse(t *testing.T) {
	cs := &claudeSession{settingsRequestID: getSettingsRequestID}
	cs.updateActiveModel("model-a")
	cs.handleControlResponse(settingsResponse(getSettingsRequestID, "success", map[string]any{"effort": "high"}))
	cs.updateActiveModel("model-b")
	cs.handleControlResponse(settingsResponse(getSettingsRequestID, "success", map[string]any{"effort": "high"}))
	if got := cs.GetReasoningEffort(); got != "" {
		t.Fatalf("stale model effort: %q", got)
	}
	cs.handleControlResponse(settingsResponse(cs.settingsRequestID, "success", map[string]any{"effort": "low"}))
	if got := cs.GetReasoningEffort(); got != "low" {
		t.Fatalf("new model effort: %q", got)
	}
}

func TestAppliedEffort_ConcurrentRefreshAndRead(t *testing.T) {
	cs := &claudeSession{configuredEffort: "medium"}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				cs.requestAppliedSettings()
				cs.effortMu.Lock()
				id := cs.settingsRequestID
				cs.effortMu.Unlock()
				cs.handleControlResponse(settingsResponse(id, "success", map[string]any{"effort": "high"}))
				_ = cs.GetReasoningEffort()
			}
		}()
	}
	wg.Wait()
}

// Test the actual spawn/stdin/read-loop contract for both fresh and resumed
// sessions, including wrappers that bundle the CLI arguments into one flag.
func TestAppliedEffort_UnsupportedCLIStillAnswers(t *testing.T) {
	for _, mode := range []string{"success", "error", "ignore", "malformed", "renamed"} {
		for _, resume := range []string{"", "test-resume-id"} {
			t.Run(mode+"/"+resume, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				wrapper := ""
				if resume != "" {
					wrapper = "--test-wrapper-args"
				}
				cs, err := newClaudeSession(ctx, t.TempDir(), os.Args[0],
					[]string{"-test.run=TestHelperProcess", "--", "claude-stdin-echo"},
					wrapper, "", "medium", resume, "default", "", "", nil, nil, nil,
					[]string{"GO_WANT_HELPER_PROCESS=1", "CC_CONNECT_TEST_SETTINGS_MODE=" + mode},
					"", false, core.SpawnOptions{}, 0, 0, "", "")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = cs.Close() }()
				if err := cs.Send("first real prompt", "test-id", nil, nil); err != nil {
					t.Fatal(err)
				}
				for {
					select {
					case ev, ok := <-cs.Events():
						if !ok {
							t.Fatal("session exited before first answer")
						}
						if ev.Type != core.EventResult {
							continue
						}
						if ev.Content != "echo: first real prompt" {
							t.Fatalf("unexpected answer: %q", ev.Content)
						}
						want := "medium"
						if mode == "success" {
							want = "xhigh"
						}
						if got := cs.GetReasoningEffort(); got != want {
							t.Fatalf("effort=%q want %q", got, want)
						}
						if !cs.Alive() {
							t.Fatal("optional settings request ended the session")
						}
						return
					case <-ctx.Done():
						t.Fatal("first answer timed out")
					}
				}
			})
		}
	}
}

func TestAppliedEffort_LateResponseUpdatesFallback(t *testing.T) {
	cs := &claudeSession{configuredEffort: "medium"}
	cs.requestAppliedSettings()
	id := cs.settingsRequestID
	if got := cs.GetReasoningEffort(); got != "medium" {
		t.Fatalf("before response: %q", got)
	}
	cs.handleControlResponse(settingsResponse(id, "success", map[string]any{"effort": "high"}))
	if got := cs.GetReasoningEffort(); got != "high" {
		t.Fatalf("after response: %q", got)
	}
	cs.requestAppliedSettings()
	cs.handleControlResponse(settingsResponse(id, "success", map[string]any{"effort": "low"}))
	if got := cs.GetReasoningEffort(); got != "medium" {
		t.Fatalf("late previous-turn response accepted: %q", got)
	}
}

func TestAppliedEffort_RefusalFallbackInvalidatesSessionSnapshot(t *testing.T) {
	for _, scope := range []string{"session", "local"} {
		t.Run(scope, func(t *testing.T) {
			cs := &claudeSession{ctx: context.Background(), events: make(chan core.Event, 1), settingsRequestID: getSettingsRequestID}
			cs.activeModel.Store("model-a")
			cs.handleControlResponse(settingsResponse(getSettingsRequestID, "success", map[string]any{"effort": "high"}))
			cs.handleRefusalFallback(map[string]any{"original_model": "model-a", "fallback_model": "model-b", "scope": scope, "direction": "retry", "request_id": "test-fallback"})
			want := ""
			if scope == "local" {
				want = "high"
			}
			if got := cs.GetReasoningEffort(); got != want {
				t.Fatalf("scope %s effort=%q want %q", scope, got, want)
			}
		})
	}
}
