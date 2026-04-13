package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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
	cs, err := newCodexSession(context.Background(), "/tmp/project", "o3", "high", "full-auto", "", "", nil)
	if err != nil {
		t.Fatalf("newCodexSession: %v", err)
	}

	args := cs.buildExecArgs("hello", nil)

	want := []string{
		"exec",
		"--skip-git-repo-check",
		"--full-auto",
		"--model",
		"o3",
		"-c",
		`model_reasoning_effort="high"`,
		"--json",
		"--cd",
		"/tmp/project",
		"-",
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

func TestBuildExecArgs_IncludesBaseURL(t *testing.T) {
	cs, err := newCodexSession(context.Background(), "/tmp/project", "o3", "high", "full-auto", "", "https://custom.api.example.com", nil)
	if err != nil {
		t.Fatalf("newCodexSession: %v", err)
	}

	args := cs.buildExecArgs("hello", nil)

	if !containsSequence(args, []string{"-c", `openai_base_url="https://custom.api.example.com"`}) {
		t.Fatalf("args missing openai_base_url config flag: %v", args)
	}
}

func TestBuildExecArgs_ResumeOmitsCdFlag(t *testing.T) {
	cs, err := newCodexSession(context.Background(), "/tmp/project", "", "", "full-auto", "thread-abc", "", nil)
	if err != nil {
		t.Fatalf("newCodexSession: %v", err)
	}

	args := cs.buildExecArgs("hello", nil)

	// codex exec resume does not support --cd; verify it's absent.
	for i, arg := range args {
		if arg == "--cd" {
			t.Fatalf("resume args should not contain --cd, but found at index %d: %v", i, args)
		}
	}

	// --json and stdin marker must still be present.
	if !containsSequence(args, []string{"--json", "-"}) {
		t.Fatalf("resume args missing --json + stdin marker: %v", args)
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

	cs, err := newCodexSession(context.Background(), workDir, "", "", "", "", "", nil)
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
	if !containsSequence(args, []string{"exec", "--skip-git-repo-check"}) {
		t.Fatalf("args missing exec prelude: %v", args)
	}
	if !containsSequence(args, []string{"--json", "--cd"}) {
		t.Fatalf("args missing --json --cd sequence: %v", args)
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
	if got := args[len(args)-1]; got != "-" {
		t.Fatalf("last arg = %q, want stdin marker '-'; args=%v", got, args)
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

	cs, err := newCodexSession(context.Background(), workDir, "", "", "", "thread-123", "", nil)
	if err != nil {
		t.Fatalf("newCodexSession: %v", err)
	}
	defer cs.Close()

	if err := cs.Send("describe this", []core.ImageAttachment{{MimeType: "image/jpeg", Data: []byte("jpg")}}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	args := waitForArgsFile(t, argsFile)
	if !containsSequence(args, []string{"exec", "resume", "--skip-git-repo-check"}) {
		t.Fatalf("args missing resume prelude: %v", args)
	}
	tidIndex := indexOf(args, "thread-123")
	imageIndex := indexOf(args, "--image")
	jsonIndex := indexOf(args, "--json")
	promptIndex := indexOf(args, "-")
	if tidIndex == -1 || imageIndex == -1 || jsonIndex == -1 || promptIndex == -1 {
		t.Fatalf("missing resume/image/json/stdin args: %v", args)
	}
	// Verify order: thread-id -> --image -> --json -> --cd -> prompt
	if !(tidIndex < imageIndex && imageIndex < jsonIndex && jsonIndex < promptIndex) {
		t.Fatalf("unexpected arg order: %v", args)
	}
}

func TestSend_UsesStdinForMultilinePrompt(t *testing.T) {
	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	argsFile := filepath.Join(workDir, "args.txt")
	stdinFile := filepath.Join(workDir, "stdin.txt")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$CODEX_ARGS_FILE\"\n" +
		"cat > \"$CODEX_STDIN_FILE\"\n" +
		"printf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"thread-stdin\"}'\n" +
		"printf '%s\\n' '{\"type\":\"turn.completed\"}'\n"
	scriptPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("CODEX_ARGS_FILE", argsFile)
	t.Setenv("CODEX_STDIN_FILE", stdinFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cs, err := newCodexSession(context.Background(), workDir, "", "", "", "thread-stdin", "", nil)
	if err != nil {
		t.Fatalf("newCodexSession: %v", err)
	}
	defer cs.Close()

	prompt := "line1\nline2"
	if err := cs.Send(prompt, nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	args := waitForArgsFile(t, argsFile)
	if !containsSequence(args, []string{"--json", "-"}) {
		t.Fatalf("args missing stdin marker: %v", args)
	}

	// cat > file creates the path before stdin is fully read; polling until
	// content matches avoids racing an empty read (flaky under -cover / CI).
	waitForFileEquals(t, stdinFile, prompt)
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

	cs, err := newCodexSession(context.Background(), workDir, "", "", "", "", "", nil)
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

func TestWaitForArgsFile_WaitsForNonEmptyContent(t *testing.T) {
	workDir := t.TempDir()
	argsFile := filepath.Join(workDir, "args.txt")

	if err := os.WriteFile(argsFile, []byte(""), 0o644); err != nil {
		t.Fatalf("write empty args file: %v", err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = os.WriteFile(argsFile, []byte("exec\n--json\n"), 0o644)
	}()

	args := waitForArgsFile(t, argsFile)
	if !containsSequence(args, []string{"exec", "--json"}) {
		t.Fatalf("expected non-empty args sequence, got: %v", args)
	}
}

func waitForArgsFile(t *testing.T, path string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			text := strings.TrimSpace(string(data))
			if text != "" {
				lines := strings.Split(text, "\n")
				args := make([]string, 0, len(lines))
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if line != "" {
						args = append(args, line)
					}
				}
				if len(args) > 0 {
					return args
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for non-empty args file: %s", path)
	return nil
}

func waitForFileEquals(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && string(data) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, _ := os.ReadFile(path)
	t.Fatalf("stdin file %s: got %q, want %q", path, string(data), want)
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

func TestCodexSession_ContinueSessionTreatedAsFresh(t *testing.T) {
	s, err := newCodexSession(context.Background(), "/tmp", "", "", "full-auto", core.ContinueSession, "", nil)
	if err != nil {
		t.Fatalf("newCodexSession: %v", err)
	}
	defer s.Close()

	if got := s.CurrentSessionID(); got != "" {
		t.Errorf("ContinueSession should be treated as fresh: threadID = %q, want empty", got)
	}
}

func TestClose_ForceKillsProcessGroupAfterGracefulTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group semantics differ on windows")
	}

	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	script := "#!/bin/sh\n" +
		"printf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"thread-close\"}'\n" +
		"(sleep 0.12; printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"late child output\"}}'; sleep 30) &\n" +
		"wait\n"
	scriptPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	oldCloseTimeout := codexSessionCloseTimeout
	oldForceKillWait := codexSessionForceKillWait
	codexSessionCloseTimeout = 50 * time.Millisecond
	codexSessionForceKillWait = 500 * time.Millisecond
	t.Cleanup(func() {
		codexSessionCloseTimeout = oldCloseTimeout
		codexSessionForceKillWait = oldForceKillWait
	})

	cs, err := newCodexSession(context.Background(), workDir, "", "", "", "", "", nil)
	if err != nil {
		t.Fatalf("newCodexSession: %v", err)
	}

	if err := cs.Send("hello", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	waitForThreadID(t, cs, "thread-close")

	closeStarted := time.Now()
	if err := cs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed := time.Since(closeStarted); elapsed > time.Second {
		t.Fatalf("Close took too long after force kill: %v", elapsed)
	}

	select {
	case evt, ok := <-cs.Events():
		if ok {
			t.Fatalf("unexpected event after Close: %#v", evt)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("timed out waiting for events channel to close")
	}
}

func TestClose_ForceKillsAllTrackedProcessesAfterCmdOverwrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group semantics differ on windows")
	}

	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	startsFile := filepath.Join(workDir, "starts.txt")
	// Prompt is passed on stdin (--json -), not as a trailing argv argument.
	script := "#!/bin/sh\n" +
		"prompt=$(cat)\n" +
		"printf '%s\\n' \"$prompt\" >> \"$CODEX_STARTS_FILE\"\n" +
		"if [ \"$prompt\" = \"first\" ]; then\n" +
		"  printf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"thread-overlap\"}'\n" +
		"  printf '%s\\n' '{\"type\":\"turn.completed\"}'\n" +
		"fi\n" +
		"sleep 30\n"
	scriptPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("CODEX_STARTS_FILE", startsFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	oldCloseTimeout := codexSessionCloseTimeout
	oldForceKillWait := codexSessionForceKillWait
	codexSessionCloseTimeout = 50 * time.Millisecond
	codexSessionForceKillWait = 500 * time.Millisecond
	t.Cleanup(func() {
		codexSessionCloseTimeout = oldCloseTimeout
		codexSessionForceKillWait = oldForceKillWait
	})

	cs, err := newCodexSession(context.Background(), workDir, "", "", "", "", "", nil)
	if err != nil {
		t.Fatalf("newCodexSession: %v", err)
	}

	if err := cs.Send("first", nil, nil); err != nil {
		t.Fatalf("Send(first): %v", err)
	}
	waitForThreadID(t, cs, "thread-overlap")
	waitForDoneResult(t, cs.Events())

	if err := cs.Send("second", nil, nil); err != nil {
		t.Fatalf("Send(second): %v", err)
	}
	waitForFileLines(t, startsFile, 2)

	closeStarted := time.Now()
	if err := cs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed := time.Since(closeStarted); elapsed > time.Second {
		t.Fatalf("Close took too long after force killing tracked processes: %v", elapsed)
	}

	select {
	case evt, ok := <-cs.Events():
		if ok {
			t.Fatalf("unexpected event after Close: %#v", evt)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("timed out waiting for events channel to close")
	}
}

func waitForThreadID(t *testing.T, cs *codexSession, want string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case <-time.After(10 * time.Millisecond):
			if cs.CurrentSessionID() == want {
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for thread id %q", want)
		}
	}
}

func waitForDoneResult(t *testing.T, events <-chan core.Event) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				t.Fatal("events channel closed before done result")
			}
			if evt.Type == core.EventError {
				t.Fatalf("unexpected error event: %v", evt.Error)
			}
			if evt.Type == core.EventResult && evt.Done {
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for done result")
		}
	}
}

func waitForFileLines(t *testing.T, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			count := 0
			for _, line := range lines {
				if strings.TrimSpace(line) != "" {
					count++
				}
			}
			if count >= want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d lines in %s", want, path)
}
