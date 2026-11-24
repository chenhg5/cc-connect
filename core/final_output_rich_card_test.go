package core

import "testing"

func TestFinalAnswerModeOnRichCardPlatformUsesOnlyAnswer(t *testing.T) {
	p := &stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := newFinalModeEngine(t, p)
	e.SetDisplayConfig(DisplayCfg{Mode: "final", CardMode: "rich", ToolMessages: true})
	session := runFinalModeTurn(t, e, p, "feishu:final-mode-rich-capable",
		Event{Type: EventText, Content: "Before tool. "},
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "echo ok"},
		Event{Type: EventToolResult, ToolName: "Bash", ToolResult: "ok"},
		Event{Type: EventText, Content: "After tool."},
		Event{Type: EventResult, Content: "After tool.", Done: true},
	)
	if starts, streams, updates, deletes := p.snapshot(); len(starts)+len(streams)+len(updates)+deletes != 0 {
		t.Fatalf("final mode touched rich card: starts=%q streams=%q updates=%q deletes=%d", starts, streams, updates, deletes)
	}
	if sent := p.getSent(); len(sent) != 1 || sent[0] != "After tool." {
		t.Fatalf("sent = %q; want one plain final answer", sent)
	}
	if history := session.GetHistory(0); len(history) == 0 || history[len(history)-1].Content != "After tool." {
		t.Fatalf("history did not record only the final answer: %#v", history)
	}
}
