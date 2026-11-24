package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

const maxHTTPErrorMessageRunes = 120

// HTTPError reports an upstream failure without embedding its raw response.
// Callers supply credentials that the upstream may echo in its message field.
func HTTPError(op string, status int, body []byte, secrets ...string) error {
	var response struct {
		ErrMsg  string `json:"errmsg"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &response)
	message := response.ErrMsg
	if message == "" {
		message = response.Message
	}
	for _, secret := range secrets {
		message = RedactToken(message, secret)
	}
	// Refuse messages that look like echoed URLs or credential assignments.
	// Their unknown values cannot be reliably redacted by the caller.
	lower := strings.ToLower(message)
	if strings.Contains(message, "://") || strings.Contains(message, "=") ||
		strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
		strings.Contains(lower, "password") || strings.Contains(lower, "api key") {
		message = "[REDACTED]"
	}
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	message = strings.TrimSpace(message)
	runes := []rune(message)
	if len(runes) > maxHTTPErrorMessageRunes {
		message = string(runes[:maxHTTPErrorMessageRunes]) + "…"
	}
	if message == "" {
		return fmt.Errorf("%s: HTTP %d (body_bytes=%d)", op, status, len(body))
	}
	return fmt.Errorf("%s: HTTP %d (body_bytes=%d, errmsg=%q)", op, status, len(body), message)
}
