package antigravity

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"cc-connect", "cc-connect"},
		{"Daily", "daily"},
		{"My Project", "my-project"},
		{"hello_world", "hello-world"},
		{"Test.123", "test-123"},
		{"---weird---", "weird"},
		{"", "project"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := slugify(tt.input)
			if got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeMode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"default", "default"},
		{"yolo", "yolo"},
		{"auto", "yolo"},
		{"force", "yolo"},
		{"plan", "plan"},
		{"sandbox", "plan"},
		{"invalid", "default"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeMode(tt.input)
			if got != tt.want {
				t.Errorf("normalizeMode(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSession_ContinueSessionTreatedAsFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s, err := newAntigravitySession(context.Background(), "echo", nil, "/tmp", "", "default", core.ContinueSession, nil, 0)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()

	if got := s.CurrentSessionID(); got != "" {
		t.Errorf("ContinueSession should be treated as fresh: chatID = %q, want empty", got)
	}
}

func TestBuildAntigravityArgs_PromptAtEnd(t *testing.T) {
	s, _ := newAntigravitySession(context.Background(), "echo", []string{"--verbose"}, "/tmp", "", "default", "", nil, 0)
	args := s.buildAntigravityArgs("sid-1", true, "plan", "/tmp/agy-config", "What is 1+1?")
	if len(args) < 2 {
		t.Fatalf("args too short: %v", args)
	}
	if args[len(args)-2] != "-p" || args[len(args)-1] != "What is 1+1?" {
		t.Fatalf("expected prompt to be final '-p <prompt>', got: %v", args)
	}
	if !contains(args, "--sandbox") {
		t.Fatalf("expected --sandbox in args, got: %v", args)
	}
	if !contains(args, "--gemini_dir=/tmp/agy-config") || !contains(args, "--print-timeout=24h") {
		t.Fatalf("expected isolated Agy config and extended print timeout, got: %v", args)
	}
	if !contains(args, "--verbose") {
		t.Fatalf("expected configured extra args, got: %v", args)
	}
	if !contains(args, "--output-format=stream-json") {
		t.Fatalf("expected AGY stream-json output, got: %v", args)
	}
	if contains(args, "-m") || contains(args, "--model") {
		t.Fatalf("did not expect model flags in args, got: %v", args)
	}
}

func TestStreamJSONResult(t *testing.T) {
	if os.Getenv("GO_WANT_ANTIGRAVITY_STREAM_HELPER") == "1" {
		_ = os.WriteFile(os.Getenv("CC_ANTIGRAVITY_ARGS_FILE"), []byte(strings.Join(os.Args, "\x00")), 0o600)
		_, _ = io.WriteString(os.Stdout, os.Getenv("CC_ANTIGRAVITY_STDOUT"))
		_, _ = io.WriteString(os.Stderr, os.Getenv("CC_ANTIGRAVITY_STDERR"))
		if path := os.Getenv("CC_ANTIGRAVITY_CHAT_FILE"); path != "" {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				os.Exit(4)
			}
			if err := os.WriteFile(path, []byte(`{"sessionId":"conv-recovered"}`), 0o600); err != nil {
				os.Exit(4)
			}
		}
		if os.Getenv("CC_ANTIGRAVITY_FAIL") == "1" {
			os.Exit(3)
		}
		os.Exit(0)
	}

	tests := []struct {
		name        string
		output      string
		stderr      string
		fail        bool
		wantText    string
		wantError   string
		wantSession string
		recoverID   bool
	}{
		{
			name:        "success",
			output:      "{\"event\":\"init\"}\n{\"event\":\"step_update\"}\n" + `{"event":"result","result":{"conversation_id":"conv-42","status":"SUCCESS","response":"done"}}`,
			wantText:    "done",
			wantSession: "conv-42",
		},
		{
			name:        "response_not_discarded_when_error_present",
			output:      `{"event":"result","result":{"conversation_id":"conv-42","status":"ERROR","response":"done","error":"Your previous response contained an improperly formatted function call"}}`,
			wantText:    "done",
			wantSession: "conv-42",
		},
		{
			name:     "response_with_error_and_success_status",
			output:   `{"event":"result","result":{"status":"SUCCESS","response":"  done\n","error":"historical error"}}`,
			wantText: "  done\n",
		},
		{
			name:     "response_with_error_status_only",
			output:   `{"event":"result","result":{"status":"error","response":"done"}}`,
			wantText: "done",
		},
		{
			name:     "response_with_nonzero_exit",
			output:   `{"event":"result","result":{"status":"ERROR","response":"done","error":"historical error"}}`,
			stderr:   "historical error",
			fail:     true,
			wantText: "done",
		},
		{
			name:      "empty_response_with_error",
			output:    `{"event":"result","result":{"status":"ERROR","response":"","error":"not available"}}`,
			stderr:    "not available",
			fail:      true,
			wantError: "not available",
		},
		{
			name:      "whitespace_response_with_error",
			output:    `{"event":"result","result":{"status":"ERROR","response":" \t\n","error":"not available"}}`,
			wantError: "not available",
		},
		{
			name:      "error_status_without_message",
			output:    `{"event":"result","result":{"status":"ERROR","response":""}}`,
			wantError: "agy reported status ERROR",
		},
		{
			name:      "empty_response_with_error_and_success_status",
			output:    `{"event":"result","result":{"status":"SUCCESS","response":"","error":"not available"}}`,
			wantError: "not available",
		},
		{
			name:      "empty_success",
			output:    `{"event":"result","result":{"status":"SUCCESS","response":""}}`,
			wantError: "without producing a response",
		},
		{
			name:      "whitespace_success_without_text_reports_empty_result",
			output:    `{"event":"result","result":{"status":"SUCCESS","response":" \t\n"}}`,
			wantError: "without producing a response",
		},
		{
			name:        "empty_success_without_id_recovers_id_and_reports_error",
			output:      `{"event":"result","result":{"status":"SUCCESS","response":""}}`,
			wantError:   "without producing a response",
			wantSession: "conv-recovered",
			recoverID:   true,
		},
		{
			name:        "response_with_old_error_without_id_recovers_id",
			output:      `{"event":"result","result":{"status":"ERROR","response":"done","error":"historical error"}}`,
			stderr:      "historical error",
			fail:        true,
			wantText:    "done",
			wantSession: "conv-recovered",
			recoverID:   true,
		},
		{
			name:        "explicit_session_id_is_not_replaced_by_fallback",
			output:      `{"event":"result","result":{"conversation_id":"conv-explicit","status":"SUCCESS","response":"done"}}`,
			wantText:    "done",
			wantSession: "conv-explicit",
			recoverID:   true,
		},
		{
			name:     "streamed_text_with_empty_success_is_valid_response",
			output:   `{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"done"}}` + "\n" + `{"event":"result","result":{"status":"SUCCESS","response":""}}`,
			wantText: "done",
		},
		{
			name:        "terminal_without_id_recovers_chat_after_process_exit",
			output:      `{"event":"result","result":{"status":"SUCCESS","response":"done"}}`,
			wantText:    "done",
			wantSession: "conv-recovered",
			recoverID:   true,
		},
		{
			name:      "empty_success_preserves_nonzero_exit",
			output:    `{"event":"result","result":{"status":"SUCCESS","response":""}}`,
			fail:      true,
			wantError: "exit status 3",
		},
		{
			name:      "empty_success_preserves_headless_diagnostic",
			output:    `{"event":"result","result":{"status":"SUCCESS","response":""}}`,
			stderr:    "headless mode cannot prompt",
			wantError: "headless mode cannot prompt",
		},
		{
			name:      "delta_only_empty_error_is_not_historical",
			output:    `{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"done"}}` + "\n" + `{"event":"result","result":{"status":"ERROR","response":"","error":"current failure"}}`,
			fail:      true,
			wantText:  "done",
			wantError: "current failure",
		},
		{
			name:      "streamed_text_empty_success_does_not_mask_failed_exit",
			output:    `{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"done"}}` + "\n" + `{"event":"result","result":{"status":"SUCCESS","response":""}}`,
			fail:      true,
			wantText:  "done",
			wantError: "exit status 3",
		},
		{
			name:     "whitespace_delta_does_not_hide_terminal_response",
			output:   `{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":" \t"}}` + "\n" + `{"event":"result","result":{"status":"ERROR","response":"done","error":"historical error"}}`,
			wantText: " \tdone",
		},
		{
			name: "whitespace_delta_preserves_streamed_markdown",
			output: strings.Join([]string{
				`{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"- item one"}}`,
				`{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"\n"}}`,
				`{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"- item two"}}`,
				`{"event":"result","result":{"conversation_id":"conv-whitespace","status":"ERROR","response":"- item one\n- item two","error":"historical error"}}`,
			}, "\n"),
			wantText:    "- item one\n- item two",
			wantSession: "conv-whitespace",
		},
		{
			name:        "streamed_response_survives_historical_result_error_without_duplication",
			output:      "{\"event\":\"step_update\",\"step_update\":{\"conversation_id\":\"conv-stream\",\"step_type\":\"agent_response\",\"text_delta\":\"done\"}}\n" + `{"event":"result","result":{"conversation_id":"conv-stream","status":"ERROR","response":"done","error":"historical error"}}`,
			stderr:      "process failed after terminal result",
			fail:        true,
			wantText:    "done",
			wantSession: "conv-stream",
		},
		{
			name:      "null_result_does_not_mask_process_failure",
			output:    `{"event":"result","result":null}`,
			stderr:    "process failed",
			fail:      true,
			wantError: "process failed",
		},
		{
			name:     "duplicate_result_is_ignored",
			output:   "{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\",\"response\":\"done\"}}\n" + `{"event":"result","result":{"status":"ERROR","error":"duplicate"}}`,
			wantText: "done",
		},
		{
			name:     "plain_text_fallback",
			output:   "legacy reply\n",
			wantText: "legacy reply\n",
		},
		{
			name:     "non_event_json_fallback",
			output:   "{\"answer\":\"legacy reply\"}\n{malformed}\n",
			wantText: "{\"answer\":\"legacy reply\"}\n{malformed}\n",
		},
		{
			name:      "plain_text_process_error",
			stderr:    "process failed",
			fail:      true,
			wantError: "process failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			argsPath := filepath.Join(t.TempDir(), "args")
			extraEnv := []string{
				"GO_WANT_ANTIGRAVITY_STREAM_HELPER=1",
				"CC_ANTIGRAVITY_CHAT_FILE=",
				"CC_ANTIGRAVITY_FAIL=",
				"CC_ANTIGRAVITY_ARGS_FILE=" + argsPath,
				"CC_ANTIGRAVITY_STDOUT=" + test.output,
				"CC_ANTIGRAVITY_STDERR=" + test.stderr,
			}
			if test.fail {
				extraEnv = append(extraEnv, "CC_ANTIGRAVITY_FAIL=1")
			}
			workDir := t.TempDir()
			if test.recoverID {
				chatFile := filepath.Join(os.Getenv("HOME"), ".gemini", "tmp", antigravityProjectSlug(workDir), "chats", "session-recovered.jsonl")
				extraEnv = append(extraEnv, "CC_ANTIGRAVITY_CHAT_FILE="+chatFile)
			}
			session, err := newAntigravitySession(context.Background(), os.Args[0], []string{"-test.run=^TestStreamJSONResult$", "--"}, workDir, "", "yolo", "", extraEnv, 5*time.Second)
			if err != nil {
				t.Fatalf("newAntigravitySession: %v", err)
			}
			defer func() { _ = session.Close() }()
			if err := session.Send("hello", "", nil, nil); err != nil {
				t.Fatalf("Send: %v", err)
			}
			var text strings.Builder
			var errors []error
			deadline := time.After(10 * time.Second)
			for {
				select {
				case event := <-session.Events():
					switch event.Type {
					case core.EventText:
						text.WriteString(event.Content)
					case core.EventError:
						errors = append(errors, event.Error)
					case core.EventResult:
						if text.String() != test.wantText {
							t.Errorf("text = %q, want %q", text.String(), test.wantText)
						}
						if test.wantError == "" {
							if len(errors) != 0 {
								t.Errorf("unexpected errors: %v", errors)
							}
						} else if len(errors) != 1 || !strings.Contains(errors[0].Error(), test.wantError) {
							t.Errorf("errors = %v, want exactly one containing %q", errors, test.wantError)
						}
						if !event.Done || event.SessionID != test.wantSession || session.CurrentSessionID() != test.wantSession {
							t.Errorf("result = %+v, current session = %q, want done with session %q", event, session.CurrentSessionID(), test.wantSession)
						}
						data, err := os.ReadFile(argsPath)
						if err != nil {
							t.Fatalf("read args: %v", err)
						}
						if !strings.Contains(string(data), "--output-format=stream-json\x00") {
							t.Errorf("args = %q, want --output-format=stream-json", data)
						}
						return
					}
				case <-deadline:
					t.Fatalf("timeout waiting for turn to end; text = %q", text.String())
				}
			}
		})
	}
}

