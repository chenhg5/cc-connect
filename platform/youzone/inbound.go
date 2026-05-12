package youzone

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func parseInboundMessage(raw []byte) (inboundMessage, bool) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return inboundMessage{}, false
	}
	msgType := readString(payload["type"])
	if msgType == "auth" || msgType == "ping" || msgType == "pong" {
		return inboundMessage{}, false
	}
	text := extractText(payload)
	if strings.TrimSpace(text) == "" {
		return inboundMessage{}, false
	}
	msg := inboundMessage{
		MessageID:      firstString(payload, "messageId", "packetId", "id", "msgId"),
		SenderID:       firstString(payload, "sender", "senderId", "senderUserId", "from", "fromUserId", "userId", "imUserId"),
		SenderName:     firstString(payload, "senderName", "fromName", "userName", "name"),
		ConversationID: firstString(payload, "conversationId", "chatId", "sessionId", "target", "from", "to"),
		Text:           strings.TrimSpace(text),
		ContentType:    readInt(payload["contentType"], payload["type"]),
		MessageVersion: firstInt(payload, "sessionVersion", "messageVersion", "version"),
		Type:           msgType,
		Raw:            append([]byte(nil), raw...),
	}
	return msg, true
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
