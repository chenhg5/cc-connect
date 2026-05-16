package youzone

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// parseInboundMessage parses one raw WebSocket frame. The returned reason is
// inboundDropNone when the frame is a real user-facing message that should be
// dispatched to the engine; any other reason means the frame must be dropped.
//
// Single-parse contract: when reason is heartbeat/empty_text the returned
// inboundMessage is still populated with whatever readable fields the frame
// carried (message id, sender, conversation, content type, version raw, type,
// text). The caller — handleInbound — relies on this so that the
// inbound_frame_received summary log and the inbound_message_dropped log share
// one JSON decode. Only json_invalid and empty_frame return a near-empty
// inboundMessage (Raw is still set so caller can record raw_len).
func parseInboundMessage(raw []byte) (inboundMessage, inboundDropReason) {
	if strings.TrimSpace(string(raw)) == "" {
		// YOUZONE xmpp-whitespace server→client heartbeat. Distinct from
		// json_invalid so the operator doesn't chase a "broken parser" ghost.
		return inboundMessage{Raw: append([]byte(nil), raw...)}, inboundDropEmptyFrame
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return inboundMessage{Raw: append([]byte(nil), raw...)}, inboundDropJSONInvalid
	}
	msgType := readString(payload["type"])
	msg := inboundMessage{
		MessageID:         firstString(payload, "messageId", "packetId", "id", "msgId"),
		SenderID:          firstString(payload, "sender", "senderId", "senderUserId", "from", "fromUserId", "userId", "imUserId"),
		SenderName:        firstString(payload, "senderName", "fromName", "userName", "name"),
		ConversationID:    firstString(payload, "conversationId", "chatId", "sessionId", "target", "from", "to"),
		ContentType:       readInt(payload["contentType"], payload["type"]),
		MessageVersion:    firstInt(payload, "sessionVersion", "messageVersion", "version"),
		MessageVersionRaw: firstNumberString(payload, "sessionVersion", "messageVersion", "version"),
		Type:              msgType,
		Raw:               append([]byte(nil), raw...),
	}
	text := extractText(payload)
	msg.Text = strings.TrimSpace(text)

	if msgType == "auth" || msgType == "ping" || msgType == "pong" {
		return msg, inboundDropHeartbeat
	}
	if msg.Text == "" {
		return msg, inboundDropEmptyText
	}
	return msg, inboundDropNone
}

func extractText(payload map[string]any) string {
	raw := firstString(payload, "content", "text", "message", "body")
	if raw == "" {
		return ""
	}
	var nested map[string]any
	if json.Unmarshal([]byte(raw), &nested) == nil {
		if text := firstString(nested, "content", "text", "message", "body"); text != "" {
			return text
		}
	}
	return raw
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if s := readString(m[key]); s != "" {
			return s
		}
	}
	return ""
}

// firstInt returns the first of keys present in m parsed as an int, or nil if
// none are present/parseable. Mirrors YonClaw's readOptionalNumber, which keeps
// "absent" distinct from 0 so the outbound reply-quote can send null.
func firstInt(m map[string]any, keys ...string) *int {
	for _, key := range keys {
		v, ok := m[key]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case float64:
			n := int(t)
			return &n
		case int:
			n := t
			return &n
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
				return &n
			}
		}
	}
	return nil
}

// firstNumberString mirrors firstInt's key precedence but returns the raw
// string form of the value. Used for log fields where we don't want to risk
// the float64->int round-trip in firstInt smudging out the original textual
// representation (and so any future replay cursor stays a stable opaque
// string).
func firstNumberString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		v, ok := m[key]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case float64:
			return strconv.FormatFloat(t, 'f', -1, 64)
		case int:
			return strconv.Itoa(t)
		case json.Number:
			return t.String()
		case string:
			s := strings.TrimSpace(t)
			if s != "" {
				return s
			}
		}
	}
	return ""
}

func readInt(values ...any) int {
	for _, v := range values {
		switch t := v.(type) {
		case float64:
			return int(t)
		case int:
			return t
		case string:
			if i, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
				return i
			}
		}
	}
	return 0
}

func sessionKey(msg inboundMessage) string {
	conv := strings.TrimSpace(msg.ConversationID)
	user := strings.TrimSpace(msg.SenderID)
	if conv == "" {
		conv = "unknown"
	}
	if user == "" {
		user = "unknown"
	}
	return fmt.Sprintf("youzone:%s:%s", conv, user)
}

// extractCommand returns the first whitespace-delimited token from text iff
// text starts with "/". Otherwise it returns "". This is only used to label
// inbound logs (e.g. command=/connect) so an operator grepping a single
// command can find the frame in seconds.
func extractCommand(text string) string {
	s := strings.TrimSpace(text)
	if !strings.HasPrefix(s, "/") {
		return ""
	}
	if i := strings.IndexAny(s, " \t\n\r"); i > 0 {
		return s[:i]
	}
	return s
}
