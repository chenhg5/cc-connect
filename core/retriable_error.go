package core

import (
	"sync"
	"time"
)

const (
	DefaultRetriableErrorInitialDelay = 30 * time.Second
	DefaultRetriableErrorRetryDelay   = 60 * time.Second
	DefaultRetriableErrorMaxAttempts  = 30

	// MaxRetriableErrorDelay caps configured retry delays so time.Duration
	// arithmetic cannot overflow (24h is far below the ~292y int64 limit).
	MaxRetriableErrorDelay = 24 * time.Hour
	// MaxRetriableErrorAttempts caps the retry loop.
	MaxRetriableErrorAttempts = 1000
)

var (
	retriableErrorMu           sync.RWMutex
	RetriableErrorInitialDelay = DefaultRetriableErrorInitialDelay
	RetriableErrorRetryDelay   = DefaultRetriableErrorRetryDelay
	RetriableErrorMaxAttempts  = DefaultRetriableErrorMaxAttempts
)

func SetRetriableErrorPolicy(initialDelay, retryDelay time.Duration, maxAttempts int) {
	if initialDelay < 0 {
		initialDelay = DefaultRetriableErrorInitialDelay
	}
	if retryDelay < 0 {
		retryDelay = DefaultRetriableErrorRetryDelay
	}
	if maxAttempts < 1 {
		maxAttempts = DefaultRetriableErrorMaxAttempts
	}
	if initialDelay > MaxRetriableErrorDelay {
		initialDelay = MaxRetriableErrorDelay
	}
	if retryDelay > MaxRetriableErrorDelay {
		retryDelay = MaxRetriableErrorDelay
	}
	if maxAttempts > MaxRetriableErrorAttempts {
		maxAttempts = MaxRetriableErrorAttempts
	}

	retriableErrorMu.Lock()
	RetriableErrorInitialDelay = initialDelay
	RetriableErrorRetryDelay = retryDelay
	RetriableErrorMaxAttempts = maxAttempts
	retriableErrorMu.Unlock()
}

func RetriableErrorMaxAttemptsValue() int {
	retriableErrorMu.RLock()
	defer retriableErrorMu.RUnlock()
	return RetriableErrorMaxAttempts
}

func RetriableErrorDelay(attempt int) time.Duration {
	retriableErrorMu.RLock()
	initialDelay := RetriableErrorInitialDelay
	retryDelay := RetriableErrorRetryDelay
	retriableErrorMu.RUnlock()

	if attempt <= 1 {
		return initialDelay
	}
	return retryDelay
}
