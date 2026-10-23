package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/agent/acp"
	"github.com/chenhg5/cc-connect/agent/codex"
	"github.com/chenhg5/cc-connect/core"
)

type bridgePromptPlatform struct {
	mu       sync.Mutex
	messages []string
}

func (*bridgePromptPlatform) Name() string                    { return "bridge-prompt-test" }
func (*bridgePromptPlatform) Start(core.MessageHandler) error { return nil }
func (*bridgePromptPlatform) Stop() error                     { return nil }
func (p *bridgePromptPlatform) Reply(_ context.Context, _ any, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, text)
	return nil
}
func (p *bridgePromptPlatform) Send(ctx context.Context, reply any, text string) error {
	return p.Reply(ctx, reply, text)
}
func (p *bridgePromptPlatform) text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.messages, "\n")
}

// The engine and session manager are real. Only the CLI process and messaging
// platform are simulated: the CLI reports whether its actual protocol input
// contained the attachment instructions. No account or real chat is needed.
func TestCUJ_A10_BridgePromptAvailableWithoutSetupAcrossTurns(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"codex", "acp"} {
		t.Run(backend, func(t *testing.T) {
			cwd := t.TempDir()
			opts := map[string]any{
				"work_dir": cwd,
				"cmd":      []string{bin, "-test.run=^TestBridgePromptCLIProcess$", "--"},
				"env":      map[string]any{"CC_CONNECT_BRIDGE_TEST_HELPER": "1"},
			}
			var agent core.Agent
			var err error
			if backend == "codex" {
				opts["backend"] = "app_server"
				agent, err = codex.New(opts)
			} else {
				agent, err = acp.New(opts)
			}
			if err != nil {
				t.Fatal(err)
			}
			p := &bridgePromptPlatform{}
			e := core.NewEngine("bridge-prompt-cuj", agent, []core.Platform{p}, filepath.Join(cwd, "sessions.json"), core.LangEnglish)
			defer func() { _ = e.Stop() }()
			key := "bridge-prompt-test:user"
			for i := 1; i <= 3; i++ {
				prompt := fmt.Sprintf("send existing report %d", i)
				e.ReceiveMessage(p, &core.Message{SessionKey: key, Platform: p.Name(), UserID: "user", MessageID: fmt.Sprint(i), Content: prompt, ReplyCtx: "test"})
				want := "ATTACHMENT_TOOL_AVAILABLE: " + prompt
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					if strings.Contains(p.text(), want) && !e.GetSessions().GetOrCreateActive(key).Busy() {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !strings.Contains(p.text(), want) {
					t.Fatalf("bridge instructions missing from user-visible turn %d: %s", i, p.text())
				}
			}
			if _, err := os.Stat(filepath.Join(cwd, "AGENTS.md")); !os.IsNotExist(err) {
				t.Fatal("bridge setup modified workspace instructions")
			}
		})
	}
}

// This test binary also serves as an isolated protocol fixture in child
// processes. Production adapters spawn it with their normal CLI argument path.
func TestBridgePromptCLIProcess(t *testing.T) {
	if os.Getenv("CC_CONNECT_BRIDGE_TEST_HELPER") != "1" {
		return
	}
	enc := json.NewEncoder(os.Stdout)
	emit := func(value any) {
		if enc.Encode(value) != nil {
			os.Exit(2)
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	native := false
	turn := 0
	for scanner.Scan() {
		var req struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(3)
		}
		if req.ID == nil {
			continue
		}
		result := map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true, "promptCapabilities": map[string]any{"embeddedContext": true}}}
			if info, ok := req.Params["clientInfo"].(map[string]any); ok {
				if name, _ := info["name"].(string); strings.Contains(name, "codex") {
					result = map[string]any{}
				}
			}
		case "config/read":
			result = map[string]any{"config": map[string]any{"model": "fixture"}}
		case "thread/start", "thread/resume":
			instructions, _ := req.Params["developerInstructions"].(string)
			native = strings.Contains(instructions, "cc-connect send --file")
			result = map[string]any{"thread": map[string]any{"id": "bridge-fixture-thread"}, "model": "fixture"}
		case "session/new", "session/load":
			result = map[string]any{"sessionId": "bridge-fixture-session"}
		case "turn/start", "session/prompt":
			input := req.Params["input"]
			if req.Method == "session/prompt" {
				input = req.Params["prompt"]
			}
			blocks, _ := input.([]any)
			prompt := ""
			if len(blocks) > 0 {
				prompt, _ = blocks[0].(map[string]any)["text"].(string)
			}
			body, _ := json.Marshal(blocks)
			available := native || strings.Contains(string(body), "cc-connect send --file")
			text := "MISSING_BRIDGE_INSTRUCTIONS: " + prompt
			if available {
				text = "ATTACHMENT_TOOL_AVAILABLE: " + prompt
			}
			if req.Method == "session/prompt" {
				emit(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": "bridge-fixture-session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}}})
				result = map[string]any{"stopReason": "end_turn"}
			} else {
				turn++
				id := fmt.Sprint("turn-", turn)
				emit(map[string]any{"id": req.ID, "result": map[string]any{"turn": map[string]any{"id": id}}})
				emit(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "bridge-fixture-thread", "turn": map[string]any{"id": id}}})
				emit(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "bridge-fixture-thread", "item": map[string]any{"type": "agentMessage", "text": text}}})
				emit(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "bridge-fixture-thread", "turn": map[string]any{"id": id, "status": "completed"}}})
				continue
			}
		}
		emit(map[string]any{"id": req.ID, "result": result})
	}
	os.Exit(0)
}
