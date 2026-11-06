package feishu

import (
	"context"
	"encoding/json"
	"fmt"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// SendRawCard implements core.RawCardSender: the card JSON goes out as an
// interactive message unchanged, the way replies go (in the thread when
// there is one), and its message id comes back. A non-empty uuid is
// Feishu's deduplication key; the transient-error retries reuse it, so they
// cannot double the card either.
func (p *Platform) SendRawCard(ctx context.Context, rctx any, card json.RawMessage, uuid string) (string, error) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return "", fmt.Errorf("%s: invalid reply context type %T", p.tag(), rctx)
	}
	if p.shouldUseThreadOrReplyAPI(rc) {
		return p.replyMessageID(ctx, rc, larkim.MsgTypeInteractive, string(card), uuid)
	}
	if rc.chatID == "" {
		return "", fmt.Errorf("%s: chatID is empty, cannot send new message", p.tag())
	}
	return p.createMessageID(ctx, rc.chatID, larkim.MsgTypeInteractive, string(card), "send", uuid)
}

// UpdateRawCard implements core.RawCardUpdater with the patch message API.
func (p *Platform) UpdateRawCard(ctx context.Context, messageID string, card json.RawMessage) error {
	return p.patchCardMessage(ctx, messageID, string(card))
}
