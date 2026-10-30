package core

import "strings"

// Trigger source vocabulary.
//
// A "trigger source" answers one question: what made the agent run this
// turn? It is attached to turn logs so an operator can tell a user-initiated
// turn apart from a turn the agent started on its own (e.g. a background
// task completion callback), and so a relay that "took too long" can be
// attributed to the right cause (issue #1803).
//
// The vocabulary is deliberately agent- and platform-agnostic: core owns the
// names, adapters report only the facts they can observe, and core falls back
// to what it knows for itself when an adapter reports nothing.
const (
	// TriggerSourceUnknown is used when neither the adapter nor core can
	// attribute the turn to a known cause. Core never guesses.
	TriggerSourceUnknown = "unknown"

	// TriggerSourceUserMessage: the turn was started by a user message.
	TriggerSourceUserMessage = "user_message"

	// TriggerSourceTimer: the turn was started by a cc-connect timer job.
	TriggerSourceTimer = "timer"

	// TriggerSourceCron: the turn was started by a cc-connect cron job.
	TriggerSourceCron = "cron"

	// TriggerSourceHeartbeat: the turn was started by a cc-connect heartbeat.
	TriggerSourceHeartbeat = "heartbeat"

	// TriggerSourceAgentInitiated: the agent ran a turn with no user prompt
	// and no cc-connect scheduled job behind it — e.g. a background/subagent
	// completion callback that the agent CLI handled on its own.
	TriggerSourceAgentInitiated = "agent_initiated"

	// TriggerSourceSubagent: the turn's output came from a subagent
	// sidechain. Reported by adapters that can observe sidechain output.
	TriggerSourceSubagent = "subagent"

	// TriggerSourceTaskNotification: the turn was triggered by a background
	// task completion notification.
	TriggerSourceTaskNotification = "task_notification"
)

// Reserved synthetic user ids. cc-connect builds a synthetic *Message when
// one of its own scheduled jobs starts a turn; the id tells the foreground
// path what triggered the turn.
const (
	syntheticUserIDTimer     = "timer"
	syntheticUserIDCron      = "cron"
	syntheticUserIDHeartbeat = "heartbeat"
)

// resolveTriggerSource picks the trigger source to report for an event.
//
// The adapter-reported value wins because it is the only source that can
// describe agent-internal causes (a subagent completion callback is invisible
// to core). When the adapter reports nothing, fallback is used — core's own
// knowledge of what started the turn.
func resolveTriggerSource(evt Event, fallback string) string {
	if s := strings.TrimSpace(evt.TriggerSource); s != "" {
		return s
	}
	if fallback == "" {
		return TriggerSourceUnknown
	}
	return fallback
}

// foregroundTriggerSource derives the trigger source of a foreground turn
// from the message that started it.
//
// Foreground turns are the ones cc-connect itself initiates, so core always
// knows the answer here: a scheduled job (timer/cron/heartbeat) or a user
// message. An adapter-reported value is deliberately ignored for foreground
// turns — a subagent that ran *inside* a user-initiated turn did not trigger
// that turn.
func foregroundTriggerSource(msg *Message) string {
	if msg == nil {
		return TriggerSourceUnknown
	}
	switch msg.UserID {
	case syntheticUserIDTimer:
		return TriggerSourceTimer
	case syntheticUserIDCron:
		return TriggerSourceCron
	case syntheticUserIDHeartbeat:
		return TriggerSourceHeartbeat
	}
	return TriggerSourceUserMessage
}
