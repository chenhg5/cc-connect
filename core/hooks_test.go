package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func boolPtr(v bool) *bool { return &v }

func TestNewHookManager_ValidatesConfig(t *testing.T) {
	hooks := []HookConfig{
		{Event: "message.received", Type: "command", Command: "echo ok"},
		{Event: "", Type: "command", Command: "echo bad"},         // missing event
		{Event: "error", Type: "http", URL: ""},                   // missing url
		{Event: "error", Type: "http", URL: "ftp://bad"},          // bad url scheme
		{Event: "error", Type: "unknown", Command: "echo"},        // bad type
		{Event: "error", Type: "command", Command: ""},            // missing command
		{Event: "message.sent", Type: "http", URL: "http://ok.com"},
	}
	hm := NewHookManager("test", hooks)
	got := hm.Hooks()
	if len(got) != 2 {
		t.Fatalf("expected 2 valid hooks, got %d", len(got))
	}
	if got[0].Event != "message.received" {
		t.Errorf("expected first hook event=message.received, got %s", got[0].Event)
	}
	if got[1].Event != "message.sent" {
		t.Errorf("expected second hook event=message.sent, got %s", got[1].Event)
	}
}

func TestHookConfig_IsAsync(t *testing.T) {
	tests := []struct {
		name  string
		async *bool
		want  bool
	}{
		{"nil defaults to true", nil, true},
		{"explicit true", boolPtr(true), true},
		{"explicit false", boolPtr(false), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := HookConfig{Async: tt.async}
			if h.isAsync() != tt.want {
				t.Errorf("isAsync() = %v, want %v", h.isAsync(), tt.want)
			}
		})
	}
}

func TestHookConfig_TimeoutDuration(t *testing.T) {
	tests := []struct {
		name    string
		typ     string
		timeout int
		want    time.Duration
	}{
		{"command default", "command", 0, 10 * time.Second},
		{"http default", "http", 0, 5 * time.Second},
		{"custom", "command", 30, 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := HookConfig{Type: tt.typ, Timeout: tt.timeout}
			if h.timeoutDuration() != tt.want {
				t.Errorf("timeoutDuration() = %v, want %v", h.timeoutDuration(), tt.want)
			}
		})
	}
}

func TestMatchEvent(t *testing.T) {
	tests := []struct {
		pattern, event string
		want           bool
	}{
		{"*", "message.received", true},
		{"*", "error", true},
		{"message.received", "message.received", true},
		{"MESSAGE.RECEIVED", "message.received", true},
		{"message.sent", "message.received", false},
		{"error", "session.started", false},
	}
	for _, tt := range tests {
		if matchEvent(tt.pattern, tt.event) != tt.want {
			t.Errorf("matchEvent(%q, %q) = %v, want %v", tt.pattern, tt.event, !tt.want, tt.want)
		}
	}
}

func TestEmit_NilManager(t *testing.T) {
	var hm *HookManager
	// Should not panic
	hm.Emit(HookEvent{Event: HookEventError, Error: "boom"})
}

func TestEmit_CommandHook(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "hook_fired")

	hooks := []HookConfig{
		{
			Event:   "message.received",
			Type:    "command",
			Command: "touch " + marker,
			Async:   boolPtr(false),
		},
	}
	hm := NewHookManager("test-project", hooks)

	hm.Emit(HookEvent{
		Event:      HookEventMessageReceived,
		SessionKey: "tg:1:1",
		Platform:   "telegram",
		UserName:   "alice",
		Content:    "hello",
	})

	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Fatal("expected marker file to be created by command hook")
	}
}

func TestEmit_CommandHookEnvVars(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "env_out")

	hooks := []HookConfig{
		{
			Event:   "message.received",
			Type:    "command",
			Command: "env > " + outFile,
			Async:   boolPtr(false),
		},
	}
	hm := NewHookManager("my-proj", hooks)

	hm.Emit(HookEvent{
		Event:      HookEventMessageReceived,
		SessionKey: "slack:C1:U1",
		Platform:   "slack",
		UserID:     "U1",
		UserName:   "bob",
		Content:    "test msg",
	})

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("failed to read env output: %v", err)
	}
	envStr := string(data)

	expected := map[string]string{
		"CC_HOOK_EVENT":       "message.received",
		"CC_HOOK_PROJECT":     "my-proj",
		"CC_HOOK_SESSION_KEY": "slack:C1:U1",
		"CC_HOOK_PLATFORM":    "slack",
		"CC_HOOK_USER_ID":     "U1",
		"CC_HOOK_USER_NAME":   "bob",
		"CC_HOOK_CONTENT":     "test msg",
	}
	for k, v := range expected {
		line := k + "=" + v
		if !strings.Contains(envStr, line) {
			t.Errorf("expected env to contain %q", line)
		}
	}
}

