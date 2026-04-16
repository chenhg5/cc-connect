package discord

import "testing"

func TestWrapTablesInCodeBlocks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no table",
			in:   "hello world\nno tables here",
			want: "hello world\nno tables here",
		},
		{
			name: "simple table",
			in:   "before\n| a | b |\n| - | - |\n| 1 | 2 |\nafter",
			want: "before\n```\n| a | b |\n| - | - |\n| 1 | 2 |\n```\nafter",
		},
		{
			name: "table already in code block",
			in:   "```\n| a | b |\n| 1 | 2 |\n```",
			want: "```\n| a | b |\n| 1 | 2 |\n```",
		},
		{
			name: "table at end of content",
			in:   "text\n| x | y |\n| 1 | 2 |",
			want: "text\n```\n| x | y |\n| 1 | 2 |\n```",
		},
		{
			name: "multiple tables",
			in:   "| a | b |\n| 1 | 2 |\n\ntext\n| c | d |\n| 3 | 4 |",
			want: "```\n| a | b |\n| 1 | 2 |\n```\n\ntext\n```\n| c | d |\n| 3 | 4 |\n```",
		},
		{
			name: "pipe in regular text not treated as table",
			in:   "use | for OR operations",
			want: "use | for OR operations",
		},
		{
			name: "table with code block after",
			in:   "| a | b |\n| 1 | 2 |\n```go\nfmt.Println()\n```",
			want: "```\n| a | b |\n| 1 | 2 |\n```\n```go\nfmt.Println()\n```",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapTablesInCodeBlocks(tt.in)
			if got != tt.want {
				t.Errorf("wrapTablesInCodeBlocks():\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}
