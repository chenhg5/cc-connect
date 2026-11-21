package codex

import (
	"runtime"
	"testing"
)

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

func TestSameCodexPath_PosixPathsAreCaseSensitive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("paths are case-insensitive on Windows")
	}
	if sameCodexPath("/srv/Codex/proj", "/srv/codex/proj") {
		t.Fatal("expected POSIX paths differing only in case to be different")
	}
	if !sameCodexPath("/srv/Codex/proj", "/srv/Codex/proj/") {
		t.Fatal("expected identical POSIX paths to match after cleaning")
	}
}
