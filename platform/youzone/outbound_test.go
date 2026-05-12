package youzone

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeCard(t *testing.T, out outboundMessage) youzoneCardData {
	t.Helper()
	if out.ContentType != youzoneUniversalMessageContentType {
		t.Fatalf("ContentType = %d, want %d", out.ContentType, youzoneUniversalMessageContentType)
	}
	var extend youzoneExtend
	if err := json.Unmarshal([]byte(out.Extend), &extend); err != nil {
		t.Fatalf("decode extend: %v (%q)", err, out.Extend)
	}
	if extend.ExtendType != "universalMessage" {
		t.Fatalf("extend_type = %q, want universalMessage", extend.ExtendType)
	}
	var card youzoneCardData
	if err := json.Unmarshal([]byte(extend.CustomData), &card); err != nil {
		t.Fatalf("decode customData: %v (%q)", err, extend.CustomData)
	}
	if card.CardVersion != youzoneCardVersion {
		t.Fatalf("cardVersion = %d, want %d", card.CardVersion, youzoneCardVersion)
	}
	return card
}

func TestBuildOutboundMessageReplyQuote(t *testing.T) {
	v := 8
	rc := replyContext{
		conversationID: "claw_robot-1.esn.upesn@pubaccount.im.yyuap.com/user-1.esn.upesn",
		senderID:       "claw_robot-1.esn.upesn@pubaccount.im.yyuap.com/user-1.esn.upesn",
		messageID:      "MSG-123",
		messageVersion: &v,
		replyText:      "  原始问题  ",
	}
	out, err := buildOutboundMessage("**好的**，这是回复", rc)
	if err != nil {
		t.Fatalf("buildOutboundMessage() error = %v", err)
	}
	card := decodeCard(t, out)

	if card.TitleZone.Type != youzoneTitleZoneReply {
		t.Fatalf("titleZone.type = %d, want %d", card.TitleZone.Type, youzoneTitleZoneReply)
	}
	if card.TitleZone.Reply == nil {
		t.Fatal("titleZone.reply is nil")
	}
	r := card.TitleZone.Reply
	if r.ReplyMessageID != "MSG-123" {
		t.Fatalf("reply.replyMessageId = %q", r.ReplyMessageID)
	}
	if r.ReplyMessageVersion == nil || *r.ReplyMessageVersion != 8 {
		t.Fatalf("reply.replyMessageVersion = %v, want 8", r.ReplyMessageVersion)
	}
	if r.ReplyUser != "user-1" {
		t.Fatalf("reply.replyUser = %q, want user-1", r.ReplyUser)
	}
	if r.ReplyText != "原始问题" {
		t.Fatalf("reply.replyText = %q, want 原始问题", r.ReplyText)
	}
	// Body keeps the raw (trimmed) markdown; digest is the stripped preview.
	if len(card.ContentZone) != 1 || card.ContentZone[0].Type != "textView" {
		t.Fatalf("contentZone = %#v", card.ContentZone)
	}
	if card.ContentZone[0].Data.Text != "**好的**，这是回复" {
		t.Fatalf("contentZone text = %q", card.ContentZone[0].Data.Text)
	}
	if card.ContentZone[0].Data.Type != youzoneTextViewPlainText {
		t.Fatalf("contentZone data.type = %d, want %d", card.ContentZone[0].Data.Type, youzoneTextViewPlainText)
	}
	if card.DigestText != "好的，这是回复" || out.Content != card.DigestText {
		t.Fatalf("digest = %q / content = %q", card.DigestText, out.Content)
	}
	if !card.IsAllowCopy || !card.IsAllowReply || !card.IsAllowForward {
		t.Fatalf("isAllow* flags = %#v", card)
	}
}

