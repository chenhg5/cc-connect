package feishu

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestRenderCardMap_PatchDiffUsesColoredFileSections(t *testing.T) {
	preview := "update /project/core/card.go\n@@ -1 +1 @@\n-old\n+new\n\nadd /project/core/new.go\n@@ -0,0 +1 @@\n+package core"
	card := core.NewCard().RichMarkdown().Title("Permission Request", "orange").
		Markdown("**Patch**").Diff(preview).Markdown("Reason: Review file changes").
		Buttons(core.PrimaryBtn("Allow", "cmd:/decision token allow")).Build()
	got := renderCardMap(card, "feishu:thread")
	if got["schema"] != "2.0" {
		t.Fatalf("schema = %v, want 2.0", got["schema"])
	}
	elements := got["body"].(map[string]any)["elements"].([]map[string]any)
	if len(elements) != 4 {
		t.Fatalf("elements = %#v, want heading, diff, reason, button", elements)
	}
	diff := elements[1]["content"].(string)
	for _, want := range []string{
		"📄 `/project/core/card.go`", "📄 `/project/core/new.go`",
		"<text_tag color='blue'>update</text_tag>",
		"<text_tag color='green'>+</text_tag> `new`",
		"<text_tag color='red'>−</text_tag> `old`",
		"<text_tag color='blue'>@@</text_tag>",
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("rendered diff missing %q: %s", want, diff)
		}
	}
	if strings.Contains(diff, "```diff") {
		t.Fatalf("diff was left as one raw code block: %s", diff)
	}
	button := elements[3]["columns"].([]map[string]any)[0]["elements"].([]map[string]any)[0]
	value := button["value"].(map[string]string)
	if value["action"] != "cmd:/decision token allow" || value["session_key"] != "feishu:thread" {
		t.Fatalf("patch button lost callback: %#v", button)
	}
}

func TestRenderApprovalDiff_UnknownPreviewFallsBackToCodeBlock(t *testing.T) {
	preview := "```diff\nreason only\n```"
	if got := renderApprovalDiff(preview); got != preview {
		t.Fatalf("unknown preview = %q, want original code block", got)
	}
}
