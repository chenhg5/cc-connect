package core

import "log/slog"

// Send first, then shrink the old preview. A failed send must not erase the
// preview or prevent the caller from finalizing it in place.
func (e *Engine) sendLatestRichFinal(p Platform, replyCtx, previewHandle any, finalCard string, builder RichCardSupporter) bool {
	policy, ok := p.(ApprovalFinalReplyPlatform)
	if !ok || !policy.FinalReplyAfterApproval() || previewHandle == nil {
		return false
	}
	if err := p.Send(e.ctx, replyCtx, finalCard); err != nil {
		slog.Warn("send latest final reply failed; completing existing preview", "platform", p.Name(), "error", err)
		return false
	}
	if updater, ok := p.(MessageUpdater); ok {
		compact := builder.BuildRichCard(CardStatusDone, "", nil, e.i18n.T(MsgFinalReplyLatest), false, "")
		if err := updater.UpdateMessage(e.ctx, previewHandle, compact); err != nil {
			slog.Warn("compact completed preview failed; latest final reply remains available", "platform", p.Name(), "error", err)
		}
	}
	return true
}
