package core

import (
	"errors"
	"fmt"
)

// ErrUsageLimit marks an agent error caused by an exhausted provider usage
// allowance. The underlying error remains available for logs, while callers
// can render a safe, user-facing quota message without exposing it.
var ErrUsageLimit = errors.New("agent usage limit reached")

// WrapUsageLimit preserves the provider error for diagnostics and marks it as
// a usage-limit failure for user-facing rendering.
func WrapUsageLimit(err error) error {
	if err == nil {
		return ErrUsageLimit
	}
	if errors.Is(err, ErrUsageLimit) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrUsageLimit, err)
}
