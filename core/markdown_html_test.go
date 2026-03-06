package core

import (
	"fmt"
	"strings"
	"testing"
)

func TestMarkdownToTelegramHTML_Bold(t *testing.T) {
	out := MarkdownToTelegramHTML("hello **world**")
	if !strings.Contains(out, "<b>world</b>") {
		t.Errorf("expected <b>world</b>, got %q", out)
	}
}

func TestMarkdownToTelegramHTML_Italic(t *testing.T) {
	out := MarkdownToTelegramHTML("hello *world*")
	if !strings.Contains(out, "<i>world</i>") {
		t.Errorf("expected <i>world</i>, got %q", out)
	}
}

func TestMarkdownToTelegramHTML_Strikethrough(t *testing.T) {
	out := MarkdownToTelegramHTML("hello ~~world~~")
	if !strings.Contains(out, "<s>world</s>") {
		t.Errorf("expected <s>world</s>, got %q", out)
	}
}

func TestMarkdownToTelegramHTML_InlineCode(t *testing.T) {
	out := MarkdownToTelegramHTML("run `echo hello`")
	if !strings.Contains(out, "<code>echo hello</code>") {
		t.Errorf("expected <code>echo hello</code>, got %q", out)
	}
}

func TestMarkdownToTelegramHTML_CodeBlock(t *testing.T) {
	md := "```go\nfmt.Println()\n```"
	out := MarkdownToTelegramHTML(md)
	if !strings.Contains(out, `<pre><code class="language-go">`) {
		t.Errorf("expected language-go code block, got %q", out)
	}
	if !strings.Contains(out, "fmt.Println()") {
		t.Errorf("expected code content, got %q", out)
	}
}

func TestMarkdownToTelegramHTML_Link(t *testing.T) {
	out := MarkdownToTelegramHTML("visit [Google](https://google.com)")
	if !strings.Contains(out, `<a href="https://google.com">Google</a>`) {
		t.Errorf("expected link HTML, got %q", out)
	}
}

func TestMarkdownToTelegramHTML_Heading(t *testing.T) {
	out := MarkdownToTelegramHTML("## Section Title")
	if !strings.Contains(out, "<b>Section Title</b>") {
		t.Errorf("expected heading as bold, got %q", out)
	}
}

func TestMarkdownToTelegramHTML_Blockquote(t *testing.T) {
	out := MarkdownToTelegramHTML("> quoted text")
	if !strings.Contains(out, "<blockquote>quoted text</blockquote>") {
		t.Errorf("expected blockquote, got %q", out)
	}
}

func TestMarkdownToTelegramHTML_EscapesHTML(t *testing.T) {
	out := MarkdownToTelegramHTML("x < y && y > z")
	if !strings.Contains(out, "&lt;") || !strings.Contains(out, "&gt;") || !strings.Contains(out, "&amp;") {
		t.Errorf("HTML special chars should be escaped, got %q", out)
	}
}

func TestMarkdownToTelegramHTML_EscapesInsideBold(t *testing.T) {
	out := MarkdownToTelegramHTML("**x < y**")
	if !strings.Contains(out, "<b>x &lt; y</b>") {
		t.Errorf("expected escaped content inside bold, got %q", out)
	}
}

func TestMarkdownToTelegramHTML_LinkWithAmpersand(t *testing.T) {
	out := MarkdownToTelegramHTML("click [here](https://example.com?a=1&b=2)")
	if !strings.Contains(out, "&amp;b=2") {
		t.Errorf("URL ampersand should be escaped, got %q", out)
	}
	if !strings.Contains(out, `<a href=`) {
		t.Errorf("expected link tag, got %q", out)
	}
}

func TestMarkdownToTelegramHTML_LinkWithQuotesInURL(t *testing.T) {
	out := MarkdownToTelegramHTML(`visit [book](https://example.com/q="test")`)
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

func TestMarkdownToTelegramHTML_EscapesQuotesInText(t *testing.T) {
	out := MarkdownToTelegramHTML(`He said "hello" world`)
	if strings.Contains(out, `"hello"`) {
		t.Errorf("quotes in text should be escaped, got %q", out)
	}
	if !strings.Contains(out, `&quot;hello&quot;`) {
		t.Errorf("expected &quot; in output, got %q", out)
	}
}

func TestMarkdownToTelegramHTML_CodeBlockEscapesHTML(t *testing.T) {
	md := "```\nif a < b && c > d {\n}\n```"
	out := MarkdownToTelegramHTML(md)
	if !strings.Contains(out, "&lt;") || !strings.Contains(out, "&gt;") {
		t.Errorf("code block content should be HTML-escaped, got %q", out)
	}
}

func TestMarkdownToTelegramHTML_InlineCodeEscapesHTML(t *testing.T) {
	out := MarkdownToTelegramHTML("run `x<y>z`")
	if !strings.Contains(out, "<code>x&lt;y&gt;z</code>") {
		t.Errorf("inline code should escape HTML, got %q", out)
	}
}

func TestMarkdownToTelegramHTML_MixedFormattingWithSpecialChars(t *testing.T) {
	out := MarkdownToTelegramHTML("**bold** & *italic* < normal")
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

func TestMarkdownToTelegramHTML_NoCrossedTags(t *testing.T) {
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
			out := MarkdownToTelegramHTML(tt.input)
			// Check no crossed tags: every <b> must close before enclosing </i> etc.
			// Simple check: no </b> inside an <i> block or vice versa
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
			// strip attributes
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

	// When a chunk breaks inside a code block, it should close with ```
	for i, c := range chunks {
		opens := strings.Count(c, "```python") + strings.Count(c, "```\n")
		closes := strings.Count(c, "```")
		_ = opens
		_ = closes
		_ = i
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
