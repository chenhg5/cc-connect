package weixin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

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
	dl, err := uploadBufferToCDN(ctx, p.httpClient, p.cdnBaseURL, resp.UploadParam, filekey, plaintext, aesKey, label)
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
	return p.api.sendMessage(ctx, &msg)
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
				AESKey:            base64.StdEncoding.EncodeToString(ref.aesKey),
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
				AESKey:            base64.StdEncoding.EncodeToString(ref.aesKey),
				EncryptType:       1,
			},
			FileName: name,
			Len:      fmt.Sprintf("%d", ref.rawSize),
		},
	}
	return p.sendSingleItem(ctx, rc, item)
}
