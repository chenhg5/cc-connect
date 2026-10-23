package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

// Opt in with CC_CODEX_INTEGRATION_BIN=/absolute/path/to/codex. The real CLI
// talks only to this local provider; no credentials or paid model are needed.
func TestAppServerSession_RealCodexEffort(t *testing.T) {
	bin := os.Getenv("CC_CODEX_INTEGRATION_BIN")
	if bin == "" {
		t.Skip("set CC_CODEX_INTEGRATION_BIN to verify real Codex requests")
	}
	requests := make(chan map[string]any, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Method == http.MethodPost && json.NewDecoder(r.Body).Decode(&body) == nil {
			requests <- body
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"local request capture complete"}}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "home"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := []string{bin, "-c", `model_provider="capture"`, "-c", `model_providers.capture.name="Local capture"`,
		"-c", fmt.Sprintf("model_providers.capture.base_url=%q", server.URL+"/v1"),
		"-c", `model_providers.capture.wire_api="responses"`, "-c", "model_providers.capture.requires_openai_auth=false",
		"-c", "model_providers.capture.request_max_retries=0", "-c", "model_providers.capture.stream_max_retries=0"}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	resumeID := ""
	for _, effort := range []string{"low", "high"} {
		a, err := New(map[string]any{"backend": "app_server", "cmd": cmd, "work_dir": dir,
			"codex_home": filepath.Join(dir, "home"), "model": "gpt-6.1-sol", "reasoning_effort": effort, "mode": "yolo"})
		if err != nil {
			t.Fatal(err)
		}
		s, err := a.StartSession(ctx, resumeID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Send("Reply OK", "probe", nil, nil); err != nil {
			_ = s.Close()
			t.Fatal(err)
		}
		select {
		case request := <-requests:
			reasoning, _ := request["reasoning"].(map[string]any)
			if reasoning["effort"] != effort {
				_ = s.Close()
				t.Fatalf("resume %q: actual model request effort = %v, want %s", resumeID, reasoning["effort"], effort)
			}
		case <-ctx.Done():
			_ = s.Close()
			t.Fatal("no model request reached the configured local provider")
		}
		resumeID = s.CurrentSessionID()
		_ = s.Close()
	}
}

func TestAppServerSession_UsesConfiguredCommandAndArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fallback executable fixture uses a POSIX shell")
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// A regression must not accidentally launch the operator's real Codex.
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\nexit 89\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("CC_CODEX_COMMAND_TEST", "1")
	trace := filepath.Join(dir, "requests.jsonl")
	t.Setenv("CC_CODEX_COMMAND_TRACE", trace)
	cmd := []string{bin, "-test.run=^TestAppServerCommandProcess$", "--", "-p", "profile with spaces", "-c", `model_reasoning_effort="medium"`}
	a, err := New(map[string]any{
		"backend": "app_server", "cmd": cmd, "work_dir": dir,
		"model": "test-model", "reasoning_effort": "high", "mode": "yolo",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, resumeID := range []string{"", "saved-thread"} {
		s, err := a.StartSession(ctx, resumeID)
		if err != nil {
			t.Fatalf("start with configured command (resume %q): %v", resumeID, err)
		}
		if err := s.Send("probe", "message", nil, nil); err != nil {
			_ = s.Close()
			t.Fatal(err)
		}
		_ = s.Close()
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	starts, resumes, turns := 0, 0, 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var record struct {
			Args   []string       `json:"args"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record.Args != nil {
			want := append(append([]string{}, cmd[1:]...), "app-server", "-c", `model="test-model"`, "-c", `model_reasoning_effort="high"`)
			if !reflect.DeepEqual(record.Args, want) {
				t.Fatalf("launched args = %q, want %q", record.Args, want)
			}
		}
		switch record.Method {
		case "thread/start":
			starts++
		case "thread/resume":
			resumes++
		case "turn/start":
			turns++
			if record.Params["effort"] != "high" {
				t.Fatalf("turn effort = %v, want high", record.Params["effort"])
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || resumes != 1 || turns != 2 {
		t.Fatalf("start/resume/turn counts = %d/%d/%d, want 1/1/2", starts, resumes, turns)
	}
}

// This test binary doubles as the configured app-server executable.
func TestAppServerCommandProcess(t *testing.T) {
	if os.Getenv("CC_CODEX_COMMAND_TEST") != "1" {
		return
	}
	trace, err := os.OpenFile(os.Getenv("CC_CODEX_COMMAND_TRACE"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(2)
	}
	defer trace.Close()
	log := json.NewEncoder(trace)
	_ = log.Encode(map[string]any{"args": os.Args[1:]})
	scanner := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request map[string]any
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(3)
		}
		_ = log.Encode(request)
		if request["id"] == nil {
			continue
		}
		result := map[string]any{}
		switch request["method"] {
		case "thread/start", "thread/resume":
			result = map[string]any{"thread": map[string]any{"id": "saved-thread"}, "model": "test-model", "reasoningEffort": "high"}
		case "turn/start":
			result = map[string]any{"turn": map[string]any{"id": "test-turn"}}
		}
		_ = out.Encode(map[string]any{"id": request["id"], "result": result})
	}
	os.Exit(0)
}
