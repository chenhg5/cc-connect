package feishu

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	larkcallback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func askqTestTexts() core.AskQuestionCardTexts {
	return core.AskQuestionCardTexts{
		Title:            "❓ Agent Question (1/2)",
		Body:             "**采用哪种部署方式？**",
		Note:             "None of the options fit? Type your answer below",
		InputPlaceholder: "Type your answer…",
		InputLabel:       "Free-form answer",
		SubmitLabel:      "Send",
		ExpiredNotice:    "expired-notice",
		TooLongNotice:    "too-long-notice",
	}
}

func newAskqTestPlatform(t *testing.T) (*interactivePlatform, chan *core.Message) {
	t.Helper()
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}
	msgCh := make(chan *core.Message, 8)
	ip.handler = func(p core.Platform, msg *core.Message) {
		msgCh <- msg
	}
	return ip, msgCh
}

// mintAskq registers a claim exactly like SendAskQuestionCard would, with
// marker notices so rejection cards are assertable.
func mintAskq(ip *interactivePlatform, sessionKey, chatID string, qIdx int) string {
	return ip.askq.mint(sessionKey, chatID, qIdx, "采用哪种部署方式？", "", "expired-notice", "too-long-notice")
}

func askqTextEvent(token, answer string, chatID string) *larkcallback.CardActionTriggerEvent {
	ev := &larkcallback.CardActionTriggerEvent{
		Event: &larkcallback.CardActionTriggerRequest{
			Operator: &larkcallback.Operator{OpenID: "ou_test_user"},
			Action: &larkcallback.CallBackAction{
				Name:      "submit",
				FormValue: map[string]any{"answer": answer},
				Value: map[string]any{
					"action":     "askq_text",
					"askq_token": token,
				},
			},
			Context: &larkcallback.Context{OpenChatID: chatID, OpenMessageID: "om_card"},
		},
	}
	return ev
}

func cardBodyMarkdown(data any) string {
	m, ok := data.(map[string]any)
	if !ok {
		return ""
	}
	body, ok := m["body"].(map[string]any)
	if !ok {
		return ""
	}
	els, ok := body["elements"].([]map[string]any)
	if !ok || len(els) == 0 {
		return ""
	}
	div, ok := els[0]["text"].(map[string]any)
	if !ok {
		return ""
	}
	c, _ := div["content"].(string)
	return c
}

func TestBuildAskQuestionCard2(t *testing.T) {
	q := core.UserQuestion{
		Question: "采用哪种部署方式？",
		Header:   "部署方式",
		Options: []core.UserQuestionOption{
			{Label: "滚动发布", Description: "流量逐步切换"},
			{Label: "蓝绿发布", Description: "需要双倍容量"},
		},
	}
	texts := askqTestTexts()
	card := buildAskQuestionCard2(texts, q, 0, "sess-1", "tok-1")

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
	foundSubmit := false
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
			// Option button: legacy action contract plus the one-time token.
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
			if value["askq_token"] != "tok-1" {
				t.Fatalf("button %d askq_token = %v, want tok-1", buttons, value["askq_token"])
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
			if formEls[0]["max_length"] != askqMaxAnswerRunes {
				t.Fatalf("input max_length = %v, want %d", formEls[0]["max_length"], askqMaxAnswerRunes)
			}
			placeholder, ok := formEls[0]["placeholder"].(map[string]any)
			if !ok || placeholder["content"] != texts.InputPlaceholder {
				t.Fatalf("input placeholder = %v, want localized %q", formEls[0]["placeholder"], texts.InputPlaceholder)
			}
			label, ok := formEls[0]["label"].(map[string]any)
			if !ok || label["content"] != texts.InputLabel {
				t.Fatalf("input label = %v, want localized %q", formEls[0]["label"], texts.InputLabel)
			}
			if formEls[1]["tag"] != "button" || formEls[1]["action_type"] != "form_submit" {
				t.Fatalf("form element[1] = %v, want form_submit button", formEls[1]["tag"])
			}
			btnText, ok := formEls[1]["text"].(map[string]any)
			if !ok || btnText["content"] != texts.SubmitLabel {
				t.Fatalf("submit label = %v, want localized %q", formEls[1]["text"], texts.SubmitLabel)
			}
			// The form_submit button carries the identity binding for the
			// free-form answer.
			behaviors, ok := formEls[1]["behaviors"].([]map[string]any)
			if !ok || len(behaviors) == 0 {
				t.Fatal("submit button missing behaviors")
			}
			value, ok := behaviors[0]["value"].(map[string]any)
			if !ok {
				t.Fatal("submit behaviors value has wrong type")
			}
			if value["action"] != "askq_text" {
				t.Fatalf("submit action = %v, want askq_text", value["action"])
			}
			if value["askq_token"] != "tok-1" {
				t.Fatalf("submit askq_token = %v, want tok-1", value["askq_token"])
			}
			if value["session_key"] != "sess-1" {
				t.Fatalf("submit session_key = %v, want sess-1", value["session_key"])
			}
			foundSubmit = true
		}
	}
	if buttons != len(q.Options) {
		t.Fatalf("buttons = %d, want %d (one per option)", buttons, len(q.Options))
	}
	if !foundForm || !foundSubmit {
		t.Fatal("missing free-form answer form or submit button")
	}
}

