package teams

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

var errBoom = errors.New("boom")

func TestImageActivity_BuildsDataURIAttachment(t *testing.T) {
	rc := replyContext{
		conversationID: "c1",
		botAccount:     channelAccount{ID: "bot"},
		userAccount:    channelAccount{ID: "user"},
	}
	data := []byte{0x89, 0x50, 0x4e, 0x47}
	a := imageActivity(rc, core.ImageAttachment{MimeType: "image/png", Data: data, FileName: "chart.png"})

	if len(a.Attachments) != 1 {
		t.Fatalf("want 1 attachment, got %d", len(a.Attachments))
	}
	att := a.Attachments[0]
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	if att.ContentURL != want {
		t.Errorf("contentUrl = %q, want %q", att.ContentURL, want)
	}
	if att.ContentType != "image/png" || att.Name != "chart.png" {
		t.Errorf("contentType/name = %q/%q", att.ContentType, att.Name)
	}
	// Conversation-reference envelope carried through.
	if a.Type != "message" || a.From == nil || a.From.ID != "bot" || a.Recipient == nil || a.Recipient.ID != "user" {
		t.Errorf("envelope missing: %+v", a)
	}
}

func TestImageActivity_DefaultsMimeAndName(t *testing.T) {
	a := imageActivity(replyContext{}, core.ImageAttachment{Data: []byte{1, 2, 3}})
	att := a.Attachments[0]
	if att.ContentType != "image/png" || att.Name != "image.png" {
		t.Errorf("defaults not applied: contentType=%q name=%q", att.ContentType, att.Name)
	}
	if !strings.HasPrefix(att.ContentURL, "data:image/png;base64,") {
		t.Errorf("contentUrl prefix wrong: %q", att.ContentURL)
	}
}

func TestSendImage_ThreadsToOriginatingActivity(t *testing.T) {
	fs := &fakeSender{}
	p := &Platform{conn: fs}
	rc := replyContext{serviceURL: "https://s/", conversationID: "c1", activityID: "a1"}

	if err := p.SendImage(context.Background(), rc, core.ImageAttachment{Data: []byte{1, 2, 3}, MimeType: "image/png"}); err != nil {
		t.Fatalf("SendImage: %v", err)
	}
	if len(fs.replied) != 1 || fs.repliedToID[0] != "a1" {
		t.Fatalf("want threaded reply to a1, got %+v / %v", fs.replied, fs.repliedToID)
	}
	if len(fs.replied[0].Attachments) != 1 || fs.replied[0].Attachments[0].ContentURL == "" {
		t.Errorf("image attachment missing: %+v", fs.replied[0])
	}
}

func TestSendImage_UnthreadedUsesSend(t *testing.T) {
	fs := &fakeSender{}
	p := &Platform{conn: fs}
	rc := replyContext{serviceURL: "https://s/", conversationID: "c1"} // no activityID

	if err := p.SendImage(context.Background(), rc, core.ImageAttachment{Data: []byte{1, 2, 3}}); err != nil {
		t.Fatalf("SendImage: %v", err)
	}
	if len(fs.replied) != 0 {
		t.Fatalf("should not thread without activityID: %+v", fs.replied)
	}
	if len(fs.last.Attachments) != 1 || fs.last.Attachments[0].ContentURL == "" {
		t.Errorf("image attachment missing on send: %+v", fs.last)
	}
}

func TestSendImage_OversizeSendsNoticeNotImage(t *testing.T) {
	fs := &fakeSender{}
	p := &Platform{conn: fs}
	rc := replyContext{serviceURL: "https://s/", conversationID: "c1", activityID: "a1"}
	big := make([]byte, maxOutboundImageBytes+1)

	if err := p.SendImage(context.Background(), rc, core.ImageAttachment{Data: big, MimeType: "image/png"}); err != nil {
		t.Fatalf("SendImage: %v", err)
	}
	if len(fs.replied) != 1 {
		t.Fatalf("want one notice reply, got %+v", fs.replied)
	}
	got := fs.replied[0]
	if got.Text != oversizeImageNotice {
		t.Errorf("notice text = %q, want %q", got.Text, oversizeImageNotice)
	}
	if len(got.Attachments) != 0 {
		t.Errorf("oversize should carry no attachment: %+v", got.Attachments)
	}
}

func TestSendImage_ConnectorTooLargeDegradesToNotice(t *testing.T) {
	// Inject the WRAPPED form the connector actually returns (do() wraps with %w),
	// so this exercises the errors.Is unwrap rather than a bare-equality match.
	fs := &fakeSender{id: "m1", attachErr: fmt.Errorf("teams: connector returned 413: %w", errActivityTooLarge)}
	p := &Platform{conn: fs}
	rc := replyContext{serviceURL: "https://s/", conversationID: "c1", activityID: "a1"}

	if err := p.SendImage(context.Background(), rc, core.ImageAttachment{Data: []byte{1, 2, 3}, MimeType: "image/png"}); err != nil {
		t.Fatalf("a 413 from the connector should degrade to a notice, got %v", err)
	}
	// First reply is the image (rejected 413); the notice text follows.
	if len(fs.replied) != 2 {
		t.Fatalf("want image attempt + notice reply, got %d", len(fs.replied))
	}
	notice := fs.replied[1]
	if notice.Text != oversizeImageNotice || len(notice.Attachments) != 0 {
		t.Errorf("last reply should be the plain notice, got %+v", notice)
	}
}

func TestSendImage_InvalidReplyCtx(t *testing.T) {
	p := &Platform{conn: &fakeSender{}}
	if err := p.SendImage(context.Background(), "not-a-reply-ctx", core.ImageAttachment{Data: []byte{1}}); err == nil {
		t.Fatal("want error for invalid reply context")
	}
}

func TestSendImage_PropagatesConnectorError(t *testing.T) {
	img := core.ImageAttachment{Data: []byte{1, 2, 3}}
	// Threaded path (activityID set) routes through replyTo; unthreaded through send.
	for _, tc := range []struct {
		name       string
		activityID string
	}{
		{"threaded", "a1"},
		{"unthreaded", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeSender{err: errBoom}
			p := &Platform{conn: fs}
			rc := replyContext{serviceURL: "https://s/", conversationID: "c1", activityID: tc.activityID}
			if err := p.SendImage(context.Background(), rc, img); err != errBoom {
				t.Fatalf("want errBoom propagated, got %v", err)
			}
		})
	}
}
