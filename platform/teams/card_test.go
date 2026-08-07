package teams

import (
	"encoding/json"
	"strings"
	"testing"
)

func firstTextBlock(card map[string]any) map[string]any {
	body, _ := card["body"].([]map[string]any)
	if len(body) == 0 {
		return nil
	}
	return body[0]
}

func TestLoadingCard(t *testing.T) {
	c := loadingCard("💭 Thinking…")
	if c["type"] != "AdaptiveCard" || c["version"] != "1.5" {
		t.Fatalf("not an AdaptiveCard 1.5: %v", c)
	}
	tb := firstTextBlock(c)
	txt, _ := tb["text"].(string)
	if tb["type"] != "TextBlock" || !strings.Contains(txt, "💭 Thinking…") {
		t.Errorf("loading text block = %v", tb)
	}
	// grayed + small + italic (markdown)
	if tb["isSubtle"] != true || tb["size"] != "Small" {
		t.Errorf("loading text should be subtle+Small, got %v", tb)
	}
	if !strings.HasPrefix(txt, "_") || !strings.HasSuffix(txt, "_") {
		t.Errorf("loading text should be italic markdown, got %q", txt)
	}
}

func TestLoadingCard_EmptyRendersNoBody(t *testing.T) {
	// Empty card_loading_text yields a label-less placeholder (no TextBlock),
	// not a TextBlock containing an empty string.
	c := loadingCard("")
	if c["type"] != "AdaptiveCard" {
		t.Fatalf("not an AdaptiveCard: %v", c)
	}
	if tb := firstTextBlock(c); tb != nil {
		t.Errorf("empty text should render no body block, got %v", tb)
	}
}

func TestAnswerCard_SingleTextBlock(t *testing.T) {
	c := answerCard("hello **world**")
	body, _ := c["body"].([]map[string]any)
	if len(body) != 1 {
		t.Fatalf("answer card should be one text block (no body footer), got %d", len(body))
	}
	if body[0]["text"] != "hello **world**" || body[0]["wrap"] != true {
		t.Errorf("answer block = %v", body[0])
	}
}

func TestAIGeneratedEntity(t *testing.T) {
	e := aiGeneratedEntity()
	if e["type"] != "https://schema.org/Message" || e["@type"] != "Message" {
		t.Errorf("entity envelope = %v", e)
	}
	at, _ := e["additionalType"].([]string)
	if len(at) != 1 || at[0] != "AIGeneratedContent" {
		t.Errorf("additionalType = %v, want [AIGeneratedContent]", e["additionalType"])
	}
}

func TestCardActivity_WrapsAttachment(t *testing.T) {
	a := cardActivity(replyContext{conversationID: "c1"}, loadingCard("hi"))
	if a.Type != "message" || len(a.Attachments) != 1 {
		t.Fatalf("card activity = %+v", a)
	}
	if a.Attachments[0].ContentType != adaptiveCardContentType {
		t.Errorf("contentType = %q", a.Attachments[0].ContentType)
	}
}

func TestPromptCard_RendersActionSubmitButtons(t *testing.T) {
	card := promptCard("**needs permission**", []cardButton{
		{title: "Allow", action: "perm:allow"},
		{title: "Deny", action: "perm:deny"},
	})
	actions, ok := card["actions"].([]map[string]any)
	if !ok || len(actions) != 2 {
		t.Fatalf("want 2 actions, got %v", card["actions"])
	}
	if actions[0]["type"] != "Action.Submit" || actions[0]["title"] != "Allow" {
		t.Errorf("action[0] = %v", actions[0])
	}
	data0, _ := actions[0]["data"].(map[string]any)
	if data0["action"] != "perm:allow" {
		t.Errorf("action[0] data = %v", actions[0]["data"])
	}
}

func TestPromptCard_NoButtonsOmitsActions(t *testing.T) {
	card := promptCard("just text", nil)
	if _, ok := card["actions"]; ok {
		t.Errorf("a buttonless prompt card must omit the actions key: %v", card)
	}
}

func TestPromptCard_ActionRoundTripsThroughCardAction(t *testing.T) {
	card := promptCard("q", []cardButton{{title: "Option A", action: "askq:0:1"}})
	actions := card["actions"].([]map[string]any)
	data, err := json.Marshal(actions[0]["data"])
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	// The outbound button data must be readable by the inbound cardAction() parser.
	a := &activity{Value: json.RawMessage(data)}
	if got := a.cardAction(); got != "askq:0:1" {
		t.Errorf("cardAction() = %q, want askq:0:1", got)
	}
}
