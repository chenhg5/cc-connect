package config

import (
	"strconv"
	"strings"
	"time"
)

// StopWaitDuration normalises stop_wait_secs (int or decimal seconds) into a
// duration. ok is false when the option is absent or invalid.
func (q QueueConfig) StopWaitDuration() (time.Duration, bool) {
	return secondsDuration(q.StopWaitSecs)
}

func secondsDuration(raw any) (time.Duration, bool) {
	switch v := raw.(type) {
	case int64:
		if v < 0 {
			return 0, false
		}
		return time.Duration(v) * time.Second, true
	case int:
		if v < 0 {
			return 0, false
		}
		return time.Duration(v) * time.Second, true
	case float64:
		if v < 0 {
			return 0, false
		}
		return time.Duration(v * float64(time.Second)), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || f < 0 {
			return 0, false
		}
		return time.Duration(f * float64(time.Second)), true
	}
	return 0, false
}
