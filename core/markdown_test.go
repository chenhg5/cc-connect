package core

import "testing"

func TestStripMarkdown(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "italic underscore",
			input: "hello _world_",
			want:  "hello world",
		},
		{
			name:  "italic asterisk",
			input: "hello *world*",
			want:  "hello world",
		},
		{
			name:  "bold underscore",
			input: "hello __world__",
			want:  "hello world",
		},
		{
			name:  "bold asterisk",
			input: "hello **world**",
			want:  "hello world",
		},
		{
			name:  "intraword underscore preserved",
			input: "edit my_var_name",
			want:  "edit my_var_name",
		},
		{
			name:  "intraword double underscore preserved",
			input: "see my__var__name",
			want:  "see my__var__name",
		},
		{
			name:  "italic underscore inside sentence",
			input: "this is _important_ today",
			want:  "this is important today",
		},
		{
			name:  "italic underscore adjacent to punctuation",
			input: "(_x_),",
			want:  "(x),",
		},
		{
			name:  "code block content preserved",
			input: "```go\nfmt.Println(\"hi\")\n```",
			want:  "fmt.Println(\"hi\")",
		},
		{
			name:  "inline code content preserved",
			input: "run `my_func()` now",
			want:  "run my_func() now",
		},
		{
			name:  "link rendered with url",
			input: "go to [docs](https://example.com)",
			want:  "go to docs (https://example.com)",
		},
		{
			name:  "heading hash removed",
			input: "# Title\n\nbody",
			want:  "Title\n\nbody",
		},
		{
			name:  "blockquote prefix removed",
			input: "> a quote\n> another line",
			want:  "a quote\nanother line",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripMarkdown(tt.input)
			if got != tt.want {
				t.Errorf("StripMarkdown(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