func TestEmit_HTTPHook(t *testing.T) {
	var received atomic.Int32
	var mu sync.Mutex
	var lastBody HookEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}
		if ev := r.Header.Get("X-Hook-Event"); ev != "error" {
			t.Errorf("expected X-Hook-Event=error, got %s", ev)
		}

		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(body, &lastBody)
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hooks := []HookConfig{
		{
			Event: "error",
			Type:  "http",
			URL:   srv.URL,
			Async: boolPtr(false),
		},
	}
	hm := NewHookManager("proj-1", hooks)

	hm.Emit(HookEvent{
		Event:      HookEventError,
		SessionKey: "tg:1:1",
		Platform:   "telegram",
		Error:      "something failed",
	})

	if received.Load() != 1 {
		t.Fatalf("expected 1 HTTP request, got %d", received.Load())
	}

	mu.Lock()
	defer mu.Unlock()
	if lastBody.Event != HookEventError {
		t.Errorf("expected event=error, got %s", lastBody.Event)
	}
	if lastBody.Project != "proj-1" {
		t.Errorf("expected project=proj-1, got %s", lastBody.Project)
	}
	if lastBody.Error != "something failed" {
		t.Errorf("expected error=something failed, got %s", lastBody.Error)
	}
}

func TestEmit_WildcardMatchesAll(t *testing.T) {
	var count atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hooks := []HookConfig{
		{Event: "*", Type: "http", URL: srv.URL, Async: boolPtr(false)},
	}
	hm := NewHookManager("proj", hooks)

	hm.Emit(HookEvent{Event: HookEventMessageReceived})
	hm.Emit(HookEvent{Event: HookEventSessionStarted})
	hm.Emit(HookEvent{Event: HookEventError})

	if count.Load() != 3 {
		t.Fatalf("expected 3 requests for wildcard, got %d", count.Load())
	}
}

func TestEmit_OnlyMatchingHooksFire(t *testing.T) {
	dir := t.TempDir()
	matchedFile := filepath.Join(dir, "matched")
	unmatchedFile := filepath.Join(dir, "unmatched")

	hooks := []HookConfig{
		{Event: "session.ended", Type: "command", Command: "touch " + matchedFile, Async: boolPtr(false)},
		{Event: "message.received", Type: "command", Command: "touch " + unmatchedFile, Async: boolPtr(false)},
	}
	hm := NewHookManager("proj", hooks)

	hm.Emit(HookEvent{Event: HookEventSessionEnded})

	if _, err := os.Stat(matchedFile); os.IsNotExist(err) {
		t.Error("expected matched hook to fire")
	}
	if _, err := os.Stat(unmatchedFile); !os.IsNotExist(err) {
		t.Error("expected unmatched hook NOT to fire")
	}
}

func TestEmit_AsyncDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "async_done")

	hooks := []HookConfig{
		{
			Event:   "message.received",
			Type:    "command",
			Command: "sleep 0.1 && touch " + marker,
			Async:   boolPtr(true),
		},
	}
	hm := NewHookManager("proj", hooks)

	start := time.Now()
	hm.Emit(HookEvent{Event: HookEventMessageReceived})
	elapsed := time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Errorf("async emit took %v, expected near-instant return", elapsed)
	}

	// Wait for the async command to finish
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Error("expected async command to eventually create marker file")
	}
}

func TestEmit_SyncBlocks(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "sync_done")

	hooks := []HookConfig{
		{
			Event:   "message.received",
			Type:    "command",
			Command: "touch " + marker,
			Async:   boolPtr(false),
		},
	}
	hm := NewHookManager("proj", hooks)
	hm.Emit(HookEvent{Event: HookEventMessageReceived})

	// File should exist immediately after synchronous emit
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Error("expected sync command to create marker file before Emit returns")
	}
}

func TestEmit_HTTPError_DoesNotPanic(t *testing.T) {
	hooks := []HookConfig{
		{Event: "error", Type: "http", URL: "http://127.0.0.1:1", Async: boolPtr(false), Timeout: 1},
	}
	hm := NewHookManager("proj", hooks)
	// Should not panic even with connection refused
	hm.Emit(HookEvent{Event: HookEventError, Error: "test"})
}

func TestEmit_CommandTimeout(t *testing.T) {
	hooks := []HookConfig{
		{
			Event:   "message.received",
			Type:    "command",
			Command: "sleep 10",
			Async:   boolPtr(false),
			Timeout: 1,
		},
	}
	hm := NewHookManager("proj", hooks)

	start := time.Now()
	hm.Emit(HookEvent{Event: HookEventMessageReceived})
	elapsed := time.Since(start)

	// 1s timeout + up to 2s WaitDelay for orphan child cleanup
	if elapsed > 5*time.Second {
		t.Errorf("expected command to timeout within ~3s, took %v", elapsed)
	}
}

