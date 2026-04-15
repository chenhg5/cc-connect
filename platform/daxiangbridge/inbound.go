package daxiangbridge

import (
	"encoding/json"
	"fmt"

	"github.com/chenhg5/cc-connect/core"
)

type replyContext struct {
	requestID      string
	sessionID      string
	conversationID string
}

func normalizeInboundMessage(frame BridgeFrame) (*core.Message, error) {
	var p BridgeEventPayload
	if err := json.Unmarshal(frame.Payload, &p); err != nil {
		return nil, fmt.Errorf("daxiangbridge: unmarshal event payload: %w", err)
	}
	if p.ChatType != "private" {
		return nil, fmt.Errorf("daxiangbridge: unsupported chatType %q (only private supported)", p.ChatType)
	}
	if p.Text == "" {
		return nil, fmt.Errorf("daxiangbridge: empty text message")
	}
	return &core.Message{
		SessionKey: frame.SessionID,
		Platform:   "daxiangbridge",
		MessageID:  p.MessageID,
		UserID:     p.FromUserID,
		UserName:   p.FromUserName,
		ChatName:   p.ConversationID,
		Content:    p.Text,
		ReplyCtx: replyContext{
			requestID:      frame.RequestID,
			sessionID:      frame.SessionID,
			conversationID: p.ConversationID,
		},
	}, nil
}

func unmarshalPayload(frame BridgeFrame, v any) error {
	return json.Unmarshal(frame.Payload, v)
}
