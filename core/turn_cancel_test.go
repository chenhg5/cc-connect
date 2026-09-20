package core

import "testing"

func TestIsIntentionalTurnCancel(t *testing.T) {
	cases := []struct {
		name string
		err  string
		want bool
	}{
		{name: "empty", err: "", want: false},
		{name: "grok stop", err: "grok: turn stopped: context canceled", want: true},
		{name: "british spelling", err: "turn stopped: context cancelled", want: true},
		{name: "timeout stays an error", err: "grok: turn stopped: context deadline exceeded", want: false},
		{name: "unrelated cancel", err: "context canceled", want: false},
		{name: "unrelated stop", err: "turn stopped by user", want: false},
		{name: "generic failure", err: "something broke", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isIntentionalTurnCancel(tc.err); got != tc.want {
				t.Fatalf("isIntentionalTurnCancel(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
