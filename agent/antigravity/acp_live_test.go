package antigravity

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Opt-in because this uses a signed-in, real agy and consumes inference.
func TestAgyACPLiveMidTurn(t *testing.T) {
	binary := os.Getenv("CC_AGY_LIVE_BINARY")
	if binary == "" {
		t.Skip("set CC_AGY_LIVE_BINARY to run the real vendor integration")
	}
	dir := t.TempDir()
	a := &Agent{cmd: binary, workDir: dir, model: "gemini-3.8-flash-low", effort: "low", mode: "yolo", activeIdx: -1,
		configEnv: []string{"AGY_CLI_DISABLE_AUTO_UPDATE=true"}, timeout: 2 * time.Minute}
	s, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	sender, ok := s.(agyMidTurnInputSender)
	if !ok {
		t.Fatal("native ACP was not selected")
	}
	sid := s.CurrentSessionID()
	if err := s.Send("Controlled protocol test. Run exactly one shell command: touch started; sleep 12; touch finished . Wait for completion, then reply exactly FIRST8767. Do not perform other tasks.", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(45 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native tool did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "finished")); err == nil {
		t.Fatal("injection was not mid-turn")
	}
	pixel, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aX1sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.SendMidTurn("Additional instruction for the running task: keep the original command alive and wait for finished. Change the final answer to the exact contents of the attached final-answer.txt. The image is only a transport test, ignore it.", "live-steer", []core.ImageAttachment{{MimeType: "image/png", Data: pixel}}, []core.FileAttachment{{FileName: "final-answer.txt", Data: []byte("SECOND8767")}}); err != nil {
		t.Fatal(err)
	}
	var answer strings.Builder
	deadline = time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case event := <-s.Events():
			if event.Type == core.EventError {
				t.Fatal(event.Error)
			}
			if event.Type == core.EventText {
				answer.WriteString(event.Content)
			}
			if event.Type != core.EventResult {
				continue
			}
			if strings.TrimSpace(answer.String()) != "SECOND8767" {
				t.Fatalf("original or incomplete answer: %q", answer.String())
			}
			if _, err := os.Stat(filepath.Join(dir, "finished")); err != nil {
				t.Fatalf("original tool did not finish: %v", err)
			}
			if event.InputTokens+event.CacheReadInputTokens <= 0 || event.OutputTokens <= 0 {
				t.Fatalf("usage missing: %+v", event)
			}
			t.Logf("same-session steer + image/file PASS; input=%d cache=%d output=%d", event.InputTokens, event.CacheReadInputTokens, event.OutputTokens)
			goto nextTurn
		case <-time.After(time.Second):
		}
	}
	t.Fatal("steered turn did not finish")
nextTurn:
	if err := s.Send("Reply exactly ALIVE8767. No tools.", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	answer.Reset()
	deadline = time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case event := <-s.Events():
			if event.Type == core.EventError {
				t.Fatal(event.Error)
			}
			if event.Type == core.EventText {
				answer.WriteString(event.Content)
			}
			if event.Type != core.EventResult {
				continue
			}
			if s.CurrentSessionID() != sid || strings.TrimSpace(answer.String()) != "ALIVE8767" {
				t.Fatalf("next turn corrupted: %q", answer.String())
			}
			return
		case <-time.After(time.Second):
		}
	}
	t.Fatal("next turn did not finish")
}
