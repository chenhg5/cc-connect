package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestNormalizeReasoningEffort_RejectsMinimal(t *testing.T) {
	if got := normalizeReasoningEffort("minimal"); got != "" {
		t.Fatalf("normalizeReasoningEffort(minimal) = %q, want empty", got)
	}
	if got := normalizeReasoningEffort("min"); got != "" {
		t.Fatalf("normalizeReasoningEffort(min) = %q, want empty", got)
	}
}

func TestAvailableReasoningEfforts_ExcludesMinimal(t *testing.T) {
	agent := &Agent{}
	got := agent.AvailableReasoningEfforts()
	want := []string{"low", "medium", "high", "xhigh"}
	if len(got) != len(want) {
		t.Fatalf("AvailableReasoningEfforts len = %d, want %d, got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AvailableReasoningEfforts[%d] = %q, want %q, got=%v", i, got[i], want[i], got)
		}
	}
}

func TestBuildExecArgs_IncludesReasoningEffort(t *testing.T) {
	cs, err := newCodexSession(context.Background(), "/tmp/project", "o3", "high", "full-auto", "", nil)
	if err != nil {
		t.Fatalf("newCodexSession: %v", err)
	}

	args := cs.buildExecArgs("hello", nil)

	want := []string{
		"exec",
		"--json",
		"--skip-git-repo-check",
		"--full-auto",
		"--model",
		"o3",
		"-c",
		`model_reasoning_effort="high"`,
		"--cd",
		"/tmp/project",
		"hello",
	}
	if len(args) != len(want) {
		t.Fatalf("args len = %d, want %d, args=%v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q, args=%v", i, args[i], want[i], args)
		}
	}
}

func TestSend_WithImages_PassesImageArgsAndDefaultPrompt(t *testing.T) {
	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	argsFile := filepath.Join(workDir, "args.txt")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$CODEX_ARGS_FILE\"\n" +
		"printf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"thread-1\"}'\n" +
		"printf '%s\\n' '{\"type\":\"turn.completed\"}'\n"
	scriptPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("CODEX_ARGS_FILE", argsFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cs, err := newCodexSession(context.Background(), workDir, "", "", "", "", nil)
	if err != nil {
		t.Fatalf("newCodexSession: %v", err)
	}
	defer cs.Close()

	img := core.ImageAttachment{
		MimeType: "image/png",
		Data:     []byte("png-bytes"),
		FileName: "sample.png",
	}
	if err := cs.Send("", []core.ImageAttachment{img}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	args := waitForArgsFile(t, argsFile)
	if !containsSequence(args, []string{"exec", "--json", "--skip-git-repo-check"}) {
		t.Fatalf("args missing exec prelude: %v", args)
	}
	imagePath := valueAfter(args, "--image")
	if imagePath == "" {
		t.Fatalf("args missing --image: %v", args)
	}
	if !strings.HasPrefix(imagePath, filepath.Join(workDir, ".cc-connect", "images")+string(filepath.Separator)) {
		t.Fatalf("image path = %q, want under work dir image cache", imagePath)
	}
	data, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatalf("read staged image: %v", err)
	}
	if string(data) != string(img.Data) {
		t.Fatalf("staged image content = %q, want %q", string(data), string(img.Data))
	}
	if got := args[len(args)-1]; got != "Please analyze the attached image(s)." {
		t.Fatalf("prompt = %q, want default image prompt; args=%v", got, args)
	}
}

func TestSend_ResumeWithImages_PlacesSessionBeforeImageFlags(t *testing.T) {
	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	argsFile := filepath.Join(workDir, "args.txt")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$CODEX_ARGS_FILE\"\n" +
		"printf '%s\\n' '{\"type\":\"turn.completed\"}'\n"
	scriptPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("CODEX_ARGS_FILE", argsFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cs, err := newCodexSession(context.Background(), workDir, "", "", "", "thread-123", nil)
	if err != nil {
		t.Fatalf("newCodexSession: %v", err)
	}
	defer cs.Close()

	if err := cs.Send("describe this", []core.ImageAttachment{{MimeType: "image/jpeg", Data: []byte("jpg")}}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	args := waitForArgsFile(t, argsFile)
	if !containsSequence(args, []string{"exec", "resume", "--json", "--skip-git-repo-check"}) {
		t.Fatalf("args missing resume prelude: %v", args)
	}
	tidIndex := indexOf(args, "thread-123")
	imageIndex := indexOf(args, "--image")
	promptIndex := indexOf(args, "describe this")
	if tidIndex == -1 || imageIndex == -1 || promptIndex == -1 {
		t.Fatalf("missing resume/image/prompt args: %v", args)
	}
	if !(tidIndex < imageIndex && imageIndex < promptIndex) {
		t.Fatalf("unexpected arg order: %v", args)
	}
}

func TestSend_HandlesLargeJSONLines(t *testing.T) {
	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	largeText := strings.Repeat("x", 11*1024*1024)
	encodedText, err := json.Marshal(largeText)
	if err != nil {
		t.Fatalf("marshal large text: %v", err)
	}

	payload := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-large"}`,
		`{"type":"item.completed","item":{"type":"agent_message","content":[{"type":"output_text","text":` + string(encodedText) + `}]}}`,
		`{"type":"turn.completed"}`,
	}, "\n") + "\n"

	payloadFile := filepath.Join(workDir, "payload.jsonl")
	if err := os.WriteFile(payloadFile, []byte(payload), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	script := "#!/bin/sh\ncat \"$CODEX_PAYLOAD_FILE\"\n"
	scriptPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("CODEX_PAYLOAD_FILE", payloadFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cs, err := newCodexSession(context.Background(), workDir, "", "", "", "", nil)
	if err != nil {
		t.Fatalf("newCodexSession: %v", err)
	}
	defer cs.Close()

	if err := cs.Send("hello", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var gotTextLen int
	var gotResult bool
	timeout := time.After(5 * time.Second)

	for !gotResult {
		select {
		case evt := <-cs.Events():
			if evt.Type == core.EventError {
				t.Fatalf("unexpected error event: %v", evt.Error)
			}
			if evt.Type == core.EventText {
				gotTextLen = len(evt.Content)
			}
			if evt.Type == core.EventResult && evt.Done {
				gotResult = true
			}
		case <-timeout:
			t.Fatal("timed out waiting for large JSON line events")
		}
	}

	if gotTextLen != len(largeText) {
		t.Fatalf("text len = %d, want %d", gotTextLen, len(largeText))
	}
	if got := cs.CurrentSessionID(); got != "thread-large" {
		t.Fatalf("CurrentSessionID() = %q, want thread-large", got)
	}
}

func waitForArgsFile(t *testing.T, path string) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
			return lines
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for args file: %s", path)
	return nil
}

func containsSequence(args, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for i := 0; i+len(want) <= len(args); i++ {
		match := true
		for j := range want {
			if args[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func valueAfter(args []string, key string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}

func indexOf(args []string, target string) int {
	for i, arg := range args {
		if arg == target {
			return i
		}
	}
	return -1
}