// The answer must be dispatched to the session the question was asked in —
// including its thread identity under thread_isolation — not to whatever
// chat/user clicked the button.
func TestOnCardActionAskqTextRoutesToBoundSession(t *testing.T) {
	ip, msgCh := newAskqTestPlatform(t)
	const threadSession = "feishu:oc_root_chat:root:om_thread_root"
	token := mintAskq(ip, threadSession, "oc_root_chat", 0)

	resp, err := ip.onCardAction(askqTextEvent(token, "滚动发布即可", "oc_someone_dms_bot"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatal("expected a confirmation card response")
	}

	select {
	case msg := <-msgCh:
		if msg.SessionKey != threadSession {
			t.Fatalf("dispatched SessionKey = %q, want the bound thread session %q", msg.SessionKey, threadSession)
		}
		if msg.Content != "滚动发布即可" {
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
	ip, _ := newAskqTestPlatform(t)
	token := mintAskq(ip, "feishu:oc_c:ou_u", "oc_c", 0)

	answer := strings.Repeat("滚动发布即可", 10) // 70 runes, 210 bytes
	resp, err := ip.onCardAction(askqTextEvent(token, answer, "oc_c"))
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

// A token can be claimed exactly once: a replayed or double-clicked submit is
// rejected explicitly and nothing further is dispatched.
func TestOnCardActionAskqTextExactlyOnce(t *testing.T) {
	ip, msgCh := newAskqTestPlatform(t)
	token := mintAskq(ip, "feishu:oc_c:ou_u", "oc_c", 0)

	first, err := ip.onCardAction(askqTextEvent(token, "第一个回答", "oc_c"))
	if err != nil {
		t.Fatalf("first onCardAction() error = %v", err)
	}
	if first == nil || first.Card == nil {
		t.Fatal("expected a confirmation card for the first submit")
	}

	second, err := ip.onCardAction(askqTextEvent(token, "重复的提交", "oc_c"))
	if err != nil {
		t.Fatalf("second onCardAction() error = %v", err)
	}
	if second == nil || second.Card == nil {
		t.Fatal("expected an explicit rejection card for the replayed submit")
	}
	if got := cardBodyMarkdown(second.Card.Data); got != "expired-notice" {
		t.Fatalf("rejection body = %q, want the expired notice", got)
	}

	select {
	case msg := <-msgCh:
		if msg.Content != "第一个回答" {
			t.Fatalf("dispatched content = %q, want only the first answer", msg.Content)
		}
	default:
	}
	select {
	case msg := <-msgCh:
		t.Fatalf("second answer must not be dispatched, got %q", msg.Content)
	default:
	}
}

// Re-asking the same question supersedes the earlier card: an old card (or a
// stale client still showing it) must not answer the new pending question.
func TestOnCardActionAskqSupersededCardRejected(t *testing.T) {
	ip, msgCh := newAskqTestPlatform(t)
	const session = "feishu:oc_c:ou_u"
	oldToken := mintAskq(ip, session, "oc_c", 0)
	newToken := mintAskq(ip, session, "oc_c", 0)

	resp, err := ip.onCardAction(askqTextEvent(oldToken, "旧卡片提交的回答", "oc_c"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatal("expected an explicit rejection card for the superseded card")
	}
	if got := cardBodyMarkdown(resp.Card.Data); got != "expired-notice" {
		t.Fatalf("rejection body = %q, want the expired notice", got)
	}
	select {
	case msg := <-msgCh:
		t.Fatalf("superseded answer must not be dispatched, got %q", msg.Content)
	default:
	}

	// The new card is still claimable.
	if _, ok := ip.askq.claim(newToken); !ok {
		t.Fatal("new card token should still be claimable")
	}
}

// A card for question 1 of a batch must not answer question 2: the user may
// have replied to the first by typing instead of clicking, leaving the old
// card live while the engine has already moved on.
func TestOnCardActionAskqNextQuestionSupersedesPreviousCard(t *testing.T) {
	ip, msgCh := newAskqTestPlatform(t)
	const session = "feishu:oc_c:ou_u"
	q0Token := mintAskq(ip, session, "oc_c", 0)
	q1Token := mintAskq(ip, session, "oc_c", 1)

	resp, err := ip.onCardAction(askqTextEvent(q0Token, "上一题卡片提交的回答", "oc_c"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatal("expected an explicit rejection card for the previous question's card")
	}
	if got := cardBodyMarkdown(resp.Card.Data); got != "expired-notice" {
		t.Fatalf("rejection body = %q, want the expired notice", got)
	}
	select {
	case msg := <-msgCh:
		t.Fatalf("previous question's answer must not be dispatched, got %q", msg.Content)
	default:
	}

	if _, ok := ip.askq.claim(q1Token); !ok {
		t.Fatal("current question's token should still be claimable")
	}
}

// Unknown token: forged, pre-token card, or after a restart. Rejected
// explicitly, nothing dispatched.
func TestOnCardActionAskqUnknownTokenRejected(t *testing.T) {
	ip, msgCh := newAskqTestPlatform(t)

	resp, err := ip.onCardAction(askqTextEvent("forged-token", "伪造的回答", "oc_c"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatal("expected an explicit rejection card for an unknown token")
	}
	select {
	case msg := <-msgCh:
		t.Fatalf("forged answer must not be dispatched, got %q", msg.Content)
	default:
	}
}

// max_length is advisory on the Feishu client: the server must enforce the
// rune limit itself, before dispatching or acknowledging.
func TestOnCardActionAskqTextTooLongRejected(t *testing.T) {
	ip, msgCh := newAskqTestPlatform(t)
	token := mintAskq(ip, "feishu:oc_c:ou_u", "oc_c", 0)

	over := strings.Repeat("答", askqMaxAnswerRunes+1) // 501 runes
	resp, err := ip.onCardAction(askqTextEvent(token, over, "oc_c"))
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatal("expected an explicit rejection card for an over-long answer")
	}
	if got := cardBodyMarkdown(resp.Card.Data); got != "too-long-notice" {
		t.Fatalf("rejection body = %q, want the too-long notice", got)
	}
	select {
	case msg := <-msgCh:
		t.Fatalf("over-long answer must not be dispatched, got %d runes", len([]rune(msg.Content)))
	default:
	}

	// The boundary: exactly the limit is accepted.
	token2 := mintAskq(ip, "feishu:oc_c:ou_u", "oc_c", 0)
	exact := strings.Repeat("答", askqMaxAnswerRunes)
	resp2, err := ip.onCardAction(askqTextEvent(token2, exact, "oc_c"))
	if err != nil {
		t.Fatalf("boundary onCardAction() error = %v", err)
	}
	if resp2 == nil || resp2.Card == nil {
		t.Fatal("expected a confirmation card for an answer at the limit")
	}
	select {
	case msg := <-msgCh:
		if len([]rune(msg.Content)) != askqMaxAnswerRunes {
			t.Fatalf("dispatched %d runes, want %d", len([]rune(msg.Content)), askqMaxAnswerRunes)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("answer at the limit should be dispatched")
	}
}

// A non-string answer field (client bug or crafted payload) is not an answer.
func TestOnCardActionAskqTextNonStringAnswerIgnored(t *testing.T) {
	ip, msgCh := newAskqTestPlatform(t)
	token := mintAskq(ip, "feishu:oc_c:ou_u", "oc_c", 0)

	ev := askqTextEvent(token, "", "oc_c")
	ev.Event.Action.FormValue["answer"] = 42
	resp, err := ip.onCardAction(ev)
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response for a non-string answer")
	}
	select {
	case msg := <-msgCh:
		t.Fatalf("non-string answer must not be dispatched, got %q", msg.Content)
	default:
	}
}

func TestOnCardActionFormSubmitWithoutAnswerIsIgnored(t *testing.T) {
	ip, msgCh := newAskqTestPlatform(t)
	token := mintAskq(ip, "feishu:oc_c:ou_u", "oc_c", 0)

	ev := askqTextEvent(token, "   ", "oc_c")
	resp, err := ip.onCardAction(ev)
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response when no answer text is present")
	}
	select {
	case msg := <-msgCh:
		t.Fatalf("no message should be dispatched for a blank answer, got %q", msg.Content)
	default:
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
