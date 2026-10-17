package claudecode

import "testing"

// Lines recorded from claude 2.1.302 (--input-format stream-json, 2026-10-17),
// trimmed to the fields that matter: a background Agent plus a Monitor.
func TestBackgroundTasksChangedTracksRunningCount(t *testing.T) {
	cs := newLineTestSession(t)
	step := func(line string, want int) {
		t.Helper()
		cs.handleReadLoopLine(line)
		if got, _ := cs.BackgroundTasks(); got != want {
			t.Fatalf("after %s: running = %d, want %d", line, got, want)
		}
	}
	step(`{"type":"system","subtype":"init","session_id":"s1"}`, 0)
	step(`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"a5","task_type":"local_agent"}],"session_id":"s1"}`, 1)
	step(`{"type":"system","subtype":"task_started","task_id":"a5","is_backgrounded":true,"task_type":"local_agent","session_id":"s1"}`, 1)
	step(`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"a5","task_type":"local_agent"},{"task_id":"b4","task_type":"local_bash"}],"session_id":"s1"}`, 2)
	step(`{"type":"system","subtype":"task_notification","task_id":"b4","status":"completed","session_id":"s1"}`, 2)
	step(`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"a5","task_type":"local_agent"}],"session_id":"s1"}`, 1)
	if _, ended := cs.BackgroundTasks(); !ended.IsZero() {
		t.Fatalf("lastEndedAt set while a task still runs: %v", ended)
	}
	step(`{"type":"system","subtype":"task_notification","task_id":"a5","status":"completed","session_id":"s1"}`, 1)
	step(`{"type":"system","subtype":"background_tasks_changed","tasks":[],"session_id":"s1"}`, 0)
	if _, ended := cs.BackgroundTasks(); ended.IsZero() {
		t.Fatal("lastEndedAt not set when the last task ended")
	}
}
