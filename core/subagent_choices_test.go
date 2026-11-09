package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

type stubSubagentSwitcher struct{ model string }

func (s *stubSubagentSwitcher) SetSubagentModel(m string) { s.model = m }
func (s *stubSubagentSwitcher) GetSubagentModel() string  { return s.model }

// TestSubagentChoicesInheritFirst pins the option order: the inherit entry is
// always first so "/model subagent 1" means the same thing on every platform.
func TestSubagentChoicesInheritFirst(t *testing.T) {
	e := &Engine{i18n: NewI18n("en")}
	models := []ModelOption{{Name: "fable"}, {Name: "sonnet"}}

	buttons, text := e.subagentChoices(&stubSubagentSwitcher{}, models)

	if len(buttons) == 0 || len(buttons[0]) == 0 {
		t.Fatal("no buttons returned")
	}
	if got := buttons[0][0].Data; got != "cmd:/model subagent inherit" {
		t.Errorf("first button data = %q, want the inherit command", got)
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) != len(models)+1 {
		t.Fatalf("got %d lines for %d models, want %d", len(lines), len(models), len(models)+1)
	}
	if !strings.HasPrefix(lines[1], "  2. fable") {
		t.Errorf("line 2 = %q, want the first model at index 2", lines[1])
	}
}

// TestSubagentChoicesMarksCurrent checks the active model is marked in both the
// text and the button label, matching the main-model list.
func TestSubagentChoicesMarksCurrent(t *testing.T) {
	e := &Engine{i18n: NewI18n("en")}
	models := []ModelOption{{Name: "fable"}, {Name: "sonnet"}}

	buttons, text := e.subagentChoices(&stubSubagentSwitcher{model: "sonnet"}, models)

	if !strings.Contains(text, "> 3. sonnet") {
		t.Errorf("current model not marked in text:\n%s", text)
	}
	var sonnetBtn ButtonOption
	for _, row := range buttons {
		for _, b := range row {
			if b.Data == "cmd:/model subagent 3" {
				sonnetBtn = b
			}
		}
	}
	if !strings.HasPrefix(sonnetBtn.Text, "\u25b6") {
		t.Errorf("current model button not marked: %q", sonnetBtn.Text)
	}
}

// TestSubagentChoicesRowsOfThree keeps the button grid within the tightest
// platform limits the code already assumes for the model list.
func TestSubagentChoicesRowsOfThree(t *testing.T) {
	e := &Engine{i18n: NewI18n("en")}
	models := make([]ModelOption, 7)
	for i := range models {
		models[i] = ModelOption{Name: fmt.Sprintf("m%d", i)}
	}

	buttons, _ := e.subagentChoices(&stubSubagentSwitcher{}, models)

	// row 0 is the single inherit button; the rest hold up to 3 each.
	for i, row := range buttons[1:] {
		if len(row) > 3 {
			t.Errorf("row %d has %d buttons, want at most 3", i+1, len(row))
		}
	}
}

// stubSubagentAgent implements ModelSwitcher and SubagentModelSwitcher so the
// engine's subagent paths can be exercised end to end.
type stubSubagentAgent struct {
	stubModelModeAgent
	subagent string
}

func (a *stubSubagentAgent) SetSubagentModel(m string) { a.subagent = m }
func (a *stubSubagentAgent) GetSubagentModel() string  { return a.subagent }

// TestSubagentChoicesButtonsRoundTrip executes the data every button carries,
// exactly as a platform would, and checks it selects the model the label
// names. Regression test for the "switch N" button syntax cmdSubagent never
// accepted.
func TestSubagentChoicesButtonsRoundTrip(t *testing.T) {
	a := &stubSubagentAgent{}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "sess", ReplyCtx: "ctx"}
	models := a.AvailableModels(context.Background())

	buttons, _ := e.subagentChoices(a, models)
	want := map[string]string{
		"cmd:/model subagent inherit": "",
		"cmd:/model subagent 2":       models[0].Name,
		"cmd:/model subagent 3":       models[1].Name,
	}
	seen := 0
	for _, row := range buttons {
		for _, b := range row {
			target, ok := want[b.Data]
			if !ok {
				t.Fatalf("unexpected button data %q", b.Data)
			}
			seen++
			a.subagent = "sentinel"
			args := strings.Fields(strings.TrimPrefix(b.Data, "cmd:/model subagent"))
			e.cmdSubagent(p, msg, args, a)
			if got := a.GetSubagentModel(); got != target {
				t.Errorf("button %q selected %q, want %q", b.Data, got, target)
			}
		}
	}
	if seen != len(want) {
		t.Fatalf("checked %d buttons, want %d", seen, len(want))
	}
}

