package core

import "testing"

func TestSemverCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int // >0, <0, or 0
	}{
		{"v1.0.0", "v1.0.0", 0},
		{"v1.0.1", "v1.0.0", 1},
		{"v1.0.0", "v1.0.1", -1},
		{"v2.0.0", "v1.9.9", 1},
		{"v1.1.0", "v1.0.9", 1},

		// pre-release vs release
		{"v1.0.0", "v1.0.0-beta.1", 1},
		{"v1.0.0-beta.1", "v1.0.0", -1},

		// pre-release ordering
		{"v1.0.0-beta.2", "v1.0.0-beta.1", 1},
		{"v1.0.0-beta.1", "v1.0.0-beta.2", -1},
		{"v1.0.0-beta.1", "v1.0.0-beta.1", 0},

		// different pre-release prefixes
		{"v1.0.0-rc.1", "v1.0.0-beta.1", 1},  // "rc" > "beta" lexicographically
		{"v1.0.0-alpha.1", "v1.0.0-beta.1", -1},

		// without 'v' prefix
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
	}

	for _, tt := range tests {
		got := semverCompare(tt.a, tt.b)
		if (tt.want > 0 && got <= 0) || (tt.want < 0 && got >= 0) || (tt.want == 0 && got != 0) {
			t.Errorf("semverCompare(%q, %q) = %d, want sign %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestParseSemver(t *testing.T) {
	s := parseSemver("v1.2.3-beta.4")
	if s.major != 1 || s.minor != 2 || s.patch != 3 {
		t.Errorf("parsed %+v, want 1.2.3", s)
	}
	if s.pre != "beta.4" {
		t.Errorf("pre = %q, want beta.4", s.pre)
	}
	if s.preNum != 4 {
		t.Errorf("preNum = %d, want 4", s.preNum)
	}
}

func TestParseSemver_NoPreRelease(t *testing.T) {
	s := parseSemver("v2.0.0")
	if s.major != 2 || s.minor != 0 || s.patch != 0 {
		t.Errorf("parsed %+v, want 2.0.0", s)
	}
	if s.pre != "" {
		t.Errorf("pre = %q, want empty", s.pre)
	}
}

func TestParseSemver_Invalid(t *testing.T) {
	s := parseSemver("not-a-version")
	if s.major != 0 && s.minor != 0 && s.patch != 0 {
		t.Errorf("expected zero semver for invalid input, got %+v", s)
	}
}

func TestNormalizeVersion(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"1.0.0", "v1.0.0"},
		{"v1.0.0", "v1.0.0"},
		{" v1.0.0 ", "v1.0.0"},
		{"  2.3.4", "v2.3.4"},
	}
	for _, tt := range tests {
		got := normalizeVersion(tt.in)
		if got != tt.want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
