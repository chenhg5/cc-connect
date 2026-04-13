package weixin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// formatAesKeyForAPI encodes a raw AES key as base64(hex_string),
// matching the format expected by the WeChat iLink sendMessage API.
func formatAesKeyForAPI(key []byte) string {
	return base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(key)))
}

// isWeixinCDNHost 检查 URL 是否指向已知的微信国内 CDN 域名
func isWeixinCDNHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return strings.HasSuffix(host, ".weixin.qq.com") || strings.HasSuffix(host, ".wechat.com")
}

type cdnUploadedRef struct {
	downloadParam string
	aesKey        []byte
	cipherSize    int
	rawSize       int
}

func (p *Platform) resolveReplyContext(replyCtx any) (*replyContext, error) {
	rc, ok := replyCtx.(*replyContext)
	if !ok || rc == nil {
		return nil, fmt.Errorf("weixin: invalid reply context")
	}
	if strings.TrimSpace(rc.contextToken) == "" {
		rc.contextToken = p.getContextToken(rc.peerUserID)
	}
	if strings.TrimSpace(rc.contextToken) == "" {
		return nil, fmt.Errorf("weixin: missing context_token for peer %q", rc.peerUserID)
	}
	return rc, nil
}

func (p *Platform) uploadToWeixinCDN(ctx context.Context, to string, plaintext []byte, mediaType int, label string) (*cdnUploadedRef, error) {
	if len(plaintext) == 0 {
		return nil, fmt.Errorf("weixin: %s: empty payload", label)
	}
	if strings.TrimSpace(p.cdnBaseURL) == "" {
		return nil, fmt.Errorf("weixin: cdn_base_url is empty")
	}
	rawSize := len(plaintext)
	aesKey := make([]byte, 16)
	if _, err := rand.Read(aesKey); err != nil {
		return nil, fmt.Errorf("weixin: %s: aes key: %w", label, err)
	}
	filekey := randomHex(16)
	req := getUploadURLRequest{
		Filekey:     filekey,
		MediaType:   mediaType,
		ToUserID:    to,
		Rawsize:     rawSize,
		Rawfilemd5:  md5Hex(plaintext),
		Filesize:    aesECBPaddedSize(rawSize),
		NoNeedThumb: true,
		Aeskey:      hex.EncodeToString(aesKey),
	}
	resp, err := p.api.getUploadURL(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("weixin: %s: %w", label, err)
	}
	// 选择上传 URL 和 HTTP client
	var cdnUploadURL string
	var uploadClient *http.Client
	if resp.UploadFullURL != "" {
		// 新版 API：使用服务端返回的完整 URL
		cdnUploadURL = resp.UploadFullURL
		// 如果 URL 指向已知的微信国内 CDN，使用无代理 client 直连
		if isWeixinCDNHost(cdnUploadURL) {
			uploadClient = p.cdnHttpClient
		} else {
			uploadClient = p.httpClient
		}
	} else {
		// 旧版 API：用 upload_param 构建 URL，使用配置的 httpClient
		cdnUploadURL = buildCdnUploadURL(p.cdnBaseURL, resp.UploadParam, filekey)
		uploadClient = p.httpClient
	}
	dl, err := uploadBufferToCDN(ctx, uploadClient, cdnUploadURL, plaintext, aesKey, label)
	if err != nil {
		return nil, err
	}
	return &cdnUploadedRef{
		downloadParam: dl,
		aesKey:        aesKey,
		cipherSize:    aesECBPaddedSize(rawSize),
		rawSize:       rawSize,
	}, nil
}

func (p *Platform) sendSingleItem(ctx context.Context, rc *replyContext, item messageItem) error {
	return p.sendSingleItemWithRetry(ctx, rc, item)
}

// sendSingleItemWithRetry sends a media item with retry mechanism for ret=-2 errors.
func (p *Platform) sendSingleItemWithRetry(ctx context.Context, rc *replyContext, item messageItem) error {
	var lastErr error
	for attempt := 0; attempt < weixinSendMaxRetries; attempt++ {
		msg := sendMessageReq{
			Msg: weixinOutboundMsg{
				FromUserID:   "",
				ToUserID:     rc.peerUserID,
				ClientID:     "cc-" + randomHex(8),
				MessageType:  messageTypeBot,
				MessageState: messageStateFinish,
				ItemList:     []messageItem{item},
				ContextToken: rc.contextToken,
			},
		}
		err := p.api.sendMessage(ctx, &msg)
		if err == nil {
			return nil
		}
		lastErr = err
		// Check if error is ret=-2 (API declined) - retry with fresh token
		if strings.Contains(err.Error(), "ret=-2") {
			slog.Warn("weixin: sendMessage ret=-2 for media, retrying",
				"attempt", attempt+1, "peer", rc.peerUserID)
			// Add delay before retry
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(weixinSendRetryDelay):
			}
			// Refresh context_token from stored tokens
			freshToken := p.getContextToken(rc.peerUserID)
			if freshToken != "" && freshToken != rc.contextToken {
				rc.contextToken = freshToken
				slog.Debug("weixin: using refreshed context_token for media retry", "peer", rc.peerUserID)
			}
			continue
		}
		// For other errors, don't retry
		return err
	}
	return lastErr
}

