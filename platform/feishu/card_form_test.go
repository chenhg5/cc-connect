package feishu

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func formFixture(t *testing.T) (*interactivePlatform, *core.Message, core.CardForm) {
	t.Helper()
	p, err := New(map[string]any{"app_id": "cli_test", "app_secret": "test", "enable_feishu_card": true})
	if err != nil {
		t.Fatal(err)
	}
	ip := p.(*interactivePlatform)
	msg := &core.Message{UserID: "owner", SessionKey: "feishu:oc_private:owner", Content: "/team bot create",
		ReplyCtx: replyContext{chatID: "oc_private", sessionKey: "feishu:oc_private:owner", chatType: "p2p", senderType: "user"}}
	form := core.CardForm{Command: "/team bot create", Language: core.LangEnglish, SubmitText: "Create", Fields: []core.CardFormField{
		{Name: "name", Label: "Bot ID", Kind: "text", Required: true},
		{Name: "backend", Label: "Backend", Kind: "select", Required: true, Options: []core.CardSelectOption{{Text: "Codex", Value: "codex"}}, Defaults: []string{"codex"}},
	}}
	return ip, msg, form
}

func formEvent(id, user, chat string, values map[string]any) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: user}, Context: &callback.Context{OpenChatID: chat, OpenMessageID: "om_card"},
		Action: &callback.CallBackAction{Name: cardFormSubmitPrefix + id, FormValue: values,
			Value: map[string]any{"session_key": "forged-other-chat", "action": "cmd:/shell injected"}},
	}}
}