// TestCmdSubagentNumbering pins the numbering the list prints: 1 = inherit,
// 2..N+1 = models in order.
func TestCmdSubagentNumbering(t *testing.T) {
	a := &stubSubagentAgent{}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "sess", ReplyCtx: "ctx"}

	for _, tc := range []struct{ arg, want string }{
		{"1", ""},
		{"2", "gpt-4.1"},
		{"3", "gpt-4.1-mini"},
	} {
		a.subagent = "sentinel"
		e.cmdSubagent(p, msg, []string{tc.arg}, a)
		if got := a.GetSubagentModel(); got != tc.want {
			t.Errorf("/model subagent %s selected %q, want %q", tc.arg, got, tc.want)
		}
	}

	a.subagent = "sentinel"
	e.cmdSubagent(p, msg, []string{"4"}, a)
	if got := a.GetSubagentModel(); got != "sentinel" {
		t.Errorf("out-of-range choice changed the model to %q", got)
	}
}

// TestHandleSubagentCardActionInherit is the regression test for the card's
// single-field "act:/subagent inherit" action.
func TestHandleSubagentCardActionInherit(t *testing.T) {
	a := &stubSubagentAgent{}
	e := NewEngine("test", a, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
	a.subagent = "gpt-4.1"

	e.handleSubagentCardAction("inherit", "sess")
	if got := a.GetSubagentModel(); got != "" {
		t.Fatalf("card inherit left model at %q, want cleared", got)
	}
}

// TestHandleSubagentCardActionSwitchNumbering pins the card to the same
// numbering as the text list.
func TestHandleSubagentCardActionSwitchNumbering(t *testing.T) {
	a := &stubSubagentAgent{}
	e := NewEngine("test", a, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)

	e.handleSubagentCardAction("switch 3", "sess")
	if got := a.GetSubagentModel(); got != "gpt-4.1-mini" {
		t.Fatalf("card switch 3 selected %q, want gpt-4.1-mini", got)
	}
}

func TestSetSubagentModelSavesBeforeApplying(t *testing.T) {
	a := &stubSubagentAgent{}
	e := NewEngine("test", a, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
	var saved []string
	e.SetSubagentModelSaveFunc(func(m string) error {
		saved = append(saved, m)
		if got := a.GetSubagentModel(); got != "" {
			t.Errorf("save ran after memory changed to %q", got)
		}
		return nil
	})

	if err := e.setSubagentModel(a, "gpt-4.1", "sess", true); err != nil {
		t.Fatalf("setSubagentModel() error: %v", err)
	}
	if got := a.GetSubagentModel(); got != "gpt-4.1" {
		t.Errorf("memory = %q, want gpt-4.1", got)
	}
	if len(saved) != 1 || saved[0] != "gpt-4.1" {
		t.Errorf("saved = %v, want [gpt-4.1]", saved)
	}
}

func TestSetSubagentModelSaveFailureKeepsMemory(t *testing.T) {
	a := &stubSubagentAgent{subagent: "gpt-4.1"}
	e := NewEngine("test", a, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
	e.SetSubagentModelSaveFunc(func(string) error { return errors.New("read-only config") })

	if err := e.setSubagentModel(a, "gpt-4.1-mini", "sess", true); err == nil {
		t.Fatal("setSubagentModel() error = nil, want save failure")
	}
	if got := a.GetSubagentModel(); got != "gpt-4.1" {
		t.Errorf("memory = %q, want unchanged gpt-4.1", got)
	}
}

func TestCmdSubagentSaveFailureKeepsModel(t *testing.T) {
	a := &stubSubagentAgent{subagent: "gpt-4.1"}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	e.SetSubagentModelSaveFunc(func(string) error { return errors.New("read-only config") })

	e.cmdSubagent(p, &Message{SessionKey: "sess", ReplyCtx: "ctx"}, []string{"3"}, a)

	if got := a.GetSubagentModel(); got != "gpt-4.1" {
		t.Errorf("memory = %q, want unchanged gpt-4.1", got)
	}
	sent := p.getSent()
	if len(sent) != 1 || !strings.Contains(sent[0], "Failed to change model") {
		t.Errorf("sent = %v, want one model change failure message", sent)
	}
}

type workdirSubagentAgent struct {
	stubSubagentAgent
	dir string
}

func (a *workdirSubagentAgent) GetWorkDir() string { return a.dir }

func TestPersistWorkspaceSubagentOverride(t *testing.T) {
	e := NewEngine("test", &stubSubagentAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
	e.multiWorkspace = true
	e.projectState = NewProjectStateStore(filepath.Join(t.TempDir(), "state.json"))
	ws := &workdirSubagentAgent{dir: t.TempDir()}

	savedConfig := false
	e.SetSubagentModelSaveFunc(func(string) error { savedConfig = true; return nil })

	e.persistWorkspaceSubagentOverride("sess", ws, "gpt-4.1-mini")
	key := normalizeWorkspacePath(ws.dir)
	if got := e.projectState.WorkspaceSubagentOverride(key); got != "gpt-4.1-mini" {
		t.Errorf("workspace override = %q, want gpt-4.1-mini", got)
	}
	if savedConfig {
		t.Error("workspace override wrote the project config")
	}

	e.persistWorkspaceSubagentOverride("sess", ws, "")
	if got := e.projectState.WorkspaceSubagentOverride(key); got != "" {
		t.Errorf("workspace override after clear = %q, want empty", got)
	}
}
