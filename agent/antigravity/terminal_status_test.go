package antigravity

import "testing"

func TestCurrentResultOutcomeDoesNotDependOnPartialOrHistoricalText(t *testing.T) {
	for _, tc := range []struct{ status, response, message, want string }{
		{"ERROR", "partial response", "synthetic-secret", "error"},
		{"SUCCESS", "historical quota failure", "stale error", "success"},
		{"", "", "", ""},
	} {
		result := agyStreamResult{Status: tc.status, Response: tc.response, Error: tc.message}
		if got := result.terminalStatus(); got != tc.want {
			t.Fatalf("status %s: got %s want %s", tc.status, got, tc.want)
		}
	}
}
