package feishu

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestExtractPostParts_TextOnly(t *testing.T) {
	p := &Platform{}
	post := &postLang{
		Title: "标题",
		Content: [][]postElement{
			{
				{Tag: "text", Text: "第一行"},
				{Tag: "text", Text: "接着"},
			},
			{
				{Tag: "text", Text: "第二行"},
			},
		},
	}
	texts, images := p.extractPostParts("", post)
	if len(texts) != 4 {
		t.Fatalf("expected 4 text parts, got %d", len(texts))
	}
	if texts[0] != "标题" {
		t.Errorf("expected title '标题', got %q", texts[0])
	}
	if texts[1] != "第一行" {
		t.Errorf("expected '第一行', got %q", texts[1])
	}
	if len(images) != 0 {
		t.Errorf("expected 0 images, got %d", len(images))
	}
}

func TestExtractPostParts_WithLink(t *testing.T) {
	p := &Platform{}
	post := &postLang{
		Content: [][]postElement{
			{
				{Tag: "text", Text: "点击 "},
				{Tag: "a", Text: "这里", Href: "https://example.com"},
			},
		},
	}
	texts, _ := p.extractPostParts("", post)
	if len(texts) != 2 {
		t.Fatalf("expected 2 text parts, got %d", len(texts))
	}
	if texts[0] != "点击 " || texts[1] != "这里" {
		t.Errorf("unexpected texts: %v", texts)
	}
}

func TestExtractPostParts_EmptyContent(t *testing.T) {
	p := &Platform{}
	post := &postLang{}
	texts, images := p.extractPostParts("", post)
	if len(texts) != 0 || len(images) != 0 {
		t.Errorf("expected empty results, got texts=%d images=%d", len(texts), len(images))
	}
}

func TestExtractPostParts_NoTitle(t *testing.T) {
	p := &Platform{}
	post := &postLang{
		Content: [][]postElement{
			{
				{Tag: "text", Text: "只有正文"},
			},
		},
	}
	texts, _ := p.extractPostParts("", post)
	if len(texts) != 1 || texts[0] != "只有正文" {
		t.Errorf("unexpected texts: %v", texts)
	}
}

func TestParsePostContent_FlatFormat(t *testing.T) {
	p := &Platform{}
	raw := `{"title":"test","content":[[{"tag":"text","text":"hello"}]]}`
	texts, _ := p.parsePostContent("", raw)
	if len(texts) != 2 || texts[0] != "test" || texts[1] != "hello" {
		t.Errorf("unexpected result: %v", texts)
	}
}

func TestParsePostContent_LangKeyedFormat(t *testing.T) {
	p := &Platform{}
	raw := `{"zh_cn":{"title":"标题","content":[[{"tag":"text","text":"内容"}]]}}`
	texts, _ := p.parsePostContent("", raw)
	if len(texts) != 2 || texts[0] != "标题" || texts[1] != "内容" {
		t.Errorf("unexpected result: %v", texts)
	}
}

func TestParsePostContent_InvalidJSON(t *testing.T) {
	p := &Platform{}
	texts, images := p.parsePostContent("", "not json")
	if texts != nil || images != nil {
		t.Errorf("expected nil results for invalid json")
	}
}

func TestPreprocessFeishuMarkdown_NewlineBeforeCodeFence(t *testing.T) {
	input := "some text```go\ncode\n```"
	out := preprocessFeishuMarkdown(input)
	if !strings.Contains(out, "text\n```go") {
		t.Errorf("expected newline before code fence, got %q", out)
	}
}

func TestPreprocessFeishuMarkdown_AlreadyNewline(t *testing.T) {
	input := "text\n```go\ncode\n```"
	out := preprocessFeishuMarkdown(input)
	if out != input {
		t.Errorf("should not change content that already has newlines, got %q", out)
	}
}

func TestPreprocessFeishuMarkdown_PreservesTablesAndHeadings(t *testing.T) {
	input := "## Title\n| A | B |\n|---|---|\n> quote"
	out := preprocessFeishuMarkdown(input)
	if !strings.Contains(out, "## Title") {
		t.Errorf("heading should be preserved, got %q", out)
	}
	if !strings.Contains(out, "| A | B |") {
		t.Errorf("table should be preserved, got %q", out)
	}
	if !strings.Contains(out, "> quote") {
		t.Errorf("blockquote should be preserved, got %q", out)
	}
}

func TestBuildCardJSON_StatusColors(t *testing.T) {
	cases := []struct {
		status   core.CardStatus
		wantTmpl string
	}{
		{core.CardStatusThinking, "grey"},
		{core.CardStatusWorking, "blue"},
		{core.CardStatusDone, "green"},
		{core.CardStatusError, "red"},
		{"unknown", "grey"}, // unknown status falls back to grey
	}
	for _, tc := range cases {
		raw := buildCardJSON("hello", tc.status)
		var card map[string]any
		if err := json.Unmarshal([]byte(raw), &card); err != nil {
			t.Fatalf("status=%s: invalid JSON: %v", tc.status, err)
		}
		header, ok := card["header"].(map[string]any)
		if !ok {
			t.Fatalf("status=%s: missing header", tc.status)
		}
		got, _ := header["template"].(string)
		if got != tc.wantTmpl {
			t.Errorf("status=%s: template=%q, want %q", tc.status, got, tc.wantTmpl)
		}
	}
}

func TestReconstructReplyCtx_Formats(t *testing.T) {
	p := &Platform{}

	// per-message session: cron not supported
	if _, err := p.ReconstructReplyCtx("feishu:msg:omsg123:ou_abc"); err == nil {
		t.Error("expected error for feishu:msg: key")
	}

	// thread session: cron not supported
	if _, err := p.ReconstructReplyCtx("feishu:thread:omsg456:ou_abc"); err == nil {
		t.Error("expected error for feishu:thread: key")
	}

	// legacy chat session: should succeed
	rc, err := p.ReconstructReplyCtx("feishu:oc_chatid123:ou_abc")
	if err != nil {
		t.Fatalf("unexpected error for legacy key: %v", err)
	}
	rctx, ok := rc.(replyContext)
	if !ok || rctx.chatID != "oc_chatid123" {
		t.Errorf("legacy key: got chatID=%q, want 'oc_chatid123'", rctx.chatID)
	}

	// invalid key
	if _, err := p.ReconstructReplyCtx("slack:foo:bar"); err == nil {
		t.Error("expected error for non-feishu key")
	}
}


