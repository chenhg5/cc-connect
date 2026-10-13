package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestResultHandoffIsNewGroupMessageWithOriginalContent(t *testing.T) {
	p, f := newSplitCardFixture(t, false)
	p.SetTaskThreadResolver(func(string, string) bool { return true })
	body := "  Original result\n```go\nfunc main() {}\n```\n"
	if _, err := p.SendResultMessage(context.Background(), "oc_group", "ou_manager", body, "result-1"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 {
		t.Fatal("duplicated result")
	}
	r := f.requests[0]
	if strings.Contains(r["path"].(string), "/reply") || r["receive_id"] != "oc_group" {
		t.Fatalf("result hidden in thread: %+v", r)
	}
	var post map[string]any
	json.Unmarshal([]byte(r["content"].(string)), &post)
	rows := post["zh_cn"].(map[string]any)["content"].([]any)
	at := rows[0].([]any)[0].(map[string]any)
	md := rows[1].([]any)[0].(map[string]any)
	if at["tag"] != "at" || at["user_id"] != "ou_manager" || md["tag"] != "md" || md["text"] != body {
		t.Fatal("mention/content changed")
	}
	// The recipient bot and later quoted replies must still read the full
	// Markdown body after switching from the legacy plain-text post node.
	p.botOpenID = "ou_manager"
	parts, _ := p.parsePostContent("result", r["content"].(string))
	if len(parts) != 1 || parts[0] != body || !strings.Contains(extractPostPlainText(r["content"].(string)), body) {
		t.Fatal("formatted result is unreadable to the receiving bot or quoted reply")
	}
}

func TestResultHandoffOversizedResponsePreservedAsGroupFile(t *testing.T) {
	p, f := newSplitCardFixture(t, false)
	body := strings.Repeat("完整原始结果\n", 4000) + "TAIL"
	for i := 0; i < 2; i++ {
		if _, err := p.SendResultMessage(context.Background(), "oc_group", "ou_manager", body, "large-result"); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.files) != 1 {
		t.Fatal("result file missing or duplicated on retry")
	}
	for _, data := range f.files {
		if data != body {
			t.Fatal("result truncated")
		}
	}
	var uuid any
	for _, r := range f.requests {
		if strings.Contains(r["path"].(string), "/reply") {
			t.Fatal("group result put in thread")
		}
		if r["msg_type"] == "post" {
			if uuid != nil && uuid != r["uuid"] {
				t.Fatal("retry lost idempotency key")
			}
			uuid = r["uuid"]
		}
	}
}
