package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseFeatureStartArgs(t *testing.T) {
	opts, err := parseFeatureStartArgs([]string{"build", "tts", "batch", "--impl", "--risk", "--review"})
	if err != nil {
		t.Fatalf("parseFeatureStartArgs: %v", err)
	}
	if opts.Title != "build tts batch" {
		t.Fatalf("Title = %q, want build tts batch", opts.Title)
	}
	if !opts.Impl || !opts.Risk || !opts.Review {
		t.Fatalf("flags = impl:%v risk:%v review:%v, want all true", opts.Impl, opts.Risk, opts.Review)
	}
}

func TestParseFeatureStartArgsRejectsUnknownFlag(t *testing.T) {
	if _, err := parseFeatureStartArgs([]string{"x", "--auto"}); err == nil {
		t.Fatal("expected unknown flag error")
	}
}

func TestFeatureBoardStoreCreate(t *testing.T) {
	dir := t.TempDir()
	store := NewFeatureBoardStore(filepath.Join(dir, "data"))
	store.now = func() time.Time {
		return time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	}

	task, err := store.Create(
		"TTS Batch spike",
		featureChefSeat,
		`F:\GitHub\resonova`,
		"Chef scope feature",
		"telegram:chat:user",
		[]string{featureChefSeat, featureImplSeat, featureCounselSeat},
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if task.TaskID != "feat-20260629-120000-tts-batch-spike" {
		t.Fatalf("TaskID = %q", task.TaskID)
	}
	if task.Owner != featureChefSeat || task.Status != "planning" || task.HandbackState != "not_started" {
		t.Fatalf("unexpected task defaults: %+v", task)
	}

	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var board FeatureBoard
	if err := json.Unmarshal(data, &board); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(board.Tasks) != 1 || board.Tasks[0].TaskID != task.TaskID {
		t.Fatalf("stored board = %+v", board.Tasks)
	}
	if board.ActiveFeature == nil || board.ActiveFeature.TaskID != task.TaskID {
		t.Fatalf("active feature = %+v, want task %s", board.ActiveFeature, task.TaskID)
	}
	if got := board.ActiveFeature.Seats[featureImplSeat].Status; got != "pending" {
		t.Fatalf("dev seat status = %q, want pending", got)
	}
}

func TestFeatureBoardStoreSeatRefreshLifecycle(t *testing.T) {
	dir := t.TempDir()
	store := NewFeatureBoardStore(filepath.Join(dir, "data"))
	store.now = func() time.Time {
		return time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	}
	task, err := store.Create("Lazy context", featureChefSeat, `F:\GitHub\resonova`, "Scope", "telegram:chat:user", []string{featureImplSeat})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	active, shouldRefresh, err := store.ActiveTaskForSeat(featureImplSeat)
	if err != nil {
		t.Fatalf("ActiveTaskForSeat: %v", err)
	}
	if !shouldRefresh || active.TaskID != task.TaskID {
		t.Fatalf("ActiveTaskForSeat = (%+v, %v), want task refresh", active, shouldRefresh)
	}
	if err := store.MarkSeatRefreshed(task.TaskID, featureImplSeat, "relay-session"); err != nil {
		t.Fatalf("MarkSeatRefreshed: %v", err)
	}
	if _, shouldRefresh, err := store.ActiveTaskForSeat(featureImplSeat); err != nil || shouldRefresh {
		t.Fatalf("after refreshed shouldRefresh=%v err=%v, want false nil", shouldRefresh, err)
	}
}