func TestCardForm_CallbackScopeValidationAndSingleDispatch(t *testing.T) {
	ip, msg, form := formFixture(t)
	id, err := ip.rememberForm(msg, form)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan *core.Message, 10)
	ip.handler = func(_ core.Platform, m *core.Message) { received <- m }
	for _, event := range []*callback.CardActionTriggerEvent{
		formEvent(id, "stranger", "oc_private", map[string]any{"name": "dev"}),
		formEvent(id, "owner", "oc_other", map[string]any{"name": "dev"}),
		formEvent("unknown", "owner", "oc_private", map[string]any{"name": "dev"}),
		formEvent(id, "owner", "oc_private", map[string]any{"name": ""}),
		formEvent(id, "owner", "oc_private", map[string]any{"name": "dev", "backend": "unknown"}),
	} {
		response, err := ip.onCardAction(event)
		if err != nil || response == nil || response.Toast.Type != "error" {
			t.Fatalf("invalid callback: %#v %v", response, err)
		}
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = ip.onCardAction(formEvent(id, "owner", "oc_private", map[string]any{"name": "dev"}))
		}()
	}
	wg.Wait()
	select {
	case got := <-received:
		if got.SessionKey != msg.SessionKey || got.Content != "/team bot create" || got.FormValues["name"][0] != "dev" || !ip.IsPrivateUserMessage(got) {
			t.Fatalf("callback lost trusted context or allowed command injection: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no submitted command")
	}
	select {
	case extra := <-received:
		t.Fatalf("duplicate dispatch: %#v", extra)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestCardForm_CallbackExpiryRestartAndAllowlists(t *testing.T) {
	ip, msg, form := formFixture(t)
	id, _ := ip.rememberForm(msg, form)
	ip.forms[id].expires = time.Now().Add(-time.Second)
	response, _ := ip.onCardAction(formEvent(id, "owner", "oc_private", nil))
	if response == nil || response.Toast.Type != "error" {
		t.Fatal("expired form accepted")
	}
	fresh, _, _ := formFixture(t)
	response, _ = fresh.onCardAction(formEvent(id, "owner", "oc_private", nil))
	if response == nil || response.Toast.Type != "error" {
		t.Fatal("form survived without its server association")
	}
	id, _ = ip.rememberForm(msg, form)
	ip.allowFrom = "someone-else"
	response, _ = ip.onCardAction(formEvent(id, "owner", "oc_private", map[string]any{"name": "dev"}))
	if response != nil || ip.forms[id].used {
		t.Fatal("form bypassed allow_from")
	}
	ip.allowFrom, ip.allowChat = "owner", "oc_other"
	response, _ = ip.onCardAction(formEvent(id, "owner", "oc_private", map[string]any{"name": "dev"}))
	if response != nil || ip.forms[id].used {
		t.Fatal("form bypassed allow_chat")
	}
}

func TestCardForm_HelpButtonUsesOnlyVerifiedPrivateContext(t *testing.T) {
	ip, msg, _ := formFixture(t)
	ip.rememberCardChatType(msg.ReplyCtx.(replyContext))
	ip.userNameCache.Store("owner", "Owner")
	ip.chatNameCache.Store("oc_private", "Private")
	ip.userNameCache.Store("owner", "Owner")
	ip.chatNameCache.Store("oc_group", "Group")
	ip.rememberCardChatType(replyContext{chatID: "oc_group", chatType: "group", senderType: "user"})
	received := make(chan *core.Message, 2)
	ip.handler = func(_ core.Platform, m *core.Message) { received <- m }
	for _, chat := range []string{"oc_private", "oc_group"} {
		event := formEvent("", "owner", chat, nil)
		event.Event.Action.Name = ""
		event.Event.Action.Value = map[string]any{"action": "cmd:/team bot create", "chat_type": "p2p"}
		_, _ = ip.onCardAction(event)
		select {
		case got := <-received:
			if ip.IsPrivateUserMessage(got) != (chat == "oc_private") {
				t.Fatal("callback inferred private chat from untrusted values")
			}
		case <-time.After(time.Second):
			t.Fatal("Help command not dispatched")
		}
	}
}

func TestCardForm_RendersSchema2WithBatchedFields(t *testing.T) {
	_, _, form := formFixture(t)
	form.ID = "opaque"
	form.Fields = append(form.Fields, core.CardFormField{Name: "members", Label: "Members", Kind: "multiselect", Options: []core.CardSelectOption{{Text: "Dev", Value: "dev"}}, Defaults: []string{"dev"}},
		core.CardFormField{Name: "goal", Label: "Goal", Kind: "multiline", Defaults: []string{"line1\nline2"}})
	card := core.NewCard().Title("Create", "wathet").Markdown("Fill in the fields").Build()
	card.Form = &form
	m := renderCardMap(card, "not-exposed")
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(data)
	for _, want := range []string{`"schema":"2.0"`, `"tag":"form"`, `"tag":"input"`, `"tag":"multi_select_static"`, `"initial_option":"codex"`, `"selected_values":["dev"]`, `"input_type":"multiline_text"`, `"name":"cc_form_submit_opaque"`, `"form_action_type":"submit"`} {
		if !strings.Contains(jsonText, want) {
			t.Fatalf("missing %s: %s", want, jsonText)
		}
	}
	for _, absent := range []string{"not-exposed", "behaviors", "cmd:/", "bridge_token"} {
		if strings.Contains(jsonText, absent) {
			t.Fatalf("unexpected %q: %s", absent, jsonText)
		}
	}
	if _, changed := renderCardMap(core.NewCard().Markdown("native").Build(), "key")["schema"]; changed {
		t.Fatal("native card renderer changed")
	}
}

func TestCardForm_DraftActionPreservesFieldsAndChecksOwner(t *testing.T) {
	ip, msg, form := formFixture(t)
	command := "/project _enter encoded-directory"
	form.Fields = append(form.Fields, core.CardFormField{Name: "folder", Label: "Directory", Kind: "select", Options: []core.CardSelectOption{{Text: "Folder", Value: "child"}}, Actions: []core.CardFormAction{{ID: "enter", Text: "Enter", Command: command}}}, core.CardFormField{Name: "goal", Label: "Goal", Kind: "multiline"})
	id, err := ip.rememberForm(msg, form)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan *core.Message, 8)
	ip.handler = func(_ core.Platform, m *core.Message) { received <- m }
	draft := func(user, chat, button, folder string) *callback.CardActionTriggerEvent {
		event := formEvent(id, user, chat, map[string]any{"name": "", "folder": folder, "goal": "Unfinished \"project\"\nSecond line"})
		event.Event.Action.Name = cardFormDraftPrefix + id + "_" + button
		return event
	}
	for _, event := range []*callback.CardActionTriggerEvent{
		draft("stranger", "oc_private", "enter", "child"), draft("owner", "wrong-chat", "enter", "child"),
		draft("owner", "oc_private", "forged", "child"), draft("owner", "oc_private", "enter", "unknown"),
		formEvent(id, "owner", "oc_private", map[string]any{"name": "", "folder": "child"}),
	} {
		response, err := ip.onCardAction(event)
		if err != nil || response == nil || response.Toast.Type != "error" {
			t.Fatalf("invalid callback accepted: %v %v", response, err)
		}
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = ip.onCardAction(draft("owner", "oc_private", "enter", "child")) }()
	}
	wg.Wait()
	select {
	case got := <-received:
		if got.Content != command || got.SessionKey != msg.SessionKey || len(got.FormValues["name"]) != 0 || got.FormValues["folder"][0] != "child" || got.FormValues["goal"][0] != "Unfinished \"project\"\nSecond line" {
			t.Fatalf("draft lost its fields/context: %+v", got)
		}
		rc := got.ReplyCtx.(replyContext)
		if rc.cardUpdate == nil || rc.cardUpdate.messageID != "om_card" {
			t.Fatal("draft did not reuse the original card")
		}
	case <-time.After(time.Second):
		t.Fatal("draft was blocked by an empty required field")
	}
	select {
	case <-received:
		t.Fatal("double click dispatched twice")
	default:
	}
}

