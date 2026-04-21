package coco

import "testing"

func TestCleanAnsi(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"plain text", "hello world", "hello world"},
		{"empty string", "", ""},
		{"single escape", "\x1b[31mred\x1b[0m", "red"},
		{"bold and color", "\x1b[1m\x1b[32mgreen bold\x1b[0m", "green bold"},
		{"cursor movement", "\x1b[2Jhello", "hello"},
		{"multiple sequences", "\x1b[36mcyan\x1b[0m normal \x1b[33myellow\x1b[0m", "cyan normal yellow"},
		{"no terminator mid-string", "before\x1b[999mafter", "beforeafter"},
		{"unicode content", "\x1b[31m你好世界\x1b[0m", "你好世界"},
		{"newlines preserved", "\x1b[32mline1\nline2\x1b[0m", "line1\nline2"},
		{"tabs preserved", "col1\tcol2", "col1\tcol2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanAnsi(tt.input)
			if got != tt.expected {
				t.Errorf("cleanAnsi(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
