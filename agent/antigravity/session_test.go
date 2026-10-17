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
	if !contains(args, "--output-format") || !contains(args, "stream-json") {
		t.Fatalf("expected stream-json output, got: %v", args)
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
		_, _ = io.WriteString(os.Stdout, "ok\n")
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

	select {
	case event := <-s.Events():
		if event.Type != core.EventText {
			t.Fatalf("first event type = %q, want %q", event.Type, core.EventText)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for helper output")
	}
	select {
	case event := <-s.Events():
		if event.Type != core.EventResult || !event.Done {
			t.Fatalf("completion event = %+v, want done EventResult", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for EventResult")
	}

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

// A fresh session learns the conversation id from agy's own stream-json
// output and resumes it on the next turn.
func TestSendLearnsConversationIDFromStream(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	cmdPath := filepath.Join(dir, "fake-agy.sh")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + argsPath + "\necho '---' >> " + argsPath + "\n" +
		`echo '{"event":"init","conversation_id":"conv-1","init":{}}'` + "\n" +
		`echo '{"event":"step_update","step_update":{"conversation_id":"conv-1","step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"hel"}}'` + "\n" +
		`echo '{"event":"step_update","step_update":{"conversation_id":"conv-1","step_index":1,"state":"DONE","step_type":"agent_response","text_delta":"lo\\n"}}'` + "\n" +
		`echo '{"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"hello\\n"}}'` + "\n"
	if err := os.WriteFile(cmdPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := newAntigravitySession(context.Background(), cmdPath, nil, dir, "", "yolo", "", nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	turn := func(prompt string) (text, sid, resultSID string) {
		if err := s.Send(prompt, "", nil, nil); err != nil {
			t.Fatalf("Send: %v", err)
		}
		deadline := time.After(5 * time.Second)
		for {
			select {
			case ev := <-s.Events():
				switch ev.Type {
				case core.EventText:
					text += ev.Content
					if ev.SessionID != "" {
						sid = ev.SessionID
					}
				case core.EventError:
					t.Fatalf("unexpected error: %v", ev.Error)
				case core.EventResult:
					return text, sid, ev.SessionID
				}
			case <-deadline:
				t.Fatal("timeout")
			}
		}
	}
	text, sid, resultSID := turn("first")
	if text != "hello\n" || sid != "conv-1" || resultSID != "conv-1" {
		t.Fatalf("first turn: text=%q sid=%q result=%q", text, sid, resultSID)
	}
	if _, sid, _ := turn("second"); sid != "" {
		t.Errorf("second turn re-announced the same id %q", sid)
	}
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	runs := strings.Split(string(data), "---\n")
	if strings.Contains(runs[0], "--conversation") {
		t.Errorf("first turn should not resume: %q", runs[0])
	}
	if !strings.Contains(runs[1], "--conversation\nconv-1\n") {
		t.Errorf("second turn should resume conv-1: %q", runs[1])
	}
}

// A failed turn reports agy's status together with its stderr, and a
// process killed mid-line does not leak the cut-off JSON into the reply.
func TestFailedResultReportsStatusAndStderr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	cmdPath := filepath.Join(dir, "fake-agy.sh")
	script := "#!/bin/sh\n" +
		`echo '{"event":"init","conversation_id":"conv-2","init":{}}'` + "\n" +
		`echo 'quota exhausted' >&2` + "\n" +
		`echo '{"event":"result","result":{"conversation_id":"conv-2","status":"ERROR"}}'` + "\n" +
		`printf '{"event":"step_update","step_update":{"step_type":"agent_re'` + "\n"
	if err := os.WriteFile(cmdPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := newAntigravitySession(context.Background(), cmdPath, nil, dir, "", "yolo", "", nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Send("hi", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	var text, errMsg string
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			switch ev.Type {
			case core.EventText:
				text += ev.Content
			case core.EventError:
				errMsg = ev.Error.Error()
			case core.EventResult:
				if text != "" {
					t.Errorf("text = %q, want nothing", text)
				}
				if !strings.Contains(errMsg, "ERROR") || !strings.Contains(errMsg, "quota exhausted") {
					t.Errorf("error = %q, want status and stderr", errMsg)
				}
				return
			}
		case <-deadline:
			t.Fatal("timeout")
		}
	}
}

// A turn that wrote its reply and then ended with a failed status keeps the
// reply and shows the error under it instead of losing the reply.
func TestFailedResultAfterReplyKeepsReply(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	cmdPath := filepath.Join(dir, "fake-agy.sh")
	script := "#!/bin/sh\n" +
		`echo '{"event":"init","conversation_id":"conv-3","init":{}}'` + "\n" +
		`echo '{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"逛完了"}}'` + "\n" +
		`echo 'root agent idle; waiting up to 30m0s for 1 background task(s)' >&2` + "\n" +
		`echo '{"event":"result","result":{"conversation_id":"conv-3","status":"ERROR"}}'` + "\n"
	if err := os.WriteFile(cmdPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := newAntigravitySession(context.Background(), cmdPath, nil, dir, "", "yolo", "", nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Send("hi", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	var text string
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			switch ev.Type {
			case core.EventText:
				text += ev.Content
			case core.EventError:
				t.Fatalf("got EventError %v, want the reply kept", ev.Error)
			case core.EventResult:
				if !strings.HasPrefix(text, "逛完了") || !strings.Contains(text, "ERROR") || !strings.Contains(text, "background task") {
					t.Errorf("text = %q, want reply then status and stderr", text)
				}
				return
			}
		case <-deadline:
			t.Fatal("timeout")
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

// A tool step is reported once, from its ACTIVE line, so core sees activity
// while agy runs tools; the DONE line for the same step yields nothing.
// Lines are from agy 1.4.0 stream-json output.
func TestParseStreamLineToolStep(t *testing.T) {
	as := &antigravitySession{}
	active := `{"event":"step_update","step_update":{"conversation_id":"978fd8b4-758b-4db4-ba69-5e2d5909c70f","step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"sleep 3 \u0026\u0026 ls /tmp/agytest"}}}}`
	done := `{"event":"step_update","step_update":{"conversation_id":"978fd8b4-758b-4db4-ba69-5e2d5909c70f","step_index":2,"state":"DONE","step_type":"tool","tool_name":"run_command","duration_seconds":3.168968,"tool_info":{"name":"run_command","parameters":{"CommandLine":"sleep 3 \u0026\u0026 ls /tmp/agytest"},"output":"err.txt\t\tstream.jsonl\r\n"}}}`

	ev, ok := as.parseStreamLine([]byte(active + "\n"))
	if !ok {
		t.Fatal("ACTIVE tool line produced no event")
	}
	if ev.Type != core.EventToolUse || ev.ToolName != "run_command" || ev.Content != "" {
		t.Fatalf("ACTIVE tool line: got %+v", ev)
	}
	if want := `{"CommandLine":"sleep 3 \u0026\u0026 ls /tmp/agytest"}`; ev.ToolInput != want {
		t.Errorf("ToolInput = %q, want %q", ev.ToolInput, want)
	}

	if ev, ok := as.parseStreamLine([]byte(done + "\n")); ok {
		t.Errorf("DONE tool line should produce no event, got %+v", ev)
	}
}
