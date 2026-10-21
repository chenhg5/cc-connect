package wecom

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

const (
	wecomWSUploadChunkSize = 512 * 1024
	wecomWSUploadMaxChunks = 100
)

// SendImage uploads and sends an image through the WeCom AI Bot WebSocket API.
func (p *WSPlatform) SendImage(ctx context.Context, rctx any, img core.ImageAttachment) error {
	rc, ok := rctx.(wsReplyContext)
	if !ok {
		return fmt.Errorf("wecom-ws: SendImage: invalid reply context type %T", rctx)
	}
	if rc.chatID == "" {
		return fmt.Errorf("wecom-ws: chatID is empty, cannot send image")
	}
	if len(img.Data) == 0 {
		return fmt.Errorf("wecom-ws: image data is empty")
	}

	mediaID, err := p.uploadWSMedia(ctx, "image", wsImageFileName(img), img.Data)
	if err != nil {
		return fmt.Errorf("wecom-ws: send image: %w", err)
	}
	if err := p.sendWSMediaMessage(ctx, rc.chatID, "image", mediaID); err != nil {
		return fmt.Errorf("wecom-ws: send image: %w", err)
	}
	return nil
}

func (p *WSPlatform) uploadWSMedia(ctx context.Context, mediaType, filename string, data []byte) (string, error) {
	totalChunks := (len(data) + wecomWSUploadChunkSize - 1) / wecomWSUploadChunkSize
	if totalChunks == 0 {
		return "", fmt.Errorf("empty media data")
	}
	if totalChunks > wecomWSUploadMaxChunks {
		return "", fmt.Errorf("media too large: %d chunks exceeds maximum %d", totalChunks, wecomWSUploadMaxChunks)
	}

	sum := md5.Sum(data)
	initReqID := p.generateReqID("aibot_upload_media_init")
	initFrame := map[string]any{
		"cmd":     "aibot_upload_media_init",
		"headers": map[string]string{"req_id": initReqID},
		"body": map[string]any{
			"type":         mediaType,
			"filename":     filename,
			"total_size":   len(data),
			"total_chunks": totalChunks,
			"md5":          hex.EncodeToString(sum[:]),
		},
	}
	initResp, err := p.writeAndWaitFrameWithTimeout(ctx, initFrame, initReqID, wsMediaAckTimeout)
	if err != nil {
		return "", fmt.Errorf("upload init: %w", err)
	}
	var initBody struct {
		UploadID string `json:"upload_id"`
	}
	if err := json.Unmarshal(initResp.Body, &initBody); err != nil {
		return "", fmt.Errorf("decode upload init response: %w", err)
	}
	if initBody.UploadID == "" {
		return "", fmt.Errorf("upload init: empty upload_id")
	}

	for i := 0; i < totalChunks; i++ {
		start := i * wecomWSUploadChunkSize
		end := start + wecomWSUploadChunkSize
		if end > len(data) {
			end = len(data)
		}
		reqID := p.generateReqID("aibot_upload_media_chunk")
		chunkFrame := map[string]any{
			"cmd":     "aibot_upload_media_chunk",
			"headers": map[string]string{"req_id": reqID},
			"body": map[string]any{
				"upload_id":   initBody.UploadID,
				"chunk_index": i,
				"base64_data": base64.StdEncoding.EncodeToString(data[start:end]),
			},
		}
		if _, err := p.writeAndWaitFrameWithTimeout(ctx, chunkFrame, reqID, wsMediaAckTimeout); err != nil {
			return "", fmt.Errorf("upload chunk %d: %w", i, err)
		}
	}

	finishReqID := p.generateReqID("aibot_upload_media_finish")
	finishFrame := map[string]any{
		"cmd":     "aibot_upload_media_finish",
		"headers": map[string]string{"req_id": finishReqID},
		"body": map[string]any{
			"upload_id": initBody.UploadID,
		},
	}
	finishResp, err := p.writeAndWaitFrameWithTimeout(ctx, finishFrame, finishReqID, wsMediaAckTimeout)
	if err != nil {
		return "", fmt.Errorf("upload finish: %w", err)
	}
	var finishBody struct {
		MediaID string `json:"media_id"`
	}
	if err := json.Unmarshal(finishResp.Body, &finishBody); err != nil {
		return "", fmt.Errorf("decode upload finish response: %w", err)
	}
	if finishBody.MediaID == "" {
		return "", fmt.Errorf("upload finish: empty media_id")
	}
	return finishBody.MediaID, nil
}

func (p *WSPlatform) sendWSMediaMessage(ctx context.Context, chatID, mediaType, mediaID string) error {
	reqID := p.generateReqID("aibot_send_msg")
	frame := map[string]any{
		"cmd":     "aibot_send_msg",
		"headers": map[string]string{"req_id": reqID},
		"body": map[string]any{
			"chatid":  chatID,
			"msgtype": mediaType,
			mediaType: map[string]string{
				"media_id": mediaID,
			},
		},
	}
	return p.writeAndWaitAckStrict(ctx, frame, reqID, wsMediaAckTimeout)
}

func wsImageFileName(img core.ImageAttachment) string {
	name := filepath.Base(strings.TrimSpace(img.FileName))
	if name != "" && name != "." {
		return name
	}
	switch strings.ToLower(img.MimeType) {
	case "image/jpeg", "image/jpg":
		return "image.jpg"
	case "image/gif":
		return "image.gif"
	case "image/webp":
		return "image.webp"
	default:
		return "image.png"
	}
}

// SendFile uploads and sends a file through the WeCom AI Bot WebSocket API.
func (p *WSPlatform) SendFile(ctx context.Context, rctx any, file core.FileAttachment) error {
	rc, ok := rctx.(wsReplyContext)
	if !ok {
		return fmt.Errorf("wecom-ws: SendFile: invalid reply context type %T", rctx)
	}
	if rc.chatID == "" {
		return fmt.Errorf("wecom-ws: chatID is empty, cannot send file")
	}
	if len(file.Data) == 0 {
		return fmt.Errorf("wecom-ws: file data is empty")
	}

	mediaID, err := p.uploadWSMedia(ctx, "file", wsFileFileName(file), file.Data)
	if err != nil {
		return fmt.Errorf("wecom-ws: send file: %w", err)
	}
	if err := p.sendWSMediaMessage(ctx, rc.chatID, "file", mediaID); err != nil {
		return fmt.Errorf("wecom-ws: send file: %w", err)
	}
	return nil
}

func wsFileFileName(file core.FileAttachment) string {
	name := filepath.Base(strings.TrimSpace(file.FileName))
	if name != "" && name != "." {
		return name
	}
	switch strings.ToLower(file.MimeType) {
	case "text/html":
		return "file.html"
	case "application/pdf":
		return "file.pdf"
	case "text/plain":
		return "file.txt"
	case "text/markdown":
		return "file.md"
	case "application/json":
		return "file.json"
	case "application/zip":
		return "file.zip"
	default:
		return "file.bin"
	}
}

var _ core.ImageSender = (*WSPlatform)(nil)
var _ core.FileSender = (*WSPlatform)(nil)
