package config

import (
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestQueueConfigStopWaitDuration(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
		ok   bool
	}{
		{"stop_wait_secs = 2", 2 * time.Second, true},
		{"stop_wait_secs = 1.5", 1500 * time.Millisecond, true},
		{"stop_wait_secs = 0", 0, true},
		{"stop_wait_secs = \"0.8\"", 800 * time.Millisecond, true},
		{"stop_wait_secs = -1", 0, false},
	}
	for _, c := range cases {
		var q QueueConfig
		if _, err := toml.Decode(c.raw, &q); err != nil {
			t.Fatalf("decode %q: %v", c.raw, err)
		}
		got, ok := q.StopWaitDuration()
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%q -> (%v,%v), want (%v,%v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}
