package feishu

import (
	"strings"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
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

func TestExtractPostParts_AtMention(t *testing.T) {
	p := &Platform{botOpenID: "ou_bot"}
	post := &postLang{
		Content: [][]postElement{
			{
				{Tag: "text", Text: "hi "},
				{Tag: "at", UserId: "ou_alice", UserName: "Alice"},
				{Tag: "text", Text: " and "},
				{Tag: "at", UserId: "all"},
				{Tag: "text", Text: " but not "},
				{Tag: "at", UserId: "ou_bot", UserName: "Bot"},
			},
		},
	}
	texts, _ := p.extractPostParts("", post)
	joined := strings.Join(texts, "")
	want := "hi @Alice and @all but not "
	if joined != want {
		t.Errorf("want %q, got %q", want, joined)
	}
}

func TestExtractPostParts_Markdown(t *testing.T) {
	p := &Platform{}
	post := &postLang{
		Content: [][]postElement{
			{{Tag: "markdown", Text: "**bold**"}},
		},
	}
	texts, _ := p.extractPostParts("", post)
	if len(texts) != 1 || texts[0] != "**bold**" {
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

func TestParseInlineMarkdown_Link(t *testing.T) {
	elements := parseInlineMarkdown("visit [Google](https://google.com) now")
	found := false
	for _, el := range elements {
		if el["tag"] == "a" && el["text"] == "Google" && el["href"] == "https://google.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected link element, got %v", elements)
	}
}

func TestParseInlineMarkdown_Italic(t *testing.T) {
	elements := parseInlineMarkdown("hello *world*")
	found := false
	for _, el := range elements {
		if styles, ok := el["style"].([]string); ok {
			for _, s := range styles {
				if s == "italic" && el["text"] == "world" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("expected italic element, got %v", elements)
	}
}

func TestParseInlineMarkdown_Strikethrough(t *testing.T) {
	elements := parseInlineMarkdown("hello ~~world~~")
	found := false
	for _, el := range elements {
		if styles, ok := el["style"].([]string); ok {
			for _, s := range styles {
				if s == "lineThrough" && el["text"] == "world" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("expected strikethrough element, got %v", elements)
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

func TestHasComplexMarkdown(t *testing.T) {
	if !hasComplexMarkdown("text\n```go\ncode\n```") {
		t.Error("should detect code blocks")
	}
	if !hasComplexMarkdown("| A | B |\n|---|---|") {
		t.Error("should detect tables")
	}
	if hasComplexMarkdown("**bold** and *italic*") {
		t.Error("should not detect simple markdown")
	}
}

func TestCountMarkdownTables(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"no tables", "hello world", 0},
		{"one table", "| A | B |\n|---|---|\n| 1 | 2 |", 1},
		{"two tables separated by text", "| A |\n|---|\n\nsome text\n\n| B |\n|---|", 2},
		{"consecutive tables no gap", "| A |\n|---|\n| 1 |\n| B |\n|---|", 1},
		{"six tables", "| A |\n|---|\n\nx\n\n| B |\n|---|\n\nx\n\n| C |\n|---|\n\nx\n\n| D |\n|---|\n\nx\n\n| E |\n|---|\n\nx\n\n| F |\n|---|", 6},
		{"table inside code block is still counted", "```\n| A |\n```\n\n| B |\n|---|", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countMarkdownTables(tt.input)
			if got != tt.want {
				t.Errorf("countMarkdownTables() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBuildReplyContent_FallbackWhenManyTables(t *testing.T) {
	// Build content with 6 tables (exceeds the 5-table card limit).
	var sb strings.Builder
	for i := 0; i < 6; i++ {
		if i > 0 {
			sb.WriteString("\n\nsome text\n\n")
		}
		sb.WriteString("| H |\n|---|\n| V |")
	}
	content := sb.String()

	msgType, _ := buildReplyContent(content)
	if msgType == larkim.MsgTypeInteractive {
		t.Errorf("expected non-card message type for >5 tables, got interactive")
	}

	// With exactly 5 tables, card should still be used.
	sb.Reset()
	for i := 0; i < 5; i++ {
		if i > 0 {
			sb.WriteString("\n\nsome text\n\n")
		}
		sb.WriteString("| H |\n|---|\n| V |")
	}
	content5 := sb.String()

	msgType5, _ := buildReplyContent(content5)
	if msgType5 != larkim.MsgTypeInteractive {
		t.Errorf("expected interactive card for 5 tables, got %s", msgType5)
	}
}

func TestParseInlineMarkdown_BoldAndCode(t *testing.T) {
	elements := parseInlineMarkdown("**bold** and `code`")
	hasBold, hasCode := false, false
	for _, el := range elements {
		if styles, ok := el["style"].([]string); ok {
			for _, s := range styles {
				if s == "bold" && el["text"] == "bold" {
					hasBold = true
				}
				if s == "code" && el["text"] == "code" {
					hasCode = true
				}
			}
		}
	}
	if !hasBold || !hasCode {
		t.Errorf("expected bold and code, got %v", elements)
	}
}

func TestExtractPostPlainText_FlatFormat(t *testing.T) {
	content := `{"title":"公告","content":[[{"tag":"text","text":"第一段"}],[{"tag":"text","text":"第二段"}]]}`
	got := extractPostPlainText(content)
	if got != "公告\n第一段\n第二段" {
		t.Errorf("expected '公告\\n第一段\\n第二段', got %q", got)
	}
}

func TestExtractPostPlainText_LocaleWrapped(t *testing.T) {
	content := `{"zh_cn":{"title":"标题","content":[[{"tag":"text","text":"内容"}]]}}`
	got := extractPostPlainText(content)
	if got != "标题\n内容" {
		t.Errorf("expected '标题\\n内容', got %q", got)
	}
}

func TestExtractPostPlainText_NoTitle(t *testing.T) {
	content := `{"content":[[{"tag":"text","text":"仅内容"}]]}`
	got := extractPostPlainText(content)
	if got != "仅内容" {
		t.Errorf("expected '仅内容', got %q", got)
	}
}

func TestExtractPostPlainText_Empty(t *testing.T) {
	got := extractPostPlainText(`{}`)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestExtractPostPlainText_LinkText(t *testing.T) {
	content := `{"content":[[{"tag":"text","text":"hello "},{"tag":"a","text":"link","href":"http://x.com"}]]}`
	got := extractPostPlainText(content)
	if got != "hello link" {
		t.Errorf("expected 'hello link', got %q", got)
	}
}

func TestExtractPostPlainText_AtMention(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"named", `{"content":[[{"tag":"text","text":"hi "},{"tag":"at","user_id":"ou_x","user_name":"Alice"}]]}`, "hi @Alice"},
		{"all", `{"content":[[{"tag":"at","user_id":"all"}]]}`, "@all"},
		{"fallback", `{"content":[[{"tag":"at","user_id":"ou_x"}]]}`, "@user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractPostPlainText(tc.content); got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestExtractPostPlainText_Markdown(t *testing.T) {
	content := `{"content":[[{"tag":"markdown","text":"**bold** and *italic*"}]]}`
	got := extractPostPlainText(content)
	if got != "**bold** and *italic*" {
		t.Errorf("expected markdown passthrough, got %q", got)
	}
}

func TestExtractPostPlainText_CodeBlock(t *testing.T) {
	content := `{"content":[[{"tag":"text","text":"see:"},{"tag":"code_block","language":"go","text":"fmt.Println()"}]]}`
	got := extractPostPlainText(content)
	// Same paragraph: inline elements are concatenated (no extra newline before the fence).
	want := "see:```go\nfmt.Println()\n```"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func strPtr(s string) *string { return &s }

func TestStripMentions(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		mentions  []*larkim.MentionEvent
		botOpenID string
		expected  string
	}{
		{
			name:      "no mentions",
			text:      "hello",
			mentions:  nil,
			botOpenID: "",
			expected:  "hello",
		},
		{
			name: "bot mention removed",
			text: "@_user_1 /help",
			mentions: []*larkim.MentionEvent{
				{Key: strPtr("@_user_1"), Id: &larkim.UserId{OpenId: strPtr("bot123")}, Name: strPtr("Bot")},
			},
			botOpenID: "bot123",
			expected:  "/help",
		},
		{
			name: "non-bot mention replaced with name",
			text: "assign to @_user_2",
			mentions: []*larkim.MentionEvent{
				{Key: strPtr("@_user_2"), Id: &larkim.UserId{OpenId: strPtr("user456")}, Name: strPtr("张三")},
			},
			botOpenID: "bot123",
			expected:  "assign to @张三",
		},
		{
			name: "bot removed and other preserved",
			text: "@_user_1 assign to @_user_2",
			mentions: []*larkim.MentionEvent{
				{Key: strPtr("@_user_1"), Id: &larkim.UserId{OpenId: strPtr("bot123")}, Name: strPtr("Bot")},
				{Key: strPtr("@_user_2"), Id: &larkim.UserId{OpenId: strPtr("user456")}, Name: strPtr("张三")},
			},
			botOpenID: "bot123",
			expected:  "assign to @张三",
		},
		{
			name: "mention with nil key skipped",
			text: "@_user_1 hello",
			mentions: []*larkim.MentionEvent{
				{Key: nil, Id: &larkim.UserId{OpenId: strPtr("bot123")}},
			},
			botOpenID: "bot123",
			expected:  "@_user_1 hello",
		},
		{
			name: "mention with no name fallback removed",
			text: "text @_user_3",
			mentions: []*larkim.MentionEvent{
				{Key: strPtr("@_user_3"), Id: &larkim.UserId{OpenId: strPtr("user789")}, Name: nil},
			},
			botOpenID: "bot123",
			expected:  "text",
		},
		{
			name: "empty botOpenID all non-named removed",
			text: "@_user_1 hello",
			mentions: []*larkim.MentionEvent{
				{Key: strPtr("@_user_1"), Id: &larkim.UserId{OpenId: strPtr("someone")}},
			},
			botOpenID: "",
			expected:  "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripMentions(tt.text, tt.mentions, tt.botOpenID)
			if got != tt.expected {
				t.Errorf("stripMentions() = %q, want %q", got, tt.expected)
			}
		})
	}
}
