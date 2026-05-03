package opencode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// TestOpencodeSessionEntry_Unmarshal verifies that OpenCode's
// `session list --format json` output can be correctly parsed.
//
// OpenCode returns `updated` and `created` as Unix timestamps in
// milliseconds (int64), not strings. This test prevents regression
// of the unmarshal error:
//
//	json: cannot unmarshal number into Go struct field opencodeSessionEntry.updated of type string
func TestOpencodeSessionEntry_Unmarshal(t *testing.T) {
	jsonData := `[
  {
    "id": "ses_2eb11bb11ffeYwQZOj25mlmGMc",
    "title": "Test Session",
    "updated": 1774174646445,
    "created": 1774172652782,
    "projectId": "b80385ead03e8b450bdb2016d434aad318f93c16",
    "directory": "/path/to/project"
  }
]`

	var entries []opencodeSessionEntry
	if err := json.Unmarshal([]byte(jsonData), &entries); err != nil {
		t.Fatalf("Failed to unmarshal OpenCode session list: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("Expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.ID != "ses_2eb11bb11ffeYwQZOj25mlmGMc" {
		t.Errorf("ID = %q, want %q", e.ID, "ses_2eb11bb11ffeYwQZOj25mlmGMc")
	}
	if e.Title != "Test Session" {
		t.Errorf("Title = %q, want %q", e.Title, "Test Session")
	}
	if e.Updated != 1774174646445 {
		t.Errorf("Updated = %d, want %d", e.Updated, 1774174646445)
	}
	if e.Created != 1774172652782 {
		t.Errorf("Created = %d, want %d", e.Created, 1774172652782)
	}
}

// TestNewOpencodeSession_ContinueSessionTreatedAsFresh verifies that
// the ContinueSession sentinel (__continue__) is not passed as a literal
// session ID to the CLI. This was fixed in PR #249.
func TestNewOpencodeSession_ContinueSessionTreatedAsFresh(t *testing.T) {
	s, err := newOpencodeSession(context.Background(), "echo", "/tmp", "", "default", core.ContinueSession, nil)
	if err != nil {
		t.Fatalf("newOpencodeSession: %v", err)
	}
	defer s.Close()

	if got := s.CurrentSessionID(); got != "" {
		t.Errorf("ContinueSession should be treated as fresh: chatID = %q, want empty", got)
	}
}

func TestOpencodeSessionStageImages(t *testing.T) {
	dir := t.TempDir()
	s := &opencodeSession{workDir: dir}

	prompt, imagePaths, err := s.stageImages("", []core.ImageAttachment{
		{MimeType: "image/jpeg", Data: []byte{0xff, 0xd8, 0xff}},
		{MimeType: "image/webp", Data: []byte("webp")},
	})
	if err != nil {
		t.Fatalf("stageImages: %v", err)
	}
	if prompt != "Please analyze the attached image(s)." {
		t.Fatalf("prompt = %q", prompt)
	}
	if len(imagePaths) != 2 {
		t.Fatalf("imagePaths len = %d, want 2", len(imagePaths))
	}
	if filepath.Ext(imagePaths[0]) != ".jpg" {
		t.Fatalf("first ext = %q, want .jpg", filepath.Ext(imagePaths[0]))
	}
	if filepath.Ext(imagePaths[1]) != ".webp" {
		t.Fatalf("second ext = %q, want .webp", filepath.Ext(imagePaths[1]))
	}
	for _, path := range imagePaths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected staged image %s: %v", path, err)
		}
	}
}

func TestOpencodeSessionBuildRunArgsIncludesImagesAsFiles(t *testing.T) {
	s := &opencodeSession{workDir: "/repo", model: "provider/model"}

	got := s.buildRunArgs("describe these images", []string{"/tmp/a.png", "/tmp/b.jpg"}, "ses_123")
	want := []string{
		"run", "--format", "json",
		"--session", "ses_123",
		"--model", "provider/model",
		"--dir", "/repo",
		"--thinking",
		"--file", "/tmp/a.png",
		"--file", "/tmp/b.jpg",
		"--", "describe these images",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestHandleStepStart(t *testing.T) {
	s, _ := newOpencodeSession(context.Background(), "echo", "/tmp", "", "default", "", nil)
	defer s.Close()

	s.handleStepStart(map[string]any{
		"type": "step_start", "sessionID": "ses_123", "part": map[string]any{},
	})

	if got := s.CurrentSessionID(); got != "ses_123" {
		t.Errorf("chatID = %q, want %q", got, "ses_123")
	}
}

func TestHandleStepFinish(t *testing.T) {
	tests := []struct {
		name      string
		raw       map[string]any
		wantEvent bool
	}{
		{
			name:      "reason stop sends EventResult",
			raw:       map[string]any{"type": "step_finish", "part": map[string]any{"reason": "stop"}},
			wantEvent: true,
		},
		{
			name:      "reason tool-calls no EventResult",
			raw:       map[string]any{"type": "step_finish", "part": map[string]any{"reason": "tool-calls"}},
			wantEvent: false,
		},
		{
			name:      "no reason no EventResult",
			raw:       map[string]any{"type": "step_finish", "part": map[string]any{}},
			wantEvent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newOpencodeSession(context.Background(), "echo", "/tmp", "", "default", "", nil)
			defer s.Close()

			s.handleStepFinish(tt.raw)

			select {
			case evt := <-s.events:
				if !tt.wantEvent {
					t.Errorf("unexpected event: %+v", evt)
				}
			default:
				if tt.wantEvent {
					t.Error("expected EventResult, got none")
				}
			}
		})
	}
}

func TestHandleStepFinishDuplicateEventResult(t *testing.T) {
	s, _ := newOpencodeSession(context.Background(), "echo", "/tmp", "", "default", "", nil)
	defer s.Close()

	raw := map[string]any{"type": "step_finish", "part": map[string]any{"reason": "stop"}}
	s.handleStepFinish(raw)
	s.handleStepFinish(raw)

	count := 0
	drain := true
	for drain {
		select {
		case <-s.events:
			count++
		default:
			drain = false
		}
	}
	if count != 1 {
		t.Errorf("expected 1 EventResult, got %d", count)
	}
}

// verify Agent implements core.Agent
var _ core.Agent = (*Agent)(nil)