func TestBuildOutboundMessageDefaultHeaderWhenNoInboundMessageID(t *testing.T) {
	out, err := buildOutboundMessage("hi there", replyContext{senderID: "user-1.esn"})
	if err != nil {
		t.Fatalf("buildOutboundMessage() error = %v", err)
	}
	card := decodeCard(t, out)
	if card.TitleZone.Type != youzoneTitleZoneDefault {
		t.Fatalf("titleZone.type = %d, want %d", card.TitleZone.Type, youzoneTitleZoneDefault)
	}
	if card.TitleZone.Reply != nil {
		t.Fatalf("titleZone.reply = %#v, want nil", card.TitleZone.Reply)
	}
	if card.TitleZone.Title != youzoneDefaultTitle {
		t.Fatalf("titleZone.title = %q, want %q", card.TitleZone.Title, youzoneDefaultTitle)
	}
	if card.TitleZone.BackgroundIndex == nil || *card.TitleZone.BackgroundIndex != youzoneTitleBackgroundIndex {
		t.Fatalf("titleZone.backgroundIndex = %v, want %d", card.TitleZone.BackgroundIndex, youzoneTitleBackgroundIndex)
	}
	// YonClaw emits replyMessageVersion: null explicitly; we only do so inside a
	// reply zone, which is absent here — so the marshalled JSON must contain no
	// "reply" key at all.
	if strings.Contains(out.Extend, `"reply"`) {
		t.Fatalf("extend unexpectedly contains a reply zone: %q", out.Extend)
	}
}

func TestBuildOutboundMessageNullReplyVersionWhenInboundHadNone(t *testing.T) {
	out, err := buildOutboundMessage("ok", replyContext{messageID: "M1", senderID: "u1.esn", replyText: "q"})
	if err != nil {
		t.Fatalf("buildOutboundMessage() error = %v", err)
	}
	card := decodeCard(t, out)
	if card.TitleZone.Reply == nil || card.TitleZone.Reply.ReplyMessageVersion != nil {
		t.Fatalf("reply.replyMessageVersion = %v, want null", card.TitleZone.Reply)
	}
	// And the marshalled JSON literally says null (matching YonClaw).
	var extend youzoneExtend
	_ = json.Unmarshal([]byte(out.Extend), &extend)
	if !strings.Contains(extend.CustomData, `"replyMessageVersion":null`) {
		t.Fatalf("customData missing replyMessageVersion:null: %q", extend.CustomData)
	}
}

func TestBuildOutboundMessageEmptyContentUsesDigestFallback(t *testing.T) {
	out, err := buildOutboundMessage("   \n  ", replyContext{})
	if err != nil {
		t.Fatalf("buildOutboundMessage() error = %v", err)
	}
	card := decodeCard(t, out)
	if card.DigestText != youzoneDefaultDigest {
		t.Fatalf("digest = %q, want %q", card.DigestText, youzoneDefaultDigest)
	}
}

func TestBuildOutboundMessageDigestTruncatesLongContent(t *testing.T) {
	long := strings.Repeat("好", 80)
	out, err := buildOutboundMessage(long, replyContext{})
	if err != nil {
		t.Fatalf("buildOutboundMessage() error = %v", err)
	}
	card := decodeCard(t, out)
	wantPrefix := strings.Repeat("好", 47) + "..."
	if card.DigestText != wantPrefix {
		t.Fatalf("digest = %q, want %q", card.DigestText, wantPrefix)
	}
	if len([]rune(card.DigestText)) != 50 {
		t.Fatalf("digest rune length = %d, want 50", len([]rune(card.DigestText)))
	}
	// Body still carries the full text.
	if card.ContentZone[0].Data.Text != long {
		t.Fatalf("body text was truncated")
	}
}

func TestStripMarkdownToPlainText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"# Heading\ntext", "Heading text"},
		{"**bold** and *italic* and __b2__ and _i2_", "bold and italic and b2 and i2"},
		{"see [docs](https://example.test) here", "see docs here"},
		{"![alt](https://img.test/x.png) caption", "alt caption"},
		{"- one\n- two\n1. three", "one two three"},
		{"```\ncode\n```\ninline `code`", "code inline code"},
		{"  spaced   out  \n\n  text ", "spaced out text"},
		{"#hashtag! [brackets]", "hashtag brackets"},
	}
	for _, c := range cases {
		if got := stripMarkdownToPlainText(c.in); got != c.want {
			t.Errorf("stripMarkdownToPlainText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPeerUserSegment(t *testing.T) {
	cases := map[string]string{
		"claw_robot-1.esn.upesn@pubaccount.im.yyuap.com/user-1.esn.upesn": "user-1",
		"user-1.esn.upesn": "user-1",
		"plainuser":        "plainuser",
		"":                 "",
		"  a.b/c.d  ":      "c",
	}
	for in, want := range cases {
		if got := peerUserSegment(in); got != want {
			t.Errorf("peerUserSegment(%q) = %q, want %q", in, got, want)
		}
	}
}
