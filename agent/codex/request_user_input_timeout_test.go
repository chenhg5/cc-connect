package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestInvalidRequestUserInputTimeoutLogOmitsRawValue(t *testing.T) {
	const sentinel = "SENTINEL_TIMEOUT_VALUE"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	if got := parseRequestUserInputTimeoutMins(sentinel); got != defaultRequestUserInputTimeout {
		t.Fatalf("fallback = %s", got)
	}
	if strings.Contains(logs.String(), sentinel) {
		t.Fatal("invalid timeout log exposed raw config value")
	}
}

// TestParseRequestUserInputTimeoutMins covers the default and fallback rules for request_user_input_timeout_mins.
func TestParseRequestUserInputTimeoutMins(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  any
		want time.Duration
	}{
		{"unset", nil, defaultRequestUserInputTimeout},
		{"toml int64", int64(30), 30 * time.Minute},
		{"int", 1, time.Minute},
		{"float64", float64(10), 10 * time.Minute},
		{"string", " 15 ", 15 * time.Minute},
		{"zero", int64(0), defaultRequestUserInputTimeout},
		{"negative", int64(-3), defaultRequestUserInputTimeout},
		{"empty string", "", defaultRequestUserInputTimeout},
		{"garbage string", "soon", defaultRequestUserInputTimeout},
		{"wrong type", true, defaultRequestUserInputTimeout},
		{"overflow", int64(1) << 62, defaultRequestUserInputTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseRequestUserInputTimeoutMins(tc.raw); got != tc.want {
				t.Fatalf("parseRequestUserInputTimeoutMins(%#v) = %s, want %s", tc.raw, got, tc.want)
			}
		})
	}
}

// TestNew_RequestUserInputTimeoutOption checks that New stores the option and derived workspaces inherit it.
func TestNew_RequestUserInputTimeoutOption(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	a, err := New(map[string]any{"cmd": bin})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.(*Agent).requestUserInputTimeout; got != defaultRequestUserInputTimeout {
		t.Fatalf("default timeout = %s, want %s", got, defaultRequestUserInputTimeout)
	}
	if _, ok := a.(*Agent).WorkspaceAgentOptions()["request_user_input_timeout_mins"]; ok {
		t.Fatal("default timeout should not be written into workspace options")
	}

	a, err = New(map[string]any{"cmd": bin, "backend": "app_server", "request_user_input_timeout_mins": int64(30)})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.(*Agent).requestUserInputTimeout; got != 30*time.Minute {
		t.Fatalf("configured timeout = %s, want 30m", got)
	}
	inherited := a.(*Agent).WorkspaceAgentOptions()["request_user_input_timeout_mins"]
	if got := parseRequestUserInputTimeoutMins(inherited); got != 30*time.Minute {
		t.Fatalf("inherited option = %#v, want 30 minutes", inherited)
	}
}

// TestAppServerSession_UserInputTimeoutDefault keeps zero-valued sessions on the legacy 5-minute wait.
func TestAppServerSession_UserInputTimeoutDefault(t *testing.T) {
	if got := (&appServerSession{}).userInputTimeout(); got != 5*time.Minute {
		t.Fatalf("userInputTimeout() = %s, want 5m", got)
	}
	if got := (&appServerSession{requestUserInputTimeout: 2 * time.Minute}).userInputTimeout(); got != 2*time.Minute {
		t.Fatalf("userInputTimeout() = %s, want 2m", got)
	}
}

// TestAppServerSession_RequestUserInputUsesConfiguredTimeout verifies an unanswered question is declined after the configured wait.
func TestAppServerSession_RequestUserInputUsesConfiguredTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stdin := &lockedWriteCloser{}
	s := &appServerSession{
		events:                  make(chan core.Event, 4),
		ctx:                     ctx,
		pendingApprovals:        make(map[string]chan core.PermissionResult),
		stdin:                   stdin,
		requestUserInputTimeout: 20 * time.Millisecond,
	}

	s.handleServerRequest(serverRequestProbe(t, `"rui-timeout"`, "item/tool/requestUserInput", map[string]any{
		"threadId": "thread-1",
		"turnId":   "turn-1",
		"itemId":   "call-timeout",
		"questions": []any{
			map[string]any{
				"id":       "database",
				"header":   "Database",
				"question": "Which database should we use?",
				"options": []any{
					map[string]any{"label": "Postgres", "description": "relational"},
				},
			},
		},
	}))

	select {
	case <-s.events:
	case <-time.After(time.Second):
		t.Fatal("expected AskUserQuestion event")
	}

	// Never answer: waitForWrittenJSONLine gives up after 1s, so only the
	// configured 20ms timeout (not the 5-minute default) can produce a reply.
	line := waitForWrittenJSONLine(t, stdin)
	var envelope struct {
		ID     string `json:"id"`
		Result struct {
			Answers map[string]any `json:"answers"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		t.Fatalf("decode response %q: %v", line, err)
	}
	if envelope.ID != "rui-timeout" {
		t.Fatalf("response id = %q, want rui-timeout", envelope.ID)
	}
	if len(envelope.Result.Answers) != 0 {
		t.Fatalf("timed-out answers = %#v, want none", envelope.Result.Answers)
	}

	s.approvalsMu.Lock()
	_, pending := s.pendingApprovals[`"rui-timeout"`]
	s.approvalsMu.Unlock()
	if pending {
		t.Fatal("timed-out request is still pending")
	}
}
