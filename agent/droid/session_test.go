package droid

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.AgentSession = (*droidSession)(nil)

func TestDroidSessionProcess(t *testing.T) {
	if os.Getenv("CC_CONNECT_DROID_HELPER") != "1" {
		return
	}
	scenario := os.Getenv("CC_CONNECT_DROID_SCENARIO")
	switch scenario {
	case "stream":
		fmt.Println(`{"type":"system","subtype":"init","session_id":"session-one"}`)
		fmt.Println(`{"type":"reasoning","text":"Considering the request"}`)
		fmt.Println(`{"type":"tool_call","toolName":"Read","parameters":{"file_path":"notes.txt"}}`)
		fmt.Println(`{"type":"tool_result","toolId":"Read","isError":false,"value":"file contents"}`)
		fmt.Println(`{"type":"tool_result","toolId":"Execute","isError":true,"error":{"type":"tool_error","message":"permission denied"}}`)
		fmt.Println(`{"type":"message","role":"user","text":"not assistant output"}`)
		fmt.Println(`{"type":"message","role":"assistant","text":"Answer"}`)
		fmt.Println(`{"type":"completion","finalText":"Final answer","session_id":"session-one","usage":{"input_tokens":12,"output_tokens":34,"cache_read_input_tokens":56,"cache_creation_input_tokens":78}}`)
	case "error":
		fmt.Println(`{"type":"error","message":"capacity exceeded","session_id":"failed-session"}`)
		os.Exit(9)
	case "failed-result":
		fmt.Println(`{"type":"result","is_error":true,"subtype":"failure","result":"request rejected","session_id":"failed-session"}`)
	case "exit-error":
		fmt.Println(`{"type":"completion","finalText":"not a successful turn","session_id":"failed-session"}`)
		fmt.Fprintln(os.Stderr, "persistence failed")
		os.Exit(9)
	case "hold":
		fmt.Println(`{"type":"system","subtype":"init","session_id":"held-session"}`)
		time.Sleep(time.Minute)
	case "writer":
		lockPath := filepath.Join(os.Getenv("CC_CONNECT_DROID_WORKDIR"), "writer.lock")
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, "overlapping session writer")
			os.Exit(9)
		}
		_ = lock.Close()
		fmt.Println(`{"type":"completion","finalText":"done","session_id":"writer-session"}`)
		time.Sleep(50 * time.Millisecond)
		_ = os.Remove(lockPath)
	case "args":
		result := map[string]any{"type": "completion", "finalText": strings.Join(os.Args[1:], "\n"), "session_id": "args-session"}
		_ = json.NewEncoder(os.Stdout).Encode(result)
	default:
		fmt.Fprintln(os.Stderr, "unknown helper scenario")
		os.Exit(9)
	}
	os.Exit(0)
}

