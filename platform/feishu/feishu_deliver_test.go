package feishu

import (
	"os"
	"testing"
)

// TestDeliverableFileContents covers the CC_DELIVER_FILE token extraction used
// by AfterReply to auto-deliver script-produced payloads to Feishu. It is a pure
// helper (no network), so a minimal Platform is sufficient.
func TestDeliverableFileContents(t *testing.T) {
	p := &Platform{platformName: "feishu"}

	t.Run("reads temp file and returns content", func(t *testing.T) {
		f, err := os.CreateTemp("", "cc-deliver-*.md")
		if err != nil {
			t.Fatalf("create temp: %v", err)
		}
		want := "# 根因分析\n\n测试内容"
		if _, err := f.WriteString(want); err != nil {
			t.Fatalf("write: %v", err)
		}
		f.Close()
		path := f.Name()
		defer os.Remove(path) // no-op if already removed by delivery

		got := p.deliverableFileContents("正文一行 CC_DELIVER_FILE=" + path + " 结尾")
		if len(got) != 1 {
			t.Fatalf("expected 1 deliverable, got %d", len(got))
		}
		if got[0] != want {
			t.Fatalf("content mismatch: got %q want %q", got[0], want)
		}
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("temp file should have been removed after delivery")
		}
	})

	t.Run("rejects path outside temp dir", func(t *testing.T) {
		got := p.deliverableFileContents("CC_DELIVER_FILE=/etc/passwd")
		if len(got) != 0 {
			t.Fatalf("expected 0 deliverables for /etc/passwd, got %d", len(got))
		}
	})

	t.Run("rejects relative path", func(t *testing.T) {
		got := p.deliverableFileContents("CC_DELIVER_FILE=foo.md")
		if len(got) != 0 {
			t.Fatalf("expected 0 deliverables for relative path, got %d", len(got))
		}
	})

	t.Run("no token returns empty", func(t *testing.T) {
		got := p.deliverableFileContents("普通正文，没有 token")
		if len(got) != 0 {
			t.Fatalf("expected 0 deliverables, got %d", len(got))
		}
	})

	t.Run("multiple tokens", func(t *testing.T) {
		f1, _ := os.CreateTemp("", "cc-deliver-*.md")
		f2, _ := os.CreateTemp("", "cc-deliver-*.md")
		f1.WriteString("A")
		f2.WriteString("B")
		f1.Close()
		f2.Close()
		p1, p2 := f1.Name(), f2.Name()
		defer os.Remove(p1)
		defer os.Remove(p2)

		got := p.deliverableFileContents("x CC_DELIVER_FILE=" + p1 + " y CC_DELIVER_FILE=" + p2)
		if len(got) != 2 {
			t.Fatalf("expected 2 deliverables, got %d", len(got))
		}
	})
}

// TestAlreadyInBody covers the dedup guard: when the agent pasted the block
// (whose leading heading is in the body) instead of only echoing the token,
// file delivery must be skipped to avoid a duplicate.
func TestAlreadyInBody(t *testing.T) {
	block := "## 根因分析\n\n一些内容\n更多内容"

	t.Run("body has the heading -> true (skip delivery)", func(t *testing.T) {
		if !alreadyInBody("前面文字\n## 根因分析\n一些内容", block) {
			t.Fatalf("expected alreadyInBody=true when heading present in body")
		}
	})

	t.Run("body lacks the heading -> false (deliver)", func(t *testing.T) {
		if alreadyInBody("CC_DELIVER_FILE=/tmp/x.md 仅 token", block) {
			t.Fatalf("expected alreadyInBody=false when only token in body")
		}
	})

	t.Run("non-heading first line -> false", func(t *testing.T) {
		if alreadyInBody("## 根因分析", "普通文字开头\n不是标题") {
			t.Fatalf("expected alreadyInBody=false when deliverable has no heading")
		}
	})
}
