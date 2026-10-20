package cursor

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

//go:embed testdata/stream_partial.jsonl
var streamPartialFixture string

//go:embed testdata/stream_tools.jsonl
var streamToolsFixture string

func TestCursorSessionImplementsModelDisplayName(t *testing.T) {
	var _ core.ModelDisplayNameReporter = (*cursorSession)(nil)
}

func writeFakeAgent(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent")
	script := "#!/bin/sh\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func startFakeSession(t *testing.T, script string) *cursorSession {
	t.Helper()
	bin := writeFakeAgent(t, script)
	cs, err := newCursorSession(context.Background(), bin, nil, t.TempDir(), "", "default", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func collectUntilTerminal(t *testing.T, cs *cursorSession) []core.Event {
	t.Helper()
	var out []core.Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt, ok := <-cs.Events():
			if !ok {
				t.Fatal("events closed before terminal event")
			}
			out = append(out, evt)
			if evt.Type == core.EventResult || evt.Type == core.EventError {
				return out
			}
		case <-deadline:
			t.Fatalf("timed out waiting for terminal event, got %d events", len(out))
		}
	}
}

func TestSessionParity_InitModelUsageAndPartialText(t *testing.T) {
	script := `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"s1","model":"Grok 4.7 High"}'
printf '%s\n' '{"type":"assistant","model_call_id":"c1","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}]}}'
printf '%s\n' '{"type":"assistant","model_call_id":"c1","message":{"role":"assistant","content":[{"type":"text","text":"Hello world"}]}}'
printf '%s\n' '{"type":"assistant","model_call_id":"c1","message":{"role":"assistant","content":[{"type":"text","text":"Hello world"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"Hello world","session_id":"s1","usage":{"inputTokens":10,"outputTokens":2,"cacheReadTokens":3,"cacheWriteTokens":4}}'
`
	cs := startFakeSession(t, script)
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	evts := collectUntilTerminal(t, cs)
	if cs.GetModelDisplayName() != "Grok 4.7 High" {
		t.Fatalf("display name = %q", cs.GetModelDisplayName())
	}
	var texts []string
	var result *core.Event
	for i := range evts {
		switch evts[i].Type {
		case core.EventText:
			if evts[i].Content != "" {
				texts = append(texts, evts[i].Content)
			}
		case core.EventResult:
			result = &evts[i]
		}
	}
	if len(texts) != 3 || texts[0] != "Hello" || texts[1] != "Hello world" || texts[2] != "Hello world" {
		t.Fatalf("streamed texts = %#v", texts)
	}
	if result == nil || result.Content != "Hello world" || !result.Done {
		t.Fatalf("result = %+v", result)
	}
	if result.InputTokens != 10 || result.OutputTokens != 2 || result.CacheReadInputTokens != 3 || result.CacheCreationInputTokens != 4 {
		t.Fatalf("usage = %+v", result)
	}
	if result.Content != "Hello world" {
		t.Fatalf("result content = %q", result.Content)
	}
}

func TestSessionParity_ToolResultOnce(t *testing.T) {
	script := `
printf '%s\n' '{"type":"tool_call","subtype":"started","call_id":"t1","tool_call":{"shellToolCall":{"args":{"command":"ls"}}}}'
printf '%s\n' '{"type":"tool_call","subtype":"started","call_id":"t1","tool_call":{"shellToolCall":{"args":{"command":"ls"}}}}'
printf '%s\n' '{"type":"tool_call","subtype":"completed","call_id":"t1","tool_call":{"shellToolCall":{"args":{"command":"ls"},"result":{"success":{"stdout":"a.go\n","exitCode":0}}}}}'
printf '%s\n' '{"type":"tool_call","subtype":"completed","call_id":"t1","tool_call":{"shellToolCall":{"args":{"command":"ls"},"result":{"success":{"stdout":"a.go\n","exitCode":0}}}}}'
printf '%s\n' '{"type":"result","is_error":false,"result":"done","session_id":"s1"}'
`
	cs := startFakeSession(t, script)
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	evts := collectUntilTerminal(t, cs)
	var uses, results int
	for _, evt := range evts {
		if evt.Type == core.EventToolUse {
			uses++
			if evt.ToolName != "Bash" || evt.ToolInput != "ls" {
				t.Fatalf("tool use = %+v", evt)
			}
		}
		if evt.Type == core.EventToolResult {
			results++
			if evt.ToolName != "Bash" || evt.ToolResult != "a.go" {
				t.Fatalf("tool result = %+v", evt)
			}
			if evt.ToolSuccess == nil || !*evt.ToolSuccess || evt.ToolExitCode == nil || *evt.ToolExitCode != 0 {
				t.Fatalf("tool status = %+v", evt)
			}
		}
	}
	if uses != 1 || results != 1 {
		t.Fatalf("uses=%d results=%d events=%+v", uses, results, evts)
	}
}

