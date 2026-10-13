package feishu

import "github.com/chenhg5/cc-connect/core"

// IsPrivateUserMessage uses trusted event metadata rather than text or a
// session-key heuristic. Form callbacks carry the original verified context;
// arbitrary callbacks and unknown senders cannot initiate registration.
func (p *Platform) IsPrivateUserMessage(msg *core.Message) bool {
	rctx, ok := msg.ReplyCtx.(replyContext)
	return ok && rctx.chatType == "p2p" && rctx.senderType == "user"
}
