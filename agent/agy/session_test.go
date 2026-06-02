package agy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestAgySession_SendEmitsTextAndInfersConversationID(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))

	conversationID := "11111111-1111-4111-8111-111111111111"
	argsFile := filepath.Join(tmp, "args.txt")
	promptFile := filepath.Join(tmp, "prompt.txt")
	script := writeFakeAgy(t, tmp, `
printf '%s\n' "$@" > "$ARGS_FILE"
cat > "$PROMPT_FILE"
mkdir -p "$HOME/.gemini/antigravity-cli/conversations"
printf 'pb' > "$HOME/.gemini/antigravity-cli/conversations/$CONVERSATION_ID.pb"
printf 'reply: '
cat "$PROMPT_FILE"
`, map[string]string{
		"ARGS_FILE":       argsFile,
		"PROMPT_FILE":     promptFile,
		"CONVERSATION_ID": conversationID,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := newAgySession(ctx, script, tmp, "", 0)
	if err != nil {
		t.Fatalf("newAgySession: %v", err)
	}
	defer sess.Close()

	if err := sess.Send("hello world", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	events := collectAgyEvents(t, sess.Events())
	text := joinEventText(events)
	if !strings.Contains(text, "reply: hello world") {
		t.Fatalf("EventText content = %q, want reply with prompt", text)
	}
	if got := lastResultSessionID(events); got != conversationID {
		t.Fatalf("EventResult.SessionID = %q, want %q", got, conversationID)
	}
	if got := sess.CurrentSessionID(); got != conversationID {
		t.Fatalf("CurrentSessionID = %q, want %q", got, conversationID)
	}

	args := readFile(t, argsFile)
	if strings.Contains(args, "--conversation") {
		t.Fatalf("fresh session args should not contain --conversation: %q", args)
	}
	if !strings.Contains(args, "--dangerously-skip-permissions") || !strings.Contains(args, "-p") {
		t.Fatalf("args missing required agy flags: %q", args)
	}
	if !strings.Contains(args, "--add-dir\n"+tmp) {
		t.Fatalf("args should register work_dir with --add-dir: %q", args)
	}
}

func TestAgySession_SendUsesResumeConversationID(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))

	argsFile := filepath.Join(tmp, "args.txt")
	script := writeFakeAgy(t, tmp, `
printf '%s\n' "$@" > "$ARGS_FILE"
cat >/dev/null
printf 'resumed'
`, map[string]string{
		"ARGS_FILE": argsFile,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := newAgySession(ctx, script, tmp, "resume-123", 0)
	if err != nil {
		t.Fatalf("newAgySession: %v", err)
	}
	defer sess.Close()

	if err := sess.Send("next", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	events := collectAgyEvents(t, sess.Events())
	if got := lastResultSessionID(events); got != "resume-123" {
		t.Fatalf("EventResult.SessionID = %q, want resume-123", got)
	}

	args := readFile(t, argsFile)
	if !strings.Contains(args, "--conversation\nresume-123") {
		t.Fatalf("resume args should contain --conversation resume-123, got %q", args)
	}
}

func TestAgySession_StripsRepeatedPreviousOutputOnResume(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))

	argsDir := filepath.Join(tmp, "args")
	script := writeFakeAgy(t, tmp, `
mkdir -p "$ARGS_DIR" "$HOME/.gemini/antigravity-cli/conversations"
idx=$(ls "$ARGS_DIR" | wc -l | tr -d ' ')
printf '%s\n' "$@" > "$ARGS_DIR/$idx.txt"
cat >/dev/null
if [ "$idx" = "0" ]; then
  printf 'pb' > "$HOME/.gemini/antigravity-cli/conversations/$CONVERSATION_ID.pb"
  printf 'first answer\n'
else
  if [ "$idx" = "1" ]; then
    printf 'first answer\nsecond answer\n'
  else
    printf 'first answer\nsecond answer\nthird answer\n'
  fi
fi
`, map[string]string{
		"ARGS_DIR":        argsDir,
		"CONVERSATION_ID": "33333333-3333-4333-8333-333333333333",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := newAgySession(ctx, script, tmp, "", 0)
	if err != nil {
		t.Fatalf("newAgySession: %v", err)
	}
	defer sess.Close()

	if err := sess.Send("first", nil, nil); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	firstEvents := collectAgyEvents(t, sess.Events())
	if got := strings.TrimSpace(joinEventText(firstEvents)); got != "first answer" {
		t.Fatalf("first response = %q, want first answer", got)
	}

	if err := sess.Send("second", nil, nil); err != nil {
		t.Fatalf("second Send: %v", err)
	}
	secondEvents := collectAgyEvents(t, sess.Events())
	if got := strings.TrimSpace(joinEventText(secondEvents)); got != "second answer" {
		t.Fatalf("second response = %q, want only new answer", got)
	}

	args := readFile(t, filepath.Join(argsDir, "1.txt"))
	if !strings.Contains(args, "--conversation\n33333333-3333-4333-8333-333333333333") {
		t.Fatalf("second turn should resume conversation, args=%q", args)
	}

	if err := sess.Send("third", nil, nil); err != nil {
		t.Fatalf("third Send: %v", err)
	}
	thirdEvents := collectAgyEvents(t, sess.Events())
	if got := strings.TrimSpace(joinEventText(thirdEvents)); got != "third answer" {
		t.Fatalf("third response = %q, want only new answer", got)
	}
}

func TestAgySession_InjectsSystemPromptOnlyOnce(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))

	promptDir := filepath.Join(tmp, "prompts")
	script := writeFakeAgy(t, tmp, `
mkdir -p "$PROMPT_DIR" "$HOME/.gemini/antigravity-cli/conversations"
idx=$(ls "$PROMPT_DIR" | wc -l | tr -d ' ')
cat > "$PROMPT_DIR/$idx.txt"
if [ "$idx" = "0" ]; then
  printf 'pb' > "$HOME/.gemini/antigravity-cli/conversations/$CONVERSATION_ID.pb"
fi
printf 'ok'
`, map[string]string{
		"PROMPT_DIR":      promptDir,
		"CONVERSATION_ID": "22222222-2222-4222-8222-222222222222",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	agent, err := New(map[string]any{"cmd": script, "work_dir": tmp})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess, err := agent.StartSession(ctx, "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	if err := sess.Send("first user message", nil, nil); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	collectAgyEvents(t, sess.Events())
	if err := sess.Send("second user message", nil, nil); err != nil {
		t.Fatalf("second Send: %v", err)
	}
	collectAgyEvents(t, sess.Events())

	first := readFile(t, filepath.Join(promptDir, "0.txt"))
	second := readFile(t, filepath.Join(promptDir, "1.txt"))
	if !strings.Contains(first, "cc-connect, a bridge") || !strings.Contains(first, "first user message") {
		t.Fatalf("first prompt = %q, want cc-connect system context and user message", first)
	}
	if strings.Contains(second, "cc-connect, a bridge") {
		t.Fatalf("second prompt should not repeat system context: %q", second)
	}
	if strings.Contains(second, "first user message") || !strings.Contains(second, "second user message") {
		t.Fatalf("second prompt should contain only current user message, got %q", second)
	}
}

func TestAgySession_StreamsTextBeforeProcessExit(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))

	script := writeFakeAgy(t, tmp, `
cat >/dev/null
printf 'first line\n'
sleep 1
printf 'second line\n'
`, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := newAgySession(ctx, script, tmp, "", 0)
	if err != nil {
		t.Fatalf("newAgySession: %v", err)
	}
	defer sess.Close()

	if err := sess.Send("stream", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case evt := <-sess.Events():
		if evt.Type != core.EventText || evt.Content != "first line\n" {
			t.Fatalf("first event = %#v, want first streamed EventText", evt)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for streamed EventText before process exit")
	}

	events := collectAgyEvents(t, sess.Events())
	if text := joinEventText(events); !strings.Contains(text, "second line") {
		t.Fatalf("remaining EventText content = %q, want second line", text)
	}
}

func TestAgyAgent_PassesSessionEnvToSubprocess(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))

	envFile := filepath.Join(tmp, "env.txt")
	script := writeFakeAgy(t, tmp, `
cat >/dev/null
printf '%s\n%s\n' "$CC_PROJECT" "$CC_SESSION_KEY" > "$ENV_FILE"
printf 'ok'
`, map[string]string{
		"ENV_FILE": envFile,
	})

	agent, err := New(map[string]any{"cmd": script, "work_dir": tmp})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	inj, ok := agent.(interface{ SetSessionEnv([]string) })
	if !ok {
		t.Fatal("agy Agent should implement SetSessionEnv")
	}
	inj.SetSessionEnv([]string{"CC_PROJECT=agy", "CC_SESSION_KEY=web:test"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := agent.StartSession(ctx, "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	if err := sess.Send("env", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	collectAgyEvents(t, sess.Events())

	got := strings.TrimSpace(readFile(t, envFile))
	if got != "agy\nweb:test" {
		t.Fatalf("session env = %q, want CC_PROJECT and CC_SESSION_KEY", got)
	}
}

func TestAgySession_AuthErrorDetection(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))

	script := writeFakeAgy(t, tmp, `
cat >/dev/null
printf 'Authentication required. Please visit the URL to log in:\nhttps://example.com/auth\n'
`, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := newAgySession(ctx, script, tmp, "", 0)
	if err != nil {
		t.Fatalf("newAgySession: %v", err)
	}
	defer sess.Close()

	if err := sess.Send("test", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	events := collectAgyEvents(t, sess.Events())
	if len(events) < 2 || events[0].Type != core.EventError {
		t.Fatalf("first event = %#v, want EventError", events)
	}
	if !strings.Contains(events[0].Error.Error(), "authentication required") {
		t.Fatalf("error = %v, want authentication required", events[0].Error)
	}
	if events[len(events)-1].Type != core.EventResult {
		t.Fatalf("last event = %s, want EventResult", events[len(events)-1].Type)
	}
}

func TestAgySession_ContinueSessionTreatedAsFresh(t *testing.T) {
	sess, err := newAgySession(context.Background(), "/bin/echo", t.TempDir(), core.ContinueSession, 0)
	if err != nil {
		t.Fatalf("newAgySession: %v", err)
	}
	defer sess.Close()

	if got := sess.CurrentSessionID(); got != "" {
		t.Fatalf("CurrentSessionID = %q, want empty", got)
	}
}

func writeFakeAgy(t *testing.T, dir, body string, env map[string]string) string {
	t.Helper()

	var b strings.Builder
	b.WriteString("#!/bin/sh\nset -eu\n")
	for k, v := range env {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(shellQuote(v))
		b.WriteByte('\n')
	}
	b.WriteString(body)
	b.WriteByte('\n')

	path := filepath.Join(dir, "fake-agy.sh")
	if err := os.WriteFile(path, []byte(b.String()), 0o755); err != nil {
		t.Fatalf("write fake agy: %v", err)
	}
	return path
}

func collectAgyEvents(t *testing.T, ch <-chan core.Event) []core.Event {
	t.Helper()

	timer := time.After(5 * time.Second)
	var events []core.Event
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				t.Fatal("events channel closed before EventResult")
			}
			events = append(events, evt)
			if evt.Type == core.EventResult {
				return events
			}
		case <-timer:
			t.Fatalf("timed out waiting for EventResult; events=%#v", events)
		}
	}
}

func joinEventText(events []core.Event) string {
	var b strings.Builder
	for _, evt := range events {
		if evt.Type == core.EventText {
			b.WriteString(evt.Content)
		}
	}
	return b.String()
}

func lastResultSessionID(events []core.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == core.EventResult {
			return events[i].SessionID
		}
	}
	return ""
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(fmt.Sprint(s), "'", "'\\''") + "'"
}
