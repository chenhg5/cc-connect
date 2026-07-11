package daemon

import (
	"strings"
	"testing"
)

func TestParseLogBackups(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		// Happy path
		{name: "one", in: "1", want: 1},
		{name: "three (issue #1222 default)", in: "3", want: 3},
		{name: "ten", in: "10", want: 10},
		{name: "hundred", in: "100", want: 100},

		// Whitespace is trimmed
		{name: "leading space", in: "  3", want: 3},
		{name: "trailing space", in: "3  ", want: 3},
		{name: "both", in: "  3  ", want: 3},
		{name: "tab", in: "\t5\t", want: 5},

		// Invalid: empty
		{name: "empty", in: "", wantErr: true},
		{name: "only spaces", in: "   ", wantErr: true},

		// Invalid: not an integer
		{name: "alpha", in: "abc", wantErr: true},
		{name: "float", in: "3.5", wantErr: true},
		{name: "with unit (we don't accept units here)", in: "3MB", wantErr: true},
		{name: "hex", in: "0x10", wantErr: true},
		{name: "trailing junk", in: "3abc", wantErr: true},

		// Invalid: too small
		{name: "zero", in: "0", wantErr: true},
		{name: "negative one", in: "-1", wantErr: true},
		{name: "negative large", in: "-100", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLogBackups(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseLogBackups(%q) = %d, nil; want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLogBackups(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ParseLogBackups(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseLogBackups_ErrorMessageEchoesInput(t *testing.T) {
	// The caller (resolveLogMaxBackups) writes the original input to a
	// stderr warning, so the error must include the original string —
	// not just the trimmed form. This is the same invariant as
	// ParseLogSize, kept consistent so users see a uniform diagnostic
	// when they typo either env var.
	for _, in := range []string{"", "abc", "0", "-3", "3.5"} {
		_, err := ParseLogBackups(in)
		if err == nil {
			t.Fatalf("ParseLogBackups(%q) = nil err, want error", in)
		}
		if in == "" {
			// Empty input: error must mention "empty" so the caller
			// can still produce a useful diagnostic.
			if !strings.Contains(err.Error(), "empty") {
				t.Fatalf("ParseLogBackups(%q) error = %q, want substring 'empty'", in, err.Error())
			}
			continue
		}
		if !strings.Contains(err.Error(), in) {
			t.Fatalf("ParseLogBackups(%q) error = %q, want substring %q", in, err.Error(), in)
		}
	}
}
