package core

import "time"

var (
	RetriableErrorInitialDelay = 30 * time.Second
	RetriableErrorRetryDelay   = 60 * time.Second
	RetriableErrorMaxAttempts  = 30
)

func RetriableErrorDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return RetriableErrorInitialDelay
	}
	return RetriableErrorRetryDelay
}
