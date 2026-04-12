package core

import (
	"encoding/json"
	"strings"
	"time"
)

// Rate-limit retry timing. The product requirement is a literal
// "30s, then every minute, for up to ~30 minutes". These are `var` rather
// than `const` so tests can shorten them to keep the suite fast; production
// code should not reassign them.
var (
	RateLimitInitialDelay = 30 * time.Second
	RateLimitRetryDelay   = 60 * time.Second
	// RateLimitMaxAttempts is the total number of Send attempts including
	// the original one. With initial=30s and retry=60s, 30 attempts caps
	// the retry window at roughly 29.5 minutes.
	RateLimitMaxAttempts = 30
)

// ClassifyAnthropicError inspects an Anthropic-style error payload and
// returns a structured ErrorKind. It accepts three input shapes:
//
//  1. A full JSON object like `{"error":{"type":"rate_limit_error",...}}`.
//  2. A JSON object with a top-level `"type"` field.
//  3. A wrapper string that embeds (1) or (2) after some prefix text,
//     e.g. `API Error: {"error":{"type":"rate_limit_error"}}`.
//
// Detection targets Anthropic's canonical `error.type` strings
// ("rate_limit_error", "overloaded_error") — these come from the API's typed
// error schema, not free-form prose. Returns ErrorKindUnknown when nothing
// matches, in which case callers should NOT retry.
func ClassifyAnthropicError(payload string) ErrorKind {
	if payload == "" {
		return ErrorKindUnknown
	}

	// 1. Try parsing the whole payload as JSON.
	if k := classifyJSON([]byte(payload)); k != ErrorKindUnknown {
		return k
	}

	// 2. If the payload has a prefix before an embedded JSON object, slice
	//    from the first '{' and retry. CLI stderr often looks like
	//    "Error: {"error":{"type":"rate_limit_error"}}".
	if i := strings.Index(payload, "{"); i > 0 {
		if k := classifyJSON([]byte(payload[i:])); k != ErrorKindUnknown {
			return k
		}
	}

	// 3. Last-resort literal fallback: match Anthropic's canonical error-type
	//    tokens directly. We still require the full token — not partial
	//    words like "rate limit" — so we don't misclassify ordinary prose.
	low := strings.ToLower(payload)
	if strings.Contains(low, "rate_limit_error") {
		return ErrorKindRateLimit
	}
	if strings.Contains(low, "overloaded_error") {
		return ErrorKindOverloaded
	}
	return ErrorKindUnknown
}

// classifyJSON walks an Anthropic error JSON object and extracts error.type.
func classifyJSON(b []byte) ErrorKind {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return ErrorKindUnknown
	}
	// Shape A: {"error": {"type": "..."}}
	if inner, ok := raw["error"].(map[string]any); ok {
		if t, _ := inner["type"].(string); t != "" {
			return kindFromAnthropicType(t)
		}
	}
	// Shape B: {"type": "..."}
	if t, _ := raw["type"].(string); t != "" {
		return kindFromAnthropicType(t)
	}
	return ErrorKindUnknown
}

// kindFromAnthropicType maps Anthropic's error.type strings to ErrorKind.
// Unknown types return ErrorKindUnknown so callers don't retry auth or
// validation failures.
func kindFromAnthropicType(t string) ErrorKind {
	switch t {
	case "rate_limit_error":
		return ErrorKindRateLimit
	case "overloaded_error":
		return ErrorKindOverloaded
	}
	return ErrorKindUnknown
}

// IsRetriable reports whether an ErrorKind should trigger rate-limit retry.
func (k ErrorKind) IsRetriable() bool {
	return k == ErrorKindRateLimit || k == ErrorKindOverloaded
}
