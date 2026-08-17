package codex

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/timmyagentic/cc-connect-next/core"
)

func TestIsCodexUsageLimitMessage(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{name: "usage limit", message: "You've reached your usage limit. Please try again later.", want: true},
		{name: "remaining credits", message: "There are no remaining credits for this account.", want: true},
		{name: "zero tokens", message: "0 tokens remaining for this request", want: true},
		{name: "rate limit reached", message: "Codex rate limit reached", want: true},
		{name: "authentication token", message: "authentication token is invalid", want: false},
		{name: "unrelated path", message: "failed to read token file at /tmp/auth.json", want: false},
		{name: "network timeout", message: "request timed out", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCodexUsageLimitMessage(tt.message); got != tt.want {
				t.Fatalf("isCodexUsageLimitMessage(%q) = %v, want %v", tt.message, got, tt.want)
			}
		})
	}
}

func TestClassifyCodexErrorMarksUsageLimit(t *testing.T) {
	err := classifyCodexError(errors.New("You've hit your usage limit"))
	if !errors.Is(err, core.ErrUsageLimit) {
		t.Fatalf("classified error = %v, want errors.Is(..., core.ErrUsageLimit)", err)
	}
	if !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("classified error = %q, want diagnostic message preserved", err)
	}
}

func TestCodexSession_TurnFailedUsageLimitIsClassified(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &codexSession{
		ctx:    ctx,
		events: make(chan core.Event, 1),
	}
	cs.handleEvent(map[string]any{
		"type": "turn.failed",
		"error": map[string]any{
			"message": "You've reached your usage limit. Please try again later.",
		},
	})

	event := <-cs.events
	if !errors.Is(event.Error, core.ErrUsageLimit) {
		t.Fatalf("turn.failed error = %v, want usage-limit marker", event.Error)
	}
}
