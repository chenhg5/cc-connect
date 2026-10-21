package daemon

import (
	"strings"
	"testing"
)

func TestParseLogSize(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		// Raw bytes
		{"0", 0, false},
		{"1", 1, false},
		{"1024", 1024, false},
		{"10485760", 10485760, false},

		// K / KB
		{"1K", 1024, false},
		{"1KB", 1024, false},
		{"10K", 10 * 1024, false},
		{"10KB", 10 * 1024, false},
		{"512k", 512 * 1024, false},
		{"512kb", 512 * 1024, false},

		// M / MB — the user's actual use case from issue #1222
		{"1M", 1024 * 1024, false},
		{"1MB", 1024 * 1024, false},
		{"10M", 10 * 1024 * 1024, false},
		{"10MB", 10 * 1024 * 1024, false},
		{"100mb", 100 * 1024 * 1024, false},

		// G / GB
		{"1G", 1024 * 1024 * 1024, false},
		{"1GB", 1024 * 1024 * 1024, false},
		{"2gb", 2 * 1024 * 1024 * 1024, false},

		// T / TB
		{"1T", 1024 * 1024 * 1024 * 1024, false},
		{"1TB", 1024 * 1024 * 1024 * 1024, false},

		// Whitespace tolerance
		{"  10MB  ", 10 * 1024 * 1024, false},
		{"10 MB", 10 * 1024 * 1024, false},

		// Explicit bare-byte suffix "B"
		{"100B", 100, false},
		{"1b", 1, false},

		// Errors
		{"", 0, true},
		{"   ", 0, true},
		{"MB", 0, true},            // missing numeric part
		{"-10MB", 0, true},         // negative rejected
		{"abc", 0, true},           // garbage
		{"10XYZ", 0, true},         // unknown suffix must fail loudly
		{"10.5MB", 0, true},        // fractional not supported
		{"0x10", 0, true},          // not hex
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseLogSize(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseLogSize(%q) = %d, nil; want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLogSize(%q): unexpected error %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ParseLogSize(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseLogSize_RegressionForIssue1222(t *testing.T) {
	// Before the fix, this was parsed via strconv.ParseInt("10MB", 10, 64)
	// which silently returned 0 + error, then the env var was ignored and
	// the default 10MB was used. The test pins the new behaviour: "10MB"
	// must equal 10 * 1024 * 1024.
	got, err := ParseLogSize("10MB")
	if err != nil {
		t.Fatalf("ParseLogSize(\"10MB\"): %v", err)
	}
	if got != int64(DefaultLogMaxSize) {
		t.Fatalf("ParseLogSize(\"10MB\") = %d, want %d (== DefaultLogMaxSize)", got, DefaultLogMaxSize)
	}
}

func TestParseLogSize_ErrorMentionsInput(t *testing.T) {
	// Error messages should echo the input verbatim so users can grep
	// their config or systemd unit without remembering what they typed.
	_, err := ParseLogSize("10XYZ")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "10XYZ") {
		t.Fatalf("error %q should mention input", err.Error())
	}
}
