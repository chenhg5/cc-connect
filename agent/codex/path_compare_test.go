package codex

import "testing"

func TestSameCodexPath_NormalizesWindowsSeparators(t *testing.T) {
	if !sameCodexPath(`D:/Codex/channels`, `D:\Codex\channels`) {
		t.Fatal("expected slash and backslash Windows paths to match")
	}
}

func TestSameCodexPath_NormalizesWindowsCase(t *testing.T) {
	if !sameCodexPath(`d:/codex/channels`, `D:\Codex\Channels`) {
		t.Fatal("expected Windows paths to match case-insensitively")
	}
}
