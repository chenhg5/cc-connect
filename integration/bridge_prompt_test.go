//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/agent/acp"
	"github.com/chenhg5/cc-connect/agent/codex"
	"github.com/chenhg5/cc-connect/core"
)

// Opt-in live tests use the installed, signed-in CLI. Delivery has a separate
// opt-in because it sends one small test attachment to the current bridge chat.
func TestLiveBridgePrompt(t *testing.T) {
	if os.Getenv("CC_CONNECT_BRIDGE_LIVE") != "1" {
		t.Skip("set CC_CONNECT_BRIDGE_LIVE=1")
	}
	for _, backend := range []string{"codex", "kimi"} {
		t.Run(backend, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
			defer cancel()
			cwd := t.TempDir()
			marker := fmt.Sprintf("BRIDGE_READY_%d", time.Now().UnixNano())
			opts := map[string]any{"work_dir": cwd, "mode": "yolo", "append_system_prompt": "When the user asks BRIDGE_CONTEXT_CHECK, reply exactly " + marker + ". Do not use tools for that check."}
			var agent core.Agent
			var err error
			if backend == "codex" {
				opts["backend"] = "app_server"
				opts["cmd"] = os.Getenv("CC_CONNECT_BRIDGE_CODEX_CLI")
				opts["model"] = "gpt-6.1-sol"
				opts["reasoning_effort"] = "low"
				agent, err = codex.New(opts)
			} else {
				opts["cmd"] = os.Getenv("CC_CONNECT_BRIDGE_KIMI_CLI")
				opts["args"] = []string{"acp"}
				agent, err = acp.New(opts)
			}
			if err != nil {
				t.Fatal(err)
			}
			if injector, ok := agent.(core.SessionEnvInjector); ok {
				var env []string
				for _, key := range []string{"CC_PROJECT", "CC_SESSION_KEY", "CC_DATA_DIR", "PATH"} {
					if value := os.Getenv(key); value != "" {
						env = append(env, key+"="+value)
					}
				}
				injector.SetSessionEnv(env)
			}
			s, err := agent.StartSession(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			turn := func(prompt string) (string, string) { t.Helper(); return liveBridgeTurn(t, ctx, s, prompt) }
			text, _ := turn("BRIDGE_CONTEXT_CHECK")
			if !strings.Contains(text, marker) {
				t.Fatalf("new session missing bridge/custom context: %s", text)
			}
			sid := s.CurrentSessionID()
			if sid == "" {
				t.Fatal("missing session ID")
			}
			t.Log("new session prompt loaded")
			if backend == "kimi" {
				_, _ = turn("/compact")
				if backend == "kimi" {
					var background strings.Builder
					deadline := time.NewTimer(3 * time.Minute)
					defer deadline.Stop()
				waitCompact:
					for {
						select {
						case ev := <-s.Events():
							background.WriteString(ev.Content)
							if ev.Type == core.EventError {
								t.Fatal(ev.Error)
							}
							if ev.Type == core.EventResult && strings.Contains(strings.ToLower(background.String()), "compaction completed") {
								break waitCompact
							}
						case <-deadline.C:
							t.Fatalf("compaction did not finish: %s", background.String())
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
				}
				text, _ = turn("BRIDGE_CONTEXT_CHECK")
				if !strings.Contains(text, marker) {
					t.Fatalf("compaction lost bridge context: %s", text)
				}
				t.Log("prompt retained after compaction")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = agent.StartSession(ctx, sid)
			if err != nil {
				t.Fatal(err)
			}
			text, _ = turn("BRIDGE_CONTEXT_CHECK")
			if !strings.Contains(text, marker) || s.CurrentSessionID() != sid {
				t.Fatalf("resume lost prompt or changed session: %s", text)
			}
			t.Log("same session resumed with bridge prompt")
			if os.Getenv("CC_CONNECT_BRIDGE_LIVE_DELIVERY") == "1" {
				if os.Getenv("CC_PROJECT") == "" || os.Getenv("CC_SESSION_KEY") == "" {
					t.Fatal("delivery requires explicit current bridge routing")
				}
				path := filepath.Join(cwd, "cc-connect-"+backend+"-附件验证.txt")
				if err := os.WriteFile(path, []byte("cc-connect 附件回传验证\nworker="+backend+"\n新会话、恢复及压缩后的提示加载已通过。\n"), 0600); err != nil {
					t.Fatal(err)
				}
				_, tools := turn("请把这个已有文件发到当前聊天供我下载：" + path + "。不要只回复本地链接。发送后简短确认。")
				if !strings.Contains(tools, "Message sent successfully") {
					t.Fatalf("no successful attachment tool result: %s", tools)
				}
				t.Log("attachment command succeeded in the real current chat")
			}
		})
	}
}

func liveBridgeTurn(t *testing.T, ctx context.Context, s core.AgentSession, prompt string) (string, string) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.Send(prompt, "bridge-live", nil, nil) }()
	var text, tools strings.Builder
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatal("session closed before completion")
			}
			switch ev.Type {
			case core.EventText:
				text.WriteString(ev.Content)
			case core.EventToolUse, core.EventToolResult:
				tools.WriteString(ev.ToolInput + "\n" + ev.ToolResult + "\n" + ev.Content + "\n")
			case core.EventPermissionRequest:
				if err := s.RespondPermission(ev.RequestID, core.PermissionResult{Behavior: "allow"}); err != nil {
					t.Fatal(err)
				}
			case core.EventError:
				t.Fatal(ev.Error)
			case core.EventResult:
				if !ev.Done {
					continue
				}
				text.WriteString(ev.Content)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				return text.String(), tools.String()
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
