package core

import "testing"

func TestClassifyAnthropicError(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    ErrorKind
	}{
		{
			name:    "shape A rate limit",
			payload: `{"error":{"type":"rate_limit_error","message":"rate limit exceeded"}}`,
			want:    ErrorKindRateLimit,
		},
		{
			name:    "shape A overloaded",
			payload: `{"error":{"type":"overloaded_error","message":"overloaded"}}`,
			want:    ErrorKindOverloaded,
		},
		{
			name:    "shape B top-level type",
			payload: `{"type":"rate_limit_error"}`,
			want:    ErrorKindRateLimit,
		},
		{
			name:    "embedded JSON with prefix",
			payload: `API Error: {"error":{"type":"rate_limit_error","message":"slow down"}}`,
			want:    ErrorKindRateLimit,
		},
		{
			name:    "embedded JSON with shell-style prefix",
			payload: `claude: error: {"error":{"type":"overloaded_error"}}`,
			want:    ErrorKindOverloaded,
		},
		{
			name:    "literal fallback rate_limit_error in prose",
			payload: `Request failed: rate_limit_error: too many requests from this key`,
			want:    ErrorKindRateLimit,
		},
		{
			name:    "literal fallback overloaded_error in prose",
			payload: `Upstream returned overloaded_error after 3 retries`,
			want:    ErrorKindOverloaded,
		},
		{
			name:    "literal fallback case-insensitive",
			payload: `API RETURNED Rate_Limit_Error STATUS`,
			want:    ErrorKindRateLimit,
		},
		{
			name:    "invalid_request_error not retried",
			payload: `{"error":{"type":"invalid_request_error","message":"bad model"}}`,
			want:    ErrorKindUnknown,
		},
		{
			name:    "authentication_error not retried",
			payload: `{"error":{"type":"authentication_error","message":"bad key"}}`,
			want:    ErrorKindUnknown,
		},
		{
			name:    "api_error (500) not retried by this helper",
			payload: `{"error":{"type":"api_error","message":"server fail"}}`,
			want:    ErrorKindUnknown,
		},
		{
			name:    "compilation error — unrelated prose",
			payload: `internal compiler error at line 42`,
			want:    ErrorKindUnknown,
		},
		{
			name:    "generic rate limit prose without canonical token",
			payload: `You are being rate limited, please wait`,
			want:    ErrorKindUnknown, // intentionally NOT matched — requires canonical token
		},
		{
			name:    "HTTP 429 text alone",
			payload: `HTTP 429 Too Many Requests`,
			want:    ErrorKindUnknown, // not a canonical Anthropic token
		},
		{
			name:    "empty",
			payload: "",
			want:    ErrorKindUnknown,
		},
		{
			name:    "malformed JSON with canonical token",
			payload: `{not valid json but contains rate_limit_error somewhere}`,
			want:    ErrorKindRateLimit,
		},
		{
			name:    "nested error object without type",
			payload: `{"error":{"message":"something went wrong"}}`,
			want:    ErrorKindUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyAnthropicError(tc.payload)
			if got != tc.want {
				t.Errorf("ClassifyAnthropicError(%q) = %q, want %q", tc.payload, got, tc.want)
			}
		})
	}
}

func TestErrorKindIsRetriable(t *testing.T) {
	cases := []struct {
		kind ErrorKind
		want bool
	}{
		{ErrorKindUnknown, false},
		{ErrorKindRateLimit, true},
		{ErrorKindOverloaded, true},
	}
	for _, tc := range cases {
		if got := tc.kind.IsRetriable(); got != tc.want {
			t.Errorf("(%q).IsRetriable() = %v, want %v", tc.kind, got, tc.want)
		}
	}
}
