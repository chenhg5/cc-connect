package core

import "testing"

func TestStripMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// Asterisk forms keep working — these are what agents typically emit.
		{"bold asterisk", "this is **bold** text", "this is bold text"},
		{"italic asterisk", "this is *italic* text", "this is italic text"},
		{"bold italic combined", "**bold** and *italic*", "bold and italic"},

		// Strikethrough, links, headings, code — unchanged behavior.
		{"strikethrough", "~~struck~~", "struck"},
		{"link", "[GitHub](https://github.com)", "GitHub (https://github.com)"},
		{"heading", "# Title\nbody", "Title\nbody"},
		{"inline code", "use `os.path.join`", "use os.path.join"},
		{"fenced code block", "```go\nfmt.Println(\"hi\")\n```", "fmt.Println(\"hi\")"},

		// Regression: underscore forms must NOT be stripped, since the same
		// patterns appear in legitimate identifiers. Stripping them used to
		// produce e.g. "mysnakecase_var" and "init", corrupting code
		// references in TTS / LINE / WeChat output.
		{"snake_case identifier", "call my_func_name() to start", "call my_func_name() to start"},
		{"two-underscore snake_case", "use module_name_var here", "use module_name_var here"},
		{"python dunder", "Python's __init__ method", "Python's __init__ method"},
		{"version dunder", "the __version__ string", "the __version__ string"},
		{"single underscore identifier", "function_name", "function_name"},

		// Underscore-form bold/italic is now kept literal (rare in practice).
		{"underscore italic kept literal", "_italic_ text", "_italic_ text"},
		{"underscore bold kept literal", "__bold__ text", "__bold__ text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripMarkdown(tt.in)
			if got != tt.want {
				t.Errorf("StripMarkdown(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
