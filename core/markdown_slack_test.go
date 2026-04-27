package core

import "testing"

func TestMarkdownToSlackMrkdwn(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "bold conversion",
			input: "This is **bold** text",
			want:  "This is *bold* text",
		},
		{
			name:  "bold-italic conversion",
			input: "This is ***bold italic*** text",
			want:  "This is *_bold italic_* text",
		},
		{
			name:  "strikethrough",
			input: "This is ~~deleted~~ text",
			want:  "This is ~deleted~ text",
		},
		{
			name:  "link conversion",
			input: "Check [Google](https://google.com) out",
			want:  "Check <https://google.com|Google> out",
		},
		{
			name:  "heading",
			input: "# Title\n## Subtitle",
			want:  "*Title*\n*Subtitle*",
		},
		{
			name:  "inline code preserved",
			input: "Use `**not bold**` here",
			want:  "Use `**not bold**` here",
		},
		{
			name:  "code block preserved",
			input: "Before\n```go\nfmt.Println(\"**hello**\")\n```\nAfter **bold**",
			want:  "Before\n```go\nfmt.Println(\"**hello**\")\n```\nAfter *bold*",
		},
		{
			name:  "multiple bold in one line",
			input: "**first** and **second**",
			want:  "*first* and *second*",
		},
		{
			name:  "mixed formatting",
			input: "**bold** and ~~strike~~ and [link](http://x.com)",
			want:  "*bold* and ~strike~ and <http://x.com|link>",
		},
		{
			name:  "no formatting",
			input: "Plain text here",
			want:  "Plain text here",
		},
		{
			name:  "image tag",
			input: "See ![screenshot](http://img.png) here",
			want:  "See screenshot here",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MarkdownToSlackMrkdwn(tt.input)
			if got != tt.want {
				t.Errorf("MarkdownToSlackMrkdwn(%q)\n  got:  %q\n  want: %q", tt.input, got, tt.want)
			}
		})
	}
}
