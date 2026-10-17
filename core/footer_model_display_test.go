package core

import "testing"

type footerModelSession struct {
	stubAgentSession
	model, display string
}

func (s *footerModelSession) GetModel() string            { return s.model }
func (s *footerModelSession) GetModelDisplayName() string { return s.display }

// TestReplyFooterModel_PrefersDisplayName: agents whose model ids are not
// human-friendly (Devin: "claude-opus-5-5-medium" for "Claude Opus 5.5")
// show the display name in the reply footer.
func TestReplyFooterModel_PrefersDisplayName(t *testing.T) {
	s := &footerModelSession{model: "claude-opus-5-5-medium", display: "Claude Opus 5.5"}
	if got := replyFooterModel(s, nil); got != "Claude Opus 5.5" {
		t.Fatalf("replyFooterModel = %q, want display name", got)
	}
	s.display = ""
	if got := replyFooterModel(s, nil); got != "claude-opus-5-5-medium" {
		t.Fatalf("replyFooterModel = %q, want raw id when no display name", got)
	}
}
