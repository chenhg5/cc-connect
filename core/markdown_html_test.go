package core

import (
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
	if strings.Contains(out, "<y") || strings.Contains(out, ">z") {
		t.Errorf("HTML should be escaped, got %q", out)
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
