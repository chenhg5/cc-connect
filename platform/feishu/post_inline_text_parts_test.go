package feishu

import (
	"reflect"
	"testing"
)

func TestExtractPostParts_AdjacentTextBoundaries(t *testing.T) {
	text := func(s string) postElement { return postElement{Tag: "text", Text: s} }
	for _, tc := range []struct {
		name, title string
		rows        [][]postElement
		want        []string
	}{
		{"three adjacent", "", [][]postElement{{text("第一"), text("行"), text("接着")}}, []string{"第一行接着"}},
		{"title and rows", "标题", [][]postElement{{text("第一"), text("行")}, {text("第二行")}}, []string{"标题", "第一行", "第二行"}},
		{"empty nodes", "标题", [][]postElement{{text(""), text("a"), text(""), text("b"), text("")}}, []string{"标题", "ab"}},
		{"all empty", "", [][]postElement{{text(""), text("")}}, nil},
		{"literal whitespace", "", [][]postElement{{text(" a\t"), text("b\nc ")}}, []string{" a\tb\nc "}},
		{"link", "", [][]postElement{{text("a"), {Tag: "a", Text: "link", Href: "https://example.com"}, text("b")}}, []string{"a", "[link](https://example.com)", "b"}},
		{"link fallback", "", [][]postElement{{text("a"), {Tag: "a", Text: "link"}, text("b")}}, []string{"a", "link", "b"}},
		{"mention", "", [][]postElement{{text("a"), {Tag: "at", UserId: "all"}, text("b")}}, []string{"a", "@all", "b"}},
		{"skipped bot", "", [][]postElement{{text("a"), {Tag: "at", UserId: "ou_bot"}, text("b")}}, []string{"a", "b"}},
		{"code", "", [][]postElement{{text("a"), {Tag: "code_block", Text: "x", Language: "go"}, text("b")}}, []string{"a", "```go\nx\n```", "b"}},
		{"markdown", "", [][]postElement{{text("a"), {Tag: "markdown", Text: "**x**"}, text("b")}}, []string{"a", "**x**", "b"}},
		{"empty image", "", [][]postElement{{text("a"), {Tag: "img"}, text("b")}}, []string{"a", "b"}},
		{"unknown", "", [][]postElement{{text("a"), {Tag: "unknown"}, text("b")}}, []string{"a", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Platform{botOpenID: "ou_bot"}
			got, images := p.extractPostParts("", &postLang{Title: tc.title, Content: tc.rows})
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parts=%q, want %q", got, tc.want)
			}
			if len(images) != 0 {
				t.Errorf("unexpected images: %d", len(images))
			}
		})
	}
}
