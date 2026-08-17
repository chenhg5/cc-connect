package codex

import (
	"errors"
	"strings"

	"github.com/timmyagentic/cc-connect-next/core"
)

// classifyCodexError keeps provider-specific error recognition next to the
// Codex transport. The core engine only consumes the provider-neutral marker.
func classifyCodexError(err error) error {
	if err == nil || errors.Is(err, core.ErrUsageLimit) {
		return err
	}
	if isCodexUsageLimitMessage(err.Error()) {
		return core.WrapUsageLimit(err)
	}
	return err
}

func isCodexUsageLimitMessage(message string) bool {
	text := strings.ToLower(strings.TrimSpace(message))
	if text == "" {
		return false
	}

	for _, marker := range []string{
		"usage limit",
		"usage_limit",
		"you've hit your limit",
		"you have hit your limit",
		"you've reached your limit",
		"you have reached your limit",
		"quota exceeded",
		"quota exhausted",
		"rate limit exceeded",
		"rate limit reached",
		"out of credits",
		"credits exhausted",
		"credit limit",
		"insufficient credits",
		"remaining credits",
		"out of tokens",
		"tokens exhausted",
		"no tokens left",
		"token limit",
		"token quota",
		"token usage limit",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}

	// Cover messages such as "no available usage remaining" and "0 tokens
	// remaining" without treating an unrelated token/path mention as quota.
	hasRemaining := strings.Contains(text, "remaining")
	hasUsageTerm := strings.Contains(text, "token") ||
		strings.Contains(text, "credit") ||
		strings.Contains(text, "usage") ||
		strings.Contains(text, "quota")
	if hasRemaining && hasUsageTerm {
		return strings.Contains(text, "no ") || strings.Contains(text, "0 ")
	}
	return false
}
