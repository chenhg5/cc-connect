package claudecode

import "time"

// maomao fork: background task tracking.
//
// The CLI emits system/background_tasks_changed with the full list of
// background tasks (run_in_background Bash, Monitor, background Agent) every
// time that list changes, and an empty list once the last one has finished or
// been stopped. Recorded from claude 2.1.302 in stream-json input mode
// (2026-10-17): task_started -> background_tasks_changed[1] -> result ->
// task_notification(status=completed) -> background_tasks_changed[] -> init ->
// result. Foreground sub-steps (is_backgrounded=false) never appear in the list.
//
// The engine reads this through core.BackgroundTaskReporter so the idle close
// does not kill the process (and its tasks) while one is still running.

func (cs *claudeSession) handleBackgroundTasksChanged(raw map[string]any) {
	tasks, _ := raw["tasks"].([]any)
	n := int32(len(tasks))
	if prev := cs.bgTasks.Swap(n); n == 0 && prev > 0 {
		cs.bgTasksEndedAt.Store(time.Now().UnixNano())
	}
}

// BackgroundTasks implements core.BackgroundTaskReporter.
func (cs *claudeSession) BackgroundTasks() (running int, lastEndedAt time.Time) {
	running = int(cs.bgTasks.Load())
	if ns := cs.bgTasksEndedAt.Load(); ns > 0 {
		lastEndedAt = time.Unix(0, ns)
	}
	return running, lastEndedAt
}
