package core

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadLineLoop_BasicLines(t *testing.T) {
	var got []string
	err := ReadLineLoop(strings.NewReader("a\nb\nc\n"), func(line []byte) error {
		got = append(got, string(line))
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("line %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestReadLineLoop_StripsCRLF(t *testing.T) {
	var got []string
	err := ReadLineLoop(strings.NewReader("hello\r\nworld\r\n"), func(line []byte) error {
		got = append(got, string(line))
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != "hello" || got[1] != "world" {
		t.Fatalf("CRLF not stripped: %q", got)
	}
}

func TestReadLineLoop_SkipsEmptyLines(t *testing.T) {
	var got []string
	err := ReadLineLoop(strings.NewReader("\n\n\nfoo\n\nbar\n\n"), func(line []byte) error {
		got = append(got, string(line))
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != "foo" || got[1] != "bar" {
		t.Fatalf("empty lines not skipped: %q", got)
	}
}

func TestReadLineLoop_TrailingLineNoNewline(t *testing.T) {
	var got []string
	err := ReadLineLoop(strings.NewReader("a\nb\nc"), func(line []byte) error {
		got = append(got, string(line))
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("line %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestReadLineLoop_HandleError(t *testing.T) {
	want := errors.New("boom")
	err := ReadLineLoop(strings.NewReader("a\nb\nc\n"), func(line []byte) error {
		if string(line) == "b" {
			return want
		}
		return nil
	})
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func TestReadLineLoop_ReaderError(t *testing.T) {
	r := &errReader{err: io.ErrUnexpectedEOF, after: 3}
	err := ReadLineLoop(r, func(line []byte) error { return nil })
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want %v", err, io.ErrUnexpectedEOF)
	}
}

func TestReadLineLoop_Empty(t *testing.T) {
	count := 0
	err := ReadLineLoop(strings.NewReader(""), func(line []byte) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("got %d lines, want 0", count)
	}
}

// TestReadLineLoop_LargeJSONL exercises a 100KB single line plus a normal
// line to confirm the reader doesn't drop or split data on large JSONL
// payloads (the bug from issue #189 was bufio.Scanner terminating on
// >64KB lines; this should now succeed cleanly).
func TestReadLineLoop_LargeJSONL(t *testing.T) {
	const big = 100 * 1024 // 100KB
	prefix := `{"data":"`  // 9 chars
	suffix := `"}`         // 2 chars
	wantBig := len(prefix) + big + len(suffix)

	payload := make([]byte, 0, wantBig+len(`{"data":"small"}\n`)+8)
	payload = append(payload, prefix...)
	payload = append(payload, bytes.Repeat([]byte("x"), big)...)
	payload = append(payload, suffix...)
	payload = append(payload, '\n')
	payload = append(payload, `{"data":"small"}`...)
	payload = append(payload, '\n')

	var got [][]byte
	err := ReadLineLoop(bytes.NewReader(payload), func(line []byte) error {
		buf := make([]byte, len(line))
		copy(buf, line)
		got = append(got, buf)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2", len(got))
	}
	if len(got[0]) != wantBig {
		t.Fatalf("line 0 size = %d, want %d", len(got[0]), wantBig)
	}
	if string(got[1]) != `{"data":"small"}` {
		t.Fatalf("line 1 = %q, want %q", got[1], `{"data":"small"}`)
	}
}

// TestReadLineLoop_OneMegabyteLine covers the boundary between typical
// and large single-line payloads (e.g. one MCP tool result).
func TestReadLineLoop_OneMegabyteLine(t *testing.T) {
	const big = 1 * 1024 * 1024      // 1MB
	prefix := `{"v":"`               // 6 chars
	wantBig := len(prefix) + big + 1 // closing '"'

	payload := append([]byte(prefix), bytes.Repeat([]byte("a"), big)...)
	payload = append(payload, '"', '\n')

	var got []byte
	err := ReadLineLoop(bytes.NewReader(payload), func(line []byte) error {
		got = append(got, line...)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != wantBig {
		t.Fatalf("got %d bytes, want %d", len(got), wantBig)
	}
}

// TestReadLineLoop_TenMegabyteLine covers a single-line payload at the
// default cap (10MB). The line should be passed through to handle without
// being dropped.
func TestReadLineLoop_TenMegabyteLine(t *testing.T) {
	// Pick big so that the wrapped line fits exactly at the cap.
	// Wrapper is `{"v":"` + <big bs> + `"` + `\n` = len(prefix)+big+2 bytes.
	// After trimming the trailing `\n`, handle receives big+len(prefix)+1 bytes.
	prefix := `{"v":"`
	big := DefaultMaxLineSize - len(prefix) - 2

	payload := append([]byte(prefix), bytes.Repeat([]byte("b"), big)...)
	payload = append(payload, '"', '\n')

	var got []byte
	err := ReadLineLoop(bytes.NewReader(payload), func(line []byte) error {
		got = append(got, line...)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := big + len(prefix) + 1
	if len(got) != want {
		t.Fatalf("got %d bytes, want %d", len(got), want)
	}
}

// TestReadLineLoop_OverflowLineDropped verifies that a line larger than
// the cap is logged and dropped, while the next valid line is still
// delivered to handle. This is the OOM-protection behavior.
func TestReadLineLoop_OverflowLineDropped(t *testing.T) {
	const cap = 1024
	const huge = cap * 4
	var payload bytes.Buffer
	payload.WriteString(`{"v":"`)
	payload.Write(bytes.Repeat([]byte("c"), huge))
	payload.WriteString(`"}`)
	payload.WriteByte('\n')
	payload.WriteString(`{"v":"ok"}`)
	payload.WriteByte('\n')

	var got []string
	err := ReadLineLoopWithLimit(&payload, cap, func(line []byte) error {
		got = append(got, string(line))
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != `{"v":"ok"}` {
		t.Fatalf("got %v, want only [%q]", got, `{"v":"ok"}`)
	}
}

// TestReadLineLoop_OverflowMidStreamDropsAndContinues stresses the
// "drop and continue" contract: a 4x oversize line in the middle of a
// stream must not abort later lines.
func TestReadLineLoop_OverflowMidStreamDropsAndContinues(t *testing.T) {
	const cap = 512
	var payload bytes.Buffer
	for i := 0; i < 5; i++ {
		payload.WriteString(`"`)
		payload.Write(bytes.Repeat([]byte("z"), cap*4))
		payload.WriteString(`"`)
		payload.WriteByte('\n')
		payload.WriteString(`{"small":true}`)
		payload.WriteByte('\n')
	}

	var smallCount int
	err := ReadLineLoopWithLimit(&payload, cap, func(line []byte) error {
		if string(line) == `{"small":true}` {
			smallCount++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if smallCount != 5 {
		t.Fatalf("got %d small lines, want 5", smallCount)
	}
}

// TestReadLineLoopWithLimit_ZeroOrNegativeDefaults verifies that
// maxLineSize <= 0 falls back to DefaultMaxLineSize.
func TestReadLineLoopWithLimit_ZeroOrNegativeDefaults(t *testing.T) {
	prefix := `{"v":"`
	big := DefaultMaxLineSize - len(prefix) - 2

	payload := append([]byte(prefix), bytes.Repeat([]byte("d"), big)...)
	payload = append(payload, '"', '\n')

	got := 0
	err := ReadLineLoopWithLimit(bytes.NewReader(payload), 0, func(line []byte) error {
		got = len(line)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := big + len(prefix) + 1
	if got != want {
		t.Fatalf("got %d bytes, want %d", got, want)
	}
}

type errReader struct {
	err   error
	after int
	n     int
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.n >= r.after {
		return 0, r.err
	}
	n := copy(p, []byte("partial"))
	r.n += n
	return n, nil
}
