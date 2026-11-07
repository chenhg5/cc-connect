package core

import "time"

// timingTestBudgetFactor widens wall-clock deadlines in tests that wait for a
// timer-driven code path to become observable.
//
// The code under test is bounded by its own timers; these tests are bounded by
// the wall clock. On shared CI runners the goroutine that should wake on a 30s
// timer can be scheduled seconds late while other packages saturate the host,
// so a correct scheduler looks like a hang. Scaling the observation window is a
// test concern only: production behavior is unchanged. The factor stays finite
// so a genuinely stuck scheduler still fails instead of hanging the suite.
const timingTestBudgetFactor = 4

func timingTestBudget(base time.Duration) time.Duration {
	return base * timingTestBudgetFactor
}
