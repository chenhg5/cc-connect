package core

import (
	"encoding/json"
	"testing"
)

// TestUpdateJobField_Model guards the `cron edit <id> model <value>` path:
// updateJobField must accept a string value for "model" and reject non-strings,
// mirroring the other string fields.
func TestUpdateJobField_Model(t *testing.T) {
	job := &CronJob{ID: "j1"}

	if err := updateJobField(job, "model", "haiku"); err != nil {
		t.Fatalf("updateJobField(model, haiku): %v", err)
	}
	if job.Model != "haiku" {
		t.Errorf("job.Model = %q, want haiku", job.Model)
	}

	if err := updateJobField(job, "model", ""); err != nil {
		t.Fatalf("updateJobField(model, empty): %v", err)
	}
	if job.Model != "" {
		t.Errorf("job.Model = %q, want empty (override cleared)", job.Model)
	}

	if err := updateJobField(job, "model", 42); err == nil {
		t.Error("updateJobField(model, 42) succeeded, want type error")
	}
}

// TestCronJobModel_JSONRoundTrip ensures the model field persists through the
// store's JSON encoding and is omitted when empty (no store-format churn for
// existing jobs).
func TestCronJobModel_JSONRoundTrip(t *testing.T) {
	job := &CronJob{ID: "j1", Model: "sonnet"}
	b, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back CronJob
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Model != "sonnet" {
		t.Errorf("round-trip Model = %q, want sonnet", back.Model)
	}

	empty, err := json.Marshal(&CronJob{ID: "j2"})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if string(empty) != "" && jsonHasKey(empty, "model") {
		t.Errorf("empty Model serialized into JSON: %s", empty)
	}
}

func jsonHasKey(b []byte, key string) bool {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
