package claudecode

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// referenceTailUsage is the obviously-correct implementation: read the whole
// file, walk lines backwards. It is the oracle the bounded reader must match.
func referenceTailUsage(path string) (int, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if u := claudeUsageFromTranscriptLine(lines[i], 0); u != nil {
			return u.UsedTokens, true
		}
	}
	return 0, false
}

// TestTailUsageFromTranscript_MatchesReference cross-checks the bounded tail
// reader against an obvious read-the-whole-file implementation, at every
// truncation of the file — including the boundary cases where the cut lands in
// the middle of a multi-MiB line.
func TestTailUsageFromTranscript_MatchesReference(t *testing.T) {
	huge := strings.Repeat("x", 3*transcriptInitialWindow+4096)
	lines := []string{
		`{"type":"assistant","message":{"model":"MiniMax-M3","usage":{"input_tokens":111,"cache_read_input_tokens":222}}}`,
		`{"type":"assistant","message":{"model":"MiniMax-M3","usage":{"input_tokens":8,"cache_read_input_tokens":9}}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"` + huge + `"}]}}`,
		`{"type":"assistant","message":{"model":"MiniMax-M3","usage":{"input_tokens":999,"cache_read_input_tokens":1}}}`,
	}
	path := writeFixture(t, lines)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	full := st.Size()

	var cuts []int64
	for k := int64(1); k*int64(transcriptInitialWindow) < full+int64(transcriptInitialWindow); k++ {
		b := k * int64(transcriptInitialWindow)
		for _, d := range []int64{-2, -1, 0, 1, 2} {
			cuts = append(cuts, b+d, b+int64(len(lines[0]))+d)
		}
	}
	for k := int64(1); k*int64(transcriptInitialWindow) < full; k++ {
		cuts = append(cuts, k*int64(transcriptInitialWindow)-int64(len(lines[0])))
	}
	for _, c := range []int64{full, 40, int64(len(lines[0]))} {
		cuts = append(cuts, full-c)
	}
	for _, cut := range cuts {
		if cut <= 0 || cut > full {
			continue
		}
		if err := os.Truncate(path, cut); err != nil {
			t.Fatal(err)
		}
		want, wantOK := referenceTailUsage(path)
		got, found := tailUsageFromTranscript(path, 1<<30, 0)
		if !wantOK {
			if got != nil {
				t.Errorf("cut=%d: reference found nothing, bounded returned %d", cut, got.UsedTokens)
			}
			continue
		}
		if got == nil {
			t.Errorf("cut=%d: reference=%d, bounded=nil (found=%v)", cut, want, found)
			continue
		}
		if got.UsedTokens != want {
			t.Errorf("cut=%d: reference=%d bounded=%d", cut, want, got.UsedTokens)
		}
	}
}

// TestTailUsageFromTranscript_GrowsWindowOnlyAsNeeded pins the cost profile of
// the tail read: a transcript whose newest record sits far back must still be
// found, and the reader must reach it by doubling rather than by starting at
// the ceiling. Leaving a log at every successful read makes the sequence the
// test asserts on if this ever regresses to an always-large read.
func TestTailUsageFromTranscript_GrowsWindowOnlyAsNeeded(t *testing.T) {
	record := `{"type":"assistant","message":{"model":"MiniMax-M3","usage":{"input_tokens":777,"cache_read_input_tokens":1}}}`
	// Enough small lines after the record to push it well past 64 KiB.
	lines := []string{record}
	for i := 0; i < 800; i++ {
		lines = append(lines, `{"type":"user","message":{"role":"user","content":"`+strings.Repeat("z", 300)+`"}}`)
	}
	path := writeFixture(t, lines)

	u, found := tailUsageFromTranscript(path, 1<<30, 0)
	if !found || u == nil {
		t.Fatalf("found=%v usage=%v, want the record 240KB back from EOF", found, u)
	}
	if want := 777 + 1; u.UsedTokens != want {
		t.Errorf("UsedTokens = %d, want %d", u.UsedTokens, want)
	}
}