func TestEventToEnv(t *testing.T) {
	e := HookEvent{
		Event:      HookEventCronTriggered,
		Timestamp:  time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC),
		Project:    "myproj",
		SessionKey: "tg:1:1",
		Platform:   "telegram",
		UserID:     "U123",
		UserName:   "alice",
		Content:    "hello world",
		Error:      "oops",
	}
	env := eventToEnv(e)
	m := make(map[string]string)
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		}
	}

	checks := map[string]string{
		"CC_HOOK_EVENT":       "cron.triggered",
		"CC_HOOK_PROJECT":     "myproj",
		"CC_HOOK_SESSION_KEY": "tg:1:1",
		"CC_HOOK_PLATFORM":    "telegram",
		"CC_HOOK_USER_ID":     "U123",
		"CC_HOOK_USER_NAME":   "alice",
		"CC_HOOK_CONTENT":     "hello world",
		"CC_HOOK_ERROR":       "oops",
	}
	for k, want := range checks {
		if got := m[k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if _, ok := m["CC_HOOK_TIMESTAMP"]; !ok {
		t.Error("expected CC_HOOK_TIMESTAMP in env")
	}
}

func TestEventToEnv_EmptyFieldsOmitted(t *testing.T) {
	e := HookEvent{
		Event:   HookEventError,
		Project: "p",
	}
	env := eventToEnv(e)
	for _, kv := range env {
		if strings.HasPrefix(kv, "CC_HOOK_SESSION_KEY=") ||
			strings.HasPrefix(kv, "CC_HOOK_PLATFORM=") ||
			strings.HasPrefix(kv, "CC_HOOK_USER_ID=") ||
			strings.HasPrefix(kv, "CC_HOOK_CONTENT=") {
			t.Errorf("expected empty field to be omitted: %s", kv)
		}
	}
}

func TestHookManager_Hooks_NilManager(t *testing.T) {
	var hm *HookManager
	if hooks := hm.Hooks(); hooks != nil {
		t.Errorf("expected nil hooks from nil manager, got %v", hooks)
	}
}

func TestHookManager_ProjectSet(t *testing.T) {
	var receivedProject string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var ev HookEvent
		_ = json.Unmarshal(body, &ev)
		receivedProject = ev.Project
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hm := NewHookManager("special-project", []HookConfig{
		{Event: "*", Type: "http", URL: srv.URL, Async: boolPtr(false)},
	})

	hm.Emit(HookEvent{Event: HookEventMessageReceived})

	if receivedProject != "special-project" {
		t.Errorf("expected project=special-project, got %s", receivedProject)
	}
}

func TestValidateHookConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  HookConfig
		wantErr bool
	}{
		{"valid command", HookConfig{Event: "error", Type: "command", Command: "echo"}, false},
		{"valid http", HookConfig{Event: "error", Type: "http", URL: "https://x.com"}, false},
		{"empty event", HookConfig{Event: "", Type: "command", Command: "echo"}, true},
		{"empty command", HookConfig{Event: "e", Type: "command", Command: ""}, true},
		{"empty url", HookConfig{Event: "e", Type: "http", URL: ""}, true},
		{"bad url scheme", HookConfig{Event: "e", Type: "http", URL: "ftp://x"}, true},
		{"unknown type", HookConfig{Event: "e", Type: "grpc", Command: "x"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateHookConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateHookConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEmit_MultipleHooksSameEvent(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hooks := []HookConfig{
		{Event: "error", Type: "http", URL: srv.URL, Async: boolPtr(false)},
		{Event: "error", Type: "http", URL: srv.URL, Async: boolPtr(false)},
		{Event: "error", Type: "http", URL: srv.URL, Async: boolPtr(false)},
	}
	hm := NewHookManager("proj", hooks)
	hm.Emit(HookEvent{Event: HookEventError})

	if count.Load() != 3 {
		t.Fatalf("expected 3 hooks to fire, got %d", count.Load())
	}
}

func TestEmit_TimestampAutoFilled(t *testing.T) {
	var receivedTime time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var ev HookEvent
		_ = json.Unmarshal(body, &ev)
		receivedTime = ev.Timestamp
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hm := NewHookManager("proj", []HookConfig{
		{Event: "*", Type: "http", URL: srv.URL, Async: boolPtr(false)},
	})

	before := time.Now()
	hm.Emit(HookEvent{Event: HookEventMessageReceived})

	if receivedTime.Before(before) {
		t.Errorf("expected auto-filled timestamp >= %v, got %v", before, receivedTime)
	}
}
