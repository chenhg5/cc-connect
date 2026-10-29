package feishu

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// richPanelExpandedByLane maps each rich card panel ("Reasoning" / "Tools") to its expanded flag.
func richPanelExpandedByLane(t *testing.T, cardJSON string) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	for _, panel := range collectCardPanels(t, cardJSON) {
		header, _ := panel["header"].(map[string]any)
		title, _ := header["title"].(map[string]any)
		content, _ := title["content"].(string)
		expanded, ok := panel["expanded"].(bool)
		if !ok {
			t.Fatalf("panel %q has no boolean expanded field: %#v", content, panel)
		}
		got[strings.Fields(content)[0]] = expanded
	}
	return got
}

var richPanelTestSteps = []core.ToolStep{
	{Kind: core.ToolStepKindThinking, Name: "Thinking", Summary: "Inspecting event routing"},
	{Kind: core.ToolStepKindTool, Name: "Bash", Summary: "ls"},
}

func TestBuildRichCard_PanelExpandedFollowsMode(t *testing.T) {
	tests := []struct {
		name          string
		panels        richPanelModes
		streaming     bool
		wantReasoning bool
		wantTools     bool
	}{
		{"zero value streaming", richPanelModes{}, true, true, true},
		{"zero value done", richPanelModes{}, false, false, false},
		{"auto streaming", richPanelModes{reasoning: richPanelAuto, tools: richPanelAuto}, true, true, true},
		{"auto done", richPanelModes{reasoning: richPanelAuto, tools: richPanelAuto}, false, false, false},
		{"collapsed streaming", richPanelModes{reasoning: richPanelCollapsed, tools: richPanelCollapsed}, true, false, false},
		{"collapsed done", richPanelModes{reasoning: richPanelCollapsed, tools: richPanelCollapsed}, false, false, false},
		{"expanded streaming", richPanelModes{reasoning: richPanelExpanded, tools: richPanelExpanded}, true, true, true},
		{"expanded done", richPanelModes{reasoning: richPanelExpanded, tools: richPanelExpanded}, false, true, true},
		{"tools collapsed only", richPanelModes{reasoning: richPanelAuto, tools: richPanelCollapsed}, true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := core.CardStatusWorking
			if !tt.streaming {
				status = core.CardStatusDone
			}
			got := richPanelExpandedByLane(t, buildRichCard(status, "", richPanelTestSteps, "answer", tt.streaming, "", tt.panels))
			want := map[string]bool{"Reasoning": tt.wantReasoning, "Tools": tt.wantTools}
			if len(got) != len(want) || got["Reasoning"] != want["Reasoning"] || got["Tools"] != want["Tools"] {
				t.Fatalf("panel expanded = %v, want %v", got, want)
			}
		})
	}
}

func TestBuildRichCard_PlaceholderPanelFollowsReasoningMode(t *testing.T) {
	for mode, want := range map[richPanelMode]bool{
		richPanelAuto:      true,
		richPanelCollapsed: false,
		richPanelExpanded:  true,
	} {
		got := richPanelExpandedByLane(t, buildRichCard(core.CardStatusThinking, "", nil, "", true, "", richPanelModes{reasoning: mode}))
		if expanded, ok := got["Reasoning"]; !ok || expanded != want || len(got) != 1 {
			t.Fatalf("mode %q: placeholder panels = %v, want only Reasoning with expanded=%v", mode, got, want)
		}
	}
}

func TestNew_RichPanelOptions(t *testing.T) {
	build := func(t *testing.T, opts map[string]any, streaming bool) map[string]bool {
		t.Helper()
		p, err := New(opts)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		rc, ok := p.(core.RichCardSupporter)
		if !ok {
			t.Fatalf("platform type %T does not implement RichCardSupporter", p)
		}
		return richPanelExpandedByLane(t, rc.BuildRichCard(core.CardStatusWorking, "", richPanelTestSteps, "answer", streaming, ""))
	}

	defaults := build(t, map[string]any{"app_id": "cli_xxx", "app_secret": "secret"}, true)
	if !defaults["Reasoning"] || !defaults["Tools"] {
		t.Fatalf("default panels while streaming = %v, want both expanded", defaults)
	}

	got := build(t, map[string]any{
		"app_id":               "cli_xxx",
		"app_secret":           "secret",
		"rich_reasoning_panel": " Collapsed ",
		"rich_tools_panel":     "expanded",
	}, false)
	if got["Reasoning"] || !got["Tools"] {
		t.Fatalf("configured panels after the turn = %v, want Reasoning collapsed and Tools expanded", got)
	}
}

func TestNew_RichPanelOptionRejectsInvalidValue(t *testing.T) {
	_, err := New(map[string]any{
		"app_id":           "cli_xxx",
		"app_secret":       "secret",
		"rich_tools_panel": "hidden",
	})
	if err == nil {
		t.Fatal("expected error for invalid rich_tools_panel")
	}
	if !strings.Contains(err.Error(), "invalid rich_tools_panel") {
		t.Fatalf("error = %q, want invalid rich_tools_panel", err.Error())
	}
}
