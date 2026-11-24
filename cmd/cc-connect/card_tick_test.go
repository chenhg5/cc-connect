package main

import (
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/config"
)

func TestCardTickIntervalClampsPositiveValues(t *testing.T) {
	intPtr := func(v int) *int { return &v }
	tests := []struct {
		name string
		ms   *int
		want time.Duration
	}{
		{"default", nil, 5 * time.Second},
		{"disabled", intPtr(0), 0},
		{"negative", intPtr(-1), 0},
		{"below_min", intPtr(1), time.Second},
		{"at_min", intPtr(1000), time.Second},
		{"normal", intPtr(5000), 5 * time.Second},
		{"at_max", intPtr(60000), time.Minute},
		{"above_max", intPtr(120000), time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Display.CardTickMs = tt.ms
			if got := cardTickInterval(cfg); got != tt.want {
				t.Fatalf("cardTickInterval = %v, want %v", got, tt.want)
			}
		})
	}
}
