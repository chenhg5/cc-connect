package core

import (
	"errors"
	"net/url"
	"strings"
)

// SafeError keeps useful diagnostics while removing URL paths, query strings,
// and known credentials. It is for logs and user-facing error summaries; it
// does not change the original error used for control flow.
func SafeError(err error, secrets ...string) string {
	if err == nil {
		return ""
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		safeURL := RedactURL(urlErr.URL)
		for _, secret := range secrets {
			safeURL = RedactToken(safeURL, secret)
		}
		parts := []string{HTTPErrorMessage(urlErr.Op, secrets...), safeURL}
		if urlErr.Err != nil {
			parts = append(parts, SafeError(urlErr.Err, secrets...))
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	}
	return HTTPErrorMessage(err.Error(), secrets...)
}