func TestAntigravitySession_ResumePassesConversationID(t *testing.T) {
	if os.Getenv("GO_WANT_ANTIGRAVITY_HELPER") == "1" {
		argsPath := os.Getenv("CC_ANTIGRAVITY_ARGS_FILE")
		if argsPath == "" {
			os.Exit(2)
		}
		if err := os.WriteFile(argsPath, []byte(strings.Join(os.Args, "\x00")), 0o600); err != nil {
			os.Exit(2)
		}
		_, _ = io.WriteString(os.Stdout,
			"{\"event\":\"init\",\"conversation_id\":\"conversation-1\"}\n"+
				"{\"event\":\"step_update\",\"step_update\":{\"conversation_id\":\"conversation-1\",\"step_index\":0,\"state\":\"DONE\",\"step_type\":\"agent_response\",\"text_delta\":\"ok\\n\"}}\n"+
				"{\"event\":\"result\",\"result\":{\"conversation_id\":\"conversation-1\",\"status\":\"SUCCESS\",\"response\":\"ok\\n\"}}\n")
		os.Exit(0)
	}

	argsPath := t.TempDir() + string(os.PathSeparator) + "args"
	workDir := t.TempDir()
	s, err := newAntigravitySession(
		context.Background(),
		os.Args[0],
		[]string{"-test.run=TestAntigravitySession_ResumePassesConversationID", "--"},
		workDir,
		"",
		"default",
		"conversation-1",
		[]string{
			"GO_WANT_ANTIGRAVITY_HELPER=1",
			"CC_ANTIGRAVITY_ARGS_FILE=" + argsPath,
		},
		0,
	)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Send("second turn", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var response strings.Builder
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-s.Events():
			switch event.Type {
			case core.EventText:
				if event.SessionID != "" && event.SessionID != "conversation-1" {
					t.Fatalf("session ID = %q, want conversation-1", event.SessionID)
				}
				response.WriteString(event.Content)
			case core.EventResult:
				if !event.Done {
					t.Fatalf("completion event = %+v, want done EventResult", event)
				}
				if got := response.String(); got != "ok\n" {
					t.Fatalf("response = %q, want %q", got, "ok\n")
				}
				goto streamComplete
			}
		case <-deadline:
			t.Fatal("timeout waiting for streamed helper output")
		}
	}

