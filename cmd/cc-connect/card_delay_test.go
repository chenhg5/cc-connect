package main

import (
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func TestCardStartDelayClamps(t *testing.T) {
	intPtr := func(v int) *int { return &v }
	tests := []struct {
		name string
		ms   *int
		want time.Duration
	}{
		{"default", nil, core.DefaultCardStartDelay},
		{"immediate", intPtr(0), 0},
		{"negative", intPtr(-1), 0},
		{"below_min", intPtr(1), 200 * time.Millisecond},
		{"at_min", intPtr(200), 200 * time.Millisecond},
		{"normal", intPtr(2000), 2 * time.Second},
		{"explicit_3s", intPtr(3000), 3 * time.Second},
		{"at_max", intPtr(10000), 10 * time.Second},
		{"above_max", intPtr(120000), 10 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Display.CardDelayMs = tt.ms
			if got := cardStartDelay(cfg); got != tt.want {
				t.Fatalf("cardStartDelay = %v, want %v", got, tt.want)
			}
		})
	}
}
