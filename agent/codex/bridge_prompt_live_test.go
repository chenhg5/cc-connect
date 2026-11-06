//go:build integration

package codex

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Exercise Codex's native compaction directly, without changing the adapter's
// existing user-facing compaction support. Requires a signed-in installed CLI.
func TestLiveCodexBridgePrompt_NativeCompaction(t *testing.T) {
	if os.Getenv("CC_CONNECT_BRIDGE_LIVE") != "1" {
		t.Skip("set CC_CONNECT_BRIDGE_LIVE=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	marker := fmt.Sprintf("BRIDGE_COMPACT_%d", time.Now().UnixNano())
	a, err := New(map[string]any{
		"work_dir": t.TempDir(), "backend": "app_server", "cmd": os.Getenv("CC_CONNECT_BRIDGE_CODEX_CLI"),
		"model": "gpt-6.1-sol", "reasoning_effort": "low", "mode": "suggest",
		"append_system_prompt": "When asked BRIDGE_CONTEXT_CHECK, reply exactly " + marker + ". Do not use tools.",
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	s := session.(*appServerSession)
	defer func() { _ = s.Close() }()
	wait := func() string {
		t.Helper()
		var text strings.Builder
		for {
			select {
			case ev := <-s.Events():
				if ev.Type == core.EventError {
					t.Fatal(ev.Error)
				}
				if ev.Type == core.EventText {
					text.WriteString(ev.Content)
				}
				if ev.Type == core.EventResult && ev.Done {
					text.WriteString(ev.Content)
					return text.String()
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	check := func() {
		t.Helper()
		if err := s.Send("BRIDGE_CONTEXT_CHECK", "bridge-live", nil, nil); err != nil {
			t.Fatal(err)
		}
		if text := wait(); !strings.Contains(text, marker) {
			t.Fatalf("native context lost bridge instructions: %s", text)
		}
	}
	check()
	if err := s.request("thread/compact/start", map[string]any{"threadId": s.CurrentSessionID()}, nil); err != nil {
		t.Fatal(err)
	}
	_ = wait()
	check()
}
