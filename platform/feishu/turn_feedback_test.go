package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	callback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type feedbackCall struct {
	sessionKey string
	args       []string
}

func newTurnFeedbackPlatform(t *testing.T) (*interactivePlatform, chan feedbackCall) {
	t.Helper()
	ip, _ := newAllowFromTestPlatform(t, "")
	ip.turnFeedbackExec = "my-feedback-cmd"
	calls := make(chan feedbackCall, 4)
	ip.turnFeedbackRun = func(sessionKey string, args []string) {
		calls <- feedbackCall{sessionKey: sessionKey, args: args}
	}
	return ip, calls
}

func feedbackClick(ip *interactivePlatform, verdict string, form map[string]any) (*callback.CardActionTriggerResponse, error) {
	return ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_clicker"},
			Action: &callback.CallBackAction{
				Value:     map[string]any{"action": "fb:" + verdict, "session_key": "feishu:oc_chat:root:om_root"},
				Name:      "fb_" + verdict,
				FormValue: form,
			},
			Context: &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_reply"},
		},
	})
}

// A 👍 runs the feedback command for the reply it sat under, in the
// conversation the button names, and answers with a toast only.
func TestTurnFeedback_VerdictRunsCommandForTheReply(t *testing.T) {
	ip, calls := newTurnFeedbackPlatform(t)
	resp, err := feedbackClick(ip, "down", map[string]any{"comment": "  漏掉了一个边界情况；请再检查一下  "})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "success" || !strings.Contains(resp.Toast.Content, "没用") {
		t.Fatalf("expected a success toast naming the verdict, got %#v", resp)
	}
	select {
	case call := <-calls:
		if call.sessionKey != "feishu:oc_chat:root:om_root" {
			t.Errorf("session key = %q", call.sessionKey)
		}
		want := []string{"--message", "om_reply", "--verdict", "down", "--comment", "漏掉了一个边界情况；请再检查一下", "--operator", "ou_clicker"}
		if strings.Join(call.args, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("args = %q, want %q", call.args, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("feedback command never ran")
	}
}

// "提交意见" without a comment asks for one and runs nothing; with one it
// sends the comment alone.
func TestTurnFeedback_CommentButtonNeedsAComment(t *testing.T) {
	ip, calls := newTurnFeedbackPlatform(t)
	resp, _ := feedbackClick(ip, "note", map[string]any{"comment": "   "})
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "warning" {
		t.Fatalf("expected a warning toast, got %#v", resp)
	}
	select {
	case call := <-calls:
		t.Fatalf("nothing should run without a comment, got %v", call.args)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := feedbackClick(ip, "note", map[string]any{"comment": strings.Repeat("长", 1200)}); err != nil {
		t.Fatal(err)
	}
	call := <-calls
	if len(call.args) != 6 || call.args[2] != "--comment" {
		t.Fatalf("a comment alone carries no verdict, args = %q", call.args)
	}
	if n := len([]rune(call.args[3])); n != turnFeedbackCommentMax {
		t.Errorf("comment kept %d characters, want %d", n, turnFeedbackCommentMax)
	}
}

// Somebody the agent does not answer cannot leave feedback either.
func TestTurnFeedback_UnauthorizedClickRunsNothing(t *testing.T) {
	ip, calls := newTurnFeedbackPlatform(t)
	ip.allowFrom = "ou_owner"
	resp, _ := feedbackClick(ip, "up", nil)
	if resp != nil {
		t.Fatalf("unauthorized click answered %#v", resp)
	}
	select {
	case call := <-calls:
		t.Fatalf("unauthorized click ran %v", call.args)
	case <-time.After(100 * time.Millisecond):
	}
}

// The gate file switches the controls without a restart; no file is on.
func TestTurnFeedbackEnabled_FollowsTheGateFile(t *testing.T) {
	ip, _ := newTurnFeedbackPlatform(t)
	gate := filepath.Join(t.TempDir(), "turn-feedback")
	ip.turnFeedbackGate = gate
	if !ip.TurnFeedbackEnabled() {
		t.Fatal("a missing gate file must mean on")
	}
	if err := os.WriteFile(gate, []byte("off\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if form, _ := ip.turnFeedbackElement("k"); ip.TurnFeedbackEnabled() || form != nil {
		t.Fatal("gate off must hide the controls")
	}
	if err := os.WriteFile(gate, []byte("on\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ip.TurnFeedbackEnabled() {
		t.Fatal("gate on must show the controls")
	}
	ip.turnFeedbackExec = ""
	if ip.TurnFeedbackEnabled() {
		t.Fatal("no command means no controls")
	}
}

// The two gates are read apart: each hides its own part, a missing file is
// on, and with both parts off there is no form at all. Without a result
// gate the result buttons follow the 👍 / 👎 gate.
func TestFeedbackSections_TwoGatesHideTheirOwnParts(t *testing.T) {
	ip, _ := newTurnFeedbackPlatform(t)
	dir := t.TempDir()
	turn := filepath.Join(dir, "turn-feedback")
	result := filepath.Join(dir, "result-feedback")
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	labels := func() string {
		form, _ := ip.turnFeedbackElement("k")
		if form == nil {
			return ""
		}
		raw, _ := json.Marshal(form)
		var parts []string
		if strings.Contains(string(raw), `"fb:up"`) {
			parts = append(parts, "turn")
		}
		if strings.Contains(string(raw), `"fb:note"`) != strings.Contains(string(raw), `"fb:up"`) {
			t.Fatalf("the comment box must go with 👍 / 👎: %s", raw)
		}
		if strings.Contains(string(raw), `"fb:success"`) {
			parts = append(parts, "result")
		}
		return strings.Join(parts, "+")
	}

	ip.turnFeedbackGate = turn
	if got := labels(); got != "turn+result" {
		t.Fatalf("no result gate, no files: %q", got)
	}
	write(turn, "off\n")
	if got := labels(); got != "" || ip.TurnFeedbackEnabled() {
		t.Fatalf("no result gate, turn off hides everything: %q", got)
	}

	ip.resultGate = result
	if got := labels(); got != "result" {
		t.Fatalf("turn off, no result file (on): %q", got)
	}
	write(result, "off\n")
	if got := labels(); got != "" || ip.TurnFeedbackEnabled() {
		t.Fatalf("both off: %q", got)
	}
	write(turn, "on\n")
	if got := labels(); got != "turn" || !ip.TurnFeedbackEnabled() {
		t.Fatalf("turn on, result off: %q", got)
	}
	write(result, "on\n")
	if got := labels(); got != "turn+result" {
		t.Fatalf("both on: %q", got)
	}
	write(turn, "off\n")
	if got := labels(); got != "result" || !ip.TurnFeedbackEnabled() {
		t.Fatalf("turn off, result on: %q", got)
	}
}

// A click redraws the card with the parts it was sent with, whatever the
// gates say now; a card stored without the part flags has both.
func TestTurnFeedback_RedrawKeepsTheCardsParts(t *testing.T) {
	ip, calls := newTurnFeedbackPlatform(t)
	ip.feedbackCards.remember("om_reply", "feishu:oc_chat:root:om_root", "the answer", "", feedbackSections{Turn: true})
	resp, _ := feedbackClick(ip, "up", nil)
	<-calls
	buttons, _ := buttonsOf(t, resp)
	if _, ok := buttons["fb_success"]; ok || buttons["fb_up"] == "" {
		t.Fatalf("a card sent without the result buttons gained them: %v", buttons)
	}

	var stored map[string]*feedbackCard
	if err := json.Unmarshal([]byte(`{"om_reply":{"session_key":"feishu:oc_chat:root:om_root","markdown":"the answer","at":1}}`), &stored); err != nil {
		t.Fatal(err)
	}
	ip.feedbackCards.mu.Lock()
	ip.feedbackCards.items = stored
	ip.feedbackCards.mu.Unlock()
	resp, _ = feedbackClick(ip, "down", nil)
	<-calls
	buttons, _ = buttonsOf(t, resp)
	if buttons["fb_success"] == "" || buttons["fb_down"] == "" {
		t.Fatalf("a card stored without part flags must keep both parts: %v", buttons)
	}
}

// The final reply card ends with the form, after the footer when there is
// one and right after the body's rule when there is not.
func TestBuildFinalReplyCardJSON_AppendsTheFeedbackForm(t *testing.T) {
	elementsOf := func(cardJSON string) []any {
		var card map[string]any
		if err := json.Unmarshal([]byte(cardJSON), &card); err != nil {
			t.Fatalf("decode card json: %v", err)
		}
		return card["body"].(map[string]any)["elements"].([]any)
	}
	form := turnFeedbackForm("feishu:oc_chat:root:om_root", "", "", feedbackSections{Turn: true, Result: true})
	withFooter := elementsOf(buildFinalReplyCardJSON("body", "model · 4%", form))
	if len(withFooter) != 4 || withFooter[3].(map[string]any)["tag"] != "form" {
		t.Fatalf("with footer: elements = %#v", withFooter)
	}
	bare := elementsOf(buildFinalReplyCardJSON("body", "", form))
	if len(bare) != 3 || bare[1].(map[string]any)["tag"] != "hr" || bare[2].(map[string]any)["tag"] != "form" {
		t.Fatalf("without footer: elements = %#v", bare)
	}
	raw, _ := json.Marshal(form)
	for _, want := range []string{`"fb:up"`, `"fb:down"`, `"fb:note"`, `"name":"comment"`, `"max_length":1000`, `"session_key":"feishu:oc_chat:root:om_root"`, `"form_action_type":"submit"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("form lacks %s: %s", want, raw)
		}
	}
	if buildFinalReplyCardJSON("body", "", nil) != buildCardJSON("body") {
		t.Error("no footer and no form must stay a plain card")
	}
}

// fakeFeishu records what is sent and patched; every send gets a fresh id.
type fakeFeishu struct {
	mu      sync.Mutex
	sent    []map[string]string // msg_type, content, id
	patched map[string]string   // message id -> card
}

func newFakeFeishuPlatform(t *testing.T) (*Platform, *fakeFeishu) {
	t.Helper()
	fake := &fakeFeishu{patched: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		var req struct {
			MsgType       string `json:"msg_type"`
			Content       string `json:"content"`
			ReplyInThread bool   `json:"reply_in_thread"`
		}
		_ = json.Unmarshal(body, &req)
		fake.mu.Lock()
		defer fake.mu.Unlock()
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "t"})
		case (r.URL.Path == "/open-apis/im/v1/messages" || (strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/") && strings.HasSuffix(r.URL.Path, "/reply"))) && r.Method == http.MethodPost:
			id := "om_sent_" + string(rune('a'+len(fake.sent)))
			fake.sent = append(fake.sent, map[string]string{"msg_type": req.MsgType, "content": req.Content, "id": id, "reply_in_thread": fmt.Sprint(req.ReplyInThread), "path": r.URL.Path})
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"message_id": id}})
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/") && r.Method == http.MethodPatch:
			fake.patched[strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/")] = req.Content
			writeJSON(t, w, map[string]any{"code": 0})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	p := &Platform{
		platformName:       "feishu",
		domain:             srv.URL,
		appID:              "cli_fb",
		appSecret:          "secret",
		useInteractiveCard: true,
		turnFeedbackExec:   "my-feedback-cmd",
		client:             lark.NewClient("cli_fb", "secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		replayClient:       lark.NewClient("cli_fb", "secret", lark.WithEnableTokenCache(false), lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
	}
	return p, fake
}

// An agent that answers and then makes one last tool call has nothing left
// to send when the turn ends: the controls go onto the answer it sent.
func TestAttachTurnFeedback_PatchesTheAnswerThatAlreadyWentOut(t *testing.T) {
	p, fake := newFakeFeishuPlatform(t)
	ctx := context.Background()
	rc := replyContext{chatID: "oc_chat", sessionKey: "feishu:oc_chat:root:om_root"}
	if err := p.Send(ctx, rc, "**markdown** answer"); err != nil {
		t.Fatal(err)
	}
	if fake.sent[0]["msg_type"] != larkim.MsgTypeInteractive {
		t.Fatalf("a markdown answer goes as a card, got %q", fake.sent[0]["msg_type"])
	}
	if err := p.AttachTurnFeedback(ctx, rc); err != nil {
		t.Fatal(err)
	}
	card := fake.patched[fake.sent[0]["id"]]
	if !strings.Contains(card, "markdown") || !strings.Contains(card, `"fb:up"`) {
		t.Fatalf("patched card lacks the answer or the controls: %s", card)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("nothing more is sent, got %d messages", len(fake.sent))
	}
	// Used once: a second attach has nothing to patch and sends the controls
	// on their own.
	if err := p.AttachTurnFeedback(ctx, rc); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 2 || !strings.Contains(fake.sent[1]["content"], `"fb:down"`) {
		t.Fatalf("expected a controls-only card, got %#v", fake.sent)
	}
}

// An answer that cannot be a card (an @mention) is never patched -- not
// even a card from earlier in the session; the controls come right under it.
func TestAttachTurnFeedback_MentionKeepsTextAndControlsFollow(t *testing.T) {
	p, fake := newFakeFeishuPlatform(t)
	ctx := context.Background()
	rc := replyContext{chatID: "oc_chat", sessionKey: "feishu:oc_chat:root:om_root"}
	if err := p.Send(ctx, rc, "**earlier** card"); err != nil {
		t.Fatal(err)
	}
	if err := p.Send(ctx, rc, `ping <at user_id="ou_x">x</at>`); err != nil {
		t.Fatal(err)
	}
	if fake.sent[1]["msg_type"] != larkim.MsgTypeText {
		t.Fatalf("a mention stays text, got %q", fake.sent[1]["msg_type"])
	}
	if err := p.AttachTurnFeedback(ctx, rc); err != nil {
		t.Fatal(err)
	}
	if len(fake.patched) != 0 {
		t.Fatalf("nothing may be patched, got %#v", fake.patched)
	}
	if len(fake.sent) != 3 || !strings.Contains(fake.sent[2]["content"], `"fb:up"`) {
		t.Fatalf("expected the controls on their own, got %#v", fake.sent)
	}
}

// With the controls off nothing changes: text stays text, nothing attaches.
func TestAttachTurnFeedback_OffChangesNothing(t *testing.T) {
	p, fake := newFakeFeishuPlatform(t)
	p.turnFeedbackExec = ""
	ctx := context.Background()
	rc := replyContext{chatID: "oc_chat", sessionKey: "k"}
	if err := p.Send(ctx, rc, "plain"); err != nil {
		t.Fatal(err)
	}
	if err := p.AttachTurnFeedback(ctx, rc); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 1 || fake.sent[0]["msg_type"] != larkim.MsgTypeText || len(fake.patched) != 0 {
		t.Fatalf("controls off must leave sending as it was: %#v %#v", fake.sent, fake.patched)
	}
}

// An answer streamed as a preview card and frozen before the last tool call
// takes the controls in place.
func TestAttachTurnFeedback_PatchesAFrozenPreview(t *testing.T) {
	p, fake := newFakeFeishuPlatform(t)
	ctx := context.Background()
	h := &feishuPreviewHandle{messageID: "om_preview", chatID: "oc_chat", sessionKey: "feishu:oc_chat:root:om_root"}
	if err := p.UpdateMessage(ctx, h, "streamed answer"); err != nil {
		t.Fatal(err)
	}
	rc := replyContext{chatID: "oc_chat", sessionKey: "feishu:oc_chat:root:om_root"}
	if err := p.AttachTurnFeedback(ctx, rc); err != nil {
		t.Fatal(err)
	}
	if card := fake.patched["om_preview"]; !strings.Contains(card, "streamed answer") || !strings.Contains(card, `"fb:note"`) {
		t.Fatalf("preview not patched with the controls: %q", card)
	}
	if len(fake.sent) != 0 {
		t.Fatalf("nothing more is sent, got %#v", fake.sent)
	}
}

// buttonsOf returns each button's text and type in a redrawn card.
func buttonsOf(t *testing.T, resp *callback.CardActionTriggerResponse) (map[string]string, string) {
	t.Helper()
	if resp == nil || resp.Card == nil {
		t.Fatalf("the click must redraw the card, got %#v", resp)
	}
	raw, _ := json.Marshal(resp.Card.Data)
	buttons := map[string]string{}
	var walk func(any)
	comment := ""
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if x["tag"] == "button" {
				text := x["text"].(map[string]any)["content"].(string)
				buttons[x["name"].(string)] = text + "|" + x["type"].(string)
			}
			if x["tag"] == "input" {
				comment, _ = x["default_value"].(string)
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	var data any
	_ = json.Unmarshal(raw, &data)
	walk(data)
	if !strings.Contains(string(raw), "the answer") {
		t.Fatalf("the redrawn card lost its answer: %s", raw)
	}
	return buttons, comment
}

// The last choice stays highlighted on the card and every button stays live:
// clicking again moves the highlight, a comment alone keeps it, and the box
// keeps the last comment.
func TestTurnFeedback_LastChoiceIsHighlightedAndCanChange(t *testing.T) {
	ip, calls := newTurnFeedbackPlatform(t)
	ip.feedbackCards.remember("om_reply", "feishu:oc_chat:root:om_root", "the answer", "model · 4%", feedbackSections{Turn: true, Result: true})

	resp, _ := feedbackClick(ip, "up", nil)
	<-calls
	buttons, _ := buttonsOf(t, resp)
	if buttons["fb_up"] != "✓ 👍 有用|primary_filled" || buttons["fb_down"] != "👎 没用|default" {
		t.Fatalf("after 👍: %v", buttons)
	}
	resp, _ = feedbackClick(ip, "down", map[string]any{"comment": "漏掉了一个边界情况"})
	<-calls
	buttons, comment := buttonsOf(t, resp)
	if buttons["fb_down"] != "✓ 👎 没用|danger_filled" || buttons["fb_up"] != "👍 有用|default" {
		t.Fatalf("after 👎: %v", buttons)
	}
	if comment != "漏掉了一个边界情况" {
		t.Fatalf("the box keeps the comment, got %q", comment)
	}
	resp, _ = feedbackClick(ip, "note", map[string]any{"comment": "补充一句"})
	<-calls
	buttons, comment = buttonsOf(t, resp)
	if buttons["fb_down"] != "✓ 👎 没用|danger_filled" || comment != "补充一句" {
		t.Fatalf("a comment alone keeps the verdict: %v %q", buttons, comment)
	}
}

// What was chosen survives a restart of the bridge when a state file is set.
func TestFeedbackCards_SurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turn-feedback.json")
	first := &feedbackCards{path: path}
	first.remember("om_1", "k", "body", "", feedbackSections{Turn: true})
	if _, ok := first.choose("om_1", "down", "c"); !ok {
		t.Fatal("remembered card unknown")
	}
	second := &feedbackCards{path: path}
	card, ok := second.choose("om_1", "", "")
	if !ok || card.Verdict != "down" || card.Comment != "c" || card.Markdown != "body" {
		t.Fatalf("after restart: %#v %v", card, ok)
	}
	if card.sections() != (feedbackSections{Turn: true}) {
		t.Fatalf("after restart the card lost its parts: %#v", card.sections())
	}
	if _, ok := second.choose("om_unknown", "up", ""); ok {
		t.Fatal("an unknown card must not be redrawn")
	}
}

// Turning the controls on does not change how anything is sent: a plain
// text answer stays text, and its controls come as a card of their own.
func TestAttachTurnFeedback_PlainTextStaysText(t *testing.T) {
	p, fake := newFakeFeishuPlatform(t)
	ctx := context.Background()
	rc := replyContext{chatID: "oc_chat", sessionKey: "feishu:oc_chat:root:om_root"}
	if err := p.Send(ctx, rc, "plain answer"); err != nil {
		t.Fatal(err)
	}
	if fake.sent[0]["msg_type"] != larkim.MsgTypeText {
		t.Fatalf("plain text must stay text, got %q", fake.sent[0]["msg_type"])
	}
	if err := p.AttachTurnFeedback(ctx, rc); err != nil {
		t.Fatal(err)
	}
	if len(fake.patched) != 0 || len(fake.sent) != 2 || !strings.Contains(fake.sent[1]["content"], `"fb:up"`) {
		t.Fatalf("expected the controls on their own: sent %#v patched %#v", fake.sent, fake.patched)
	}
}

// A final reply with a real @mention stays text, and the controls follow it.
func TestSendWithStatusFooter_MentionReplyGetsControlsAfterIt(t *testing.T) {
	p, fake := newFakeFeishuPlatform(t)
	ctx := context.Background()
	rc := replyContext{chatID: "oc_chat", sessionKey: "feishu:oc_chat:root:om_root"}
	if err := p.SendWithStatusFooter(ctx, rc, `done <at user_id="ou_x">x</at>`, "model"); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 2 || fake.sent[0]["msg_type"] != larkim.MsgTypeText || !strings.Contains(fake.sent[1]["content"], `"fb:down"`) {
		t.Fatalf("expected text then controls: %#v", fake.sent)
	}
}

// Clicks are submitted one at a time in the order they came, so an earlier
// slow 👍 cannot land after the 👎 that replaced it.
func TestTurnFeedback_ClicksAreSubmittedInOrder(t *testing.T) {
	ip, _ := newAllowFromTestPlatform(t, "")
	ip.turnFeedbackExec = "my-feedback-cmd"
	var mu sync.Mutex
	var order []string
	done := make(chan struct{}, 2)
	ip.turnFeedbackRun = func(_ string, args []string) {
		if args[3] == "up" {
			time.Sleep(200 * time.Millisecond)
		}
		mu.Lock()
		order = append(order, args[3])
		mu.Unlock()
		done <- struct{}{}
	}
	if _, err := feedbackClick(ip, "up", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := feedbackClick(ip, "down", nil); err != nil {
		t.Fatal(err)
	}
	<-done
	<-done
	if strings.Join(order, ",") != "up,down" {
		t.Fatalf("submitted out of order: %v", order)
	}
}

func TestTurnFeedback_OverallResults(t *testing.T) {
	for _, outcome := range []string{"success", "fail", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			ip, calls := newTurnFeedbackPlatform(t)
			resp, err := feedbackClick(ip, outcome, nil)
			if err != nil || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
				t.Fatalf("result click = %#v, %v", resp, err)
			}
			select {
			case call := <-calls:
				want := []string{"--message", "om_reply", "--outcome", outcome, "--operator", "ou_clicker"}
				if strings.Join(call.args, "\x00") != strings.Join(want, "\x00") {
					t.Fatalf("args = %q, want %q", call.args, want)
				}
			case <-time.After(time.Second):
				t.Fatal("result feedback never ran")
			}
		})
	}
	form, _ := json.Marshal(turnFeedbackForm("session", "", "", feedbackSections{Turn: true, Result: true}))
	for _, label := range []string{"任务整体结果评价", "成功", "失败", "终止", "👍 有用", "👎 没用"} {
		if !strings.Contains(string(form), label) {
			t.Errorf("missing %q in %s", label, form)
		}
	}
}

func TestTurnFeedback_ResultCommandPostsReturnedCard(t *testing.T) {
	p, fake := newFakeFeishuPlatform(t)
	script := filepath.Join(t.TempDir(), "feedback")
	card := `{"score_card":{"schema":"2.0","body":{"elements":[{"tag":"markdown","content":"已反馈，可重复提交"},{"tag":"form","name":"score","elements":[{"tag":"button","name":"submit","form_action_type":"submit","behaviors":[{"type":"callback","value":{"action":"cmd:/score 2"}}]}]}]}}}`
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho diagnostic >&2\nprintf '%s' '"+card+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p.turnFeedbackExec = script
	p.threadIsolation = true
	p.runTurnFeedback("feishu:oc_chat:root:om_root", []string{"--message", "om_reply", "--outcome", "success"})
	fake.mu.Lock()
	if len(fake.sent) != 1 || fake.sent[0]["msg_type"] != "interactive" || !strings.Contains(fake.sent[0]["content"], "已反馈，可重复提交") {
		t.Fatalf("expected the returned score card in the topic, got %#v", fake.sent)
	}
	sent := fake.sent[0]["content"]
	fake.mu.Unlock()
	var posted map[string]any
	if err := json.Unmarshal([]byte(sent), &posted); err != nil {
		t.Fatal(err)
	}
	form := posted["body"].(map[string]any)["elements"].([]any)[1].(map[string]any)
	button := form["elements"].([]any)[0].(map[string]any)
	value := button["behaviors"].([]any)[0].(map[string]any)["value"].(map[string]any)
	ip, messages := newAllowFromTestPlatform(t, "")
	ip.threadIsolation = true
	ip.userNameCache.Store("ou_clicker", "tester")
	ip.chatNameCache.Store("oc_chat", "test chat")
	_, err := ip.onCardAction(&callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_clicker"},
		Action:   &callback.CallBackAction{Value: value, Name: "submit", FormValue: map[string]any{"outcome": "success"}},
		Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_sent_a"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	msg := waitAnyMessage(t, messages, time.Second, "returned score form")
	if msg.SessionKey != "feishu:oc_chat:root:om_root" {
		t.Fatalf("score submit lost topic: %q", msg.SessionKey)
	}
	if err := p.Reply(context.Background(), msg.ReplyCtx, "已提交"); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sent) != 2 || fake.sent[1]["reply_in_thread"] != "true" {
		t.Fatalf("score confirmation must stay in the thread: %#v", fake.sent)
	}
}
