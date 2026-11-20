package tool_preview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/agent/claudecode"
	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/platform/feishu"
)

// Only the external CLI and message delivery are faked. The real Claude
// decoder, engine aggregation and Feishu renderer must preserve the output end.
func TestToolPreview_RealAdapterToRenderedCardPreservesTail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI requires a POSIX shell")
	}
	dir := t.TempDir()
	output := strings.Repeat("旧输出\n", 700) + "倒数第二行\nFINAL_ERROR_MARKER"
	frames := []any{
		map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "tool-1", "name": "Bash", "input": map[string]any{"command": "run test"}}}}},
		map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tool-1", "content": output}}}},
		map[string]any{"type": "result", "subtype": "success", "result": "FINAL_REPLY", "session_id": "test-session"},
	}
	var script strings.Builder
	script.WriteString("#!/bin/sh\nwhile IFS= read -r line; do\ncat <<'CC_TEST_EVENTS'\n")
	for _, frame := range frames {
		data, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		script.Write(data)
		script.WriteByte('\n')
	}
	script.WriteString("CC_TEST_EVENTS\ndone\n")
	cmd := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(cmd, []byte(script.String()), 0700); err != nil {
		t.Fatal(err)
	}
	agent, err := claudecode.New(map[string]any{"cmd": cmd, "work_dir": dir, "cc_data_dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	p := &previewPlatform{cards: make(chan string, 64)}
	engine := core.NewEngine("tail-test", agent, []core.Platform{p}, filepath.Join(dir, "sessions.json"), core.LangEnglish)
	engine.SetDisplayConfig(core.DisplayCfg{Mode: "full", CardMode: "rich", ToolMessages: true, ToolMaxLen: 50})
	t.Cleanup(func() {
		if err := engine.Stop(); err != nil {
			t.Error(err)
		}
	})
	engine.ReceiveMessage(p, &core.Message{SessionKey: "test:user", Platform: p.Name(), UserID: "user", Content: "run test", ReplyCtx: "chat"})
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case card := <-p.cards:
			if !strings.Contains(card, "FINAL_REPLY") {
				continue
			}
			var decoded struct {
				Body struct {
					Elements []map[string]any `json:"elements"`
				} `json:"body"`
			}
			if err := json.Unmarshal([]byte(card), &decoded); err != nil {
				t.Fatalf("not a rendered rich card: %v", err)
			}
			for _, element := range decoded.Body.Elements {
				if element["tag"] != "div" {
					continue
				}
				text, _ := element["text"].(map[string]any)
				content, _ := text["content"].(string)
				if strings.Contains(content, "FINAL_ERROR_MARKER") && strings.Contains(content, "倒数第二行") && !strings.Contains(content, "旧输出") {
					return
				}
			}
			t.Fatalf("final card preview lost true output tail: %s", card)
		case <-timeout.C:
			t.Fatal("no final rendered card")
		}
	}
}

type previewPlatform struct{ cards chan string }

func (*previewPlatform) Name() string                    { return "test" }
func (*previewPlatform) Start(core.MessageHandler) error { return nil }
func (*previewPlatform) Stop() error                     { return nil }
func (p *previewPlatform) Reply(_ context.Context, _ any, text string) error {
	p.cards <- text
	return nil
}
func (p *previewPlatform) Send(ctx context.Context, reply any, text string) error {
	return p.Reply(ctx, reply, text)
}
func (p *previewPlatform) SendPreviewStart(ctx context.Context, reply any, text string) (any, error) {
	return "preview", p.Reply(ctx, reply, text)
}
func (p *previewPlatform) UpdateMessage(ctx context.Context, reply any, text string) error {
	return p.Reply(ctx, reply, text)
}
func (*previewPlatform) KeepPreviewOnFinish() bool { return true }
func (*previewPlatform) BuildRichCard(status core.CardStatus, title string, steps []core.ToolStep, markdown string, streaming bool, footer string) string {
	return (&feishu.Platform{}).BuildRichCard(status, title, steps, markdown, streaming, footer)
}
