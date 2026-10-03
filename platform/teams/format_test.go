package teams

import (
	"strings"
	"testing"
)

func TestFormattingInstructions_NonEmptyTeamsGuidance(t *testing.T) {
	p := &Platform{}
	got := p.FormattingInstructions()
	if got == "" {
		t.Fatal("FormattingInstructions must be non-empty")
	}
	if !strings.Contains(got, "Teams") {
		t.Error("instructions should name the Teams platform")
	}
	// Teams uses standard Markdown bold (**), unlike Slack's single-asterisk mrkdwn.
	if !strings.Contains(got, "**text**") {
		t.Error("instructions should specify standard Markdown bold")
	}
}