func TestSessionParity_IsErrorResult(t *testing.T) {
	script := `printf '%s\n' '{"type":"result","subtype":"error","is_error":true,"result":"boom","session_id":"s1"}'`
	cs := startFakeSession(t, script)
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	evts := collectUntilTerminal(t, cs)
	n := 0
	for _, evt := range evts {
		if evt.Type == core.EventResult {
			t.Fatalf("is_error must not emit EventResult: %+v", evt)
		}
		if evt.Type == core.EventError {
			n++
			if evt.Error == nil || evt.Error.Error() != "boom" {
				t.Fatalf("error = %+v", evt.Error)
			}
		}
	}
	if n != 1 {
		t.Fatalf("error events = %d", n)
	}
}

func TestSessionParity_NonZeroEmptyStderr(t *testing.T) {
	cs := startFakeSession(t, "exit 1\n")
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	evts := collectUntilTerminal(t, cs)
	if len(evts) != 1 || evts[0].Type != core.EventError || evts[0].Error == nil {
		t.Fatalf("events = %+v", evts)
	}
}

func TestSessionParity_ZeroExitNoResult(t *testing.T) {
	cs := startFakeSession(t, "exit 0\n")
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	evts := collectUntilTerminal(t, cs)
	if len(evts) != 1 || evts[0].Type != core.EventError || evts[0].Error == nil || !strings.Contains(evts[0].Error.Error(), "without a result") {
		t.Fatalf("events = %+v", evts)
	}
}

func TestSessionParity_ResultThenNonZeroDoesNotDoubleTerminate(t *testing.T) {
	script := `
printf '%s\n' '{"type":"result","is_error":false,"result":"ok","session_id":"s1"}'
exit 1
`
	cs := startFakeSession(t, script)
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	evts := collectUntilTerminal(t, cs)
	if len(evts) != 1 || evts[0].Type != core.EventResult || evts[0].Content != "ok" {
		t.Fatalf("events = %+v", evts)
	}
}

func TestSessionParity_ConcurrentSendRejected(t *testing.T) {
	cs := startFakeSession(t, "sleep 2\nprintf '%s\\n' '{\"type\":\"result\",\"result\":\"ok\"}'\n")
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	err := cs.Send("again", "m2", nil, nil)
	if err == nil {
		t.Fatal("expected concurrent Send to fail")
	}
}

func TestSessionParity_SendAfterClose(t *testing.T) {
	cs, err := newCursorSession(context.Background(), "agent", nil, t.TempDir(), "", "default", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cs.Send("hi", "m1", nil, nil); err == nil {
		t.Fatal("expected Send after Close to fail")
	}
}

func TestSessionParity_CloseDuringSendNoHang(t *testing.T) {
	cs := startFakeSession(t, "sleep 30\n")
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = cs.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung")
	}
}

func TestSessionParity_NextSendBeforeDelayedExit(t *testing.T) {
	script := `
printf '%s\n' '{"type":"result","is_error":false,"result":"ok","session_id":"s1"}'
sleep 3
`
	cs := startFakeSession(t, script)
	start := time.Now()
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	evts := collectUntilTerminal(t, cs)
	if evts[len(evts)-1].Type != core.EventResult || evts[len(evts)-1].Content != "ok" {
		t.Fatalf("events = %+v", evts)
	}
	if err := cs.Send("next", "m2", nil, nil); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("next Send waited for process exit: %s", time.Since(start))
	}
}

