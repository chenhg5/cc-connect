//go:build smoke

package dsh

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// TestSmoke_RoundTrip runs the adapter against a REAL dsh web server
// (default http://127.0.0.1:3080, override via DSH_SMOKE_URL). It creates a
// fresh session in this project's directory, sends one prompt, and verifies
// the streamed events come back and the turn completes.
//
// Run: go test -tags smoke ./agent/dsh/ -run TestSmoke -v
func TestSmoke_RoundTrip(t *testing.T) {
	baseURL := "http://127.0.0.1:3080"
	if v := envOr("DSH_SMOKE_URL", ""); v != "" {
		baseURL = v
	}
	workDir := envOr("DSH_SMOKE_WORKDIR", ".")

	agent, err := New(map[string]any{
		"base_url": baseURL,
		"work_dir": workDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = agent.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Fresh session.
	s, err := agent.StartSession(ctx, "")
	if err != nil {
		t.Fatalf("StartSession: %v (is `dsh web` running at %s?)", err, agent.(*Agent).baseURL)
	}
	defer func() { _ = s.Close() }()
	ds := s.(*dshSession)
	t.Logf("session id: %s", ds.sessionID)

	if err := ds.Send("Reply with exactly: SMOKE_OK", "smoke-msg", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var text strings.Builder
	var done bool
	deadline := time.After(90 * time.Second)
	for !done {
		select {
		case evt, ok := <-ds.events:
			if !ok {
				t.Fatal("events channel closed before turn end")
			}
			switch evt.Type {
			case core.EventText:
				text.WriteString(evt.Content)
			case core.EventThinking:
				t.Logf("thinking: %s", truncate(evt.Content, 80))
			case core.EventToolUse:
				t.Logf("tool: %s %s", evt.ToolName, truncate(evt.ToolInput, 80))
			case core.EventError:
				t.Logf("error event: %v", evt.Error)
			case core.EventResult:
				done = true
				if evt.Error != nil {
					t.Logf("turn ended with error: %v", evt.Error)
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn end")
		}
	}
	t.Logf("assistant text: %q", text.String())
	if !strings.Contains(text.String(), "SMOKE_OK") {
		t.Errorf("expected SMOKE_OK in reply, got %q", text.String())
	}

	// List + validation against the real server.
	sessions, err := agent.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	t.Logf("sessions in %s: %d", workDir, len(sessions))
	validator := agent.(core.SessionIDValidator)
	if !validator.ValidateSessionID(ctx, ds.sessionID) {
		t.Errorf("ValidateSessionID should accept our own session")
	}
	if validator.ValidateSessionID(ctx, "session-does-not-exist") {
		t.Errorf("ValidateSessionID must reject unknown sessions")
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// The runner uses the published DSH process and a local model endpoint. Only
// the external model is simulated; sessions, tools, approvals and logs are real.
func TestSmoke_RemoteLifecycle(t *testing.T) {
	if os.Getenv("DSH_SMOKE_FIXTURE") != "1" {
		t.Skip("run through testdata/remote_smoke.py")
	}
	opts := map[string]any{"base_url": os.Getenv("DSH_SMOKE_URL"), "work_dir": os.Getenv("DSH_SMOKE_WORKDIR"), "mode": "default"}
	native, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	a := native.(*Agent)
	t.Cleanup(func() { _ = a.Stop() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	models := a.AvailableModels(ctx)
	if len(models) < 2 {
		t.Fatalf("model catalog before first session: %v", models)
	}
	a.SetModel(models[0].Name)
	opts["model"] = models[0].Name
	s, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	id := s.CurrentSessionID()
	skillDir := filepath.Join(a.GetWorkDir(), ".dsh", "skills", "smoke-skill")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: smoke-skill\ndescription: Exercise native skill discovery.\n---\nReply with SMOKE_OK.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var skills struct{ Skills []struct{ Name string } }
	if err := s.(*dshSession).client.remoteInvoke(ctx, "skills/list", map[string]any{"request": map[string]any{"sessionId": id}}, &skills); err != nil {
		t.Fatal(err)
	}
	foundSkill := false
	for _, skill := range skills.Skills {
		if skill.Name == "smoke-skill" {
			foundSkill = true
		}
	}
	if !foundSkill {
		t.Fatalf("native skill discovery: %+v", skills)
	}
	var selected sessionModels
	if err := s.(*dshSession).client.call(ctx, "session.models", map[string]any{"sessionId": id}, &selected); err != nil || selected.Current.Provider+"/"+selected.Current.Model != models[0].Name {
		t.Fatalf("model selection not applied: %+v, %v", selected.Current, err)
	}
	efforts := a.AvailableReasoningEfforts()
	if len(efforts) == 0 {
		t.Fatal("missing native effort catalog")
	}
	opts["reasoning_effort"] = efforts[0]
	for i := 0; i < 2; i++ {
		if err := s.Send("Reply with exactly: SMOKE_OK", fmt.Sprintf("native-turn-%d", i), nil, nil); err != nil {
			t.Fatal(err)
		}
		smokeResult(t, s, "SMOKE_OK", "")
		nativeSession := s.(*dshSession)
		nativeSession.turnMu.Lock()
		armed := nativeSession.turnDone != nil
		nativeSession.turnMu.Unlock()
		if armed {
			t.Fatal("completed real DSH RPC did not disarm its watchdog")
		}
	}
	if !s.(core.LiveModeSwitcher).SetLiveMode("plan") {
		t.Fatal("read-only mode failed")
	}
	var view struct {
		Values struct {
			Permissions struct{ CurrentValue string } `json:"permissions"`
		} `json:"values"`
	}
	if err := s.(*dshSession).client.remoteInvoke(ctx, "session/projections", map[string]any{"request": map[string]any{"sessionId": id}}, &view); err != nil || view.Values.Permissions.CurrentValue != "read-only" {
		t.Fatalf("permission projection: %+v, %v", view, err)
	}
	if !s.(core.LiveModeSwitcher).SetLiveMode("default") {
		t.Fatal("restore mode failed")
	}
	_ = s.Close()
	_ = a.Stop()
	native, err = New(opts)
	if err != nil {
		t.Fatal(err)
	}
	a = native.(*Agent)
	t.Cleanup(func() { _ = a.Stop() })
	s, err = a.StartSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if s.CurrentSessionID() != id {
		t.Fatal("bridge restart replaced the native session")
	}
	var selection struct {
		Values struct {
			ModelSelection struct {
				Next struct {
					ReasoningEffort string `json:"reasoningEffort"`
				} `json:"next"`
			} `json:"modelSelection"`
		} `json:"values"`
	}
	if err := s.(*dshSession).client.remoteInvoke(ctx, "session/projections", map[string]any{"request": map[string]any{"sessionId": id}}, &selection); err != nil || selection.Values.ModelSelection.Next.ReasoningEffort != efforts[0] {
		t.Fatalf("reasoning selection: %+v, %v", selection, err)
	}
	if err := s.Send("Reply with exactly: SMOKE_OK", "native-resume", nil, nil); err != nil {
		t.Fatal(err)
	}
	smokeResult(t, s, "SMOKE_OK", "")
	history, err := a.GetSessionHistory(ctx, id, 4)
	if err != nil || len(history) != 4 || history[3].Role != "assistant" || history[3].Content != "SMOKE_OK" {
		t.Fatalf("native history: %v, %v", history, err)
	}
	usage := s.(core.ContextUsageReporter).GetContextUsage()
	if usage == nil || usage.ContextWindow == 0 || usage.UsedTokens == 0 {
		t.Fatalf("context usage: %+v", usage)
	}
	other, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	for _, behavior := range []string{"deny", "allow"} {
		path := filepath.Join(a.GetWorkDir(), "approval-"+behavior+".txt")
		if err := s.Send("DSH_FIXTURE_WRITE "+path, "native-approval-"+behavior, nil, nil); err != nil {
			t.Fatal(err)
		}
		smokeResult(t, s, "SMOKE_OK", behavior)
		_, err := os.Stat(path)
		if behavior == "deny" && !os.IsNotExist(err) {
			t.Fatalf("denied tool wrote a file: %v", err)
		}
		if behavior == "allow" && err != nil {
			t.Fatal(err)
		}
		select {
		case event := <-other.Events():
			t.Fatalf("another topic received an event: %+v", event)
		default:
		}
	}
	reconnectPath := filepath.Join(a.GetWorkDir(), "approval-reconnect.txt")
	if err := s.Send("DSH_FIXTURE_WRITE "+reconnectPath, "native-reconnect-approval", nil, nil); err != nil {
		t.Fatal(err)
	}
	pendingID := ""
	approvalDeadline := time.After(5 * time.Second)
	for pendingID == "" {
		select {
		case event := <-s.Events():
			if event.Type == core.EventPermissionRequest {
				pendingID = event.RequestID
			}
			if event.Type == core.EventError {
				t.Fatal(event.Error)
			}
		case <-approvalDeadline:
			t.Fatal("approval before reconnect not received")
		}
	}
	_ = s.Close()
	s, err = a.StartSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	smokeResult(t, s, "SMOKE_OK", "allow")
	if _, err := os.Stat(reconnectPath); err != nil {
		t.Fatalf("replayed approval did not finish its tool: %v", err)
	}
	if err := s.Send("DSH_FIXTURE_QUESTION", "native-question", nil, nil); err != nil {
		t.Fatal(err)
	}
	smokeResult(t, s, "SMOKE_OK", "question")
	if err := s.Send("DSH_FIXTURE_SLOW", "native-stop", nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-s.Events():
		if event.Type != core.EventText || event.Content != "WAITING." {
			t.Fatalf("slow-turn prefix: %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("slow turn did not start")
	}
	if err := s.(core.AgentSessionCanceller).CancelTurn(); err != nil {
		t.Fatal(err)
	}
	smokeResult(t, s, "", "")
	if err := s.Send("Reply with exactly: SMOKE_OK", "native-after-stop", nil, nil); err != nil {
		t.Fatal(err)
	}
	smokeResult(t, s, "SMOKE_OK", "")
	if err := s.Send("/compact", "native-compact", nil, nil); err != nil {
		t.Fatal(err)
	}
	smokeResult(t, s, "", "")
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := s.Send("Inspect the attached image.", "native-image", []core.ImageAttachment{{Data: pngData.Bytes(), MimeType: "image/png", FileName: "fixture.png"}}, nil); err != nil {
		t.Fatal(err)
	}
	smokeResult(t, s, "IMAGE_OK", "")
	_ = other.Close()
	if err := a.DeleteSession(ctx, other.CurrentSessionID()); err != nil {
		t.Fatal(err)
	}
	if a.ValidateSessionID(ctx, other.CurrentSessionID()) {
		t.Fatal("archived session still offered for resume")
	}
}

func smokeResult(t *testing.T, s core.AgentSession, want, answer string) {
	t.Helper()
	var text strings.Builder
	requests := 0
	deadline := time.After(20 * time.Second)
	for {
		select {
		case e, ok := <-s.Events():
			if !ok {
				t.Fatal("native stream ended")
			}
			switch e.Type {
			case core.EventText:
				text.WriteString(e.Content)
			case core.EventError:
				t.Fatalf("native event error: %v", e.Error)
			case core.EventPermissionRequest:
				requests++
				if answer == "" {
					t.Fatal("unexpected permission request")
				}
				result := core.PermissionResult{Behavior: answer}
				if answer == "question" {
					result.Behavior = "allow"
					result.UpdatedInput = map[string]any{"answers": map[string]any{"Choose value": "A"}}
				}
				if err := s.RespondPermission(e.RequestID, result); err != nil {
					t.Fatal(err)
				}
			case core.EventResult:
				if e.Error != nil {
					t.Fatalf("native turn failed: %v", e.Error)
				}
				if want != "" && text.String() != want {
					t.Fatalf("native text %q, want %q", text.String(), want)
				}
				if answer != "" && requests != 1 {
					t.Fatalf("wanted one real interaction; got %d", requests)
				}
				return
			}
		case <-deadline:
			t.Fatal("native turn did not complete")
		}
	}
}
