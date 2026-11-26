package core

import (
	"strings"
	"testing"
	"time"
)

func TestFormatToolInputTodoWrite(t *testing.T) {
	raw := `{"todos":[{"content":"plan","status":"pending"},{"content":"build","status":"in_progress"},{"content":"verify","status":"completed"}]}`
	got := formatToolInput("TodoWrite", raw)
	for _, want := range []string{"⬜ plan", "🔄 build", "✅ verify"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted input %q missing %q", got, want)
		}
	}
	if strings.Contains(got, `"todos"`) {
		t.Errorf("formatted input contains raw JSON: %q", got)
	}
}

func TestFormatToolInputTodoWriteFallback(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"TodoWrite", `{broken`},
		{"Bash", `echo hi`},
	} {
		if got := formatToolInput(tc.name, tc.input); got != tc.input {
			t.Errorf("formatToolInput(%q, %q) = %q", tc.name, tc.input, got)
		}
	}
}

func TestTodoWriteStepReplacesPreviousList(t *testing.T) {
	steps := appendToolStep(nil, "TodoWrite", "⬜ first")
	steps = appendToolStep(steps, "Bash", "echo hi")
	steps = appendToolStep(steps, "TodoWrite", "✅ first")
	if len(steps) != 2 || steps[0].Summary != "✅ first" || steps[1].Name != "Bash" {
		t.Fatalf("latest checklist should replace prior TodoWrite step: %+v", steps)
	}
}

func TestTodoWriteSummaryRespectsToolLimit(t *testing.T) {
	raw := `{"todos":[{"content":"` + strings.Repeat("长", 100) + `","status":"pending"}]}`
	formatted := truncateIf(formatToolInput("TodoWrite", raw), 25)
	if strings.Contains(formatted, `"todos"`) || len([]rune(formatted)) > 28 || !strings.HasPrefix(formatted, "⬜") {
		t.Fatalf("checklist summary not bounded and readable: %q", formatted)
	}
}

func TestTodoWriteResultCreatesReadableRichStep(t *testing.T) {
	raw := `{"todos":[{"content":"ship","status":"completed"}]}`
	steps := mergeRichToolResult(nil, Event{ToolName: "TodoWrite", ToolInput: raw}, "", 100)
	if len(steps) != 1 || steps[0].Summary != "✅ ship" {
		t.Fatalf("rich card should show checklist: %+v", steps)
	}
}

func TestTodoWriteLegacyProgressShowsChecklist(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: "full", ToolMessages: true, ToolMaxLen: 500})
	key := "test:todo-progress"
	session := e.sessions.GetOrCreateActive(key)
	agentSession := newControllableSession("todo-session")
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx"}
	e.interactiveStates[key] = state
	agentSession.events <- Event{Type: EventToolUse, ToolName: "TodoWrite", ToolInput: `{"todos":[{"content":"plan","status":"pending"},{"content":"ship","status":"completed"}]}`}
	agentSession.events <- Event{Type: EventResult, Content: "done", Done: true}
	e.processInteractiveEvents(state, session, e.sessions, key, "message-1", time.Now(), nil, nil, state.replyCtx, 0)
	sent := strings.Join(p.getSent(), "\n")
	if !strings.Contains(sent, "⬜ plan") || !strings.Contains(sent, "✅ ship") || strings.Contains(sent, `"todos"`) {
		t.Fatalf("progress should contain readable checklist, got %q", sent)
	}
}
