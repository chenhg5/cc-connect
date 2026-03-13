package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateStr(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "ascii short no truncation",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "ascii exact length",
			input:  "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "ascii truncated",
			input:  "hello world",
			maxLen: 5,
			want:   "hello...",
		},
		{
			name:   "chinese no truncation",
			input:  "你好世界",
			maxLen: 10,
			want:   "你好世界",
		},
		{
			name:   "chinese truncated",
			input:  "你好世界测试文本超长",
			maxLen: 4,
			want:   "你好世界...",
		},
		{
			name:   "emoji truncated",
			input:  "👋🌍🎉✨🚀💡",
			maxLen: 3,
			want:   "👋🌍🎉...",
		},
		{
			name:   "mixed multibyte truncated",
			input:  "hello你好world🎉",
			maxLen: 7,
			want:   "hello你好...",
		},
		{
			name:   "empty string",
			input:  "",
			maxLen: 5,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateStr(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateStr(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("truncateStr(%q, %d) produced invalid UTF-8: %q", tt.input, tt.maxLen, got)
			}
		})
	}
}

func TestTruncateRelay(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "short no truncation",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "exact length",
			input:  "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "ascii truncated with ellipsis",
			input:  "hello world",
			maxLen: 5,
			want:   "hello…",
		},
		{
			name:   "chinese truncated",
			input:  "这是一段很长的中文文本需要截断",
			maxLen: 6,
			want:   "这是一段很长…",
		},
		{
			name:   "emoji truncated",
			input:  "🎉🚀💡✨👋🌍",
			maxLen: 2,
			want:   "🎉🚀…",
		},
		{
			name:   "empty string",
			input:  "",
			maxLen: 5,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateRelay(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateRelay(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("truncateRelay(%q, %d) produced invalid UTF-8: %q", tt.input, tt.maxLen, got)
			}
		})
	}
}

func TestShellOutputTruncation(t *testing.T) {
	// Simulate the inline shell truncation logic from engine.go
	truncateShell := func(result string) string {
		if runes := []rune(result); len(runes) > 4000 {
			result = string(runes[:3997]) + "..."
		}
		return result
	}

	t.Run("short output unchanged", func(t *testing.T) {
		input := "hello world"
		got := truncateShell(input)
		if got != input {
			t.Errorf("expected no truncation, got %q", got)
		}
	})

	t.Run("long ascii output truncated", func(t *testing.T) {
		input := strings.Repeat("a", 5000)
		got := truncateShell(input)
		runes := []rune(got)
		if len(runes) != 4000 {
			t.Errorf("expected 4000 runes, got %d", len(runes))
		}
		if !utf8.ValidString(got) {
			t.Errorf("produced invalid UTF-8")
		}
	})

	t.Run("long chinese output truncated at char boundary", func(t *testing.T) {
		input := strings.Repeat("中", 5000)
		got := truncateShell(input)
		if !utf8.ValidString(got) {
			t.Errorf("produced invalid UTF-8: truncation split a multi-byte character")
		}
		runes := []rune(got)
		// 3997 中 + 3 dots = 4000 runes
		if len(runes) != 4000 {
			t.Errorf("expected 4000 runes, got %d", len(runes))
		}
	})

	t.Run("long emoji output truncated at char boundary", func(t *testing.T) {
		input := strings.Repeat("🎉", 5000)
		got := truncateShell(input)
		if !utf8.ValidString(got) {
			t.Errorf("produced invalid UTF-8: truncation split an emoji")
		}
		runes := []rune(got)
		if len(runes) != 4000 {
			t.Errorf("expected 4000 runes, got %d", len(runes))
		}
	})

	t.Run("exactly 4000 runes unchanged", func(t *testing.T) {
		input := strings.Repeat("你", 4000)
		got := truncateShell(input)
		if got != input {
			t.Errorf("expected no truncation for exactly 4000 runes")
		}
	})
}
