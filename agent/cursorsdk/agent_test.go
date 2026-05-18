package cursorsdk

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func writeFakeSidecar(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-sidecar.cjs")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCursorSDKSessionStreamsEvents(t *testing.T) {
	script := writeFakeSidecar(t, `
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin });
function write(x) { process.stdout.write(JSON.stringify(x) + "\n"); }
rl.on("line", (line) => {
  const req = JSON.parse(line);
  if (req.op === "send") {
    write({ id: req.id, event: "session", sessionId: req.sessionId || "sdk-new" });
    write({ id: req.id, event: "text", text: "hello ", sessionId: req.sessionId || "sdk-new" });
    write({ id: req.id, event: "tool", toolName: "Read", toolInput: "README.md", sessionId: req.sessionId || "sdk-new" });
    write({ id: req.id, event: "result", text: "hello world", sessionId: req.sessionId || "sdk-new", inputTokens: 123, outputTokens: 45 });
    return;
  }
  if (req.op === "close") {
    write({ id: req.id, event: "closed", sessionId: req.sessionId || "" });
    return;
  }
});
`)
	agent, err := New(map[string]any{
		"work_dir":       t.TempDir(),
		"sidecar_cmd":    "node",
		"sidecar_script": script,
		"model":          "composer-2",
		"api_key":        "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Stop() })

	sess, err := agent.StartSession(context.Background(), "sdk-existing")
	if err != nil {
		t.Fatal(err)
	}
	if got := sess.CurrentSessionID(); got != "sdk-existing" {
		t.Fatalf("CurrentSessionID before send = %q", got)
	}
	if err := sess.Send("hi", nil, nil); err != nil {
		t.Fatal(err)
	}

	var sawText, sawTool bool
	for {
		select {
		case evt := <-sess.Events():
			switch evt.Type {
			case core.EventText:
				if evt.Content == "hello " {
					sawText = true
				}
			case core.EventToolUse:
				if evt.ToolName == "Read" && evt.ToolInput == "README.md" {
					sawTool = true
				}
			case core.EventResult:
				if evt.Content != "hello world" {
					t.Fatalf("result content = %q", evt.Content)
				}
				if evt.InputTokens != 123 || evt.OutputTokens != 45 {
					t.Fatalf("tokens = %d/%d", evt.InputTokens, evt.OutputTokens)
				}
				if !sawText || !sawTool {
					t.Fatalf("missing events: text=%v tool=%v", sawText, sawTool)
				}
				return
			case core.EventError:
				t.Fatalf("unexpected error event: %v", evt.Error)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for result")
		}
	}
}

func TestCursorSDKListSessions(t *testing.T) {
	script := writeFakeSidecar(t, `
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin });
function write(x) { process.stdout.write(JSON.stringify(x) + "\n"); }
rl.on("line", (line) => {
  const req = JSON.parse(line);
  if (req.op === "list") {
    write({ id: req.id, event: "list", sessions: [{ sessionId: "sdk-a" }, { sessionId: "sdk-b" }] });
  }
});
`)
	agent, err := New(map[string]any{
		"work_dir":       t.TempDir(),
		"sidecar_cmd":    "node",
		"sidecar_script": script,
		"api_key":        "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Stop() })

	infos, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 || infos[0].ID != "sdk-a" || infos[1].ID != "sdk-b" {
		t.Fatalf("infos = %#v", infos)
	}
}

func TestCursorSDKSidecarPoolUsesSessionKey(t *testing.T) {
	script := writeFakeSidecar(t, `
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin });
function write(x) { process.stdout.write(JSON.stringify(x) + "\n"); }
rl.on("line", (line) => {
  const req = JSON.parse(line);
  if (req.op === "send") {
    const sid = req.sessionId || "sdk-" + process.pid;
    write({ id: req.id, event: "session", sessionId: sid });
    write({ id: req.id, event: "text", text: (process.env.CC_SESSION_KEY || "") + "|" + process.pid, sessionId: sid });
    write({ id: req.id, event: "result", text: "ok", sessionId: sid });
    return;
  }
  if (req.op === "close") {
    write({ id: req.id, event: "closed", sessionId: req.sessionId || "" });
    return;
  }
});
`)
	agentAny, err := New(map[string]any{
		"work_dir":       t.TempDir(),
		"sidecar_cmd":    "node",
		"sidecar_script": script,
		"api_key":        "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agentAny.Stop() })
	agent := agentAny.(*Agent)

	first := startPoolSession(t, agent, "feishu:chat:user-a")
	second := startPoolSession(t, agent, "feishu:chat:user-b")
	again := startPoolSession(t, agent, "feishu:other-chat:user-a")

	firstText := sendAndCollectText(t, first)
	secondText := sendAndCollectText(t, second)
	againText := sendAndCollectText(t, again)

	firstParts := strings.Split(firstText, "|")
	secondParts := strings.Split(secondText, "|")
	againParts := strings.Split(againText, "|")
	if len(firstParts) != 2 || len(secondParts) != 2 || len(againParts) != 2 {
		t.Fatalf("unexpected pool text: %q %q %q", firstText, secondText, againText)
	}
	if firstParts[0] != "feishu:chat:user-a" || secondParts[0] != "feishu:chat:user-b" || againParts[0] != "feishu:chat:user-a" {
		t.Fatalf("session env mismatch: %q %q %q", firstText, secondText, againText)
	}
	if firstParts[1] == secondParts[1] {
		t.Fatalf("different session keys reused sidecar pid %s", firstParts[1])
	}
	if firstParts[1] != againParts[1] {
		t.Fatalf("same user did not reuse sidecar: first=%s again=%s", firstParts[1], againParts[1])
	}
}

func TestCursorSDKSidecarPoolKeyUsesHashedUserID(t *testing.T) {
	key := sidecarPoolKey([]string{"CC_SESSION_KEY=feishu:chat-123:user-abc"}, "")
	if !strings.HasPrefix(key, "user:") {
		t.Fatalf("pool key = %q, want user hash", key)
	}
	if strings.Contains(key, "user-abc") || strings.Contains(key, "chat-123") {
		t.Fatalf("pool key leaked raw session details: %q", key)
	}
	if got, want := key, sidecarPoolKey([]string{"CC_SESSION_KEY=feishu:other-chat:user-abc"}, ""); got != want {
		t.Fatalf("same user should share pool key: got %q want %q", got, want)
	}
	if got, want := key, sidecarPoolKey([]string{"CC_SESSION_KEY=feishu:chat-123:user-other"}, ""); got == want {
		t.Fatalf("different users should not share pool key: %q", got)
	}
}

func startPoolSession(t *testing.T, agent *Agent, sessionKey string) core.AgentSession {
	t.Helper()
	agent.SetSessionEnv([]string{"CC_SESSION_KEY=" + sessionKey})
	sess, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func sendAndCollectText(t *testing.T, sess core.AgentSession) string {
	t.Helper()
	if err := sess.Send("hi", nil, nil); err != nil {
		t.Fatal(err)
	}
	var text string
	for {
		select {
		case evt := <-sess.Events():
			switch evt.Type {
			case core.EventText:
				text += evt.Content
			case core.EventResult:
				return text
			case core.EventError:
				t.Fatalf("unexpected error event: %v", evt.Error)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for sidecar pool result")
		}
	}
}

func TestCursorSDKRequiresAPIKey(t *testing.T) {
	t.Setenv("CURSOR_API_KEY", "")
	_, err := New(map[string]any{
		"work_dir":    t.TempDir(),
		"sidecar_cmd": "node",
	})
	if err == nil {
		t.Fatal("expected missing auth error")
	}
}

func TestCursorSDKRealSidecarSmoke(t *testing.T) {
	if os.Getenv("CURSOR_SDK_SMOKE") == "" {
		t.Skip("set CURSOR_SDK_SMOKE=1 with @cursor/sdk installed to run")
	}
	if os.Getenv("CURSOR_API_KEY") == "" {
		t.Skip("CURSOR_API_KEY is required for real Cursor SDK smoke test")
	}
	agent, err := New(map[string]any{
		"work_dir":             t.TempDir(),
		"sidecar_cmd":          "node",
		"model":                "composer-2",
		"turn_timeout_seconds": 120,
		"idle_ttl_minutes":     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Stop() })

	sess, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send("Reply with exactly: pong", nil, nil); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case evt := <-sess.Events():
			if evt.Type == core.EventError {
				t.Fatalf("real sidecar error: %v", evt.Error)
			}
			if evt.Type == core.EventResult {
				if evt.Content == "" {
					t.Fatal("empty result")
				}
				return
			}
		case <-time.After(150 * time.Second):
			t.Fatal("timed out waiting for real Cursor SDK result")
		}
	}
}
