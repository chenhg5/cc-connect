package core

import (
	"log/slog"
	"path/filepath"
	"strings"
)

// deliverTurnMedia forwards images and files an agent attached to an event.
// Names it could not hand to the platform are returned so the caller can show
// a visible fallback instead of dropping an attachment-only reply.
func (e *Engine) deliverTurnMedia(p Platform, replyCtx any, images []ImageAttachment, files []FileAttachment) []string {
	if len(images) == 0 && len(files) == 0 {
		return nil
	}
	if p == nil || !e.attachmentSendEnabled {
		if p != nil && !e.attachmentSendEnabled {
			slog.Warn("turn media dropped: attachment send disabled",
				"platform", p.Name(), "images", len(images), "files", len(files))
		}
		return mediaNames(images, files)
	}

	var dropped []string
	if len(images) > 0 {
		sender, ok := p.(ImageSender)
		if !ok {
			slog.Warn("turn media: platform cannot send images", "platform", p.Name(), "count", len(images))
			for _, img := range images {
				dropped = append(dropped, mediaName(img.FileName, "image"))
			}
		} else {
			for _, img := range images {
				if err := e.waitOutgoing(p); err != nil {
					slog.Warn("turn media: outgoing rate limit", "platform", p.Name(), "error", err)
					dropped = append(dropped, mediaName(img.FileName, "image"))
					continue
				}
				if err := sender.SendImage(e.ctx, replyCtx, img); err != nil {
					slog.Error("turn media: send image", "platform", p.Name(), "file", img.FileName, "error", err)
					dropped = append(dropped, mediaName(img.FileName, "image"))
				}
			}
		}
	}
	if len(files) > 0 {
		sender, ok := p.(FileSender)
		if !ok {
			slog.Warn("turn media: platform cannot send files", "platform", p.Name(), "count", len(files))
			for _, file := range files {
				dropped = append(dropped, mediaName(file.FileName, "file"))
			}
		} else {
			for _, file := range files {
				if err := e.waitOutgoing(p); err != nil {
					slog.Warn("turn media: outgoing rate limit", "platform", p.Name(), "error", err)
					dropped = append(dropped, mediaName(file.FileName, "file"))
					continue
				}
				if err := sender.SendFile(e.ctx, replyCtx, file); err != nil {
					slog.Error("turn media: send file", "platform", p.Name(), "file", file.FileName, "error", err)
					dropped = append(dropped, mediaName(file.FileName, "file"))
				}
			}
		}
	}
	return dropped
}

// noteUndeliveredMedia tells the user which attachments never reached the chat.
func (e *Engine) noteUndeliveredMedia(p Platform, replyCtx any, dropped []string) {
	if p == nil || e.i18n == nil || len(dropped) == 0 {
		return
	}
	e.send(p, replyCtx, e.i18n.Tf(MsgTurnMediaUndeliverable, strings.Join(dropped, ", ")))
}

func mediaNames(images []ImageAttachment, files []FileAttachment) []string {
	names := make([]string, 0, len(images)+len(files))
	for _, img := range images {
		names = append(names, mediaName(img.FileName, "image"))
	}
	for _, file := range files {
		names = append(names, mediaName(file.FileName, "file"))
	}
	return names
}

func mediaName(name, fallback string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return fallback
	}
	return filepath.Base(name)
}
