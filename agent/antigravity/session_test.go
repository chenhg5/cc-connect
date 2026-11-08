package antigravity

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
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
	t.Setenv("HOME", t.TempDir())
	s, err := newAntigravitySession(context.Background(), "echo", []string{"--verbose"}, "/tmp", "", "default", "", nil, 0)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()
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
	if contains(args, "--settings="+agyBridgeSessionSettings) {
		t.Fatalf("plan mode must not widen non-workspace access, got: %v", args)
	}
}

func TestAntigravitySession_DefaultBridgeAllowsNonWorkspaceAccessViaHook(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, err := newAntigravitySession(context.Background(), "echo", nil, "/tmp", "", "default", "", nil, 0)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()

	args := s.buildAntigravityArgs("", false, "default", "/tmp/agy-config", "hi")
	if !contains(args, "--settings="+agyBridgeSessionSettings) {
		t.Fatalf("default mode with permission bridge should delegate non-workspace access to the hook, got: %v", args)
	}
	args = s.buildAntigravityArgs("", false, "default", "", "hi")
	if contains(args, "--settings="+agyBridgeSessionSettings) {
		t.Fatalf("without the permission bridge agy must keep its own non-workspace check, got: %v", args)
	}

	for _, userArgs := range [][]string{{"--settings", `{"model":"x"}`}, {`--settings={"model":"x"}`}} {
		s.extraArgs = userArgs
		args = s.buildAntigravityArgs("", false, "default", "/tmp/agy-config", "hi")
		if contains(args, "--settings="+agyBridgeSessionSettings) {
			t.Fatalf("user --settings in cmd must not be overridden, got: %v", args)
		}
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

	t.Setenv("HOME", t.TempDir())
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
	got := redactAgyStderr("  denied token=secret-value-123 path=/tmp/project  ", []string{"API_TOKEN=secret-value-123", "PATH=/tmp/project"})
	if strings.Contains(got, "secret-value-123") || !strings.Contains(got, "/tmp/project") || strings.TrimSpace(got) != got {
		t.Fatalf("redacted stderr = %q", got)
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

func TestAntigravitySession_MultiTurnPersistsStreamedConversationID(t *testing.T) {
	if os.Getenv("GO_WANT_ANTIGRAVITY_MULTITURN_HELPER") == "1" {
		argsPath := os.Getenv("CC_ANTIGRAVITY_ARGS_FILE")
		if argsPath == "" {
			os.Exit(2)
		}
		if err := os.WriteFile(argsPath, []byte(strings.Join(os.Args, "\x00")), 0o600); err != nil {
			os.Exit(2)
		}
		_, _ = io.WriteString(os.Stdout,
			"{\"event\":\"init\",\"conversation_id\":\"conv-stream-1\"}\n"+
				"{\"event\":\"step_update\",\"step_update\":{\"conversation_id\":\"conv-stream-1\",\"step_index\":0,\"state\":\"DONE\",\"step_type\":\"agent_response\",\"text_delta\":\"turn ok\\n\"}}\n"+
				"{\"event\":\"result\",\"result\":{\"conversation_id\":\"conv-stream-1\",\"status\":\"SUCCESS\",\"response\":\"turn ok\\n\"}}\n")
		os.Exit(0)
	}

	t.Setenv("HOME", t.TempDir())
	argsPath := filepath.Join(t.TempDir(), "args")
	workDir := t.TempDir()
	s, err := newAntigravitySession(
		context.Background(),
		os.Args[0],
		[]string{"-test.run=^TestAntigravitySession_MultiTurnPersistsStreamedConversationID$", "--"},
		workDir,
		"",
		"yolo",
		"",
		[]string{
			"GO_WANT_ANTIGRAVITY_MULTITURN_HELPER=1",
			"CC_ANTIGRAVITY_ARGS_FILE=" + argsPath,
		},
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()

	drainTurn := func(prompt string) core.Event {
		t.Helper()
		if err := s.Send(prompt, "", nil, nil); err != nil {
			t.Fatalf("Send(%q): %v", prompt, err)
		}
		deadline := time.After(3 * time.Second)
		for {
			select {
			case ev := <-s.Events():
				if ev.Type == core.EventResult {
					return ev
				}
			case <-deadline:
				t.Fatalf("timeout waiting for turn %q completion", prompt)
			}
		}
	}

	res1 := drainTurn("first turn")
	if got := s.CurrentSessionID(); got != "conv-stream-1" {
		t.Fatalf("after first turn CurrentSessionID() = %q, want conv-stream-1", got)
	}
	if res1.SessionID != "conv-stream-1" {
		t.Fatalf("first turn EventResult.SessionID = %q, want conv-stream-1", res1.SessionID)
	}

	_ = drainTurn("second turn")
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	args := strings.Split(string(data), "\x00")
	if !contains(args, "--conversation") || !contains(args, "conv-stream-1") {
		t.Fatalf("second turn args = %v, want --conversation conv-stream-1", args)
	}
}

func TestDetectNewSessionID_AntigravityCLICacheAndMetadata(t *testing.T) {
	t.Run("last_conversations_json", func(t *testing.T) {
		fakeHome := t.TempDir()
		t.Setenv("HOME", fakeHome)
		workDir := t.TempDir()

		s, err := newAntigravitySession(context.Background(), "echo", nil, workDir, "", "yolo", "", nil, 0)
		if err != nil {
			t.Fatalf("newAntigravitySession: %v", err)
		}
		defer func() { _ = s.Close() }()

		cacheDir := filepath.Join(fakeHome, ".gemini", "antigravity-cli", "cache")
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			t.Fatalf("mkdir cacheDir: %v", err)
		}
		lastMap := map[string]string{
			filepath.Clean(workDir): "conv-from-last-map",
		}
		data, _ := json.Marshal(lastMap)
		if err := os.WriteFile(filepath.Join(cacheDir, "last_conversations.json"), data, 0o600); err != nil {
			t.Fatalf("write last_conversations.json: %v", err)
		}

		got := s.detectNewSessionID(map[string]bool{"old-session": true}, time.Now())
		if got != "conv-from-last-map" {
			t.Fatalf("detectNewSessionID = %q, want conv-from-last-map", got)
		}
	})

	t.Run("conversation_metadata_json_and_concurrent_workspaces", func(t *testing.T) {
		fakeHome := t.TempDir()
		t.Setenv("HOME", fakeHome)
		wsA := filepath.Join(t.TempDir(), "Workspace A")
		wsB := filepath.Join(t.TempDir(), "Workspace B")
		if err := os.MkdirAll(wsA, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(wsB, 0o755); err != nil {
			t.Fatal(err)
		}

		sA, err := newAntigravitySession(context.Background(), "echo", nil, wsA, "", "yolo", "", nil, 0)
		if err != nil {
			t.Fatalf("newAntigravitySession: %v", err)
		}
		defer func() { _ = sA.Close() }()

		cacheDir := filepath.Join(fakeHome, ".gemini", "antigravity-cli", "cache")
		brainDir := filepath.Join(fakeHome, ".gemini", "antigravity-cli", "brain")
		if err := os.MkdirAll(filepath.Join(brainDir, "conv-wsB-unscoped"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			t.Fatal(err)
		}

		now := time.Now().UTC()
		wsBURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(wsB)}).String()
		wsAURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(wsA)}).String()

		// Only wsB has a new conversation in metadata; wsA must NOT bind to wsB or unscoped brain dir.
		metaOnlyB := map[string]any{
			"conversations": map[string]any{
				"conv-wsB-1": map[string]any{
					"summary": map[string]any{
						"ID":            "conv-wsB-1",
						"WorkspaceURIs": []string{wsBURI},
						"UpdatedAt":     now.Format(time.RFC3339Nano),
					},
					"is_internal":        false,
					"last_modified_time": now.Format(time.RFC3339Nano),
				},
			},
		}
		dataB, _ := json.Marshal(metaOnlyB)
		if err := os.WriteFile(filepath.Join(cacheDir, "conversation_metadata.json"), dataB, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := sA.detectNewSessionID(map[string]bool{}, now); got != "" {
			t.Fatalf("detectNewSessionID for wsA with only wsB active = %q, want empty", got)
		}

		// Now add wsA's conversation; wsA should discover conv-wsA-1.
		metaBoth := map[string]any{
			"conversations": map[string]any{
				"conv-wsB-1": metaOnlyB["conversations"].(map[string]any)["conv-wsB-1"],
				"conv-wsA-1": map[string]any{
					"summary": map[string]any{
						"ID":            "conv-wsA-1",
						"WorkspaceURIs": []string{wsAURI},
						"UpdatedAt":     now.Format(time.RFC3339Nano),
					},
					"is_internal":        false,
					"last_modified_time": now.Format(time.RFC3339Nano),
				},
			},
		}
		dataBoth, _ := json.Marshal(metaBoth)
		if err := os.WriteFile(filepath.Join(cacheDir, "conversation_metadata.json"), dataBoth, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := sA.detectNewSessionID(map[string]bool{}, now); got != "conv-wsA-1" {
			t.Fatalf("detectNewSessionID for wsA = %q, want conv-wsA-1", got)
		}
	})
}

func TestListAndDeleteSessions_WorkspaceScopeAndSafety(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	wsA := filepath.Join(t.TempDir(), "Project Alpha")
	wsB := filepath.Join(t.TempDir(), "Project Beta")
	if err := os.MkdirAll(wsA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wsB, 0o755); err != nil {
		t.Fatal(err)
	}

	cacheDir := filepath.Join(fakeHome, ".gemini", "antigravity-cli", "cache")
	brainDir := filepath.Join(fakeHome, ".gemini", "antigravity-cli", "brain")
	convsDir := filepath.Join(fakeHome, ".gemini", "antigravity-cli", "conversations")
	for _, d := range []string{cacheDir, brainDir, convsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	wsAURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(wsA)}).String()
	wsBURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(wsB)}).String()
	now := time.Now().UTC()

	meta := map[string]any{
		"conversations": map[string]any{
			"session-1": map[string]any{
				"summary": map[string]any{
					"ID":            "session-1",
					"Title":         "Alpha Title",
					"Preview":       "Alpha first prompt",
					"NumSteps":      4,
					"UpdatedAt":     now.Format(time.RFC3339Nano),
					"WorkspaceURIs": []string{wsAURI},
				},
				"is_internal":        false,
				"last_modified_time": now.Format(time.RFC3339Nano),
			},
			"session-10": map[string]any{
				"summary": map[string]any{
					"ID":            "session-10",
					"Preview":       "Alpha ten prompt",
					"NumSteps":      2,
					"UpdatedAt":     now.Add(-time.Minute).Format(time.RFC3339Nano),
					"WorkspaceURIs": []string{wsAURI},
				},
				"is_internal":        false,
				"last_modified_time": now.Add(-time.Minute).Format(time.RFC3339Nano),
			},
			"session-b": map[string]any{
				"summary": map[string]any{
					"ID":            "session-b",
					"Preview":       "Beta prompt",
					"NumSteps":      3,
					"UpdatedAt":     now.Format(time.RFC3339Nano),
					"WorkspaceURIs": []string{wsBURI},
				},
				"is_internal":        false,
				"last_modified_time": now.Format(time.RFC3339Nano),
			},
			"session-internal": map[string]any{
				"summary": map[string]any{
					"ID":            "session-internal",
					"Preview":       "Subagent task",
					"NumSteps":      5,
					"UpdatedAt":     now.Format(time.RFC3339Nano),
					"WorkspaceURIs": []string{wsAURI},
				},
				"is_internal":        true,
				"last_modified_time": now.Format(time.RFC3339Nano),
			},
			"../escape": map[string]any{
				"summary": map[string]any{
					"ID":            "../escape",
					"Preview":       "Malicious ID",
					"NumSteps":      1,
					"UpdatedAt":     now.Format(time.RFC3339Nano),
					"WorkspaceURIs": []string{wsAURI},
				},
				"is_internal": false,
			},
		},
	}
	metaBytes, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(cacheDir, "conversation_metadata.json"), metaBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"session-1", "session-10", "session-b"} {
		if err := os.MkdirAll(filepath.Join(brainDir, id), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(convsDir, id+".db"), []byte("sqlite"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(convsDir, "session-1.db-wal"), []byte("wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(convsDir, "session-1-keep.db"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	sessionsA, err := listAntigravitySessions(wsA)
	if err != nil {
		t.Fatalf("listAntigravitySessions(wsA): %v", err)
	}
	if len(sessionsA) != 2 || sessionsA[0].ID != "session-1" || sessionsA[1].ID != "session-10" {
		t.Fatalf("sessionsA = %+v, want [session-1, session-10]", sessionsA)
	}

	agentA := &Agent{workDir: wsA}

	// Path traversal must be rejected without touching sibling directories.
	victimDir := filepath.Join(fakeHome, ".gemini", "antigravity-cli", "victim")
	if err := os.MkdirAll(victimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, badID := range []string{"../victim", "../../etc", "session-1/../session-10", ".", "..", ""} {
		if err := agentA.DeleteSession(context.Background(), badID); err == nil {
			t.Fatalf("DeleteSession(%q) succeeded, want traversal/invalid ID error", badID)
		}
	}
	if _, err := os.Stat(victimDir); err != nil {
		t.Fatalf("victimDir was removed or damaged: %v", err)
	}

	// Deleting session-1 must remove its brain/db/wal and metadata entry, without touching session-10 or session-1-keep.db.
	if err := agentA.DeleteSession(context.Background(), "session-1"); err != nil {
		t.Fatalf("DeleteSession(session-1): %v", err)
	}
	if _, err := os.Stat(filepath.Join(brainDir, "session-1")); !os.IsNotExist(err) {
		t.Fatalf("brain/session-1 still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(convsDir, "session-1.db")); !os.IsNotExist(err) {
		t.Fatalf("conversations/session-1.db still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(convsDir, "session-1.db-wal")); !os.IsNotExist(err) {
		t.Fatalf("conversations/session-1.db-wal still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(convsDir, "session-10.db")); err != nil {
		t.Fatalf("session-10.db was wrongly deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(convsDir, "session-1-keep.db")); err != nil {
		t.Fatalf("session-1-keep.db was wrongly deleted: %v", err)
	}

	sessionsAfter, err := listAntigravitySessions(wsA)
	if err != nil {
		t.Fatalf("listAntigravitySessions(wsA) after delete: %v", err)
	}
	if len(sessionsAfter) != 1 || sessionsAfter[0].ID != "session-10" {
		t.Fatalf("sessionsAfter = %+v, want [session-10]", sessionsAfter)
	}

	// Cross-workspace delete from wsA must not delete wsB's session-b.
	if err := agentA.DeleteSession(context.Background(), "session-b"); err == nil {
		t.Fatal("DeleteSession(session-b) from wsA succeeded, want cross-workspace rejection")
	}
	if _, err := os.Stat(filepath.Join(convsDir, "session-b.db")); err != nil {
		t.Fatalf("session-b.db was wrongly deleted by wsA: %v", err)
	}
}

func TestAntigravityZeroTimestampAndInternalIsolation(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	ws := filepath.Join(t.TempDir(), "Workspace ZeroTS")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	// Invalid resumeID must be ignored on session creation.
	sInvalid, err := newAntigravitySession(context.Background(), "echo", nil, ws, "", "yolo", "../bad-resume", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sInvalid.Close() }()
	if got := sInvalid.CurrentSessionID(); got != "" {
		t.Fatalf("CurrentSessionID() with invalid resumeID = %q, want empty", got)
	}

	cliRoot := filepath.Join(fakeHome, ".gemini", "antigravity-cli")
	cacheDir := filepath.Join(cliRoot, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	wsURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(ws)}).String()

	meta := map[string]any{
		"conversations": map[string]any{
			"conv-internal": map[string]any{
				"summary": map[string]any{
					"ID":            "conv-internal",
					"Preview":       "Internal subagent",
					"UpdatedAt":     now.Format(time.RFC3339Nano),
					"WorkspaceURIs": []string{wsURI},
					"Internal":      true,
				},
				"is_internal":        true,
				"last_modified_time": now.Format(time.RFC3339Nano),
			},
			"conv-zero-updated-at": map[string]any{
				"summary": map[string]any{
					"ID":            "conv-zero-updated-at",
					"Preview":       "Real user session with zero UpdatedAt",
					"NumSteps":      3,
					"UpdatedAt":     "0001-01-01T00:00:00Z",
					"WorkspaceURIs": []string{wsURI},
				},
				"is_internal":        false,
				"last_modified_time": now.Format(time.RFC3339Nano),
			},
		},
	}
	metaBytes, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(cacheDir, "conversation_metadata.json"), metaBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	// Even if conv-internal appears in last_conversations.json and history.jsonl, it must be skipped.
	lastBytes, _ := json.Marshal(map[string]string{ws: "conv-internal"})
	if err := os.WriteFile(filepath.Join(cacheDir, "last_conversations.json"), lastBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	histLine := fmt.Sprintf(`{"display":"subagent","timestamp":%d,"workspace":%q,"conversationId":"conv-internal"}`+"\n", now.UnixMilli(), ws)
	if err := os.WriteFile(filepath.Join(cliRoot, "history.jsonl"), []byte(histLine), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := sInvalid.detectNewSessionID(map[string]bool{}, now); got != "conv-zero-updated-at" {
		t.Fatalf("detectNewSessionID = %q, want conv-zero-updated-at", got)
	}

	sessions, err := listAntigravitySessions(ws)
	if err != nil {
		t.Fatalf("listAntigravitySessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "conv-zero-updated-at" {
		t.Fatalf("sessions = %+v, want [conv-zero-updated-at]", sessions)
	}
	if !sessions[0].ModifiedAt.Equal(now) {
		t.Fatalf("ModifiedAt = %v, want %v (from last_modified_time)", sessions[0].ModifiedAt, now)
	}
}

func TestAntigravityWorkspaceMatches_CrossPlatformAndURIs(t *testing.T) {
	cases := []struct {
		name      string
		workDir   string
		candidate string
		want      bool
	}{
		{
			name:      "unix_path_and_escaped_file_uri",
			workDir:   "/Users/alice/My Project/",
			candidate: "file:///Users/alice/My%20Project",
			want:      true,
		},
		{
			name:      "windows_drive_and_file_uri_lowercase_drive",
			workDir:   `C:\Users\alice\My Project\`,
			candidate: "file:///c:/Users/alice/My%20Project",
			want:      true,
		},
		{
			name:      "windows_drive_and_escaped_colon_file_uri",
			workDir:   `c:\Users\alice\My Project`,
			candidate: "file:///C%3A/Users/alice/My%20Project/",
			want:      true,
		},
		{
			name:      "different_workspaces_do_not_match",
			workDir:   "/Users/alice/ProjectA",
			candidate: "file:///Users/alice/ProjectB",
			want:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := antigravityWorkspaceMatches(tc.workDir, tc.candidate); got != tc.want {
				t.Fatalf("antigravityWorkspaceMatches(%q, %q) = %v, want %v", tc.workDir, tc.candidate, got, tc.want)
			}
		})
	}
}

func TestAntigravitySessions_CorruptMetadataAndHistoryFallback(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	ws := filepath.Join(t.TempDir(), "Workspace History")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	cliRoot := filepath.Join(fakeHome, ".gemini", "antigravity-cli")
	cacheDir := filepath.Join(cliRoot, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write corrupt metadata and corrupt last_conversations; fallback to history.jsonl must still work.
	if err := os.WriteFile(filepath.Join(cacheDir, "conversation_metadata.json"), []byte("{not-valid-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "last_conversations.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	historyLines := strings.Join([]string{
		fmt.Sprintf(`{"display":"/help","timestamp":%d,"workspace":%q,"conversationId":"conv-hist-1","type":"slash_command"}`, now.Add(-10*time.Second).UnixMilli(), ws),
		fmt.Sprintf(`{"display":"Fix context loss in agy","timestamp":%d,"workspace":%q,"conversationId":"conv-hist-1"}`, now.UnixMilli(), ws),
		fmt.Sprintf(`{"display":"Other workspace prompt","timestamp":%d,"workspace":"/tmp/other-ws","conversationId":"conv-other"}`, now.UnixMilli()),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(cliRoot, "history.jsonl"), []byte(historyLines), 0o600); err != nil {
		t.Fatal(err)
	}

	preEntries := captureAntigravityPreEntries(ws)
	if !preEntries["conv-hist-1"] {
		t.Fatalf("captureAntigravityPreEntries did not capture conv-hist-1: %v", preEntries)
	}

	s, err := newAntigravitySession(context.Background(), "echo", nil, ws, "", "yolo", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	// Pre-existing session must not be misidentified as new.
	if got := s.detectNewSessionID(preEntries, now); got != "" {
		t.Fatalf("detectNewSessionID with pre-existing conv-hist-1 = %q, want empty", got)
	}
	// When not in preEntries, history.jsonl fallback discovers conv-hist-1.
	if got := s.detectNewSessionID(map[string]bool{}, now); got != "conv-hist-1" {
		t.Fatalf("detectNewSessionID from history.jsonl = %q, want conv-hist-1", got)
	}

	sessions, err := listAntigravitySessions(ws)
	if err != nil {
		t.Fatalf("listAntigravitySessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "conv-hist-1" {
		t.Fatalf("sessions = %+v, want [conv-hist-1]", sessions)
	}
	if sessions[0].Summary != "Fix context loss in agy" {
		t.Fatalf("session summary = %q, want %q", sessions[0].Summary, "Fix context loss in agy")
	}
	if sessions[0].MessageCount != 2 {
		t.Fatalf("session MessageCount = %d, want 2", sessions[0].MessageCount)
	}
}

func TestAgyPermissionBridge_OverlayPreservesAntigravityCLIState(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	events := make(chan core.Event, 4)
	bridge, err := newAgyPermissionBridge(context.Background(), events)
	if err != nil {
		t.Fatalf("newAgyPermissionBridge: %v", err)
	}

	overlayCacheDir := filepath.Join(bridge.AgyConfigDir(), "antigravity-cli", "cache")
	if err := os.MkdirAll(overlayCacheDir, 0o755); err != nil {
		bridge.Close()
		t.Fatalf("mkdir overlay cache dir: %v", err)
	}
	payload := []byte(`{"/workspace":"conv-persisted-1"}`)
	if err := os.WriteFile(filepath.Join(overlayCacheDir, "last_conversations.json"), payload, 0o600); err != nil {
		bridge.Close()
		t.Fatalf("write overlay last_conversations.json: %v", err)
	}

	// Closing the bridge removes its temporary overlay root, but ~/.gemini/antigravity-cli must remain intact.
	bridge.Close()

	realFile := filepath.Join(fakeHome, ".gemini", "antigravity-cli", "cache", "last_conversations.json")
	got, err := os.ReadFile(realFile)
	if err != nil {
		t.Fatalf("persisted last_conversations.json missing after bridge.Close(): %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("persisted content = %q, want %q", string(got), string(payload))
	}
}

func TestAntigravitySummariesDB_ListDetectAndDelete(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	ws := filepath.Join(t.TempDir(), "SQLite Workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	cliRoot := filepath.Join(fakeHome, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(cliRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(cliRoot, "conversation_summaries.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE conversation_summaries (
			conversation_id text PRIMARY KEY,
			title text NOT NULL DEFAULT "",
			preview text NOT NULL DEFAULT "",
			step_count integer NOT NULL DEFAULT 0,
			last_modified_time datetime NOT NULL,
			workspace_uris text NOT NULL,
			parent_conversation_id text NOT NULL DEFAULT "",
			nesting_depth integer NOT NULL DEFAULT 0,
			raw_summary blob
		);
	`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}

	wsURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(ws)}).String()
	// Protobuf field 7 (tag 0x3a) length-delimited string containing wsURI.
	protoBlob := append([]byte{0x3a, byte(len(wsURI))}, []byte(wsURI)...)
	nowStr := time.Now().UTC().Format("2006-01-02 15:04:05.000000+00:00")

	// Insert one active user session with empty workspace_uris column (relying on raw_summary proto)
	// and one subagent session with parent_conversation_id set.
	_, err = db.Exec(
		`INSERT INTO conversation_summaries (conversation_id, title, preview, step_count, last_modified_time, workspace_uris, parent_conversation_id, nesting_depth, raw_summary) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"conv-sqlite-1", "", "Print mode prompt", 9, nowStr, "", "", 0, protoBlob,
		"conv-sqlite-sub", "", "Subagent prompt", 4, nowStr, `["`+wsURI+`"]`, "conv-sqlite-1", 1, nil,
	)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}

	s, err := newAntigravitySession(context.Background(), "echo", nil, ws, "", "yolo", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	if got := s.detectNewSessionID(map[string]bool{}, time.Now().UTC()); got != "conv-sqlite-1" {
		t.Fatalf("detectNewSessionID from conversation_summaries.db = %q, want conv-sqlite-1", got)
	}

	sessions, err := listAntigravitySessions(ws)
	if err != nil {
		t.Fatalf("listAntigravitySessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "conv-sqlite-1" || sessions[0].Summary != "Print mode prompt" {
		t.Fatalf("sessions = %+v, want [conv-sqlite-1]", sessions)
	}

	ag := &Agent{workDir: ws}
	if err := ag.DeleteSession(context.Background(), "conv-sqlite-1"); err != nil {
		t.Fatalf("DeleteSession(conv-sqlite-1): %v", err)
	}
	sessionsAfter, err := listAntigravitySessions(ws)
	if err != nil {
		t.Fatalf("listAntigravitySessions after delete: %v", err)
	}
	if len(sessionsAfter) != 0 {
		t.Fatalf("sessionsAfter = %+v, want empty", sessionsAfter)
	}
}

// A conversation with no workspace record anywhere must not be deletable from
// an unrelated project just because its ID is known.
func TestDeleteSession_RefusesCLISessionWithoutWorkspaceRecord(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	id := "11111111-2222-3333-4444-555555555555"
	brain := filepath.Join(fakeHome, ".gemini", "antigravity-cli", "brain", id)
	if err := os.MkdirAll(brain, 0o700); err != nil {
		t.Fatal(err)
	}

	a := &Agent{workDir: filepath.Join(fakeHome, "projectA")}
	if err := a.DeleteSession(context.Background(), id); err == nil {
		t.Fatal("DeleteSession succeeded for a session not recorded under this workspace")
	}
	if _, err := os.Stat(brain); err != nil {
		t.Fatalf("brain dir of unrelated session was touched: %v", err)
	}
}

// If the summaries index cannot be updated, DeleteSession must report it and
// leave the conversation files in place so the delete can be retried.
func TestDeleteSession_SummariesDBFailureKeepsFiles(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	ws := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	cliRoot := filepath.Join(fakeHome, ".gemini", "antigravity-cli")
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	brain := filepath.Join(cliRoot, "brain", id)
	if err := os.MkdirAll(brain, 0o700); err != nil {
		t.Fatal(err)
	}
	history := fmt.Sprintf("{\"display\":\"hi\",\"timestamp\":%d,\"workspace\":%q,\"conversationId\":%q}\n", time.Now().UnixMilli(), ws, id)
	if err := os.WriteFile(filepath.Join(cliRoot, "history.jsonl"), []byte(history), 0o600); err != nil {
		t.Fatal(err)
	}
	// A database without the expected table makes the DELETE fail.
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(cliRoot, "conversation_summaries.db")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE unrelated (x text)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()

	a := &Agent{workDir: ws}
	err = a.DeleteSession(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "conversation_summaries") {
		t.Fatalf("DeleteSession err = %v, want conversation_summaries failure", err)
	}
	if _, err := os.Stat(brain); err != nil {
		t.Fatalf("brain dir removed despite index failure: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(cliRoot, "history.jsonl")); !strings.Contains(string(data), id) {
		t.Fatal("history.jsonl entry removed despite index failure")
	}
}

// Deleting a session must remove only its history lines and leave the rest of
// agy's file (blank lines, ordering, permissions) untouched.
func TestRemoveSessionFromCaches_PreservesOtherHistoryBytes(t *testing.T) {
	cliRoot := t.TempDir()
	historyPath := filepath.Join(cliRoot, "history.jsonl")
	keepA := `{"display":"a","workspace":"/w","conversationId":"keep-1"}`
	drop := `{"display":"b","workspace":"/w","conversationId":"drop-1"}`
	keepB := `{"display":"c","workspace":"/w","conversationId":"keep-2"}`
	original := keepA + "\n\n" + drop + "\n" + "not json\n" + keepB + "\n"
	if err := os.WriteFile(historyPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(historyPath, 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := removeSessionFromAntigravityCaches(cliRoot, "drop-1")
	if err != nil || !removed {
		t.Fatalf("removeSessionFromAntigravityCaches = %v, %v; want removed", removed, err)
	}
	got, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	want := keepA + "\n\n" + "not json\n" + keepB + "\n"
	if string(got) != want {
		t.Fatalf("history.jsonl =\n%q\nwant\n%q", got, want)
	}
	if info, err := os.Stat(historyPath); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("history.jsonl mode = %v (err %v), want 0644 preserved", info.Mode().Perm(), err)
	}
}

func TestNewAntigravitySession_RejectsUnsafeResumeID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, id := range []string{"../victim", "a b", "id/with/slash"} {
		s, err := newAntigravitySession(context.Background(), "echo", nil, t.TempDir(), "", "yolo", id, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.CurrentSessionID(); got != "" {
			t.Errorf("resume ID %q was accepted as %q", id, got)
		}
		_ = s.Close()
	}
}
