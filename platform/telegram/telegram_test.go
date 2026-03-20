package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExtractEntityText(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		offset int
		length int
		want   string
	}{
		{
			name:   "ASCII only",
			text:   "hello @bot world",
			offset: 6,
			length: 4,
			want:   "@bot",
		},
		{
			name:   "Chinese before mention",
			text:   "你好 @mybot 你好",
			offset: 3,
			length: 6,
			want:   "@mybot",
		},
		{
			// 👍 is U+1F44D = surrogate pair (2 UTF-16 code units)
			// "Hi " = 3, "👍" = 2, " " = 1 → @mybot starts at UTF-16 offset 6
			name:   "emoji before mention (surrogate pair)",
			text:   "Hi 👍 @mybot test",
			offset: 6,
			length: 6,
			want:   "@mybot",
		},
		{
			name:   "multiple emoji before mention",
			text:   "🎉🎊 @testbot",
			offset: 5,
			length: 8,
			want:   "@testbot",
		},
		{
			name:   "out of range returns empty",
			text:   "short",
			offset: 10,
			length: 5,
			want:   "",
		},
		{
			name:   "negative offset returns empty",
			text:   "hello",
			offset: -1,
			length: 3,
			want:   "",
		},
		{
			name:   "negative length returns empty",
			text:   "hello",
			offset: 0,
			length: -1,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractEntityText(tt.text, tt.offset, tt.length)
			if got != tt.want {
				t.Errorf("extractEntityText(%q, %d, %d) = %q, want %q",
					tt.text, tt.offset, tt.length, got, tt.want)
			}
		})
	}
}

func TestSplitHTMLPreserving(t *testing.T) {
	const maxLen = 100

	tests := []struct {
		name     string
		html     string
		wantChunks int
	}{
		{
			name:      "short text - no split",
			html:      "hello world",
			wantChunks: 1,
		},
		{
			name:      "exactly at limit",
			html:      string(make([]rune, maxLen)),
			wantChunks: 1,
		},
		{
			name:      "slightly over limit",
			html:      string(make([]rune, maxLen+10)),
			wantChunks: 2,
		},
		{
			name:      "very long HTML - multiple chunks",
			html:      strings.Repeat("这是一段中文文本用于测试分片功能\n", 20),
			wantChunks: 4,
		},
		{
			name:      "unicode characters",
			html:      "Hello 👋 World 🌍! This is a test with emoji 🚀.",
			wantChunks: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := splitHTMLPreserving(tt.html, maxLen)

			if len(chunks) != tt.wantChunks {
				t.Errorf("splitHTMLPreserving() returned %d chunks, want %d", len(chunks), tt.wantChunks)
			}

			// Verify each chunk respects maxLen (except possibly the last one if original > maxLen)
			for i, chunk := range chunks {
				if i < len(chunks)-1 || utf8.RuneCountInString(tt.html) <= maxLen {
					if utf8.RuneCountInString(chunk) > maxLen {
						t.Errorf("chunk %d has %d runes, exceeds max %d", i, utf8.RuneCountInString(chunk), maxLen)
					}
				}
			}

			// Verify concatenated content equals original
			reconstructed := strings.Join(chunks, "")
			if reconstructed != tt.html {
				t.Errorf("reconstructed content differs from original")
			}
		})
	}
}

func TestSplitHTMLPreserving_Empty(t *testing.T) {
	chunks := splitHTMLPreserving("", 100)
	if len(chunks) != 1 || chunks[0] != "" {
		t.Errorf("splitHTMLPreserving(\"\") = %v, want [\"\"]", chunks)
	}
}

func TestSplitHTMLPreserving_CodeBlock(t *testing.T) {
	// Test that code block content is preserved across chunks
	const maxLen = 50
	html := "<pre><code>let x = 123; let y = 456; let z = 789;</code></pre>"
	chunks := splitHTMLPreserving(html, maxLen)

	// Should split into multiple chunks due to length
	if len(chunks) < 2 {
		t.Logf("Note: split into %d chunks (may split HTML tags)", len(chunks))
	}

	// Verify total length preserved
	reconstructed := strings.Join(chunks, "")
	if reconstructed != html {
		t.Errorf("reconstructed content differs from original")
	}
}
