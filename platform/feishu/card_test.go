package feishu

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func decodeRenderedCard(t *testing.T, card *core.Card) map[string]any {
	t.Helper()

	var got map[string]any
	if err := json.Unmarshal([]byte(renderCard(card, "")), &got); err != nil {
		t.Fatalf("renderCard JSON decode failed: %v", err)
	}
	return got
}

func TestRenderCardMap_EqualColumnsActionsUseColumnSet(t *testing.T) {
	buttons := []core.CardButton{
		core.PrimaryBtn("Session Management", "nav:/help session"),
		core.DefaultBtn("Agent Configuration", "nav:/help agent"),
		core.DefaultBtn("Tools & Automation", "nav:/help tools"),
		core.DefaultBtn("System", "nav:/help system"),
	}
	card := core.NewCard().ButtonsEqual(buttons...).Build()
	got := decodeRenderedCard(t, card)

	elements, ok := got["elements"].([]any)
	if !ok || len(elements) != 1 {
		t.Fatalf("elements = %#v, want one element", got["elements"])
	}
	columnSet, ok := elements[0].(map[string]any)
	if !ok {
		t.Fatalf("first element = %#v, want object", elements[0])
	}
	if tag := columnSet["tag"]; tag != "column_set" {
		t.Fatalf("tag = %#v, want column_set", tag)
	}
	columns, ok := columnSet["columns"].([]any)
	if !ok || len(columns) != len(buttons) {
		t.Fatalf("columns = %#v, want %d columns", columnSet["columns"], len(buttons))
	}

	for i, want := range buttons {
		col, ok := columns[i].(map[string]any)
		if !ok {
			t.Fatalf("column %d = %#v, want object", i, columns[i])
		}
		if width := col["width"]; width != "weighted" {
			t.Fatalf("column %d width = %#v, want weighted", i, width)
		}
		if weight := col["weight"]; weight != float64(1) {
			t.Fatalf("column %d weight = %#v, want 1", i, weight)
		}
		innerElems, ok := col["elements"].([]any)
		if !ok || len(innerElems) != 1 {
			t.Fatalf("column %d elements = %#v, want one button", i, col["elements"])
		}
		btn, ok := innerElems[0].(map[string]any)
		if !ok {
			t.Fatalf("column %d button = %#v, want object", i, innerElems[0])
		}
		if tag := btn["tag"]; tag != "button" {
			t.Fatalf("column %d tag = %#v, want button", i, tag)
		}
		text, ok := btn["text"].(map[string]any)
		if !ok || text["content"] != want.Text {
			t.Fatalf("column %d text = %#v, want %q", i, btn["text"], want.Text)
		}
		if btnType := btn["type"]; btnType != want.Type {
			t.Fatalf("column %d type = %#v, want %q", i, btnType, want.Type)
		}
		value, ok := btn["value"].(map[string]any)
		if !ok || value["action"] != want.Value {
			t.Fatalf("column %d value = %#v, want %q", i, btn["value"], want.Value)
		}
	}
}

func TestRenderCardMap_TwoEqualColumnsUseBisectAndCenteredButtons(t *testing.T) {
	buttons := []core.CardButton{
		core.PrimaryBtn("Session Management", "nav:/help session"),
		core.DefaultBtn("Agent Configuration", "nav:/help agent"),
	}
	card := core.NewCard().ButtonsEqual(buttons...).Build()
	got := decodeRenderedCard(t, card)

	elements, ok := got["elements"].([]any)
	if !ok || len(elements) != 1 {
		t.Fatalf("elements = %#v, want one element", got["elements"])
	}
	columnSet, ok := elements[0].(map[string]any)
	if !ok {
		t.Fatalf("first element = %#v, want object", elements[0])
	}
	if flexMode := columnSet["flex_mode"]; flexMode != "bisect" {
		t.Fatalf("flex_mode = %#v, want bisect", flexMode)
	}
	columns, ok := columnSet["columns"].([]any)
	if !ok || len(columns) != len(buttons) {
		t.Fatalf("columns = %#v, want %d columns", columnSet["columns"], len(buttons))
	}
	for i := range buttons {
		col, ok := columns[i].(map[string]any)
		if !ok {
			t.Fatalf("column %d = %#v, want object", i, columns[i])
		}
		if align := col["horizontal_align"]; align != "center" {
			t.Fatalf("column %d horizontal_align = %#v, want center", i, align)
		}
		innerElems, ok := col["elements"].([]any)
		if !ok || len(innerElems) != 1 {
			t.Fatalf("column %d elements = %#v, want one button", i, col["elements"])
		}
		btn, ok := innerElems[0].(map[string]any)
		if !ok {
			t.Fatalf("column %d button = %#v, want object", i, innerElems[0])
		}
		if width := btn["width"]; width != "fill" {
			t.Fatalf("column %d button width = %#v, want fill", i, width)
		}
	}
}

