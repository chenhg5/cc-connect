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
			name: "table at end of content with separator",
			in:   "text\n| x | y |\n| --- | --- |\n| 1 | 2 |",
			want: "text\n```\n| x | y |\n| --- | --- |\n| 1 | 2 |\n```",
		},
		{
			name: "pipe rows without separator not wrapped",
			in:   "text\n| x | y |\n| 1 | 2 |",
			want: "text\n| x | y |\n| 1 | 2 |",
		},
		{
			name: "multiple tables with separators",
			in:   "| a | b |\n| - | - |\n| 1 | 2 |\n\ntext\n| c | d |\n| --- | --- |\n| 3 | 4 |",
			want: "```\n| a | b |\n| - | - |\n| 1 | 2 |\n```\n\ntext\n```\n| c | d |\n| --- | --- |\n| 3 | 4 |\n```",
		},
		{
			name: "pipe in regular text not treated as table",
			in:   "use | for OR operations",
			want: "use | for OR operations",
		},
		{
			name: "table with code block after",
			in:   "| a | b |\n| - | - |\n| 1 | 2 |\n```go\nfmt.Println()\n```",
			want: "```\n| a | b |\n| - | - |\n| 1 | 2 |\n```\n```go\nfmt.Println()\n```",
		},
		{
			name: "aligned separator with colons",
			in:   "| left | center | right |\n| :--- | :---: | ---: |\n| a | b | c |",
			want: "```\n| left | center | right |\n| :--- | :---: | ---: |\n| a | b | c |\n```",
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
