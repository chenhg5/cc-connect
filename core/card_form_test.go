package core

import (
	"reflect"
	"strings"
	"testing"
)

func TestCardForm_ValidatesFieldsAndPreservesStructuredText(t *testing.T) {
	f := &CardForm{Language: LangEnglish, Fields: []CardFormField{
		{Name: "name", Label: "Name", Kind: "text", Required: true},
		{Name: "backend", Label: "Backend", Kind: "select", Required: true, Options: []CardSelectOption{{Value: "codex"}}, Defaults: []string{"codex"}},
		{Name: "members", Label: "Members", Kind: "multiselect", Options: []CardSelectOption{{Value: "dev"}}, Defaults: []string{"dev"}},
	}}
	text := "directory with spaces\n\"quote\" $(untouched)"
	got, err := f.ValidateValues(map[string]any{"name": text, "members": []any{}, "command": "/shell do-something"})
	if err != nil || got["name"][0] != text || got["backend"][0] != "codex" || len(got["members"]) != 0 || len(got) != 3 {
		t.Fatalf("structured values/defaults/whitelist: %#v %v", got, err)
	}
	for _, raw := range []map[string]any{
		{}, {"name": " "}, {"name": []string{"no"}}, {"name": strings.Repeat("a", 1001)},
		{"name": "ok", "backend": "unknown"}, {"name": "ok", "members": []any{12}}, {"name": "ok", "members": []any{"outsider"}},
	} {
		if _, err := f.ValidateValues(raw); err == nil {
			t.Fatalf("accepted malformed fields: %#v", raw)
		}
	}
	got, err = f.ValidateValues(map[string]any{"name": "ok", "members": []any{"dev", "dev"}})
	if err != nil || !reflect.DeepEqual(got["members"], []string{"dev"}) {
		t.Fatalf("deduplicate: %#v %v", got, err)
	}
}

func TestCardForm_DraftAllowsIncompleteFieldsButCreateStillRequiresThem(t *testing.T) {
	form := CardForm{Language: LangEnglish, Fields: []CardFormField{
		{Name: "id", Label: "ID", Kind: "text", Required: true},
		{Name: "folder", Label: "Directory", Kind: "select", Options: []CardSelectOption{{Value: "child"}}},
		{Name: "members", Label: "Members", Kind: "multiselect", Options: []CardSelectOption{{Value: "dev"}}, Defaults: []string{"dev"}},
		{Name: "current", Label: "Read only", Kind: "display"},
	}}
	raw := map[string]any{"id": "", "folder": "child", "members": []string{}, "current": "forged"}
	values, err := form.ValidateDraft(raw)
	if err != nil || len(values["id"]) != 0 || len(values["members"]) != 0 || values["folder"][0] != "child" {
		t.Fatalf("draft validation failed: %v %v", values, err)
	}
	if _, ok := values["current"]; ok {
		t.Fatal("read-only field was submitted")
	}
	if _, err := form.ValidateValues(raw); err == nil {
		t.Fatal("final submission allowed missing required field")
	}
	raw["folder"] = "unknown"
	if _, err := form.ValidateDraft(raw); err == nil {
		t.Fatal("draft accepted an undeclared folder")
	}
	form.Fields[1].Defaults = []string{"child"}
	raw["folder"] = nil
	values, err = form.ValidateDraft(raw)
	if err != nil || len(values["folder"]) != 0 {
		t.Fatalf("empty folder selection blocked browsing or restored default: %v %v", values, err)
	}
	raw["id"] = "shop"
	if _, err := form.ValidateValues(raw); err != nil {
		t.Fatalf("could not create in current directory without selecting a child: %v", err)
	}
}