func TestRenderCardMap_DefaultActionsStayActionRow(t *testing.T) {
	buttons := []core.CardButton{
		core.PrimaryBtn("Yes", "act:/yes"),
		core.DefaultBtn("No", "act:/no"),
	}
	card := core.NewCard().Buttons(buttons...).Build()
	got := decodeRenderedCard(t, card)

	elements, ok := got["elements"].([]any)
	if !ok || len(elements) != 1 {
		t.Fatalf("elements = %#v, want one element", got["elements"])
	}
	actionRow, ok := elements[0].(map[string]any)
	if !ok {
		t.Fatalf("first element = %#v, want object", elements[0])
	}
	if tag := actionRow["tag"]; tag != "action" {
		t.Fatalf("tag = %#v, want action", tag)
	}
	actions, ok := actionRow["actions"].([]any)
	if !ok || len(actions) != len(buttons) {
		t.Fatalf("actions = %#v, want %d buttons", actionRow["actions"], len(buttons))
	}
	for i, want := range buttons {
		btn, ok := actions[i].(map[string]any)
		if !ok {
			t.Fatalf("button %d = %#v, want object", i, actions[i])
		}
		if tag := btn["tag"]; tag != "button" {
			t.Fatalf("button %d tag = %#v, want button", i, tag)
		}
		text, ok := btn["text"].(map[string]any)
		if !ok || text["content"] != want.Text {
			t.Fatalf("button %d text = %#v, want %q", i, btn["text"], want.Text)
		}
		if btnType := btn["type"]; btnType != want.Type {
			t.Fatalf("button %d type = %#v, want %q", i, btnType, want.Type)
		}
		value, ok := btn["value"].(map[string]any)
		if !ok || value["action"] != want.Value {
			t.Fatalf("button %d value = %#v, want %q", i, btn["value"], want.Value)
		}
	}
}

func TestRenderCardMap_DeleteModeUsesCheckerForm(t *testing.T) {
	card := core.NewCard().
		Title("删除会话", "carmine").
		ListItemBtn("☑ **1.** One · **10** msgs · 03-13 20:00", "已选择", "primary", "act:/delete-mode toggle session-1").
		ListItemBtn("▶ **2.** Active · **30** msgs · 03-13 20:01", "当前会话", "primary", "act:/delete-mode noop session-2").
		ListItemBtn("◻ **3.** Three · **20** msgs · 03-13 20:02", "选择", "default", "act:/delete-mode toggle session-3").
		Note("2 selected").
		Buttons(
			core.DangerBtn("删除已选", "act:/delete-mode confirm"),
			core.DefaultBtn("取消", "act:/delete-mode cancel"),
		).
		Buttons(core.DefaultBtn("下一页 →", "act:/delete-mode page 2")).
		Build()

	got := decodeRenderedCard(t, card)
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal rendered card failed: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, `"tag":"form"`) || !strings.Contains(s, `"tag":"checker"`) {
		t.Fatalf("expected form+checker rendering, got %s", s)
	}
	if got := strings.Count(s, `"tag":"checker"`); got != 2 {
		t.Fatalf("checker count = %d, want 2, got %s", got, s)
	}
	if !strings.Contains(s, deleteModeCheckerName("session-1")) {
		t.Fatalf("selectable session checker missing, got %s", s)
	}
	if strings.Contains(s, deleteModeCheckerName("session-2")) {
		t.Fatalf("active session should not render checker, got %s", s)
	}
	if !strings.Contains(s, deleteModeCheckerName("session-3")) {
		t.Fatalf("second selectable session checker missing, got %s", s)
	}
	activeIdx := strings.Index(s, `▶ **2.** Active`)
	firstIdx := strings.Index(s, deleteModeCheckerName("session-1"))
	thirdIdx := strings.Index(s, deleteModeCheckerName("session-3"))
	if activeIdx < 0 || firstIdx < 0 || thirdIdx < 0 {
		t.Fatalf("missing expected order markers in rendered card: %s", s)
	}
	if !(firstIdx < activeIdx && activeIdx < thirdIdx) {
		t.Fatalf("row order changed unexpectedly, got %s", s)
	}
	if !strings.Contains(s, `"name":"delete_mode_form"`) {
		t.Fatalf("expected form name for feishu validation, got %s", s)
	}
	if !strings.Contains(s, `"name":"delete_mode_submit"`) || !strings.Contains(s, `"name":"delete_mode_cancel"`) {
		t.Fatalf("expected button names inside form, got %s", s)
	}
	if !strings.Contains(s, `"form_action_type":"submit"`) || !strings.Contains(s, `act:/delete-mode form-submit`) {
		t.Fatalf("expected form submit action, got %s", s)
	}
	if strings.Contains(s, `act:/delete-mode toggle`) {
		t.Fatalf("expected no toggle buttons in rendered card, got %s", s)
	}
}

func TestRenderCardMap_InjectsSessionKeyIntoCallbacks(t *testing.T) {
	card := core.NewCard().
		Buttons(core.PrimaryBtn("Open", "nav:/help session")).
		ListItem("Choose", "Confirm", "act:/confirm").
		Select("Pick one", []core.CardSelectOption{{Text: "A", Value: "askq:0:1"}}, "").
		Build()

	got := renderCardMap(card, "feishu:oc_chat:root:om_root")
	elements, ok := got["elements"].([]map[string]any)
	if !ok || len(elements) != 3 {
		t.Fatalf("elements = %#v, want 3 elements", got["elements"])
	}

	actionRow := elements[0]
	actions := actionRow["actions"].([]map[string]any)
	firstButton := actions[0]
	value := firstButton["value"].(map[string]string)
	if value["session_key"] != "feishu:oc_chat:root:om_root" {
		t.Fatalf("button session_key = %#v, want thread session key", value["session_key"])
	}

	listRow := elements[1]
	columns := listRow["columns"].([]map[string]any)
	actionCol := columns[1]
	listBtn := actionCol["elements"].([]map[string]any)[0]
	listValue := listBtn["value"].(map[string]string)
	if listValue["session_key"] != "feishu:oc_chat:root:om_root" {
		t.Fatalf("list item session_key = %#v, want thread session key", listValue["session_key"])
	}

	selectRow := elements[2]
	selectActions := selectRow["actions"].([]map[string]any)
	selectValue := selectActions[0]["value"].(map[string]string)
	if selectValue["session_key"] != "feishu:oc_chat:root:om_root" {
		t.Fatalf("select session_key = %#v, want thread session key", selectValue["session_key"])
	}
}
