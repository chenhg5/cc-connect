package core

import (
	"context"
	"testing"
)

// stubFeedbackPlatform is a footer-capable platform that ends final replies
// with feedback controls.
type stubFeedbackPlatform struct {
	stubFooterSendingPlatform
	feedback bool
}

func (p *stubFeedbackPlatform) TurnFeedbackEnabled() bool { return p.feedback }

// With feedback controls on, the final reply takes the footer path even
// when there is no footer text: that is where the controls are rendered.
func TestSendChunksWithStatusFooter_FeedbackControlsUseFooterPathWithoutFooter(t *testing.T) {
	send := func(pl Platform, rctx any, content string) error {
		return pl.Send(context.Background(), rctx, content)
	}
	on := &stubFeedbackPlatform{stubFooterSendingPlatform: stubFooterSendingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}, feedback: true}
	if !sendChunksWithStatusFooter(context.Background(), on, "ctx", "hello body", "", send) {
		t.Fatal("expected success")
	}
	if len(on.footerCalls) != 1 || on.footerCalls[0] != "hello body|FOOTER|" {
		t.Fatalf("footer calls = %#v, want the body with an empty footer", on.footerCalls)
	}
	if got := on.getSent(); len(got) != 0 {
		t.Fatalf("plain Send = %#v, want none", got)
	}

	off := &stubFeedbackPlatform{stubFooterSendingPlatform: stubFooterSendingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}
	if !sendChunksWithStatusFooter(context.Background(), off, "ctx", "hello body", "", send) {
		t.Fatal("expected success")
	}
	if len(off.footerCalls) != 0 {
		t.Fatalf("controls off and no footer must send plainly, got footer calls %#v", off.footerCalls)
	}
	if got := off.getSent(); len(got) != 1 || got[0] != "hello body" {
		t.Fatalf("plain Send = %#v", got)
	}
}

type stubAttachingPlatform struct {
	stubFeedbackPlatform
	attached int
}

func (p *stubAttachingPlatform) AttachTurnFeedback(context.Context, any) error {
	p.attached++
	return nil
}

// A turn whose answer went out before its last tool call has no final reply
// to carry the controls; the platform is asked to attach them, only when on.
func TestAttachTurnFeedback_OnlyWhenTheControlsAreOn(t *testing.T) {
	on := &stubAttachingPlatform{stubFeedbackPlatform: stubFeedbackPlatform{feedback: true}}
	attachTurnFeedback(context.Background(), on, "ctx")
	if on.attached != 1 {
		t.Fatalf("attached %d times, want 1", on.attached)
	}
	off := &stubAttachingPlatform{}
	attachTurnFeedback(context.Background(), off, "ctx")
	if off.attached != 0 {
		t.Fatalf("controls off must not attach, attached %d", off.attached)
	}
	attachTurnFeedback(context.Background(), &stubPlatformEngine{n: "plain"}, "ctx")
}
