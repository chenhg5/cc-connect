package core

import "time"

type EventTimeline struct {
	AssistantHistoryIdx int             `json:"assistant_history_idx,omitempty"`
	StartedAt           time.Time       `json:"started_at"`
	CompletedAt         time.Time       `json:"completed_at"`
	Events              []TimelineEvent `json:"events,omitempty"`
}

type TimelineEvent struct {
	Index      int       `json:"index"`
	Kind       EventType `json:"kind"`
	Text       string    `json:"text,omitempty"`
	ToolName   string    `json:"tool_name,omitempty"`
	ToolInput  string    `json:"tool_input,omitempty"`
	ToolResult string    `json:"tool_result,omitempty"`
	Status     string    `json:"status,omitempty"`
	ExitCode   *int      `json:"exit_code,omitempty"`
	Success    *bool     `json:"success,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

func newEventTimeline(startedAt time.Time) *EventTimeline {
	return &EventTimeline{
		StartedAt: startedAt,
		Events:    make([]TimelineEvent, 0, 8),
	}
}

func (t *EventTimeline) appendEvent(ev TimelineEvent) {
	ev.Index = len(t.Events) + 1
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now()
	}
	t.Events = append(t.Events, ev)
}
