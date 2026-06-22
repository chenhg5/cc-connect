package webex

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

// webexMaxBytes is Webex's per-message body cap.
const webexMaxBytes = 7439

// asReplyContext recovers a replyContext from the engine's any-typed value.
func asReplyContext(replyCtx any) (replyContext, error) {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return replyContext{}, fmt.Errorf("webex: invalid reply context %T", replyCtx)
	}
	return rc, nil
}

// Reply posts a threaded response to the originating message.
func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	rc, err := asReplyContext(replyCtx)
	if err != nil {
		return err
	}
	return p.post(ctx, rc.roomID, rc.messageID, content)
}

// Send posts a non-threaded (proactive) message to the room.
func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	rc, err := asReplyContext(replyCtx)
	if err != nil {
		return err
	}
	return p.post(ctx, rc.roomID, "", content)
}

// post chunks content and posts each chunk; only the first chunk threads.
func (p *Platform) post(ctx context.Context, roomID, parentID, content string) error {
	chunks := chunkMarkdown(content, webexMaxBytes)
	for i, chunk := range chunks {
		pid := ""
		if i == 0 {
			pid = parentID
		}
		if err := p.client.PostMessage(ctx, roomID, pid, chunk); err != nil {
			return fmt.Errorf("webex: post chunk %d/%d: %w", i+1, len(chunks), err)
		}
	}
	return nil
}

// chunkMarkdown splits text to fit within limit bytes, preferring paragraph
// (\n\n), then line (\n), then a hard cut.
func chunkMarkdown(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}
	var out []string
	rest := text
	for len(rest) > limit {
		cut := strings.LastIndex(rest[:limit], "\n\n")
		if cut <= 0 {
			cut = strings.LastIndex(rest[:limit], "\n")
		}
		if cut <= 0 {
			cut = limit
			// back up to a rune boundary so we never split a multibyte char
			for cut > 0 && !utf8.RuneStart(rest[cut]) {
				cut--
			}
			if cut == 0 { // pathological: single rune larger than limit; force full limit
				cut = limit
			}
		}
		out = append(out, rest[:cut])
		rest = strings.TrimLeft(rest[cut:], "\n")
	}
	if rest != "" {
		out = append(out, rest)
	}
	return out
}

// SendImage implements core.ImageSender.
func (p *Platform) SendImage(ctx context.Context, replyCtx any, img core.ImageAttachment) error {
	rc, err := asReplyContext(replyCtx)
	if err != nil {
		return err
	}
	return p.client.PostFile(ctx, rc.roomID, &downloadedFile{
		Data: img.Data, MimeType: img.MimeType, FileName: img.FileName,
	})
}

// SendFile implements core.FileSender.
func (p *Platform) SendFile(ctx context.Context, replyCtx any, file core.FileAttachment) error {
	rc, err := asReplyContext(replyCtx)
	if err != nil {
		return err
	}
	return p.client.PostFile(ctx, rc.roomID, &downloadedFile{
		Data: file.Data, MimeType: file.MimeType, FileName: file.FileName,
	})
}

// ReconstructReplyCtx implements core.ReplyContextReconstructor for cron jobs.
// Session key format is "webex:{roomID}:{personID}".
func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "webex" {
		return nil, fmt.Errorf("webex: cannot reconstruct reply ctx from %q", sessionKey)
	}
	rc := replyContext{roomID: parts[1]}
	if len(parts) == 3 {
		rc.personID = parts[2]
	}
	return rc, nil
}

// FormattingInstructions implements core.FormattingInstructionProvider.
func (p *Platform) FormattingInstructions() string {
	return "Webex supports standard Markdown (bold, italic, lists, code blocks, links). Use it freely."
}

// Compile-time interface conformance checks.
var (
	_ core.Platform                      = (*Platform)(nil)
	_ core.ImageSender                   = (*Platform)(nil)
	_ core.FileSender                    = (*Platform)(nil)
	_ core.ReplyContextReconstructor     = (*Platform)(nil)
	_ core.FormattingInstructionProvider = (*Platform)(nil)
	_ core.AsyncRecoverablePlatform      = (*Platform)(nil)
)
