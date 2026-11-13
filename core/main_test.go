package core

import (
	"os"
	"testing"
	"time"
)

// TestMain shortens the stop-unwind wait so unit tests do not block for
// seconds; production keeps the 2s window.
func TestMain(m *testing.M) {
	interruptUnwindWaitTimeout = 0
	suppressForceStopAfterInterrupt = true
	outboxInline = true
	os.Exit(m.Run())
}

var _ = time.Second