func TestCardForm_DraftRendererKeepsDirectoryButtonsInsideCompleteForm(t *testing.T) {
	_, _, form := formFixture(t)
	form.ID = "opaque"
	form.Fields = append(form.Fields, core.CardFormField{Name: "folder", Kind: "select", Label: "Current directory", Options: []core.CardSelectOption{{Text: "Folder", Value: "child"}}, Actions: []core.CardFormAction{
		{ID: "enter", Text: "Enter", Command: "/project _enter current"}, {ID: "up", Text: "Up", Command: "/project _browse parent"},
	}})
	card := core.NewCard().Build()
	card.Form = &form
	rendered := renderFormCard(card, "")
	body := rendered["body"].(map[string]any)["elements"].([]map[string]any)
	if len(body) != 1 || body[0]["tag"] != "form" {
		t.Fatal("directory picker must be inside the complete form")
	}
	names := map[string]bool{}
	var inspect func(any)
	inspect = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if name, ok := v["name"].(string); ok {
				if names[name] {
					t.Fatalf("duplicate name %s", name)
				}
				names[name] = true
			}
			if v["tag"] == "input" && v["required"] == true {
				t.Fatal("client validation prevents browsing an unfinished form")
			}
			if v["tag"] == "button" && v["form_action_type"] != "submit" {
				t.Fatal("navigation will not collect the unsaved fields")
			}
			if _, ok := v["behaviors"]; ok {
				t.Fatal("standalone callbacks cannot preserve form values")
			}
			for _, child := range v {
				inspect(child)
			}
		case []map[string]any:
			for _, child := range v {
				inspect(child)
			}
		}
	}
	inspect(rendered)
	for _, name := range []string{"name", "backend", "folder", cardFormDraftPrefix + form.ID + "_enter", cardFormDraftPrefix + form.ID + "_up", cardFormSubmitPrefix + form.ID} {
		if !names[name] {
			t.Fatalf("missing complete-form control %s", name)
		}
	}
}

