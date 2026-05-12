package youzone

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Outbound message construction, ported from YonClaw's `yon-im` plugin so that
// cc-connect agent replies render in YOUZONE exactly the way YonClaw's replies
// do — in particular as a "reply card" that quotes the inbound message.
//
// Background: the `claw-robot/client/sendMessage` HTTP endpoint carries NO
// conversation/recipient target. A "claw robot" is bound to a single
// conversation, so `robotId` alone identifies where the message lands — that is
// why neither YonClaw nor cc-connect ever sends a conversationId/to/target
// field. What the inbound `replyContext` (conversationID/senderID/messageID/
// messageVersion/replyText) is actually used for here is building the reply
// quote header below, plus session/dedup keying — not routing.
//
// YonClaw sends every bot reply as contentType 18 ("UniversalMessage") with an
// `extend.customData` card body. Sending raw text (contentType 2) also gets
// delivered, but then the YOUZONE client can't show the reply-quote header. We
// mirror YonClaw's shape instead.
//
// Reference: YonClaw-0.1.15 — openclaw-plugins/yon-im/index.js
//   - sendZhiyouMessage()                -> outbound HTTP body shape
//   - buildRemoteUniversalMessagePayload -> content (digest) + extend
//   - buildTitleZone()                   -> reply-quote vs default header
//   - buildContentZone()                 -> textView body
//   - buildDigestText() / stripMarkdownToPlainText() -> the short preview text
//   - resolveReplyUser()                 -> peer segment shown in the quote header
const youzoneUniversalMessageContentType = 18

// Card constants copied verbatim from YonClaw's zhiyou protocol module
// (ZHIYOU_PROTOCOL_* in yon-im/index.js).
const (
	youzoneCardVersion          = 9
	youzoneTextViewLevel        = 0
	youzoneTextViewPlainText    = 1 // TextViewType.PlainText
	youzoneTitleBackgroundIndex = 2

	// YonClaw's resolveZhiyouUiLanguage defaults to "zh"; we reuse its zh
	// strings. The digest fallback only ever surfaces when the agent reply is
	// empty (which cc-connect does not normally produce), and the default title
	// is only shown when there is no inbound message to quote.
	youzoneDefaultTitle  = "🦞 友空间"
	youzoneDefaultDigest = "回复了一条消息"
)

// titleZone "type" enum values from YonClaw's protocol.
const (
	youzoneTitleZoneDefault = 0
	youzoneTitleZoneReply   = 2
)

// outboundMessage is the rendered payload for claw-robot/client/sendMessage.
type outboundMessage struct {
	ContentType int
	Content     string // digest text — also what shows in notification / chat-list previews
	Extend      string // JSON string: {"customData":"<json>","extend_type":"universalMessage"}
}

// JSON shapes mirror YonClaw's customData card. Struct field order is preserved
// in the marshalled JSON; pointer fields render as `null` when unset (matching
// YonClaw, e.g. replyMessageVersion) or are omitted (backgroundIndex/reply).
type (
	youzoneCardData struct {
		BusinessID     string               `json:"businessId"`
		CardVersion    int                  `json:"cardVersion"`
		DigestText     string               `json:"digestText"`
		TitleZone      youzoneTitleZone     `json:"titleZone"`
		IsAllowCopy    bool                 `json:"isAllowCopy"`
		IsAllowReply   bool                 `json:"isAllowReply"`
		IsAllowForward bool                 `json:"isAllowForward"`
		ContentZone    []youzoneContentView `json:"contentZone"`
	}

	youzoneTitleZone struct {
		Type            int               `json:"type"`
		Title           string            `json:"title,omitempty"`
		BackgroundIndex *int              `json:"backgroundIndex,omitempty"`
		Reply           *youzoneReplyZone `json:"reply,omitempty"`
	}

	youzoneReplyZone struct {
		ReplyUser           string `json:"replyUser"`
		ReplyText           string `json:"replyText"`
		ReplyMessageID      string `json:"replyMessageId"`
		ReplyMessageVersion *int   `json:"replyMessageVersion"` // null when the inbound frame had no version
	}

	youzoneContentView struct {
		Type string                 `json:"type"`
		Data youzoneContentViewData `json:"data"`
	}

	youzoneContentViewData struct {
		Level int    `json:"level"`
		Type  int    `json:"type"`
		Text  string `json:"text"`
	}

	youzoneExtend struct {
		CustomData string `json:"customData"`
		ExtendType string `json:"extend_type"`
	}
)

// buildOutboundMessage renders an agent text reply into YonClaw's
// UniversalMessage shape. When rc carries the inbound message id the card
// header becomes a reply-quote of that message; otherwise it falls back to
// YonClaw's default titled header.
func buildOutboundMessage(content string, rc replyContext) (outboundMessage, error) {
	// YonClaw leaves the body text raw and only cleans the digest; cc-connect
	// trims the agent reply first because a card with a trailing newline looks
	// worse than it reads.
	content = strings.TrimSpace(content)
	digest := buildDigestText(content, youzoneDefaultDigest)

	card := youzoneCardData{
		BusinessID:     "",
		CardVersion:    youzoneCardVersion,
		DigestText:     digest,
		TitleZone:      buildTitleZone(rc),
		IsAllowCopy:    true,
		IsAllowReply:   true,
		IsAllowForward: true,
		ContentZone:    buildContentZone(content),
	}
	cardJSON, err := json.Marshal(card)
	if err != nil {
		return outboundMessage{}, err
	}
	extendJSON, err := json.Marshal(youzoneExtend{
		CustomData: string(cardJSON),
		ExtendType: "universalMessage",
	})
	if err != nil {
		return outboundMessage{}, err
	}
	return outboundMessage{
		ContentType: youzoneUniversalMessageContentType,
		Content:     digest,
		Extend:      string(extendJSON),
	}, nil
}

