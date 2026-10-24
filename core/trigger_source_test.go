package core

import "testing"

// TestResolveTriggerSource covers the precedence rules for a turn's trigger
// source: the adapter's observation wins (only the adapter can see
// agent-internal causes), and core's own fallback is used when the adapter
// reported nothing.
func TestResolveTriggerSource(t *testing.T) {
	cases := []struct {
		name     string
		evt      Event
		fallback string
		want     string
	}{
		{
			name:     "adapter value wins over fallback",
			evt:      Event{TriggerSource: TriggerSourceSubagent},
			fallback: TriggerSourceAgentInitiated,
			want:     TriggerSourceSubagent,
		},
		{
			name:     "task notification wins over fallback",
			evt:      Event{TriggerSource: TriggerSourceTaskNotification},
			fallback: TriggerSourceAgentInitiated,
			want:     TriggerSourceTaskNotification,
		},
		{
			name:     "fallback used when adapter reported nothing",
			evt:      Event{},
			fallback: TriggerSourceAgentInitiated,
			want:     TriggerSourceAgentInitiated,
		},
		{
			name:     "blank adapter value falls back",
			evt:      Event{TriggerSource: "   "},
			fallback: TriggerSourceAgentInitiated,
			want:     TriggerSourceAgentInitiated,
		},
		{
			name: "unknown when nothing is known",
			evt:  Event{},
			want: TriggerSourceUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveTriggerSource(tc.evt, tc.fallback); got != tc.want {
				t.Fatalf("resolveTriggerSource() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestForegroundTriggerSource covers how core attributes a foreground turn.
// Core always knows the answer: cc-connect itself started the turn, either
// from a scheduled job or from a user message.
func TestForegroundTriggerSource(t *testing.T) {
	cases := []struct {
		name string
		msg  *Message
		want string
	}{
		{"user message", &Message{UserID: "u-42"}, TriggerSourceUserMessage},
		{"timer job", &Message{UserID: syntheticUserIDTimer}, TriggerSourceTimer},
		{"cron job", &Message{UserID: syntheticUserIDCron}, TriggerSourceCron},
		{"heartbeat", &Message{UserID: syntheticUserIDHeartbeat}, TriggerSourceHeartbeat},
		{"nil message", nil, TriggerSourceUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := foregroundTriggerSource(tc.msg); got != tc.want {
				t.Fatalf("foregroundTriggerSource() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSyntheticUserIDsMatchTriggerSources guards the coupling between the
// reserved synthetic user ids and the trigger source vocabulary: the ids are
// what core uses to recognise a turn it started itself.
func TestSyntheticUserIDsMatchTriggerSources(t *testing.T) {
	if syntheticUserIDTimer != TriggerSourceTimer {
		t.Fatalf("timer user id %q != trigger source %q", syntheticUserIDTimer, TriggerSourceTimer)
	}
	if syntheticUserIDCron != TriggerSourceCron {
		t.Fatalf("cron user id %q != trigger source %q", syntheticUserIDCron, TriggerSourceCron)
	}
	if syntheticUserIDHeartbeat != TriggerSourceHeartbeat {
		t.Fatalf("heartbeat user id %q != trigger source %q", syntheticUserIDHeartbeat, TriggerSourceHeartbeat)
	}
}
