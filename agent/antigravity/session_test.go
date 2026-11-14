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

	s, err := newAntigravitySession(context.Background(), "echo", nil, "/tmp", "", "", "default", core.ContinueSession, nil, 0)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()

	if got := s.CurrentSessionID(); got != "" {
		t.Errorf("ContinueSession should be treated as fresh: chatID = %q, want empty", got)
	}
}

func TestBuildAntigravityArgs_PromptAtEnd(t *testing.T) {
	s, _ := newAntigravitySession(context.Background(), "echo", []string{"--verbose"}, "/tmp", "", "", "default", "", nil, 0)
	args := s.buildAntigravityArgs("sid-1", true, "plan", "/tmp/agy-config", "", "", "What is 1+1?")
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

	s, err := newAntigravitySession(context.Background(), "echo", nil, "/tmp", "", "", "default", "", nil, 0)
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

			s, err := newAntigravitySession(context.Background(), "echo", nil, "/tmp", "", "", mode, "", nil, 0)
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
	s, err := newAntigravitySession(context.Background(), "echo", nil, "/tmp", "", "", "plan", "", nil, 0)
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

	s, err := newAntigravitySession(context.Background(), cmdPath, nil, workDir, "", "", "default", "", nil, 2*time.Second)
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

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if strings.TrimSpace(x) == want {
			return true
		}
	}
	return false
}

func TestBuildAntigravityArgs_ModelAndEffort(t *testing.T) {
	s, _ := newAntigravitySession(context.Background(), "echo", nil, "/tmp", "", "", "yolo", "", nil, 0)
	args := s.buildAntigravityArgs("", false, "yolo", "", "gemini-3.8-flash", "high", "hi")
	if !containsPair(args, "--model", "gemini-3.8-flash") {
		t.Fatalf("expected --model gemini-3.8-flash, got: %v", args)
	}
	if !containsPair(args, "--effort", "high") {
		t.Fatalf("expected --effort high, got: %v", args)
	}
	if args[len(args)-2] != "-p" || args[len(args)-1] != "hi" {
		t.Fatalf("expected prompt to stay last, got: %v", args)
	}
}

func TestAntigravitySession_RemembersStreamConversationID(t *testing.T) {
	if os.Getenv("GO_WANT_ANTIGRAVITY_STREAM_ID_HELPER") == "1" {
		argsPath := os.Getenv("CC_ANTIGRAVITY_ARGS_FILE")
		f, err := os.OpenFile(argsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(2)
		}
		_, _ = f.WriteString(strings.Join(os.Args, "\x00") + "\n")
		_ = f.Close()
		_, _ = io.WriteString(os.Stdout,
			"{\"event\":\"init\",\"conversation_id\":\"conv-new\"}\n"+
				"{\"event\":\"result\",\"result\":{\"conversation_id\":\"conv-new\",\"status\":\"SUCCESS\",\"response\":\"ok\"}}\n")
		os.Exit(0)
	}

	t.Setenv("HOME", t.TempDir())
	argsPath := filepath.Join(t.TempDir(), "args")
	s, err := newAntigravitySession(context.Background(), os.Args[0],
		[]string{"-test.run=TestAntigravitySession_RemembersStreamConversationID", "--"},
		t.TempDir(), "", "", "yolo", "",
		[]string{"GO_WANT_ANTIGRAVITY_STREAM_ID_HELPER=1", "CC_ANTIGRAVITY_ARGS_FILE=" + argsPath}, 0)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()

	for _, prompt := range []string{"first", "second"} {
		if err := s.Send(prompt, "", nil, nil); err != nil {
			t.Fatalf("Send(%s): %v", prompt, err)
		}
		waitForResult(t, s)
	}
	if got := s.CurrentSessionID(); got != "conv-new" {
		t.Fatalf("CurrentSessionID = %q, want conv-new", got)
	}

	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	runs := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if strings.Contains(runs[0], "--conversation") {
		t.Fatalf("first turn should not resume: %q", runs[0])
	}
	if !containsPair(strings.Split(runs[1], "\x00"), "--conversation", "conv-new") {
		t.Fatalf("second turn should resume conv-new: %q", runs[1])
	}
}

func waitForResult(t *testing.T, s *antigravitySession) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			if ev.Type == core.EventResult && ev.Done {
				return
			}
		case <-deadline:
			t.Fatal("timeout waiting for result")
		}
	}
}

func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