// SendImage implements core.ImageSender.
func (p *Platform) SendImage(ctx context.Context, replyCtx any, img core.ImageAttachment) error {
	rc, err := p.resolveReplyContext(replyCtx)
	if err != nil {
		return err
	}
	if len(img.Data) == 0 {
		return fmt.Errorf("weixin: empty image")
	}
	ref, err := p.uploadToWeixinCDN(ctx, rc.peerUserID, img.Data, uploadMediaImage, "SendImage")
	if err != nil {
		return err
	}
	item := messageItem{
		Type: messageItemImage,
		ImageItem: &imageItem{
			Media: &cdnMedia{
				EncryptQueryParam: ref.downloadParam,
				AESKey:            formatAesKeyForAPI(ref.aesKey),
				EncryptType:       1,
			},
			MidSize: ref.cipherSize,
		},
	}
	return p.sendSingleItem(ctx, rc, item)
}

// SendFile implements core.FileSender.
func (p *Platform) SendFile(ctx context.Context, replyCtx any, file core.FileAttachment) error {
	rc, err := p.resolveReplyContext(replyCtx)
	if err != nil {
		return err
	}
	if len(file.Data) == 0 {
		return fmt.Errorf("weixin: empty file")
	}
	name := strings.TrimSpace(file.FileName)
	if name == "" {
		name = "file.bin"
	}
	ref, err := p.uploadToWeixinCDN(ctx, rc.peerUserID, file.Data, uploadMediaFile, "SendFile")
	if err != nil {
		return err
	}
	item := messageItem{
		Type: messageItemFile,
		FileItem: &fileItem{
			Media: &cdnMedia{
				EncryptQueryParam: ref.downloadParam,
				AESKey:            formatAesKeyForAPI(ref.aesKey),
				EncryptType:       1,
			},
			FileName: name,
			Len:      fmt.Sprintf("%d", ref.rawSize),
		},
	}
	return p.sendSingleItem(ctx, rc, item)
}

// SendAudio implements core.AudioSender.
// Weixin voice messages require AMR or SILK format. Since SILK encoding is not
// widely supported, we convert to AMR format using ffmpeg.
func (p *Platform) SendAudio(ctx context.Context, replyCtx any, audio []byte, format string) error {
	rc, err := p.resolveReplyContext(replyCtx)
	if err != nil {
		return err
	}
	if len(audio) == 0 {
		return fmt.Errorf("weixin: empty audio")
	}

	// Convert to AMR format if not already AMR
	sendData := audio
	sendFormat := strings.ToLower(strings.TrimSpace(format))
	if sendFormat == "" {
		sendFormat = "wav" // TTS typically outputs WAV
	}
	if sendFormat != "amr" {
		converted, err := core.ConvertAudioToAMR(ctx, audio, sendFormat)
		if err != nil {
			return fmt.Errorf("weixin: convert %s to AMR: %w", sendFormat, err)
		}
		sendData = converted
		sendFormat = "amr"
	}

	slog.Debug("weixin: audio converted", "format", sendFormat, "size", len(sendData))

	// Upload to CDN as file type (voice uses same CDN upload mechanism)
	ref, err := p.uploadToWeixinCDN(ctx, rc.peerUserID, sendData, uploadMediaFile, "SendAudio")
	if err != nil {
		return err
	}

	// Send as voice message
	item := messageItem{
		Type: messageItemVoice,
		VoiceItem: &voiceItem{
			Media: &cdnMedia{
				EncryptQueryParam: ref.downloadParam,
				AESKey:            formatAesKeyForAPI(ref.aesKey),
				EncryptType:       1,
			},
			EncodeType: 0, // 0 = AMR format, 1 = SILK format
		},
	}
	return p.sendSingleItem(ctx, rc, item)
}
