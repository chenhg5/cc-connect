package core

import (
	"log/slog"
)

// deliverTurnMedia forwards images and files an agent attached to an event.
// Text delivery is separate. Failures are logged so one bad attachment does
// not drop the rest of the reply.
func (e *Engine) deliverTurnMedia(p Platform, replyCtx any, images []ImageAttachment, files []FileAttachment) {
	if p == nil || (len(images) == 0 && len(files) == 0) {
		return
	}
	if !e.attachmentSendEnabled {
		slog.Warn("turn media dropped: attachment send disabled",
			"platform", p.Name(), "images", len(images), "files", len(files))
		return
	}
	if len(images) > 0 {
		sender, ok := p.(ImageSender)
		if !ok {
			slog.Warn("turn media: platform cannot send images", "platform", p.Name(), "count", len(images))
		} else {
			for _, img := range images {
				if err := e.waitOutgoing(p); err != nil {
					slog.Warn("turn media: outgoing rate limit", "platform", p.Name(), "error", err)
					return
				}
				if err := sender.SendImage(e.ctx, replyCtx, img); err != nil {
					slog.Error("turn media: send image", "platform", p.Name(), "file", img.FileName, "error", err)
				}
			}
		}
	}
	if len(files) > 0 {
		sender, ok := p.(FileSender)
		if !ok {
			slog.Warn("turn media: platform cannot send files", "platform", p.Name(), "count", len(files))
		} else {
			for _, file := range files {
				if err := e.waitOutgoing(p); err != nil {
					slog.Warn("turn media: outgoing rate limit", "platform", p.Name(), "error", err)
					return
				}
				if err := sender.SendFile(e.ctx, replyCtx, file); err != nil {
					slog.Error("turn media: send file", "platform", p.Name(), "file", file.FileName, "error", err)
				}
			}
		}
	}
}