func newTestDroidSession(t *testing.T, scenario, workDir string) *droidSession {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if workDir == "" {
		workDir = t.TempDir()
	}
	session, err := newDroidSession(context.Background(), binary, []string{"-test.run=^TestDroidSessionProcess$", "--"}, workDir, "", "", "", false, "", []string{
		"CC_CONNECT_DROID_HELPER=1",
		"CC_CONNECT_DROID_SCENARIO=" + scenario,
		"CC_CONNECT_DROID_WORKDIR=" + workDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func nextDroidEvent(t *testing.T, session core.AgentSession) core.Event {
	t.Helper()
	select {
	case event, ok := <-session.Events():
		if !ok {
			t.Fatal("session events closed unexpectedly")
		}
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Droid event")
		return core.Event{}
	}
}

func droidTerminalEvent(t *testing.T, session core.AgentSession) core.Event {
	t.Helper()
	for {
		event := nextDroidEvent(t, session)
		if event.Type == core.EventError || event.Type == core.EventResult {
			return event
		}
	}
}

func TestDroidSessionMapsInstalledStreamEvents(t *testing.T) {
	session := newTestDroidSession(t, "stream", "")
	if err := session.Send("hello", "message-one", nil, nil); err != nil {
		t.Fatal(err)
	}
	var events []core.Event
	for {
		event := nextDroidEvent(t, session)
		events = append(events, event)
		if event.Type == core.EventError || event.Type == core.EventResult {
			break
		}
	}
	var types []core.EventType
	for _, event := range events {
		types = append(types, event.Type)
		if event.SessionID != "session-one" {
			t.Fatalf("event session ID = %q", event.SessionID)
		}
	}
	wantTypes := []core.EventType{core.EventText, core.EventThinking, core.EventToolUse, core.EventToolResult, core.EventToolResult, core.EventText, core.EventResult}
	if !reflect.DeepEqual(types, wantTypes) {
		t.Fatalf("event types = %v, want %v", types, wantTypes)
	}
	if events[2].ToolName != "Read" || events[2].ToolInput != `{"file_path":"notes.txt"}` {
		t.Fatalf("tool invocation = %+v", events[2])
	}
	if events[3].ToolResult != "file contents" || events[3].ToolStatus != "completed" || events[3].ToolSuccess == nil || !*events[3].ToolSuccess {
		t.Fatalf("successful tool result = %+v", events[3])
	}
	if events[4].ToolName != "Execute" || events[4].ToolResult != "permission denied" || events[4].ToolStatus != "failed" || events[4].ToolSuccess == nil || *events[4].ToolSuccess {
		t.Fatalf("failed tool result = %+v", events[4])
	}
	result := events[len(events)-1]
	if result.Content != "Final answer" || !result.Done || result.InputTokens != 12 || result.OutputTokens != 34 || result.CacheReadInputTokens != 56 || result.CacheCreationInputTokens != 78 {
		t.Fatalf("terminal result = %+v", result)
	}
	if session.CurrentSessionID() != "session-one" || !session.Alive() {
		t.Fatal("completed session lost its ID or cannot accept another turn")
	}
}

func TestDroidSessionFailedTurnsNeverPublishSuccess(t *testing.T) {
	for _, scenario := range []string{"error", "failed-result", "exit-error"} {
		t.Run(scenario, func(t *testing.T) {
			session := newTestDroidSession(t, scenario, "")
			if err := session.Send("hello", "", nil, nil); err != nil {
				t.Fatal(err)
			}
			event := droidTerminalEvent(t, session)
			if event.Type != core.EventError || event.Error == nil || event.SessionID != "failed-session" {
				t.Fatalf("failed turn = %+v", event)
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			for extra := range session.Events() {
				t.Fatalf("unexpected event after failed turn: %+v", extra)
			}
		})
	}
}

func TestDroidSessionFollowupWaitsForWriterExit(t *testing.T) {
	session := newTestDroidSession(t, "writer", "")
	for i := range 2 {
		if err := session.Send("hello", "", nil, nil); err != nil {
			t.Fatal(err)
		}
		if event := droidTerminalEvent(t, session); event.Type != core.EventResult || event.Content != "done" {
			t.Fatalf("turn %d result = %+v", i, event)
		}
	}
}

func TestDroidSessionRejectsOverlappingTurns(t *testing.T) {
	session := newTestDroidSession(t, "hold", "")
	if err := session.Send("first", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	_ = nextDroidEvent(t, session)
	if err := session.Send("second", "", nil, nil); err == nil {
		t.Fatal("overlapping turn accepted")
	}
}

func TestDroidSessionConcurrentSendAndClose(t *testing.T) {
	session := newTestDroidSession(t, "hold", "")
	if err := session.Send("first", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	_ = nextDroidEvent(t, session)
	var workers sync.WaitGroup
	for range 16 {
		workers.Add(2)
		go func() {
			defer workers.Done()
			_ = session.Send("racing turn", "", nil, nil)
		}()
		go func() {
			defer workers.Done()
			_ = session.Close()
		}()
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent shutdown did not terminate the process")
	}
	if session.Alive() {
		t.Fatal("closed session is alive")
	}
	if err := session.Send("after close", "", nil, nil); err == nil {
		t.Fatal("closed session accepted a prompt")
	}
	for range session.Events() {
	}
}

func TestDroidSessionCloseRacesInitialSend(t *testing.T) {
	for range 8 {
		session := newTestDroidSession(t, "hold", "")
		start := make(chan struct{})
		done := make(chan struct{}, 2)
		go func() {
			<-start
			_ = session.Send("first", "", nil, nil)
			done <- struct{}{}
		}()
		go func() {
			<-start
			_ = session.Close()
			done <- struct{}{}
		}()
		close(start)
		for range 2 {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("initial send/shutdown race did not complete")
			}
		}
		for range session.Events() {
		}
	}
}

func TestDroidSessionStartFailureReleasesTurn(t *testing.T) {
	workDir := t.TempDir()
	session, err := newDroidSession(context.Background(), filepath.Join(workDir, "missing-droid"), nil, workDir, "", "", "", false, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	for range 2 {
		if err := session.Send("hello", "", nil, nil); err == nil || !strings.Contains(err.Error(), "start:") {
			t.Fatalf("failed start did not release turn: %v", err)
		}
	}
}

func TestDroidSessionAttachmentsRemainDistinctAndReadable(t *testing.T) {
	absoluteWorkDir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workDir, err := filepath.Rel(cwd, absoluteWorkDir)
	if err != nil {
		t.Fatal(err)
	}
	session := newTestDroidSession(t, "args", workDir)
	for i, messageID := range []string{"message-one", "message-two"} {
		content := fmt.Sprintf("upload %d", i)
		if err := session.Send("--not-a-cli-option", messageID, []core.ImageAttachment{{MimeType: "image/png", Data: []byte(content)}}, []core.FileAttachment{{FileName: "same.txt", Data: []byte(content)}}); err != nil {
			t.Fatal(err)
		}
		result := droidTerminalEvent(t, session)
		if result.Type != core.EventResult {
			t.Fatalf("attachment turn = %+v", result)
		}
		filePath := filepath.Join(absoluteWorkDir, ".cc-connect", "attachments", messageID, "same.txt")
		if !strings.Contains(result.Content, filePath) {
			t.Fatalf("prompt omitted absolute attachment path: %q", result.Content)
		}
		if !strings.Contains(result.Content, "\n--\n--not-a-cli-option") {
			t.Fatalf("prompt was not separated from CLI flags: %q", result.Content)
		}
		if i == 1 && !strings.Contains(result.Content, "--session-id\nargs-session") {
			t.Fatalf("follow-up omitted session ID: %q", result.Content)
		}
		if !strings.Contains(result.Content, filepath.Join(absoluteWorkDir, ".cc-connect", "images")) {
			t.Fatalf("prompt omitted absolute image path: %q", result.Content)
		}
	}
	for i, messageID := range []string{"message-one", "message-two"} {
		data, err := os.ReadFile(filepath.Join(absoluteWorkDir, ".cc-connect", "attachments", messageID, "same.txt"))
		if err != nil || string(data) != fmt.Sprintf("upload %d", i) {
			t.Fatalf("attachment %s = %q, %v", messageID, data, err)
		}
	}
	images, err := filepath.Glob(filepath.Join(absoluteWorkDir, ".cc-connect", "images", "*.png"))
	if err != nil || len(images) != 2 {
		t.Fatalf("image paths = %v, %v", images, err)
	}
	contents := map[string]bool{}
	for _, path := range images {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		contents[string(data)] = true
	}
	if !contents["upload 0"] || !contents["upload 1"] {
		t.Fatalf("image uploads overwritten: %v", contents)
	}
}
