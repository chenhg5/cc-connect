package wecom

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestBuildWeComTemplateCard(t *testing.T) {
	card := core.NewCard().
		Title("Permission Request", "red").
		Markdown("Agent requests to run `rm -rf /tmp/test`").
		Buttons(
			core.PrimaryBtn("Allow", "perm:allow"),
			core.DangerBtn("Deny", "perm:deny"),
		).
		Build()

	payload, err := buildWeComTemplateCard(card)
	if err != nil {
		t.Fatalf("buildWeComTemplateCard error: %v", err)
	}

	if payload["card_type"] != "button_interaction" {
		t.Errorf("expected card_type=button_interaction, got %v", payload["card_type"])
	}

	mainTitle, ok := payload["main_title"].(map[string]string)
	if !ok || mainTitle["title"] != "Permission Request" {
		t.Errorf("expected main_title.title='Permission Request', got %v", payload["main_title"])
	}

	subTitle, ok := payload["sub_title_text"].(string)
	if !ok || !strings.Contains(subTitle, "rm -rf /tmp/test") {
		t.Errorf("expected sub_title_text containing markdown, got %v", payload["sub_title_text"])
	}

	buttons, ok := payload["button_list"].([]map[string]any)
	if !ok || len(buttons) != 2 {
		t.Fatalf("expected 2 buttons, got %v", payload["button_list"])
	}

	if buttons[0]["text"] != "Allow" || buttons[0]["key"] != "perm:allow" || buttons[0]["style"] != 1 {
		t.Errorf("button 0 mismatch: %+v", buttons[0])
	}
	if buttons[1]["text"] != "Deny" || buttons[1]["key"] != "perm:deny" || buttons[1]["style"] != 3 {
		t.Errorf("button 1 mismatch: %+v", buttons[1])
	}
}

func TestBuildWeComTemplateCard_TextNotice(t *testing.T) {
	card := core.NewCard().
		Title("Notice", "blue").
		Markdown("Simple text notice without buttons").
		Build()

	payload, err := buildWeComTemplateCard(card)
	if err != nil {
		t.Fatalf("buildWeComTemplateCard error: %v", err)
	}

	if payload["card_type"] != "text_notice" {
		t.Errorf("expected card_type=text_notice, got %v", payload["card_type"])
	}
}

func TestParseWeComPermissionResponse(t *testing.T) {
	tests := []struct {
		key      string
		wantText string
		wantPerm bool
	}{
		{"perm:allow", "allow", true},
		{"perm:deny", "deny", true},
		{"perm:allow_all", "allow all", true},
		{"perm:deny_all", "deny all", true},
		{"act:/stop", "act:/stop", false},
		{"custom_key", "custom_key", false},
	}

	for _, tt := range tests {
		gotText, gotPerm := parseWeComPermissionResponse(tt.key)
		if gotText != tt.wantText || gotPerm != tt.wantPerm {
			t.Errorf("parseWeComPermissionResponse(%q) = (%q, %v), want (%q, %v)",
				tt.key, gotText, gotPerm, tt.wantText, tt.wantPerm)
		}
	}
}

func TestWSEventCallback_TemplateCardEvent(t *testing.T) {
	var handledMsg *core.Message
	var wg sync.WaitGroup
	wg.Add(1)

	p := &WSPlatform{
		botID: "bot_123",
		handler: func(platform core.Platform, msg *core.Message) {
			handledMsg = msg
			wg.Done()
		},
	}

	eventJSON := `{
		"msgid": "msg_event_1",
		"create_time": 1700000000,
		"aibotid": "bot_123",
		"chatid": "chat_456",
		"from": {"userid": "user_789"},
		"msgtype": "event",
		"event": {
			"eventtype": "template_card_event",
			"event_key": "perm:allow",
			"task_id": "task_abc"
		}
	}`

	frame := wsFrame{
		Cmd:     "aibot_event_callback",
		Headers: wsFrameHeaders{ReqID: "req_evt_1"},
		Body:    json.RawMessage(eventJSON),
	}

	p.handleEventCallback(frame)

	if waitTimeout(&wg, 2*time.Second) {
		t.Fatal("timed out waiting for event callback handler")
	}

	if handledMsg == nil {
		t.Fatal("expected handledMsg to be set")
	}

	if handledMsg.UserID != "user_789" {
		t.Errorf("UserID = %q, want user_789", handledMsg.UserID)
	}
	if handledMsg.Content != "allow" {
		t.Errorf("Content = %q, want allow", handledMsg.Content)
	}
	if !handledMsg.IsPermissionResponse {
		t.Errorf("IsPermissionResponse = false, want true")
	}
}

func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return false // completed
	case <-time.After(timeout):
		return true // timed out
	}
}

// Regression test for #2277: WeCom requires template_card.task_id on
// card_type=button_interaction and rejects the message without it. On the
// WebSocket ReplyCard path the frame is fire-and-forget, so the missing field
// made permission prompts disappear without an error.
func TestBuildWeComTemplateCard_ButtonCardHasTaskID(t *testing.T) {
	card := core.NewCard().
		Title("Permission Request", "red").
		Markdown("Agent requests to run a command").
		Buttons(
			core.PrimaryBtn("Allow", "perm:allow"),
			core.DangerBtn("Deny", "perm:deny"),
		).
		Build()

	validID := regexp.MustCompile(`^[A-Za-z0-9_@-]+$`)
	seen := make(map[string]bool)
	for i := 0; i < 2; i++ {
		payload, err := buildWeComTemplateCard(card)
		if err != nil {
			t.Fatalf("buildWeComTemplateCard error: %v", err)
		}
		if payload["card_type"] != "button_interaction" {
			t.Fatalf("expected button_interaction, got %v", payload["card_type"])
		}
		taskID, _ := payload["task_id"].(string)
		if taskID == "" {
			t.Fatal("button_interaction card has no task_id")
		}
		if len(taskID) > 128 {
			t.Errorf("task_id %q exceeds 128 bytes", taskID)
		}
		if !validID.MatchString(taskID) {
			t.Errorf("task_id %q contains characters WeCom rejects", taskID)
		}
		if seen[taskID] {
			t.Errorf("task_id %q reused across cards", taskID)
		}
		seen[taskID] = true
	}

	// task_id is only mandatory for button cards; text_notice keeps the payload
	// minimal.
	textCard := core.NewCard().Title("Notice", "blue").Markdown("hello").Build()
	textPayload, err := buildWeComTemplateCard(textCard)
	if err != nil {
		t.Fatalf("buildWeComTemplateCard error: %v", err)
	}
	if _, ok := textPayload["task_id"]; ok {
		t.Errorf("text_notice card should not carry task_id, got %v", textPayload["task_id"])
	}
}

// WeCom also requires main_title for button_interaction; a header-less card must
// still be deliverable rather than silently rejected.
func TestBuildWeComTemplateCard_ButtonCardWithoutHeaderGetsTitle(t *testing.T) {
	card := core.NewCard().
		Markdown("Agent requests to run a command").
		Buttons(core.PrimaryBtn("Allow", "perm:allow")).
		Build()

	payload, err := buildWeComTemplateCard(card)
	if err != nil {
		t.Fatalf("buildWeComTemplateCard error: %v", err)
	}
	if payload["card_type"] != "button_interaction" {
		t.Fatalf("expected button_interaction, got %v", payload["card_type"])
	}
	mainTitle, ok := payload["main_title"].(map[string]string)
	if !ok || strings.TrimSpace(mainTitle["title"]) == "" {
		t.Fatalf("button card without header must still carry main_title, got %v", payload["main_title"])
	}
}
