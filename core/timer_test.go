package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseDelayOrTime_Relative(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"2h", false},
		{"30m", false},
		{"1h30m", false},
		{"2h30m15s", false},
		{"500ms", false},
		{"0s", true},       // zero = not positive
		{"-1h", true},      // negative
		{"", true},         // empty
		{"garbage", true},  // invalid
		{"2", true},        // bare number
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseDelayOrTime(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseDelayOrTime(%q) = %v, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDelayOrTime(%q): %v", tt.input, err)
			}
			// Should be in the future
			if got.Before(time.Now().Add(-time.Second)) {
				t.Errorf("ParseDelayOrTime(%q) = %v, want future time", tt.input, got)
			}
		})
	}
}

func TestParseDelayOrTime_Absolute(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"2026-05-15T14:00:00+08:00", false},
		{"2026-05-15T14:00:00Z", false},
		{"2026-05-15T14:00:00", false},
		{"2026-05-15T14:00", false},
		{"2026-05-15 14:00:00", false},
		{"2026-05-15 14:00", false},
		{"not-a-date", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseDelayOrTime(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseDelayOrTime(%q) = %v, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDelayOrTime(%q): %v", tt.input, err)
			}
			if got.IsZero() {
				t.Errorf("ParseDelayOrTime(%q) returned zero time", tt.input)
			}
		})
	}
}

func TestParseDelayOrTime_LocalTimezone(t *testing.T) {
	// When no timezone is specified, should use system local timezone
	got, err := ParseDelayOrTime("2026-05-15T14:00")
	if err != nil {
		t.Fatalf("ParseDelayOrTime: %v", err)
	}
	if got.Location() != time.Local {
		t.Errorf("expected local timezone %v, got %v", time.Local, got.Location())
	}
	if got.Hour() != 14 || got.Minute() != 0 {
		t.Errorf("expected 14:00 local, got %02d:%02d", got.Hour(), got.Minute())
	}

	// With explicit timezone, should use that timezone
	got2, err := ParseDelayOrTime("2026-05-15T14:00:00+08:00")
	if err != nil {
		t.Fatalf("ParseDelayOrTime: %v", err)
	}
	_, offset := got2.Zone()
	if offset != 8*3600 {
		t.Errorf("expected +08:00 offset (%d), got %d", 8*3600, offset)
	}
}

func TestTimerStore_CRUD(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTimerStore(dir)
	if err != nil {
		t.Fatalf("NewTimerStore: %v", err)
	}

	job := &TimerJob{
		ID:          "abcd1234",
		Project:     "test",
		SessionKey:  "feishu:chat1:user1",
		ScheduledAt: time.Now().Add(time.Hour),
		Prompt:      "test prompt",
		Description: "test",
		CreatedAt:   time.Now(),
	}

	// Add
	if err := store.Add(job); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Get
	got := store.Get("abcd1234")
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.Prompt != "test prompt" {
		t.Errorf("Get.Prompt = %q, want %q", got.Prompt, "test prompt")
	}

	// List
	all := store.List()
	if len(all) != 1 {
		t.Errorf("List returned %d jobs, want 1", len(all))
	}

	// ListPending
	pending := store.ListPending()
	if len(pending) != 1 {
		t.Errorf("ListPending returned %d jobs, want 1", len(pending))
	}

	// ListByProject
	byProject := store.ListByProject("test")
	if len(byProject) != 1 {
		t.Errorf("ListByProject returned %d jobs, want 1", len(byProject))
	}

	// ListBySessionKey
	bySession := store.ListBySessionKey("feishu:chat1:user1")
	if len(bySession) != 1 {
		t.Errorf("ListBySessionKey returned %d jobs, want 1", len(bySession))
	}

	// SetMute
	if !store.SetMute("abcd1234", true) {
		t.Error("SetMute returned false")
	}
	got = store.Get("abcd1234")
	if !got.Mute {
		t.Error("Mute not set")
	}

	// MarkFired
	store.MarkFired("abcd1234", nil)
	got = store.Get("abcd1234")
	if !got.Fired {
		t.Error("Fired not set")
	}
	if got.FiredAt.IsZero() {
		t.Error("FiredAt is zero")
	}

	// ListPending should be empty now
	pending = store.ListPending()
	if len(pending) != 0 {
		t.Errorf("ListPending after fire returned %d jobs, want 0", len(pending))
	}

	// Remove
	if !store.Remove("abcd1234") {
		t.Error("Remove returned false")
	}
	if store.Get("abcd1234") != nil {
		t.Error("Get after Remove returned non-nil")
	}
}

func TestTimerStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	store1, err := NewTimerStore(dir)
	if err != nil {
		t.Fatalf("NewTimerStore: %v", err)
	}

	job := &TimerJob{
		ID:          "persist1",
		Project:     "test",
		SessionKey:  "key1",
		ScheduledAt: time.Now().Add(time.Hour),
		Prompt:      "hello",
		CreatedAt:   time.Now(),
	}
	if err := store1.Add(job); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Reload from disk
	store2, err := NewTimerStore(dir)
	if err != nil {
		t.Fatalf("NewTimerStore reload: %v", err)
	}

	got := store2.Get("persist1")
	if got == nil {
		t.Fatal("Get after reload returned nil")
	}
	if got.Prompt != "hello" {
		t.Errorf("Prompt after reload = %q, want %q", got.Prompt, "hello")
	}
}

func TestTimerScheduler_FiresOnTime(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTimerStore(dir)
	if err != nil {
		t.Fatalf("NewTimerStore: %v", err)
	}

	sched := NewTimerScheduler(store)

	// Register a stub engine
	engine := &Engine{name: "test"}
	sched.RegisterEngine("test", engine)

	// We can't easily test ExecuteTimerJob without a full engine setup,
	// so we test the scheduling mechanics by verifying the timer is created.
	job := &TimerJob{
		ID:          "fire1",
		Project:     "test",
		SessionKey:  "key1",
		ScheduledAt: time.Now().Add(100 * time.Millisecond),
		Prompt:      "test",
		CreatedAt:   time.Now(),
	}

	if err := sched.AddJob(job); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	// Verify timer is registered
	sched.mu.RLock()
	_, has := sched.timers["fire1"]
	sched.mu.RUnlock()
	if !has {
		t.Error("timer not registered in sched.timers")
	}

	// Wait for fire (the job will fail because engine is bare, but that's OK)
	time.Sleep(300 * time.Millisecond)

	// Timer entry should be cleaned up
	sched.mu.RLock()
	_, has = sched.timers["fire1"]
	sched.mu.RUnlock()
	if has {
		t.Error("timer entry not cleaned up after fire")
	}

	// Job should be marked fired
	got := store.Get("fire1")
	if got == nil {
		t.Fatal("job not found after fire")
	}
	if !got.Fired {
		t.Error("job not marked as fired")
	}
}

func TestTimerScheduler_Cancellation(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTimerStore(dir)
	if err != nil {
		t.Fatalf("NewTimerStore: %v", err)
	}

	sched := NewTimerScheduler(store)
	sched.RegisterEngine("test", &Engine{name: "test"})

	job := &TimerJob{
		ID:          "cancel1",
		Project:     "test",
		SessionKey:  "key1",
		ScheduledAt: time.Now().Add(time.Hour),
		Prompt:      "test",
		CreatedAt:   time.Now(),
	}

	if err := sched.AddJob(job); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	// Remove before fire
	if !sched.RemoveJob("cancel1") {
		t.Error("RemoveJob returned false")
	}

	// Timer should be stopped and removed
	sched.mu.RLock()
	_, has := sched.timers["cancel1"]
	sched.mu.RUnlock()
	if has {
		t.Error("timer entry not removed after cancellation")
	}

	// Job should be gone from store
	if store.Get("cancel1") != nil {
		t.Error("job still in store after removal")
	}
}

func TestTimerScheduler_Recovery(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTimerStore(dir)
	if err != nil {
		t.Fatalf("NewTimerStore: %v", err)
	}

	// Add a job that's due very soon
	job := &TimerJob{
		ID:          "recover1",
		Project:     "test",
		SessionKey:  "key1",
		ScheduledAt: time.Now().Add(50 * time.Millisecond),
		Prompt:      "recover test",
		CreatedAt:   time.Now(),
	}
	if err := store.Add(job); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Simulate restart: create new scheduler from same store
	sched := NewTimerScheduler(store)
	sched.RegisterEngine("test", &Engine{name: "test"})

	if err := sched.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sched.Stop()

	// The job should fire (within grace period)
	time.Sleep(500 * time.Millisecond)

	got := store.Get("recover1")
	if got == nil {
		t.Fatal("job not found after recovery")
	}
	if !got.Fired {
		t.Error("recovered job not fired")
	}
}