streamComplete:

	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	args := strings.Split(string(data), "\x00")
	if !contains(args, "--conversation") || !contains(args, "conversation-1") {
		t.Fatalf("resume args = %v, want --conversation conversation-1", args)
	}
	if !contains(args, "-p") || !contains(args, "second turn") {
		t.Fatalf("prompt args = %v, want -p second turn", args)
	}
}

func TestDefaultModeCreatesPermissionBridge(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s, err := newAntigravitySession(context.Background(), "echo", nil, "/tmp", "", "default", "", nil, 0)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()

	if s.permissionBridge == nil {
		t.Fatal("permissionBridge = nil, want default-mode permission bridge")
	}
	if _, err := os.Stat(filepath.Join(s.permissionBridge.AgyConfigDir(), "config", "hooks.json")); err != nil {
		t.Fatalf("stat Agy hook overlay: %v", err)
	}
}

func TestNonDefaultModesDoNotCreatePermissionBridge(t *testing.T) {
	for _, mode := range []string{"yolo", "plan"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())

			s, err := newAntigravitySession(context.Background(), "echo", nil, "/tmp", "", mode, "", nil, 0)
			if err != nil {
				t.Fatalf("newAntigravitySession: %v", err)
			}
			defer func() { _ = s.Close() }()

			if s.permissionBridge != nil {
				t.Fatalf("permissionBridge = %v, want nil", s.permissionBridge)
			}
		})
	}
}