func TestCardForm_LabelAuthorizationAndRetryButtonsRemainOutsideForm(t *testing.T) {
	card := core.NewCard().Title("Labels", "blue").Buttons(core.DefaultBtn("Authorize", "cmd:/team labels authorize"), core.DefaultBtn("Retry", "cmd:/team labels retry")).Build()
	card.Form = &core.CardForm{ID: "ref", Command: "/team labels save", SubmitText: "Save", Fields: []core.CardFormField{{Name: "new_bot_labels", Kind: "text"}}}
	rendered := renderFormCard(card, "session")
	body := rendered["body"].(map[string]any)["elements"].([]map[string]any)
	found := map[string]bool{}
	for _, element := range body {
		if element["tag"] != "button" {
			continue
		}
		behaviors, ok := element["behaviors"].([]map[string]any)
		if !ok {
			continue
		}
		value := behaviors[0]["value"].(map[string]string)
		if element["form_action_type"] != nil || value["form_ref"] != "ref" || value["session_key"] != "session" {
			t.Fatal("action lost trusted form association")
		}
		found[value["action"]] = true
	}
	if !found["cmd:/team labels authorize"] || !found["cmd:/team labels retry"] {
		t.Fatal("label authorization/retry buttons disappeared from form card")
	}
}

func TestCardForm_LabelAuthorizationCallbackRetainsPrivateContext(t *testing.T) {
	ip, msg, form := formFixture(t)
	ip.userNameCache.Store("owner", "Owner")
	ip.chatNameCache.Store("oc_private", "Private")
	form.Command = "/team labels save"
	id, err := ip.rememberForm(msg, form)
	if err != nil {
		t.Fatal(err)
	}
	ip.rememberCardChatType(msg.ReplyCtx.(replyContext))
	received := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, m *core.Message) { received <- m }
	event := formEvent(id, "owner", "oc_private", nil)
	event.Event.Action.Name = ""
	event.Event.Action.Value = map[string]any{"action": "cmd:/team labels authorize", "form_ref": id, "session_key": msg.SessionKey, "reuse_card": "true"}
	if _, err = ip.onCardAction(event); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got.Content != "/team labels authorize" || !ip.IsPrivateUserMessage(got) || got.FormValues != nil {
			t.Fatalf("callback lost private context: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("authorization button did not dispatch")
	}
}

func TestCardForm_LabelAuthorizationDoesNotTrustPrivateFlag(t *testing.T) {
	for _, observed := range []string{"", "group"} {
		t.Run("chat_"+observed, func(t *testing.T) {
			ip, _, _ := formFixture(t)
			ip.userNameCache.Store("owner", "Owner")
			ip.chatNameCache.Store("oc_test", "Test")
			if observed != "" {
				ip.cardChatTypes.Store("oc_test", observed)
			}
			received := make(chan *core.Message, 1)
			ip.handler = func(_ core.Platform, msg *core.Message) { received <- msg }
			event := formEvent("fake", "owner", "oc_test", nil)
			event.Event.Action.Name = ""
			event.Event.Action.Value = map[string]any{"action": "cmd:/team labels authorize", "chat_type": "p2p", "sender_type": "user"}
			ip.onCardAction(event)
			select {
			case msg := <-received:
				if ip.IsPrivateUserMessage(msg) {
					t.Fatal("forged private context accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("no command dispatch")
			}
		})
	}
}

func TestCardForm_RolePermissionsCallbackRetainsPrivateContext(t *testing.T) {
	ip, msg, form := formFixture(t)
	ip.userNameCache.Store("owner", "Owner")
	ip.chatNameCache.Store("oc_private", "Private")
	form.Command = "/team permissions"
	id, err := ip.rememberForm(msg, form)
	if err != nil {
		t.Fatal(err)
	}
	ip.rememberCardChatType(msg.ReplyCtx.(replyContext))
	received := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, m *core.Message) { received <- m }
	event := formEvent(id, "owner", "oc_private", nil)
	event.Event.Action.Name = ""
	event.Event.Action.Value = map[string]any{"action": "cmd:/team permissions", "form_ref": id, "session_key": msg.SessionKey, "reuse_card": "true"}
	if _, err = ip.onCardAction(event); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got.Content != "/team permissions" || !ip.IsPrivateUserMessage(got) || got.FormValues != nil {
			t.Fatalf("callback lost private context: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("authorization button did not dispatch")
	}
}