func TestTimerScheduler_StaleJobSkipped(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTimerStore(dir)
	if err != nil {
		t.Fatalf("NewTimerStore: %v", err)
	}

	// Add a job that's way past due (>5 min)
	job := &TimerJob{
		ID:          "stale1",
		Project:     "test",
		SessionKey:  "key1",
		ScheduledAt: time.Now().Add(-10 * time.Minute),
		Prompt:      "stale test",
		CreatedAt:   time.Now().Add(-15 * time.Minute),
	}
	if err := store.Add(job); err != nil {
		t.Fatalf("Add: %v", err)
	}

	sched := NewTimerScheduler(store)
	sched.RegisterEngine("test", &Engine{name: "test"})

	if err := sched.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sched.Stop()

	// Give scheduler a moment to process
	time.Sleep(100 * time.Millisecond)

	got := store.Get("stale1")
	if got == nil {
		t.Fatal("job not found")
	}
	if !got.Fired {
		t.Error("stale job not marked as fired")
	}
	if got.LastError == "" {
		t.Error("stale job should have a LastError")
	}
}

func TestValidateTimerJob(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		job     *TimerJob
		wantErr bool
	}{
		{
			name:    "valid prompt job",
			job:     &TimerJob{SessionKey: "feishu:chat1:user1", ScheduledAt: now.Add(time.Hour), Prompt: "test"},
			wantErr: false,
		},
		{
			name:    "valid exec job",
			job:     &TimerJob{SessionKey: "feishu:chat1:user1", ScheduledAt: now.Add(time.Hour), Exec: "echo hello"},
			wantErr: false,
		},
		{
			name:    "empty session_key",
			job:     &TimerJob{ScheduledAt: now.Add(time.Hour), Prompt: "test"},
			wantErr: true,
		},
		{
			name:    "whitespace session_key",
			job:     &TimerJob{SessionKey: "   ", ScheduledAt: now.Add(time.Hour), Prompt: "test"},
			wantErr: true,
		},
		{
			name:    "missing scheduled_at",
			job:     &TimerJob{SessionKey: "feishu:chat1:user1", Prompt: "test"},
			wantErr: true,
		},
		{
			name:    "missing prompt and exec",
			job:     &TimerJob{SessionKey: "feishu:chat1:user1", ScheduledAt: now.Add(time.Hour)},
			wantErr: true,
		},
		{
			name:    "both prompt and exec",
			job:     &TimerJob{SessionKey: "feishu:chat1:user1", ScheduledAt: now.Add(time.Hour), Prompt: "test", Exec: "echo"},
			wantErr: true,
		},
		{
			name:    "invalid session_mode",
			job:     &TimerJob{SessionKey: "feishu:chat1:user1", ScheduledAt: now.Add(time.Hour), Prompt: "test", SessionMode: "bad"},
			wantErr: true,
		},
		{
			name:    "valid session_mode",
			job:     &TimerJob{SessionKey: "feishu:chat1:user1", ScheduledAt: now.Add(time.Hour), Prompt: "test", SessionMode: "new-per-run"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTimerJob(tt.job)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestGenerateTimerID(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := GenerateTimerID()
		if len(id) != 8 {
			t.Errorf("GenerateTimerID() = %q (len %d), want len 8", id, len(id))
		}
		if ids[id] {
			t.Errorf("duplicate ID: %s", id)
		}
		ids[id] = true
	}
}

func TestFormatTimerRemaining(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"seconds", 30 * time.Second, "30s"},
		{"minutes", 5 * time.Minute, "5m"},
		{"hours", 2 * time.Hour, "2h"},
		{"hours and minutes", 90 * time.Minute, "1h30m"},
		{"overdue", -time.Minute, "overdue"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatTimerRemaining(time.Now().Add(tt.d))
			if got != tt.want {
				t.Errorf("FormatTimerRemaining() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTimerStore_FilePath(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTimerStore(dir)
	if err != nil {
		t.Fatalf("NewTimerStore: %v", err)
	}
	expected := filepath.Join(dir, "timers", "jobs.json")
	if store.path != expected {
		t.Errorf("path = %q, want %q", store.path, expected)
	}
	if _, err := os.Stat(filepath.Dir(store.path)); err != nil {
		t.Errorf("timers directory not created: %v", err)
	}
}