// buildTitleZone mirrors YonClaw's buildTitleZone: a reply-quote header when an
// inbound message id is available, otherwise the default titled header.
func buildTitleZone(rc replyContext) youzoneTitleZone {
	if strings.TrimSpace(rc.messageID) != "" {
		return youzoneTitleZone{
			Type: youzoneTitleZoneReply,
			Reply: &youzoneReplyZone{
				ReplyUser:           peerUserSegment(rc.senderID),
				ReplyText:           strings.TrimSpace(rc.replyText),
				ReplyMessageID:      strings.TrimSpace(rc.messageID),
				ReplyMessageVersion: rc.messageVersion,
			},
		}
	}
	bg := youzoneTitleBackgroundIndex
	return youzoneTitleZone{
		Type:            youzoneTitleZoneDefault,
		Title:           youzoneDefaultTitle,
		BackgroundIndex: &bg,
	}
}

// buildContentZone mirrors YonClaw's buildContentZone for a text reply: it
// always emits exactly one plain-text textView (YonClaw falls back to one even
// when the text is empty). Image/file views are out of scope for this version.
func buildContentZone(content string) []youzoneContentView {
	return []youzoneContentView{{
		Type: "textView",
		Data: youzoneContentViewData{
			Level: youzoneTextViewLevel,
			Type:  youzoneTextViewPlainText,
			Text:  content,
		},
	}}
}

// peerUserSegment ports YonClaw's extractPeerUserSegment: take the part after
// the last "/" (the IM "resource"), then the segment before the first ".".
// For e.g. "claw_robot-1.esn.upesn@.../user-1.esn.upesn" -> "user-1"; an
// already-bare sender id like "user-1.esn.upesn" -> "user-1".
func peerUserSegment(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	return s
}

// Regexes for stripMarkdownToPlainText. Go's RE2 has no backreferences, so
// YonClaw's `(\*\*|__)(.*?)\1` bold and `(\*|_)(.*?)\1` italic rules are split
// into separate `**…**` / `__…__` and `*…*` / `_…_` passes, and its three
// "remove N-or-more backticks" passes collapse into one "remove all backticks".
var (
	mdLineBreaks  = regexp.MustCompile(`\r\n?`)
	mdImage       = regexp.MustCompile(`!\[([^\]]*)\]\([^)]+\)`)
	mdLink        = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	mdHeading     = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+`)
	mdBullet      = regexp.MustCompile(`(?m)^\s*[-*+]\s+`)
	mdOrdered     = regexp.MustCompile(`(?m)^\s*\d+\.\s+`)
	mdBoldStar    = regexp.MustCompile(`\*\*(.*?)\*\*`)
	mdBoldUnder   = regexp.MustCompile(`__(.*?)__`)
	mdItalicStar  = regexp.MustCompile(`\*(.*?)\*`)
	mdItalicUnder = regexp.MustCompile(`_(.*?)_`)
	mdPunct       = regexp.MustCompile(`[#!\[\]]`)
	mdSpaces      = regexp.MustCompile(`\s+`)
)

// stripMarkdownToPlainText is a faithful port of YonClaw's
// stripMarkdownToPlainText (yon-im outbound/payload-utils.ts). It is only used
// to derive the short digest/preview text, never the rendered body.
func stripMarkdownToPlainText(s string) string {
	s = mdLineBreaks.ReplaceAllString(s, "\n")
	s = mdImage.ReplaceAllString(s, "$1")
	s = mdLink.ReplaceAllString(s, "$1")
	s = mdHeading.ReplaceAllString(s, "")
	s = mdBullet.ReplaceAllString(s, "")
	s = mdOrdered.ReplaceAllString(s, "")
	s = mdBoldStar.ReplaceAllString(s, "$1")
	s = mdBoldUnder.ReplaceAllString(s, "$1")
	s = mdItalicStar.ReplaceAllString(s, "$1")
	s = mdItalicUnder.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, "`", "")
	s = mdPunct.ReplaceAllString(s, " ")
	s = mdSpaces.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// buildDigestText ports YonClaw's buildDigestText: strip markdown, collapse to a
// single line, fall back to fallback when empty, then cap at 50 runes (47 + "…"
// rendered as "..." to match YonClaw's ASCII ellipsis).
func buildDigestText(text, fallback string) string {
	plain := stripMarkdownToPlainText(text)
	singleLine := strings.TrimSpace(strings.NewReplacer("\r", "", "\n", "").Replace(plain))
	if singleLine == "" {
		singleLine = fallback
	}
	runes := []rune(singleLine)
	if len(runes) <= 50 {
		return singleLine
	}
	return string(runes[:47]) + "..."
}