func TestSessionParity_ConcurrentClose(t *testing.T) {
	cs := startFakeSession(t, "sleep 30\n")
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- cs.Close()
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionParity_ScannerErrorDoesNotHang(t *testing.T) {
	script := `
dd if=/dev/zero bs=1024 count=11000 2>/dev/null
sleep 30
`
	cs := startFakeSession(t, script)
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	evts := collectUntilTerminal(t, cs)
	if evts[len(evts)-1].Type != core.EventError {
		t.Fatalf("events = %+v", evts)
	}
}

func TestSessionParity_RepeatedToolsWithoutID(t *testing.T) {
	script := `
printf '%s\n' '{"type":"tool_call","subtype":"started","tool_call":{"shellToolCall":{"args":{"command":"ls"}}}}'
printf '%s\n' '{"type":"tool_call","subtype":"completed","tool_call":{"shellToolCall":{"args":{"command":"ls"},"result":{"success":{"stdout":"one","exitCode":0}}}}}'
printf '%s\n' '{"type":"tool_call","subtype":"started","tool_call":{"shellToolCall":{"args":{"command":"ls"}}}}'
printf '%s\n' '{"type":"tool_call","subtype":"completed","tool_call":{"shellToolCall":{"toolCallId":"nested-2","args":{"command":"ls"},"result":{"success":{"stdout":"two","exitCode":0}}}}}'
printf '%s\n' '{"type":"tool_call","subtype":"started","tool_call":{"shellToolCall":{"toolCallId":"nested-2","args":{"command":"ls"}}}}'
printf '%s\n' '{"type":"result","is_error":false,"result":"done","session_id":"s1"}'
`
	cs := startFakeSession(t, script)
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	evts := collectUntilTerminal(t, cs)
	var uses int
	var results []string
	for _, evt := range evts {
		if evt.Type == core.EventToolUse {
			uses++
		}
		if evt.Type == core.EventToolResult {
			results = append(results, evt.ToolResult)
		}
	}
	if uses != 2 || len(results) != 2 || results[0] != "one" || results[1] != "two" {
		t.Fatalf("uses=%d results=%#v", uses, results)
	}
}

func TestSessionParity_RepeatedTextBlocks(t *testing.T) {
	script := `
printf '%s\n' '{"type":"assistant","model_call_id":"c1","message":{"role":"assistant","content":[{"type":"text","text":"Hi"},{"type":"text","text":"Hi"}]}}'
printf '%s\n' '{"type":"result","is_error":false,"result":"HiHi","session_id":"s1"}'
`
	cs := startFakeSession(t, script)
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	evts := collectUntilTerminal(t, cs)
	var texts []string
	var result string
	for _, evt := range evts {
		if evt.Type == core.EventText && evt.Content != "" {
			texts = append(texts, evt.Content)
		}
		if evt.Type == core.EventResult {
			result = evt.Content
		}
	}
	if len(texts) != 2 || texts[0] != "Hi" || texts[1] != "Hi" {
		t.Fatalf("texts = %#v", texts)
	}
	if result != "HiHi" {
		t.Fatalf("result content = %q", result)
	}
}

func TestSessionParity_CancelClearsPendingPermission(t *testing.T) {
	script := `
printf '%s\n' '{"type":"interaction_query","subtype":"request","query_type":"shellRequestQuery","query":{"id":7,"shellRequestQuery":{"args":{"command":"ls"},"skipApproval":false}}}'
sleep 30
`
	cs := startFakeSession(t, script)
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case evt := <-cs.Events():
			if evt.Type == core.EventPermissionRequest {
				goto got
			}
		case <-deadline:
			t.Fatal("no permission event")
		}
	}
got:
	_ = cs.Close()
	if err := cs.RespondPermission("shellRequestQuery:7", core.PermissionResult{Behavior: "allow"}); err != nil {
		t.Fatal(err)
	}
}

