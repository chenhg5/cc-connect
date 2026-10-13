package feishu

import (
	"encoding/json"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Check the external schema, including removed 1.0 components nested in
// columns/forms; a top-level schema flag alone does not make a valid 2.0 card.
func assertPanelSchema2(t *testing.T, data any) map[string]any {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var card map[string]any
	if err := json.Unmarshal(b, &card); err != nil {
		t.Fatal(err)
	}
	if card["schema"] != "2.0" || card["body"] == nil || card["elements"] != nil {
		t.Fatalf("not a schema 2.0 card: %s", b)
	}
	config := card["config"].(map[string]any)
	if config["wide_screen_mode"] != nil || config["update_multi"] != true {
		t.Fatalf("legacy card configuration: %#v", config)
	}
	walkPanel(card["body"], func(node map[string]any) {
		switch node["tag"] {
		case "action", "note":
			t.Fatalf("unsupported 1.0 component: %#v", node)
		case "button", "select_static":
			if node["value"] != nil {
				t.Fatalf("legacy callback instead of behaviors: %#v", node)
			}
		}
	})
	return card
}

func walkPanel(value any, visit func(map[string]any)) {
	switch v := value.(type) {
	case map[string]any:
		visit(v)
		for _, child := range v {
			walkPanel(child, visit)
		}
	case []any:
		for _, child := range v {
			walkPanel(child, visit)
		}
	}
}

func TestCardPanel_Schema2PreservesControlsAndCallbackRouting(t *testing.T) {
	button := core.PrimaryBtn("Open", "cmd:/team members")
	button.Extra = map[string]string{"request_id": "request-1"}
	card := core.NewCard().Title("Menu", "blue").Markdown("Content").Divider().
		Buttons(button).ButtonsEqual(core.DefaultBtn("Back", "nav:/help"), core.DefaultBtn("Next", "nav:/help team")).
		ListItem("Member", "Edit", "cmd:/team members dev").
		Select("Pick", []core.CardSelectOption{{Text: "A", Value: "act:/pick a"}}, "act:/pick a").
		Note("Note").Build()
	card.Reusable = true
	const session = "feishu:oc_private:owner"
	got := assertPanelSchema2(t, renderCardMap(card, session))
	counts := make(map[string]int)
	callbackCount := 0
	walkPanel(got["body"], func(node map[string]any) {
		tag, _ := node["tag"].(string)
		counts[tag]++
		if tag == "select_static" && node["initial_option"] != "act:/pick a" {
			t.Fatal("selected value lost")
		}
		if node["type"] == "callback" {
			callbackCount++
			value := node["value"].(map[string]any)
			if value["session_key"] != session || value["reuse_card"] != "true" {
				t.Fatalf("lost callback routing: %#v", value)
			}
			if value["action"] == "cmd:/team members" && value["request_id"] != "request-1" {
				t.Fatal("extra callback data lost")
			}
		}
	})
	if counts["button"] != 4 || counts["select_static"] != 1 || counts["markdown"] != 3 || callbackCount != 5 {
		t.Fatalf("controls/content lost: %#v; callbacks=%d", counts, callbackCount)
	}
}

func TestCardPanel_Schema2DeleteFormPreservesSubmitAndCancel(t *testing.T) {
	card := core.NewCard().ListItem("☑ Old session", "Selected", "act:/delete-mode toggle one").
		Note("Choose sessions").Buttons(core.DangerBtn("Delete", "act:/delete-mode confirm"), core.DefaultBtn("Cancel", "act:/delete-mode cancel")).
		Buttons(core.DefaultBtn("Next", "act:/delete-mode page 2")).Build()
	card.Reusable = true
	got := assertPanelSchema2(t, renderCardMap(card, "feishu:oc_private:owner"))
	counts := make(map[string]int)
	var submit, cancel bool
	walkPanel(got["body"], func(node map[string]any) {
		tag, _ := node["tag"].(string)
		counts[tag]++
		if node["name"] == "delete_mode_submit" && node["form_action_type"] == "submit" {
			submit = true
		}
		if node["type"] == "callback" && node["value"].(map[string]any)["action"] == "act:/delete-mode cancel" {
			cancel = true
		}
		if tag == "form" {
			walkPanel(node["elements"], func(child map[string]any) {
				if child["tag"] == "button" && child["form_action_type"] != "submit" {
					t.Fatal("navigation button must be outside schema 2.0 form")
				}
			})
		}
	})
	if counts["form"] != 1 || counts["checker"] != 1 || !submit || !cancel {
		t.Fatalf("delete form lost controls: %#v submit=%v cancel=%v", counts, submit, cancel)
	}
}
