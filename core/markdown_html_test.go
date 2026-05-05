package core

import (
	"fmt"
	"strings"
	"testing"
)

func TestMarkdownToSimpleHTML_Bold(t *testing.T) {
	out := MarkdownToSimpleHTML("hello **world**")
	if !strings.Contains(out, "<b>world</b>") {
		t.Errorf("expected <b>world</b>, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_Italic(t *testing.T) {
	out := MarkdownToSimpleHTML("hello *world*")
	if !strings.Contains(out, "<i>world</i>") {
		t.Errorf("expected <i>world</i>, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_Strikethrough(t *testing.T) {
	out := MarkdownToSimpleHTML("hello ~~world~~")
	if !strings.Contains(out, "<s>world</s>") {
		t.Errorf("expected <s>world</s>, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_InlineCode(t *testing.T) {
	out := MarkdownToSimpleHTML("run `echo hello`")
	if !strings.Contains(out, "<code>echo hello</code>") {
		t.Errorf("expected <code>echo hello</code>, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_CodeBlock(t *testing.T) {
	md := "```go\nfmt.Println()\n```"
	out := MarkdownToSimpleHTML(md)
	if !strings.Contains(out, `<pre><code class="language-go">`) {
		t.Errorf("expected language-go code block, got %q", out)
	}
	if !strings.Contains(out, "fmt.Println()") {
		t.Errorf("expected code content, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_Link(t *testing.T) {
	out := MarkdownToSimpleHTML("visit [Google](https://google.com)")
	if !strings.Contains(out, `<a href="https://google.com">Google</a>`) {
		t.Errorf("expected link HTML, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_Heading(t *testing.T) {
	out := MarkdownToSimpleHTML("## Section Title")
	if !strings.Contains(out, "<b>Section Title</b>") {
		t.Errorf("expected heading as bold, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_Blockquote(t *testing.T) {
	out := MarkdownToSimpleHTML("> quoted text")
	if !strings.Contains(out, "<blockquote>quoted text</blockquote>") {
		t.Errorf("expected blockquote, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_EscapesHTML(t *testing.T) {
	out := MarkdownToSimpleHTML("x < y && y > z")
	if !strings.Contains(out, "&lt;") || !strings.Contains(out, "&gt;") || !strings.Contains(out, "&amp;") {
		t.Errorf("HTML special chars should be escaped, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_EscapesInsideBold(t *testing.T) {
	out := MarkdownToSimpleHTML("**x < y**")
	if !strings.Contains(out, "<b>x &lt; y</b>") {
		t.Errorf("expected escaped content inside bold, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_LinkWithAmpersand(t *testing.T) {
	out := MarkdownToSimpleHTML("click [here](https://example.com?a=1&b=2)")
	if !strings.Contains(out, "&amp;b=2") {
		t.Errorf("URL ampersand should be escaped, got %q", out)
	}
	if !strings.Contains(out, `<a href=`) {
		t.Errorf("expected link tag, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_LinkWithQuotesInURL(t *testing.T) {
	out := MarkdownToSimpleHTML(`visit [book](https://example.com/q="test")`)
	if strings.Contains(out, `href="https://example.com/q="`) {
		t.Errorf("unescaped quote in href attribute, got %q", out)
	}
	if !strings.Contains(out, `&quot;`) {
		t.Errorf("expected escaped quote in URL, got %q", out)
	}
	if err := validateHTMLNesting(out); err != nil {
		t.Errorf("invalid HTML: %v, got %q", err, out)
	}
}

func TestMarkdownToSimpleHTML_EscapesQuotesInText(t *testing.T) {
	out := MarkdownToSimpleHTML(`He said "hello" world`)
	if strings.Contains(out, `"hello"`) {
		t.Errorf("quotes in text should be escaped, got %q", out)
	}
	if !strings.Contains(out, `&quot;hello&quot;`) {
		t.Errorf("expected &quot; in output, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_CodeBlockEscapesHTML(t *testing.T) {
	md := "```\nif a < b && c > d {\n}\n```"
	out := MarkdownToSimpleHTML(md)
	if !strings.Contains(out, "&lt;") || !strings.Contains(out, "&gt;") {
		t.Errorf("code block content should be HTML-escaped, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_InlineCodeEscapesHTML(t *testing.T) {
	out := MarkdownToSimpleHTML("run `x<y>z`")
	if !strings.Contains(out, "<code>x&lt;y&gt;z</code>") {
		t.Errorf("inline code should escape HTML, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_MixedFormattingWithSpecialChars(t *testing.T) {
	out := MarkdownToSimpleHTML("**bold** & *italic* < normal")
	if !strings.Contains(out, "<b>bold</b>") {
		t.Errorf("expected bold tag, got %q", out)
	}
	if !strings.Contains(out, "&amp;") {
		t.Errorf("expected escaped &, got %q", out)
	}
	if !strings.Contains(out, "&lt;") {
		t.Errorf("expected escaped <, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_NoCrossedTags(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"bold then italic", "**bold *text***"},
		{"italic around bold", "*italic **bold** more*"},
		{"heading with bold", "## **important** heading"},
		{"heading with italic", "## *weather* report"},
		{"mixed line", "**北京** *晴天* 25°C"},
		{"triple star", "***bold italic***"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := MarkdownToSimpleHTML(tt.input)
			if err := validateHTMLNesting(out); err != nil {
				t.Errorf("crossed tags in output %q: %v", out, err)
			}
		})
	}
}

func validateHTMLNesting(html string) error {
	var stack []string
	i := 0
	for i < len(html) {
		if html[i] != '<' {
			i++
			continue
		}
		end := strings.Index(html[i:], ">")
		if end < 0 {
			break
		}
		tag := html[i+1 : i+end]
		i += end + 1
		if strings.HasPrefix(tag, "/") {
			closing := tag[1:]
			if sp := strings.IndexByte(closing, ' '); sp > 0 {
				closing = closing[:sp]
			}
			if len(stack) == 0 {
				return fmt.Errorf("unexpected closing tag </%s>", closing)
			}
			top := stack[len(stack)-1]
			if top != closing {
				return fmt.Errorf("expected </%s>, found </%s>", top, closing)
			}
			stack = stack[:len(stack)-1]
		} else {
			name := tag
			if sp := strings.IndexByte(name, ' '); sp > 0 {
				name = name[:sp]
			}
			stack = append(stack, name)
		}
	}
	return nil
}

func TestMarkdownToSimpleHTML_UnorderedList(t *testing.T) {
	md := "Items:\n- first item\n- second item\n- third item"
	out := MarkdownToSimpleHTML(md)
	if !strings.Contains(out, "• first item") {
		t.Errorf("expected bullet for unordered list, got %q", out)
	}
	if !strings.Contains(out, "• second item") {
		t.Errorf("expected bullet for second item, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_UnorderedListAsterisk(t *testing.T) {
	md := "* one\n* two"
	out := MarkdownToSimpleHTML(md)
	if !strings.Contains(out, "• one") {
		t.Errorf("expected bullet for asterisk list, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_OrderedList(t *testing.T) {
	md := "Steps:\n1. first\n2. second\n3. third"
	out := MarkdownToSimpleHTML(md)
	if !strings.Contains(out, "1.") || !strings.Contains(out, "first") {
		t.Errorf("expected ordered list items, got %q", out)
	}
	if !strings.Contains(out, "2.") || !strings.Contains(out, "second") {
		t.Errorf("expected ordered list items, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_ListWithInlineFormatting(t *testing.T) {
	md := "- **bold item**\n- `code item`\n- *italic item*"
	out := MarkdownToSimpleHTML(md)
	if !strings.Contains(out, "• <b>bold item</b>") {
		t.Errorf("expected bold in list item, got %q", out)
	}
	if !strings.Contains(out, "• <code>code item</code>") {
		t.Errorf("expected code in list item, got %q", out)
	}
	if err := validateHTMLNesting(out); err != nil {
		t.Errorf("invalid HTML nesting: %v, got %q", err, out)
	}
}

func TestMarkdownToSimpleHTML_NestedList(t *testing.T) {
	md := "- top\n  - nested\n    - deep"
	out := MarkdownToSimpleHTML(md)
	if !strings.Contains(out, "• top") {
		t.Errorf("expected top-level bullet, got %q", out)
	}
	if !strings.Contains(out, "  • nested") {
		t.Errorf("expected indented nested bullet, got %q", out)
	}
	if !strings.Contains(out, "    • deep") {
		t.Errorf("expected double-indented deep bullet, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_GeminiTypicalOutput(t *testing.T) {
	md := `## Analysis Results

Here are the findings:

- **File structure**: The project has 3 main directories
- **Dependencies**: All up to date
- **Tests**: 15 passing, 0 failing

### Recommendations

1. Update the ` + "`README.md`" + ` file
2. Add **error handling** to the main function
3. Consider using ~~deprecated~~ updated API

> Note: This is an automated analysis

For more info, visit [docs](https://example.com).`

	out := MarkdownToSimpleHTML(md)

	if !strings.Contains(out, "<b>Analysis Results</b>") {
		t.Error("heading should be bold")
	}
	if !strings.Contains(out, "• <b>File structure</b>") {
		t.Errorf("list item with bold not converted properly, got %q", out)
	}
	if !strings.Contains(out, "<blockquote>") {
		t.Error("blockquote should be present")
	}
	if !strings.Contains(out, `<a href=`) {
		t.Error("link should be present")
	}
	if err := validateHTMLNesting(out); err != nil {
		t.Errorf("invalid HTML nesting: %v\nfull output: %q", err, out)
	}
}

func TestMarkdownToSimpleHTML_CodeBlockWithHTMLTags(t *testing.T) {
	md := "```html\n<div class=\"test\">\n  <p>Hello</p>\n</div>\n```"
	out := MarkdownToSimpleHTML(md)
	if !strings.Contains(out, "&lt;div") {
		t.Errorf("HTML tags in code block should be escaped, got %q", out)
	}
	if err := validateHTMLNesting(out); err != nil {
		t.Errorf("invalid HTML: %v, got %q", err, out)
	}
}

func TestMarkdownToSimpleHTML_HorizontalRule(t *testing.T) {
	out := MarkdownToSimpleHTML("before\n---\nafter")
	if !strings.Contains(out, "——————————") {
		t.Errorf("expected wide horizontal rule, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_UnclosedCodeBlock(t *testing.T) {
	md := "```python\nprint('hello')\nprint('world')"
	out := MarkdownToSimpleHTML(md)
	if !strings.Contains(out, "print") {
		t.Errorf("unclosed code block content should still appear, got %q", out)
	}
	if !strings.Contains(out, "<pre><code>") {
		t.Errorf("unclosed code block should still get code tags, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_MultiLineBlockquote(t *testing.T) {
	md := "> line 1\n> line 2\n> line 3"
	out := MarkdownToSimpleHTML(md)
	if strings.Count(out, "<blockquote>") != 1 {
		t.Errorf("expected single blockquote, got %q", out)
	}
	if !strings.Contains(out, "line 1\nline 2\nline 3") {
		t.Errorf("expected all lines joined in blockquote, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_BlockquoteBreaksOnBlankLine(t *testing.T) {
	md := "> quote 1\n\n> quote 2"
	out := MarkdownToSimpleHTML(md)
	if strings.Count(out, "<blockquote>") != 2 {
		t.Errorf("blank line should create separate blockquotes, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_Table(t *testing.T) {
	md := "| Name | Age |\n|------|-----|\n| Alice | 30 |\n| Bob | 25 |"
	out := MarkdownToSimpleHTML(md)
	if !strings.Contains(out, "<pre>") {
		t.Errorf("expected table wrapped in <pre>, got %q", out)
	}
	if !strings.Contains(out, "Name") || !strings.Contains(out, "Age") {
		t.Errorf("expected table header cells, got %q", out)
	}
	if !strings.Contains(out, "Alice") || !strings.Contains(out, "30") {
		t.Errorf("expected table data cells, got %q", out)
	}
	// Columns should be aligned with padding
	if !strings.Contains(out, "-----+-") {
		t.Errorf("expected aligned separator row, got %q", out)
	}
}

func TestMarkdownToSimpleHTML_TableWithFormatting(t *testing.T) {
	// Telegram's HTML parser accepts <b>, <i>, <code>, <a> inside <pre>, so
	// bold/italic/inline-code/link cells should render as the corresponding
	// tags — not as literal `**Header**` and friends.
	md := "| **Header** | `code` |\n|---|---|\n| *italic* | normal |"
	out := MarkdownToSimpleHTML(md)
	if !strings.Contains(out, "<pre>") {
		t.Errorf("expected table wrapped in <pre>, got %q", out)
	}
	if !strings.Contains(out, "<b>Header</b>") {
		t.Errorf("expected **Header** to render as <b>Header</b>, got %q", out)
	}
	if !strings.Contains(out, "<code>code</code>") {
		t.Errorf("expected `code` to render as <code>code</code>, got %q", out)
	}
	if !strings.Contains(out, "<i>italic</i>") {
		t.Errorf("expected *italic* to render as <i>italic</i>, got %q", out)
	}
	// The literal markdown markers must be gone from cells.
	if strings.Contains(out, "**Header**") {
		t.Errorf("literal **Header** should have been replaced by <b>Header</b>, got %q", out)
	}
	if strings.Contains(out, "`code`") {
		t.Errorf("literal `code` should have been replaced by <code>code</code>, got %q", out)
	}
}

// TestMarkdownToSimpleHTML_TableCellAlignmentWithFormatting regresses the
// column-width calculation: `**hunt**` must be measured as visual width 4
// (the runes of "hunt"), not as byte count including the asterisks. Before
// the fix, flushTable used byte length of the raw cell, which mis-aligned
// columns once the cells contained markdown markers.
func TestMarkdownToSimpleHTML_TableCellAlignmentWithFormatting(t *testing.T) {
	md := "| Skill | Use |\n|---|---|\n| **hunt** | debug |\n| **think** | plan |"
	out := MarkdownToSimpleHTML(md)
	// Expected column widths: col1 = max("Skill", "hunt", "think") = 5,
	// col2 = max("Use", "debug", "plan") = 5. So separator row is `-----+-----`.
	if !strings.Contains(out, "-----+-----") {
		t.Errorf("expected separator row matching stripped column widths (5+5), got %q", out)
	}
	// Body rows should pad to the same visual column width.
	// "hunt" is 4 runes, col width is 5, so one trailing space after </b>.
	if !strings.Contains(out, "<b>hunt</b>  | debug") {
		t.Errorf("expected hunt cell rendered with bold + single-space pad, got %q", out)
	}
	if !strings.Contains(out, "<b>think</b> | plan") {
		t.Errorf("expected think cell rendered with bold + no pad (5 runes matches col width), got %q", out)
	}
}

// TestMarkdownToSimpleHTML_TableCellWithLink verifies that links in cells
// are rendered as clickable <a> tags (Telegram supports <a> inside <pre>).
func TestMarkdownToSimpleHTML_TableCellWithLink(t *testing.T) {
	md := "| Source | Dest |\n|---|---|\n| [Waza](https://github.com/tw93/Waza) | local |"
	out := MarkdownToSimpleHTML(md)
	if !strings.Contains(out, `<a href="https://github.com/tw93/Waza">Waza</a>`) {
		t.Errorf("expected link rendered inside table cell, got %q", out)
	}
	if strings.Contains(out, "[Waza]") {
		t.Errorf("literal [Waza] should have been replaced by <a> tag, got %q", out)
	}
}

func TestTableCellVisualWidth(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"hunt", 4},
		{"**hunt**", 4},
		{"`hunt`", 4},
		{"*hunt*", 4},
		{"~~hunt~~", 4},
		{"***hunt***", 4},
		{"[Waza](https://github.com/tw93/Waza)", 4},
		{"调试错误", 4}, // 4 runes, regardless of UTF-8 byte count
		{"normal", 6},
		{"", 0},
	}
	for _, c := range cases {
		if got := tableCellVisualWidth(c.in); got != c.want {
			t.Errorf("tableCellVisualWidth(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSplitMessageCodeFenceAware_Short(t *testing.T) {
	chunks := SplitMessageCodeFenceAware("hello", 100)
	if len(chunks) != 1 || chunks[0] != "hello" {
		t.Errorf("unexpected: %v", chunks)
	}
}

func TestSplitMessageCodeFenceAware_PreservesCodeBlock(t *testing.T) {
	lines := []string{
		"before",
		"```python",
		"print('hello')",
		"print('world')",
		"```",
		"after",
	}
	text := strings.Join(lines, "\n")

	chunks := SplitMessageCodeFenceAware(text, 30)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}

	full := strings.Join(chunks, "")
	if !strings.Contains(full, "print('hello')") {
		t.Error("content should be preserved")
	}
}

func TestSplitMessageCodeFenceAware_NoCodeBlock(t *testing.T) {
	text := strings.Repeat("abcdefghij\n", 20)
	chunks := SplitMessageCodeFenceAware(text, 50)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for _, chunk := range chunks {
		if len(chunk) > 50 {
			t.Errorf("chunk exceeds max len: %d", len(chunk))
		}
	}
}

func TestSplitMessageCodeFenceAware_ChunkDoesNotExceedMaxLen(t *testing.T) {
	// Build text: a code block long enough to force splitting
	var sb strings.Builder
	sb.WriteString("```go\n")
	for i := 0; i < 30; i++ {
		sb.WriteString(fmt.Sprintf("line %d: some code content here\n", i))
	}
	sb.WriteString("```\n")
	text := sb.String()

	maxLen := 100
	chunks := SplitMessageCodeFenceAware(text, maxLen)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if len(chunk) > maxLen {
			t.Errorf("chunk %d exceeds maxLen (%d): len=%d, content=%q", i, maxLen, len(chunk), chunk)
		}
	}
}

func TestMarkdownToSimpleHTML_BoldItalic(t *testing.T) {
	out := MarkdownToSimpleHTML("this is ***bold italic*** text")
	if !strings.Contains(out, "<b><i>bold italic</i></b>") {
		t.Errorf("expected <b><i>bold italic</i></b>, got %q", out)
	}
	if err := validateHTMLNesting(out); err != nil {
		t.Errorf("invalid HTML nesting: %v, got %q", err, out)
	}
}

func TestMarkdownToSimpleHTML_Wikilink(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple wikilink", "see [[MyPage]]", "MyPage"},
		{"wikilink with display text", "see [[MyPage|Display Text]]", "Display Text"},
		{"wikilink escapes html", "see [[Page<script>]]", "Page&lt;script&gt;"},  // escapeHTML in step 3 handles this
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := MarkdownToSimpleHTML(tt.input)
			if !strings.Contains(out, tt.want) {
				t.Errorf("expected %q in output, got %q", tt.want, out)
			}
			// Should not contain [[ or ]] in output
			if strings.Contains(out, "[[") || strings.Contains(out, "]]") {
				t.Errorf("wikilink brackets should be removed, got %q", out)
			}
		})
	}
}

func TestMarkdownToSimpleHTML_Callout(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"callout with title",
			"> [!info] Important Note\n> This is the content",
			"<blockquote><b>info: Important Note</b>\nThis is the content</blockquote>",
		},
		{
			"callout without title",
			"> [!warn]\n> Be careful",
			"<blockquote><b>warn</b>\nBe careful</blockquote>",
		},
		{
			"normal blockquote unchanged",
			"> just a quote",
			"<blockquote>just a quote</blockquote>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := MarkdownToSimpleHTML(tt.input)
			if !strings.Contains(out, tt.want) {
				t.Errorf("expected %q in output, got %q", tt.want, out)
			}
		})
	}
}
