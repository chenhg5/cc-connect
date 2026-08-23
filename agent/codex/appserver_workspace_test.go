package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestWorkspaceAppServerUsesOneConnectionAndIsolatesThreads(t *testing.T) {
	agent, logPath := newWorkspaceFakeAppServerAgent(t)
	t.Cleanup(func() {
		if err := agent.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	threads, err := agent.ListNativeThreads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 || threads[0].ID != "thread-1" || threads[1].ID != "thread-2" {
		t.Fatalf("paginated threads = %#v", threads)
	}
	detail, err := agent.ReadNativeThread(context.Background(), "thread-1")
	if err != nil || len(detail.Turns) != 1 || len(detail.Turns[0].Items) != 2 {
		t.Fatalf("ReadNativeThread() = %#v, %v", detail, err)
	}
	created, err := agent.StartNativeThread(context.Background(), "named thread")
	if err != nil || created.ID != "thread-new" || created.Name != "named thread" {
		t.Fatalf("StartNativeThread() = %#v, %v", created, err)
	}

	sessionOneRaw, err := agent.StartSession(context.Background(), "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	sessionOneAgain, err := agent.StartSession(context.Background(), "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if sessionOneRaw != sessionOneAgain {
		t.Fatal("the same thread did not reuse its logical session")
	}
	sessionTwo, err := agent.StartSession(context.Background(), "thread-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := sessionOneRaw.Send("first", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := sessionTwo.Send("second", "m2", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := collectAppServerText(t, sessionOneRaw.Events()); got != "reply:thread-1:first" {
		t.Fatalf("thread-1 reply = %q", got)
	}
	if got := collectAppServerText(t, sessionTwo.Events()); got != "reply:thread-2:second" {
		t.Fatalf("thread-2 reply = %q", got)
	}

	if err := sessionOneRaw.Send("approve", "m3", nil, nil); err != nil {
		t.Fatal(err)
	}
	var approval core.Event
	select {
	case approval = <-sessionOneRaw.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("thread-1 did not receive approval")
	}
	if approval.Type != core.EventPermissionRequest || !strings.Contains(approval.ToolInput, "thread-1") {
		t.Fatalf("approval = %#v", approval)
	}
	select {
	case event := <-sessionTwo.Events():
		t.Fatalf("thread-2 received thread-1 event: %#v", event)
	case <-time.After(50 * time.Millisecond):
	}
	if err := sessionOneRaw.RespondPermission(approval.RequestID, core.PermissionResult{Behavior: "allow"}); err != nil {
		t.Fatal(err)
	}
	if got := collectAppServerText(t, sessionOneRaw.Events()); got != "approved:thread-1" {
		t.Fatalf("approved reply = %q", got)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(data)
	if strings.Count(logText, "initialize\n") != 1 {
		t.Fatalf("physical initialize count = %d, log:\n%s", strings.Count(logText, "initialize\n"), logText)
	}
	if strings.Count(logText, "thread/list\n") != 2 || !strings.Contains(logText, "approval-response:approval-thread-1") {
		t.Fatalf("fake server log:\n%s", logText)
	}

	if err := sessionOneRaw.Send("hold", "m4", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := sessionOneRaw.Close(); err != nil {
		t.Fatal(err)
	}
	if !sessionTwo.Alive() {
		t.Fatal("closing one logical session closed another thread")
	}
	data, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "turn/interrupt:thread-1:turn-thread-1") {
		t.Fatalf("logical close did not interrupt the active turn, log:\n%s", data)
	}
}

func TestWorkspaceAppServerReconnectsAfterDisconnect(t *testing.T) {
	agent, logPath := newWorkspaceFakeAppServerAgent(t)
	t.Cleanup(func() {
		if err := agent.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	session, err := agent.StartSession(context.Background(), "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Send("disconnect", "m1", nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("disconnected logical session did not terminate")
	}
	deadline := time.Now().Add(2 * time.Second)
	for session.Alive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if session.Alive() {
		t.Fatal("logical session remained alive after transport disconnect")
	}
	if _, err := agent.ListNativeThreads(context.Background()); err != nil {
		t.Fatalf("ListNativeThreads after reconnect: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "initialize\n"); got != 2 {
		t.Fatalf("initialize count after reconnect = %d, log:\n%s", got, data)
	}
}

func TestAppServerLogicalSessionKeepsEventsChannelAfterClose(t *testing.T) {
	session := &appServerSession{
		events:           make(chan core.Event, 1),
		pendingApprovals: make(map[string]chan core.PermissionResult),
	}
	session.alive.Store(true)

	before := session.Events()
	session.closeLogical()
	after := session.Events()
	if after != before {
		t.Fatal("Events channel changed while the logical session was closing")
	}
	if _, ok := <-after; ok {
		t.Fatal("Events channel remains open after the logical session closes")
	}
}

func collectAppServerText(t *testing.T, events <-chan core.Event) string {
	t.Helper()
	var text string
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("event stream closed before result")
			}
			if event.Type == core.EventText {
				text += event.Content
			}
			if event.Type == core.EventResult {
				return text
			}
			if event.Type == core.EventError {
				t.Fatalf("agent event error: %v", event.Error)
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn result")
		}
	}
}

func newWorkspaceFakeAppServerAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "fake-codex")
	scriptBody := fmt.Sprintf("#!/bin/sh\nexec %q -test.run '^TestWorkspaceFakeAppServerProcess$' -- \"$@\"\n", executable)
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "requests.log")
	return &Agent{
		workDir: dir, backend: "app_server", appServerURL: "stdio://", mode: "suggest",
		cmd: script, activeIdx: -1,
		configEnv: []string{"CC_WORKSPACE_FAKE_SERVER=1", "CC_WORKSPACE_FAKE_LOG=" + logPath, "CC_WORKSPACE_FAKE_CWD=" + dir},
	}, logPath
}

func TestWorkspaceFakeAppServerProcess(t *testing.T) {
	if os.Getenv("CC_WORKSPACE_FAKE_SERVER") != "1" {
		return
	}
	logPath := os.Getenv("CC_WORKSPACE_FAKE_LOG")
	cwd := os.Getenv("CC_WORKSPACE_FAKE_CWD")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	defer func() { _ = logFile.Close() }()
	writer := bufio.NewWriter(os.Stdout)
	write := func(value any) {
		data, _ := json.Marshal(value)
		_, _ = writer.Write(append(data, '\n'))
		_ = writer.Flush()
	}
	logMethod := func(method string) {
		_, _ = fmt.Fprintln(logFile, method)
		_ = logFile.Sync()
	}
	pendingApprovals := make(map[string]string)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request map[string]json.RawMessage
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		var method string
		_ = json.Unmarshal(request["method"], &method)
		if method == "" {
			var approvalID string
			_ = json.Unmarshal(request["id"], &approvalID)
			if threadID := pendingApprovals[approvalID]; threadID != "" {
				logMethod("approval-response:" + approvalID)
				delete(pendingApprovals, approvalID)
				fakeAppServerCompleteTurn(write, threadID, "approved:"+threadID)
			}
			continue
		}
		logMethod(method)
		if method == "initialized" {
			continue
		}
		id := request["id"]
		respond := func(result any) { write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}) }
		switch method {
		case "initialize":
			respond(map[string]any{"protocolVersion": "test"})
		case "account/rateLimits/read":
			respond(map[string]any{"rateLimits": map[string]any{}})
		case "thread/list":
			var params struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(request["params"], &params)
			if params.Cursor == "" {
				respond(map[string]any{"data": []any{fakeNativeThread("thread-1", cwd)}, "nextCursor": "page-2"})
			} else {
				respond(map[string]any{"data": []any{fakeNativeThread("thread-2", cwd)}, "nextCursor": nil})
			}
		case "thread/read":
			var params struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(request["params"], &params)
			thread := fakeNativeThread(params.ThreadID, cwd)
			thread["turns"] = []any{map[string]any{"id": "turn-history", "status": "completed", "items": []any{map[string]any{"type": "userMessage", "text": "hello"}, map[string]any{"type": "agentMessage", "text": "world"}}}}
			respond(map[string]any{"thread": thread})
		case "thread/start":
			thread := fakeNativeThread("thread-new", cwd)
			respond(map[string]any{"thread": thread, "cwd": cwd})
		case "thread/name/set":
			respond(map[string]any{})
		case "thread/resume":
			var params struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(request["params"], &params)
			respond(map[string]any{"thread": map[string]any{"id": params.ThreadID}, "cwd": cwd})
		case "turn/start":
			var params struct {
				ThreadID string `json:"threadId"`
				Input    []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			_ = json.Unmarshal(request["params"], &params)
			prompt := ""
			if len(params.Input) > 0 {
				prompt = params.Input[0].Text
			}
			respond(map[string]any{"turn": map[string]any{"id": "turn-" + params.ThreadID}})
			write(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": params.ThreadID, "turn": map[string]any{"id": "turn-" + params.ThreadID}}})
			switch prompt {
			case "approve":
				approvalID := "approval-" + params.ThreadID
				pendingApprovals[approvalID] = params.ThreadID
				write(map[string]any{"jsonrpc": "2.0", "id": approvalID, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": params.ThreadID, "command": "echo " + params.ThreadID, "cwd": cwd}})
			case "disconnect":
				return
			case "hold":
				// 保持 Turn 活动，测试逻辑 session Close 发送 turn/interrupt。
			default:
				fakeAppServerCompleteTurn(write, params.ThreadID, "reply:"+params.ThreadID+":"+prompt)
			}
		case "turn/interrupt":
			var params struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
			}
			_ = json.Unmarshal(request["params"], &params)
			logMethod("turn/interrupt:" + params.ThreadID + ":" + params.TurnID)
			respond(map[string]any{})
		default:
			respond(map[string]any{})
		}
	}
}

func fakeNativeThread(threadID, cwd string) map[string]any {
	return map[string]any{"id": threadID, "cwd": cwd, "name": threadID, "preview": threadID, "createdAt": int64(1), "updatedAt": int64(2), "turns": []any{}}
}

func fakeAppServerCompleteTurn(write func(any), threadID, text string) {
	turnID := "turn-" + threadID
	write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": threadID, "turnId": turnID, "item": map[string]any{"type": "agentMessage", "text": text}}})
	write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID, "status": "completed"}}})
}
