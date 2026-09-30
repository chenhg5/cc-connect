package feishu

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	larkcallback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestBuildAskQuestionCard2(t *testing.T) {
	q := core.UserQuestion{
		Question: "TJ 订单何时置已完成？",
		Header:   "TJ状态语义",
		Options: []core.UserQuestionOption{
			{Label: "出码即完成", Description: "PRD 字面"},
			{Label: "CK 发货才完成", Description: "兼容现有状态机"},
		},
	}
	card := buildAskQuestionCard2("❓ 问题 (1/2)", "**TJ 订单何时置已完成？**", "选项不合适？直接输入", q, 0, "sess-1")

	if card["schema"] != "2.0" {
		t.Fatalf("schema = %v, want 2.0", card["schema"])
	}
	header, ok := card["header"].(map[string]any)
	if !ok {
		t.Fatal("missing header")
	}
	if header["title"] == nil {
		t.Fatal("missing header title")
	}
	body, ok := card["body"].(map[string]any)
	if !ok {
		t.Fatal("missing body")
	}
	elements, ok := body["elements"].([]map[string]any)
	if !ok {
		t.Fatal("missing body elements")
	}

	buttons := 0
	foundForm := false
	for _, el := range elements {
		switch el["tag"] {
		case "button":
			buttons++
			behaviors, ok := el["behaviors"].([]map[string]any)
			if !ok || len(behaviors) == 0 {
				t.Fatalf("button %d missing behaviors", buttons)
			}
			value, ok := behaviors[0]["value"].(map[string]any)
			if !ok {
				t.Fatalf("button %d behaviors value has wrong type", buttons)
			}
			wantAction := "askq:0:" + strconv.Itoa(buttons)
			if value["action"] != wantAction {
				t.Fatalf("button %d action = %v, want %q", buttons, value["action"], wantAction)
			}
			if value["session_key"] != "sess-1" {
				t.Fatalf("button %d session_key = %v, want sess-1", buttons, value["session_key"])
			}
			if value["askq_label"] == "" {
				t.Fatalf("button %d missing askq_label", buttons)
			}
		case "form":
			foundForm = true
			formEls, ok := el["elements"].([]map[string]any)
			if !ok || len(formEls) != 2 {
				t.Fatal("form should contain an input and a submit button")
			}
			if formEls[0]["tag"] != "input" || formEls[0]["name"] != "answer" {
				t.Fatalf("form element[0] = %v, want input named answer", formEls[0]["tag"])
			}
			if formEls[1]["tag"] != "button" || formEls[1]["action_type"] != "form_submit" {
				t.Fatalf("form element[1] = %v, want form_submit button", formEls[1]["tag"])
			}
		}
	}
	if buttons != 2 {
		t.Fatalf("buttons = %d, want 2 (one per option)", buttons)
	}
	if !foundForm {
		t.Fatal("missing free-form answer form")
	}
}

func TestOnCardActionAskqTextFormSubmit(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	msgCh := make(chan *core.Message, 1)
	ip.handler = func(p core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	resp, err := ip.onCardAction(&larkcallback.CardActionTriggerEvent{
		Event: &larkcallback.CardActionTriggerRequest{
			Operator: &larkcallback.Operator{OpenID: "ou_test_user"},
			Action: &larkcallback.CallBackAction{
				Name:      "submit",
				FormValue: map[string]any{"answer": "先出码后置完成，但要加对账补偿"},
				Value:     map[string]any{},
			},
			Context: &larkcallback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatal("expected a confirmation card response")
	}

	select {
	case msg := <-msgCh:
		if msg.Content != "先出码后置完成，但要加对账补偿" {
			t.Fatalf("forwarded content = %q, want the raw answer text", msg.Content)
		}
		if msg.IsPermissionResponse {
			t.Fatal("free-form answer must not be flagged as permission response")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected the answer to be dispatched as a user message")
	}
}

// The confirmation card echoes the answer in its title, truncated to 50. That
// cut must count runes, not bytes: free-form answers are exactly where CJK text
// shows up, and a byte cut slices a multi-byte character in half and shows
// about a third of the intended length.
func TestOnCardActionAskqTextTruncatesTitleByRunes(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}
	ip.handler = func(core.Platform, *core.Message) {}

	answer := strings.Repeat("先出码后置完成", 10) // 70 runes, 210 bytes
	resp, err := ip.onCardAction(&larkcallback.CardActionTriggerEvent{
		Event: &larkcallback.CardActionTriggerRequest{
			Operator: &larkcallback.Operator{OpenID: "ou_test_user"},
			Action: &larkcallback.CallBackAction{
				Name:      "submit",
				FormValue: map[string]any{"answer": answer},
				Value:     map[string]any{},
			},
			Context: &larkcallback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatal("expected a confirmation card response")
	}

	want := "✅ " + string([]rune(answer)[:50]) + "…"
	if got := cardTitle(resp.Card.Data); got != want {
		t.Fatalf("confirmation title = %q, want %q", got, want)
	}
}

// cardTitle digs the header title out of a raw schema 2.0 card payload.
func cardTitle(data any) string {
	m, ok := data.(map[string]any)
	if !ok {
		return ""
	}
	header, ok := m["header"].(map[string]any)
	if !ok {
		return ""
	}
	title, ok := header["title"].(map[string]any)
	if !ok {
		return ""
	}
	content, _ := title["content"].(string)
	return content
}

func TestOnCardActionFormSubmitWithoutAnswerIsIgnored(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	called := false
	ip.handler = func(p core.Platform, msg *core.Message) {
		called = true
	}

	resp, err := ip.onCardAction(&larkcallback.CardActionTriggerEvent{
		Event: &larkcallback.CardActionTriggerRequest{
			Operator: &larkcallback.Operator{OpenID: "ou_test_user"},
			Action: &larkcallback.CallBackAction{
				Name:      "submit",
				FormValue: map[string]any{"answer": "   "},
				Value:     map[string]any{},
			},
			Context: &larkcallback.Context{OpenChatID: "oc_test_chat"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response when no answer text is present")
	}
	if called {
		t.Fatal("no message should be dispatched for a blank answer")
	}
}
