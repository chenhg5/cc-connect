package core

import "testing"

func TestExecutionHistoryPreservesAttemptCardsAndIndexesWithoutDuplicates(t *testing.T) {
	task := TeamTask{ID: "T001", Title: "Build", Attempt: 1, RootMessageID: "instruction", ProcessMessageIDs: []string{"process-1", "process-2"}, ResultMessageID: "result-1"}
	nextTaskAttempt(&task, "Rework")
	if len(task.ProcessMessageIDs) != 0 || len(task.History[0].ProcessMessageIDs) != 2 {
		t.Fatal("attempt history lost execution cards")
	}
	task.ProcessMessageIDs = []string{"process-3"}
	task.RootMessageID = "rework"
	task.ResultMessageID = "result-2"
	p := TeamProject{Tasks: map[string]TeamTask{"T001": task}, ManagerContexts: map[string]TeamManagerContext{"ctx": {Instruction: "Build", SourceMessageID: "instruction", ProcessMessageIDs: []string{"manager-card"}, ResultMessageID: "final"}}}
	e := &Engine{i18n: NewI18n(LangEnglish)}
	rows := e.executionHistoryRows(p)
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.message] {
			t.Fatal("duplicate history link")
		}
		seen[r.message] = true
	}
	for _, id := range []string{"instruction", "manager-card", "final", "rework", "process-1", "process-2", "process-3", "result-1", "result-2"} {
		if !seen[id] {
			t.Fatalf("missing history entry %s", id)
		}
	}
}
