package feishu

import (
	"testing"
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
