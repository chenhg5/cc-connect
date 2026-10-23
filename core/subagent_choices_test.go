package core

import (
	"fmt"
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
			if b.Data == "cmd:/model subagent switch 2" {
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
