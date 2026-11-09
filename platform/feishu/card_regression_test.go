package feishu

import (
	"github.com/chenhg5/cc-connect/core"
	"strings"
	"testing"
)

func TestRenderCardMap_DiffBackticksDoNotCreateCodeFences(t *testing.T) {
	card := core.NewCard().RichMarkdown().Diff("update README.md\n@@ -1 +1 @@\n-```sh\n+```bash").Markdown("AFTER DIFF").Build()
	elements := renderCardMap(card, "chat")["body"].(map[string]any)["elements"].([]map[string]any)
	content := elements[0]["content"].(string)
	if strings.Contains(content, "```") || strings.Count(content, "\n") != 3 {
		t.Fatalf("diff fences corrupted: %q", content)
	}
	if !strings.Contains(content, "bash") || elements[1]["content"] != "AFTER DIFF" {
		t.Fatalf("diff content lost: %#v", elements)
	}
}

func TestRenderCardMap_RichEmptyCardHasBody(t *testing.T) {
	for _, card := range []*core.Card{core.NewCard().RichMarkdown().Build(), {RichMarkdown: true, CollapsedDetailsTitle: "Details"}} {
		elements := renderCardMap(card, "chat")["body"].(map[string]any)["elements"].([]map[string]any)
		if len(elements) == 0 {
			t.Fatal("empty body")
		}
		if card.CollapsedDetailsTitle != "" {
			elements = elements[0]["elements"].([]map[string]any)
		}
		if len(elements) == 0 {
			t.Fatal("empty collapsed panel")
		}
	}
}

func TestRenderCardMap_RichSelectPreservesOptionsAndCallback(t *testing.T) {
	card := core.NewCard().RichMarkdown().Select("Choose", []core.CardSelectOption{{Text: "One", Value: "cmd:/one"}}, "cmd:/one").Build()
	elements := renderCardMap(card, "chat")["body"].(map[string]any)["elements"].([]map[string]any)
	if len(elements) != 1 {
		t.Fatalf("selector missing: %#v", elements)
	}
	selectElement := elements[0]
	if selectElement["tag"] != "select_static" || selectElement["initial_option"] != "cmd:/one" || selectElement["value"].(map[string]string)["session_key"] != "chat" {
		t.Fatalf("selector broken: %#v", selectElement)
	}
	options := selectElement["options"].([]map[string]any)
	if len(options) != 1 || options[0]["value"] != "cmd:/one" {
		t.Fatalf("options broken: %#v", options)
	}
}