func TestRespondPermissionRequiresDefaultMode(t *testing.T) {
	s, err := newAntigravitySession(context.Background(), "echo", nil, "/tmp", "", "plan", "", nil, 0)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()

	err = s.RespondPermission("req", core.PermissionResult{Behavior: "allow"})
	if err == nil || !strings.Contains(err.Error(), "only available in default mode") {
		t.Fatalf("RespondPermission() error = %v, want default-mode error", err)
	}
}

func TestSendDoesNotHoldStdinOpen(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	workDir := t.TempDir()
	cmdPath := filepath.Join(t.TempDir(), "fake-agy.sh")
	script := "#!/bin/sh\ncat >/dev/null\nprintf 'done\\n'\n"
	if err := os.WriteFile(cmdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile fake agy: %v", err)
	}

	s, err := newAntigravitySession(context.Background(), cmdPath, nil, workDir, "", "default", "", nil, 2*time.Second)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Send("hello", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	deadline := time.After(3 * time.Second)
	var text strings.Builder
	for {
		select {
		case ev := <-s.Events():
			switch ev.Type {
			case core.EventPermissionRequest:
				t.Fatal("unexpected permission request from unstructured stdout")
			case core.EventText:
				text.WriteString(ev.Content)
			case core.EventResult:
				if !strings.Contains(text.String(), "done") {
					t.Fatalf("text = %q, want done", text.String())
				}
				return
			}
		case <-deadline:
			t.Fatal("timeout waiting for agy process to receive stdin EOF")
		}
	}
}

func TestAntigravitySession_ReportsHeadlessExitFailures(t *testing.T) {
	for _, tt := range []struct {
		name, script, want string
	}{
		{"soft_denied_success", "printf 'headless mode cannot prompt; auto-denied' >&2; exit 0", "headless mode cannot prompt"},
		{"empty_success", "exit 0", "without producing a response"},
		{"failed_without_stderr", "exit 2", "exit status 2"},
		{"failed_with_stderr", "printf 'tool execution failed' >&2; exit 2", "tool execution failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			cmdPath := filepath.Join(t.TempDir(), "fake-agy.sh")
			if err := os.WriteFile(cmdPath, []byte("#!/bin/sh\n"+tt.script+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			s, err := newAntigravitySession(context.Background(), cmdPath, nil, t.TempDir(), "", "yolo", "existing-session", nil, 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			if err := s.Send("test", "", nil, nil); err != nil {
				t.Fatal(err)
			}
			select {
			case event := <-s.Events():
				if event.Type != core.EventError || event.Error == nil || !strings.Contains(event.Error.Error(), tt.want) {
					t.Fatalf("event = %+v, want error containing %q", event, tt.want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("timeout waiting for process error")
			}
		})
	}
}

func TestRedactAgyStderr_RemovesEnvironmentSecrets(t *testing.T) {
	const marker = "stderr redaction marker"
	got := redactAgyStderr("  denied value="+marker+" path=/tmp/project  ", []string{"API_TOKEN=" + marker, "PATH=/tmp/project"})
	if want := "denied value=[REDACTED] path=/tmp/project"; got != want {
		t.Fatal("stderr should redact the sensitive environment value, retain the path, and trim outer whitespace")
	}
}

func TestAntigravitySession_StreamResponseDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		name, output, want string
	}{
		{"session_only", `{"event":"init","conversation_id":"session-1"}`, ""},
		{"tool_only", `{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"tool","tool_info":{"name":"run_command","output":"tool output"}}}`, ""},
		{"whitespace", `{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"  "}}`, ""},
		{"text_delta", `{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"answer"}}`, "answer"},
		{"final_response", `{"event":"result","result":{"status":"SUCCESS","response":"answer"}}`, "answer"},
		{"plain_text", "answer", "answer\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			cmdPath := filepath.Join(t.TempDir(), "fake-agy.sh")
			script := "#!/bin/sh\ncat <<'AGY_OUTPUT'\n" + tt.output + "\nAGY_OUTPUT\n"
			if err := os.WriteFile(cmdPath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			s, err := newAntigravitySession(context.Background(), cmdPath, nil, t.TempDir(), "", "yolo", "existing-session", nil, 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			if err := s.Send("test", "", nil, nil); err != nil {
				t.Fatal(err)
			}
			var response strings.Builder
			var responseErr error
			deadline := time.After(3 * time.Second)
			for {
				select {
				case event := <-s.Events():
					switch event.Type {
					case core.EventText:
						response.WriteString(event.Content)
					case core.EventError:
						responseErr = event.Error
					case core.EventResult:
						if tt.want == "" {
							if responseErr == nil || !strings.Contains(responseErr.Error(), "without producing a response") {
								t.Fatalf("error = %v, want missing response diagnostic", responseErr)
							}
						} else if responseErr != nil || response.String() != tt.want {
							t.Fatalf("response = %q, error = %v, want %q", response.String(), responseErr, tt.want)
						}
						return
					}
				case <-deadline:
					t.Fatal("timeout waiting for response diagnostics")
				}
			}
		})
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if strings.TrimSpace(x) == want {
			return true
		}
	}
	return false
}