func replayFixture(t *testing.T, body string) *cursorSession {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return startFakeSession(t, "cat "+strconv.Quote(path)+"\n")
}

func TestSessionParity_PartialStreamFixture(t *testing.T) {
	cs := replayFixture(t, streamPartialFixture)
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	evts := collectUntilTerminal(t, cs)
	var text strings.Builder
	var result *core.Event
	for i := range evts {
		if evts[i].Type == core.EventText && evts[i].Content != "" {
			text.WriteString(evts[i].Content)
		}
		if evts[i].Type == core.EventResult {
			result = &evts[i]
		}
	}
	if result == nil {
		t.Fatal("missing result")
	}
	want := "ABCDEFGHIJKLMNOPQRSTUVWXYZ\nABCDEFGHIJKLMNOPQRSTUVWXYZ"
	if text.String() != want || result.Content != want {
		t.Fatalf("stream=%q result=%q", text.String(), result.Content)
	}
	if cs.GetModelDisplayName() != "Grok 4.7  Low" {
		t.Fatalf("model = %q", cs.GetModelDisplayName())
	}
	if result.InputTokens != 11347 || result.OutputTokens != 53 || result.CacheReadInputTokens != 5888 || result.CacheCreationInputTokens != 0 {
		t.Fatalf("usage = %+v", result)
	}
}

func TestSessionParity_MultiToolStreamEqualsResult(t *testing.T) {
	cs := replayFixture(t, streamToolsFixture)
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	evts := collectUntilTerminal(t, cs)
	var text strings.Builder
	var result string
	for _, evt := range evts {
		if evt.Type == core.EventText && evt.Content != "" {
			text.WriteString(evt.Content)
		}
		if evt.Type == core.EventResult {
			result = evt.Content
		}
	}
	if result != "BEFORE_TOOLAFTER_TOOL" || text.String() != result {
		t.Fatalf("streamed=%q result=%q", text.String(), result)
	}
}

func TestSessionParity_StartFailureDoesNotLeakWait(t *testing.T) {
	cs, err := newCursorSession(context.Background(), "/no/such/cursor-agent", nil, filepath.Join(t.TempDir(), "missing"), "", "default", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = cs.Send("hi", "m1", nil, nil)
	if err == nil {
		t.Fatal("expected start failure")
	}
	err2 := cs.Send("retry", "m2", nil, nil)
	if err2 == nil {
		t.Fatal("expected retry start failure")
	}
	if strings.Contains(err2.Error(), "turn already in progress") {
		t.Fatalf("retry stuck in turn: %v", err2)
	}
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("Close waited on leaked WaitGroup: %s", time.Since(start))
	}
}

func TestSessionParity_CloseKillsProcessTree(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "child.pid")
	script := fmt.Sprintf(`
printf '%%s\n' '{"type":"result","is_error":false,"result":"ok","session_id":"s1"}'
sleep 60 &
echo $! > %q
wait
`, pidfile)
	cs := startFakeSession(t, script)
	if err := cs.Send("hi", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	evts := collectUntilTerminal(t, cs)
	if evts[len(evts)-1].Type != core.EventResult || evts[len(evts)-1].Content != "ok" {
		t.Fatalf("events = %+v", evts)
	}
	var pid int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(pidfile)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("child pid not recorded")
	}
	start := time.Now()
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("Close slow: %s", time.Since(start))
	}
	select {
	case _, ok := <-cs.Events():
		if ok {
			t.Fatal("events channel still open")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("events channel not closed")
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild %d still running", pid)
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil || proc.Signal(syscall.Signal(0)) != nil {
		return false
	}
	if runtime.GOOS == "linux" {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if os.IsNotExist(err) {
			return false
		}
		if err == nil {
			// A killed orphan can remain as a zombie until its adoptive parent
			// reaps it. PID 0-signalling succeeds, but it cannot execute work.
			end := strings.LastIndexByte(string(stat), ')')
			if end >= 0 && len(stat) > end+2 && stat[end+2] == 'Z' {
				return false
			}
		}
	}
	return true
}
