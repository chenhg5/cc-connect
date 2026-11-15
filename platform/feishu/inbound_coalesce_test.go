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
