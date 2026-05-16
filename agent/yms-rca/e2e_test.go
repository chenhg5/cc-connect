package ymsagent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type e2eAgent struct {
	session core.AgentSession
}

func (a *e2eAgent) Name() string { return "yms-rca" }

func (a *e2eAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	return a.session, nil
}

func (a *e2eAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}

func (a *e2eAgent) Stop() error { return nil }

type e2ePlatform struct {
	mu   sync.Mutex
	sent []string
}

func (p *e2ePlatform) Name() string { return "youzone" }

func (p *e2ePlatform) Start(core.MessageHandler) error { return nil }

func (p *e2ePlatform) Reply(_ context.Context, _ any, content string) error {
	p.record(content)
	return nil
}

func (p *e2ePlatform) Send(_ context.Context, _ any, content string) error {
	p.record(content)
	return nil
}

func (p *e2ePlatform) Stop() error { return nil }

func (p *e2ePlatform) record(content string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, content)
}

func (p *e2ePlatform) messages() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.sent))
	copy(out, p.sent)
	return out
}

func waitForPromptFrame(t *testing.T, enc *mockEncoder, contains string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, frame := range enc.framesCopy() {
			if frame["type"] == "prompt" && strings.Contains(asString(frame, "message", ""), contains) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for prompt frame containing %q; frames=%+v", contains, enc.framesCopy())
}

func waitForPlatformMessage(t *testing.T, p *e2ePlatform, contains string) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sent := p.messages()
		for _, msg := range sent {
			if strings.Contains(msg, contains) {
				return sent
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for platform message containing %q; sent=%+v", contains, p.messages())
	return nil
}

func TestE2E_ClusterNodeQuestionWaitsForPostToolSummary(t *testing.T) {
	session, enc := newTestSession(t, "default")
	session.sessionID.Store("yms-e2e-session")
	session.currentProfile.Store("new5")
	platform := &e2ePlatform{}
	engine := core.NewEngine("yms-rca-e2e", &e2eAgent{session: session}, []core.Platform{platform}, "", core.LangChinese)
	defer engine.Stop()

	engine.ReceiveMessage(platform, &core.Message{
		SessionKey: "youzone:claw_2537994238045454345.esn.upesn@pubaccount.im.yyuap.com/5837619.esn.upesn:5837619.esn.upesn",
		Platform:   "youzone",
		UserID:     "5837619.esn.upesn",
		UserName:   "邵书超",
		Content:    "集群有几个节点",
		ReplyCtx:   "reply-context",
		MessageID:  "cluster-node-question",
	})

	waitForPromptFrame(t, enc, "集群有几个节点")

	session.handleEvent(map[string]any{
		"type":       "tool_execution_start",
		"toolCallId": "tc-get-nodes",
		"toolName":   "kubectl",
		"arguments": map[string]any{
			"command": "kubectl get nodes --no-headers",
		},
	})
	session.handleEvent(map[string]any{
		"type":  "agent_end",
		"usage": map[string]any{"input": 120.0, "output": 20.0},
	})
	session.handleEvent(map[string]any{
		"type": "turn_end",
		"data": map[string]any{"toolCallsInTurn": 1.0},
	})
	session.handleEvent(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role": "assistant",
			"content": []any{map[string]any{
				"type":      "toolCall",
				"id":        "tc-get-nodes",
				"name":      "kubectl",
				"arguments": map[string]any{"command": "kubectl get nodes --no-headers"},
			}},
		},
	})
	session.handleEvent(map[string]any{
		"type":       "tool_execution_end",
		"toolCallId": "tc-get-nodes",
		"toolName":   "kubectl",
		"result":     "node-a\nnode-b\n",
		"status":     "completed",
		"success":    true,
	})
	session.handleEvent(map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type":  "text_delta",
			"delta": "集群有 2 个节点。",
		},
	})
	session.handleEvent(map[string]any{
		"type": "turn_end",
		"data": map[string]any{"toolCallsInTurn": 0.0},
	})

	sent := waitForPlatformMessage(t, platform, "集群有 2 个节点")
	if !strings.Contains(strings.Join(sent, "\n"), "profile: new5") {
		t.Fatalf("platform response missing yms-rca profile footer: %+v", sent)
	}
	if !strings.Contains(strings.Join(sent, "\n"), "[ctx:") {
		t.Fatalf("platform response missing context indicator from deferred usage: %+v", sent)
	}
	for _, msg := range sent {
		trimmed := strings.TrimSpace(msg)
		if trimmed == "<tool_call>" {
			t.Fatalf("platform received raw tool-call placeholder instead of final summary: %+v", sent)
		}
		if strings.Contains(msg, "空响应") || strings.Contains(msg, "empty response") {
			t.Fatalf("platform received empty-response fallback instead of final summary: %+v", sent)
		}
	}
}
