package feishu

import (
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func newInboundTestPlatform(t *testing.T, window int) (*Platform, chan *core.Message) {
	t.Helper()
	created, err := New(map[string]any{
		"app_id": "unit-test-app", "app_secret": "unit-test-secret",
		"enable_feishu_card": false, "inbound_coalesce_window_ms": window,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := created.(*Platform)
	got := make(chan *core.Message, 10)
	p.handler = func(_ core.Platform, msg *core.Message) { got <- msg }
	return p, got
}

func inboundTestMessage(id, text string) *core.Message {
	return &core.Message{SessionKey: "feishu:test:user", UserID: "user", MessageID: id, Content: text}
}

func TestInboundCoalesce_DefaultDispatchesImmediately(t *testing.T) {
	p, got := newInboundTestPlatform(t, 0)
	p.dispatchCoreMessage(inboundTestMessage("text", "Inspect this"))
	if len(got) != 1 {
		t.Fatal("disabled coalescing delayed a message")
	}
}

func TestInboundCoalesce_WindowValidation(t *testing.T) {
	for _, window := range []int{-1, 60001} {
		_, err := New(map[string]any{
			"app_id": "unit-test-app", "app_secret": "unit-test-secret",
			"inbound_coalesce_window_ms": window,
		})
		if err == nil {
			t.Fatalf("accepted invalid window %d", window)
		}
	}
}

func TestInboundCoalesce_TextAndImagesBecomeOneRequest(t *testing.T) {
	p, got := newInboundTestPlatform(t, 60000)
	accepted := 0
	text := inboundTestMessage("text", "Inspect these images")
	text.UserMessageTimeMs = 100
	text.OnAccepted = func() { accepted++ }
	image := inboundTestMessage("image", "")
	image.Images = []core.ImageAttachment{{MimeType: "image/png", Data: []byte("first")}}
	image.UserMessageTimeMs = 200
	image.OnAccepted = func() { accepted++ }
	second := inboundTestMessage("second", "")
	second.Images = []core.ImageAttachment{{MimeType: "image/png", Data: []byte("second")}}
	second.UserMessageTimeMs = 300
	p.dispatchCoreMessage(text)
	p.dispatchCoreMessage(image)
	p.dispatchCoreMessage(second)
	if len(got) != 0 {
		t.Fatal("a burst was dispatched before it could be combined")
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d requests, want one", len(got))
	}
	merged := <-got
	if merged.Content != text.Content || len(merged.Images) != 2 {
		t.Fatalf("lost text or images: %#v", merged)
	}
	if merged.MessageID != "second" || merged.UserMessageTimeMs != 300 {
		t.Fatal("merged request did not retain the newest ordering metadata")
	}
	merged.OnAccepted()
	merged.OnAccepted()
	if accepted != 2 {
		t.Fatalf("accepted callbacks ran %d times, want two", accepted)
	}
}

func TestInboundCoalesce_ImageThenText(t *testing.T) {
	p, got := newInboundTestPlatform(t, 60000)
	image := inboundTestMessage("image", "")
	image.Images = []core.ImageAttachment{{Data: []byte("image")}}
	p.dispatchCoreMessage(image)
	p.dispatchCoreMessage(inboundTestMessage("text", "Explain this"))
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d requests, want one", len(got))
	}
	merged := <-got
	if merged.Content != "Explain this" || len(merged.Images) != 1 {
		t.Fatal("image-first burst was not preserved")
	}
}

func TestInboundCoalesce_CommandFlushesBeforeImmediateDispatch(t *testing.T) {
	p, got := newInboundTestPlatform(t, 60000)
	p.dispatchCoreMessage(inboundTestMessage("text", "pending"))
	p.dispatchCoreMessage(inboundTestMessage("command", "/new"))
	if len(got) != 2 {
		t.Fatal("command did not immediately flush the preceding request")
	}
	if (<-got).Content != "pending" || (<-got).Content != "/new" {
		t.Fatal("command overtook pending text")
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestInboundCoalesce_DifferentSendersAreNotCombined(t *testing.T) {
	p, got := newInboundTestPlatform(t, 60000)
	p.dispatchCoreMessage(inboundTestMessage("first", "first sender"))
	other := inboundTestMessage("other", "second sender")
	other.UserID = "other-user"
	p.dispatchCoreMessage(other)
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || (<-got).Content != "first sender" || (<-got).Content != "second sender" {
		t.Fatal("messages from different senders were mixed or reordered")
	}
}

func TestInboundCoalesce_RecalledMemberIsExcluded(t *testing.T) {
	p, got := newInboundTestPlatform(t, 60000)
	p.dispatchCoreMessage(inboundTestMessage("recalled", "remove this text"))
	image := inboundTestMessage("image", "")
	image.Images = []core.ImageAttachment{{Data: []byte("image")}}
	p.dispatchCoreMessage(image)
	p.markMessageRecalled("recalled")
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d requests, want one", len(got))
	}
	merged := <-got
	if merged.Content != "" || len(merged.Images) != 1 {
		t.Fatal("recalled content was retained in the merged request")
	}
}

func TestInboundCoalesce_PermissionResponseIsImmediate(t *testing.T) {
	p, got := newInboundTestPlatform(t, 60000)
	message := inboundTestMessage("permission", "allow")
	message.IsPermissionResponse = true
	p.dispatchCoreMessage(message)
	if len(got) != 1 || !(<-got).IsPermissionResponse {
		t.Fatal("permission response was delayed")
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestInboundCoalesce_StopInvalidatesPendingTimer(t *testing.T) {
	p, got := newInboundTestPlatform(t, 20)
	p.dispatchCoreMessage(inboundTestMessage("text", "pending"))
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("shutdown and timer emitted %d requests, want one", len(got))
	}
}

func TestInboundCoalesce_CommandCannotOvertakeTimerDelivery(t *testing.T) {
	p, _ := newInboundTestPlatform(t, 1)
	started, release, commandDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	p.handler = func(_ core.Platform, msg *core.Message) {
		if msg.Content == "pending" {
			close(started)
			<-release
		}
	}
	p.dispatchCoreMessage(inboundTestMessage("text", "pending"))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timer did not start delivering the pending message")
	}
	go func() {
		p.dispatchCoreMessage(inboundTestMessage("command", "/new"))
		close(commandDone)
	}()
	select {
	case <-commandDone:
		close(release)
		t.Fatal("command overtook an in-flight batch delivery")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-commandDone:
	case <-time.After(time.Second):
		t.Fatal("command was not dispatched after the preceding batch")
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
}

// newCardCoalesceTestPlatform builds a card-enabled platform with a long
// coalescing window, so anything that gets buffered stays buffered until Stop.
func newCardCoalesceTestPlatform(t *testing.T) (*interactivePlatform, chan *core.Message) {
	t.Helper()
	created, err := New(map[string]any{
		"app_id": "unit-test-app", "app_secret": "unit-test-secret",
		"enable_feishu_card": true, "inbound_coalesce_window_ms": 60000,
		"domain": stubOpenAPIDomain(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	ip := created.(*interactivePlatform)
	got := make(chan *core.Message, 10)
	ip.handler = func(_ core.Platform, msg *core.Message) { got <- msg }
	return ip, got
}

// cardChatMessage is a plain text message from the same user and chat that
// the card action in cardActionEvent comes from.
func cardChatMessage(p *Platform, id, text string) *core.Message {
	sessionKey := p.sessionKeyFromCardAction("oc_test_chat", "ou_user", nil)
	return &core.Message{
		SessionKey: sessionKey, UserID: "ou_user", MessageID: id, Content: text,
		ReplyCtx: replyContext{messageID: id, chatID: "oc_test_chat", sessionKey: sessionKey},
	}
}

// waitCardActionHandled waits until the asynchronous card dispatch has either
// reached the handler or been buffered for the session.
func waitCardActionHandled(t *testing.T, p *Platform, got chan *core.Message, sessionKey string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p.inboundCoalesceMu.Lock()
		buffered := p.inboundCoalesce[sessionKey] != nil
		p.inboundCoalesceMu.Unlock()
		if buffered || len(got) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("card action was neither dispatched nor buffered")
}

func TestInboundCoalesce_AskQuestionClickIsNotMergedWithFollowingText(t *testing.T) {
	ip, got := newCardCoalesceTestPlatform(t)
	text := cardChatMessage(ip.Platform, "text", "and one more thing")
	if _, err := ip.onCardAction(cardActionEvent("ou_user", "askq:0:1")); err != nil {
		t.Fatal(err)
	}
	waitCardActionHandled(t, ip.Platform, got, text.SessionKey)
	ip.dispatchCoreMessage(text)
	if err := ip.Stop(); err != nil {
		t.Fatal(err)
	}
	var contents []string
	for len(got) > 0 {
		contents = append(contents, (<-got).Content)
	}
	if len(contents) != 2 || contents[0] != "askq:0:1" || contents[1] != "and one more thing" {
		t.Fatalf("dispatched %q; want the unmodified option click, then the text", contents)
	}
}

func TestInboundCoalesce_CardCallbackFlushesPendingTextFirst(t *testing.T) {
	ip, got := newCardCoalesceTestPlatform(t)
	ip.dispatchCoreMessage(cardChatMessage(ip.Platform, "text", "pending"))
	if _, err := ip.onCardAction(cardActionEvent("ou_user", "askq:0:1")); err != nil {
		t.Fatal(err)
	}
	first := waitAnyMessage(t, got, 2*time.Second, "pending text flushed by the click")
	second := waitAnyMessage(t, got, 2*time.Second, "option click")
	if first.Content != "pending" || second.Content != "askq:0:1" {
		t.Fatalf("dispatch order = %q, %q; want pending text, then the unmodified click",
			first.Content, second.Content)
	}
	if err := ip.Stop(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d extra requests", len(got))
	}
}

func TestInboundCoalesce_AskQuestionContentIsNeverBuffered(t *testing.T) {
	p, got := newInboundTestPlatform(t, 60000)
	p.dispatchCoreMessage(inboundTestMessage("askq", "askq:0:1"))
	if len(got) != 1 || (<-got).Content != "askq:0:1" {
		t.Fatal("askq: callback was buffered")
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
}
