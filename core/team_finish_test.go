package core

import (
	"errors"
	"reflect"
	"testing"
)

func TestProjectFinishPreservesHistoryAndStopsCurrentRound(t *testing.T) {
	s := NewTeamProjectStore(t.TempDir() + "/projects.json")
	p := TeamProject{ID: "p", State: "ready", Creator: "owner", Manager: "manager", Bots: []string{"manager", "dev"}, Directories: []string{t.TempDir()}, Tasks: map[string]TeamTask{
		"T001": {ID: "T001", Assignee: "dev", Status: "accepted", Result: "artifact", Evidence: "checked", Attempt: 1},
		"T002": {ID: "T002", Assignee: "dev", Status: "waiting_approval", Attempt: 1, Followups: map[string]string{"extra": "extra input"}},
		"T003": {ID: "T003", Assignee: "dev", Status: "planned", Attempt: 1},
		"T004": {ID: "T004", Assignee: "dev", Status: "submitted", Result: "unreviewed artifact", Attempt: 1},
	}, Reports: map[string]TeamMemberReport{"dev": {Bot: "dev", Status: "waiting_approval"}}}
	if err := s.Put(p); err != nil {
		t.Fatal(err)
	}
	other := p
	other.ID = "other"
	if err := s.Put(other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.finishProject("p", "owner", "old"); !errors.Is(err, errProjectFinishChanged) {
		t.Fatal("stale confirmation accepted", err)
	}
	ended, err := s.finishProject("p", "owner", projectFinishFingerprint(p))
	if err != nil {
		t.Fatal(err)
	}
	if ended.State != "ready" || ended.RoundState != "ending" || ended.RoundEndedBy != "owner" || ended.RoundEndedAt != "" || ended.Paused || ended.WorkflowEpoch != 1 {
		t.Fatalf("bad end state: %+v", ended)
	}
	if ended.Tasks["T002"].Status != "stopped" || len(ended.Tasks["T002"].Followups) != 0 || ended.Tasks["T003"].Status != "cancelled" {
		t.Fatal("active/planned work survived")
	}
	if !reflect.DeepEqual(ended.Tasks["T001"], p.Tasks["T001"]) || !reflect.DeepEqual(ended.Tasks["T004"], p.Tasks["T004"]) {
		t.Fatal("ending changed delivery/acceptance history")
	}
	if err := s.setWorkflowPaused("p", false); !errors.Is(err, errProjectEnded) {
		t.Fatal("ended project resumed", err)
	}
	if err := s.Put(p); !errors.Is(err, errProjectEnded) {
		t.Fatal("stale creation snapshot reopened project", err)
	}
	if err := s.acceptTask("p", "T004", "late acceptance"); !errors.Is(err, errProjectEnded) {
		t.Fatal("ended result changed", err)
	}
	if _, err := s.assignTask("p", TeamActionRequest{To: "dev", Message: "new task"}); err == nil {
		t.Fatal("ended project dispatched")
	}
	if _, err := s.planTask("p", TeamActionRequest{To: "dev", Title: "new", Message: "new", Deliverable: "new"}); err == nil {
		t.Fatal("ended project planned")
	}
	if err := s.saveReport("p", TeamMemberReport{Bot: "dev", ID: "late", Epoch: ended.WorkflowEpoch, Status: "completed", Content: "late result"}); err != nil {
		t.Fatal(err)
	}
	ended, err = s.completeProjectRound("p", 1)
	if err != nil || ended.RoundState != "idle" || len(ended.Rounds) != 1 || ended.RoundEndedAt == "" {
		t.Fatal("end snapshot missing", err)
	}
	again, err := s.finishProject("p", "owner", projectFinishFingerprint(p))
	if err != nil || again.EndedAt != ended.EndedAt || again.WorkflowEpoch != ended.WorkflowEpoch {
		t.Fatal("replayed finish changed ending", err)
	}
	all, err := NewTeamProjectStore(s.path).All()
	if err != nil || all["p"].Reports["dev"].Status != "stopped" || all["p"].RoundState != "idle" || all["other"].State != "ready" {
		t.Fatal("restart/late result/other project isolation", err)
	}
}

func TestProjectFinishConfirmationTracksWorkChanges(t *testing.T) {
	s := NewTeamProjectStore(t.TempDir() + "/p.json")
	p := TeamProject{ID: "p", State: "ready", Directories: []string{t.TempDir()}, Tasks: map[string]TeamTask{"T001": {ID: "T001", Status: "running"}}}
	if err := s.Put(p); err != nil {
		t.Fatal(err)
	}
	preview := projectFinishFingerprint(p)
	if err := s.updateTask("p", "T001", func(t *TeamTask) error { t.Status = "submitted"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.finishProject("p", "owner", preview); !errors.Is(err, errProjectFinishChanged) {
		t.Fatal("changed task did not require a new preview", err)
	}
}
