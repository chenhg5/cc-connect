package feishu

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Progress panels are re-rendered on every card update. Rendering them
// expanded while the turn runs re-opens them on each refresh, which makes the
// text below the panels jump; they must stay collapsed in every state.
func TestProgressCards_PanelsStayCollapsed(t *testing.T) {
	exitCode := 0
	entries := []core.ProgressCardEntry{
		{Kind: core.ProgressEntryThinking, Text: "Inspecting event routing"},
		{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "pwd"},
		{Kind: core.ProgressEntryToolResult, Tool: "Bash", Text: "/tmp/project", ExitCode: &exitCode},
		{Kind: core.ProgressEntryInfo, Text: "Context compacted"},
	}
	steps := []core.ToolStep{
		{Kind: core.ToolStepKindThinking, Summary: "Inspecting event routing"},
		{Kind: core.ToolStepKindTool, Name: "Bash", Summary: "pwd"},
	}

	cases := []struct {
		name   string
		card   string
		panels int
	}{
		{"progress_running", buildPreviewCardJSON(core.BuildProgressCardPayloadV2(entries, false, "Codex", core.LangEnglish, core.ProgressCardStateRunning)), 3},
		{"progress_completed", buildPreviewCardJSON(core.BuildProgressCardPayloadV2(entries, false, "Codex", core.LangEnglish, core.ProgressCardStateCompleted)), 3},
		{"rich_streaming", buildRichCard(core.CardStatusWorking, "", steps, "answer", true, ""), 2},
		{"rich_streaming_placeholder", buildRichCard(core.CardStatusThinking, "", nil, "", true, ""), 1},
		{"rich_done", buildRichCard(core.CardStatusDone, "", steps, "answer", false, ""), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			panels := collectCardPanels(t, tc.card)
			if len(panels) != tc.panels {
				t.Fatalf("panel count = %d, want %d: %#v", len(panels), tc.panels, panels)
			}
			for _, panel := range panels {
				if expanded, _ := panel["expanded"].(bool); expanded {
					t.Fatalf("panel %q is expanded, want collapsed: %#v", cardPanelTitle(panel), panel)
				}
			}
		})
	}
}
