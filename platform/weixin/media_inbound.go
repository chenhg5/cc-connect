package weixin

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"mime"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func imageDecryptMaterial(img *imageItem) (encParam, aesKeyBase64 string, ok bool) {
	if img == nil || img.Media == nil {
		return "", "", false
	}
	encParam = strings.TrimSpace(img.Media.EncryptQueryParam)
	if encParam == "" {
		return "", "", false
	}
	if hx := strings.TrimSpace(img.AESKeyHex); hx != "" {
		raw, err := hex.DecodeString(hx)
		if err == nil && len(raw) == 16 {
			return encParam, base64.StdEncoding.EncodeToString(raw), true
		}
	}
	if k := strings.TrimSpace(img.Media.AESKey); k != "" {
		return encParam, k, true
	}
	return encParam, "", false
}

func (p *Platform) collectInboundMedia(ctx context.Context, items []messageItem) (images []core.ImageAttachment, files []core.FileAttachment, audio *core.AudioAttachment) {
	if p == nil || len(items) == 0 || strings.TrimSpace(p.cdnBaseURL) == "" {
		return nil, nil, nil
	}
	dlCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	client := p.httpClient
	base := p.cdnBaseURL

	for _, it := range items {
		switch it.Type {
		case messageItemImage:
			img := it.ImageItem
			enc, keyB64, hasKey := imageDecryptMaterial(img)
			if enc == "" {
				continue
			}
			var buf []byte
			var err error
			if hasKey && keyB64 != "" {
				buf, err = downloadAndDecryptCDN(dlCtx, client, base, enc, keyB64, "weixin inbound image")
			} else {
				buf, err = downloadPlainCDN(dlCtx, client, base, enc, "weixin inbound image-plain")
			}
			if err != nil {
				slog.Warn("weixin: inbound image CDN failed", "error", err)
				continue
			}
			mt := detectImageMime(buf)
			images = append(images, core.ImageAttachment{MimeType: mt, Data: buf})

		case messageItemFile:
			f := it.FileItem
			if f == nil || f.Media == nil {
				continue
			}
			enc := strings.TrimSpace(f.Media.EncryptQueryParam)
			keyB64 := strings.TrimSpace(f.Media.AESKey)
			if enc == "" || keyB64 == "" {
				continue
			}
			buf, err := downloadAndDecryptCDN(dlCtx, client, base, enc, keyB64, "weixin inbound file")
			if err != nil {
				slog.Warn("weixin: inbound file CDN failed", "error", err)
				continue
			}
			name := strings.TrimSpace(f.FileName)
			if name == "" {
				name = "attachment.bin"
			}
			mt := mime.TypeByExtension(filepath.Ext(name))
			if mt == "" {
				mt = "application/octet-stream"
			}
			files = append(files, core.FileAttachment{MimeType: mt, Data: buf, FileName: name})

		case messageItemVideo:
			v := it.VideoItem
			if v == nil || v.Media == nil {
				continue
			}
			enc := strings.TrimSpace(v.Media.EncryptQueryParam)
			keyB64 := strings.TrimSpace(v.Media.AESKey)
			if enc == "" || keyB64 == "" {
				continue
			}
			buf, err := downloadAndDecryptCDN(dlCtx, client, base, enc, keyB64, "weixin inbound video")
			if err != nil {
				slog.Warn("weixin: inbound video CDN failed", "error", err)
				continue
			}
			files = append(files, core.FileAttachment{MimeType: "video/mp4", Data: buf, FileName: "video.mp4"})

		case messageItemVoice:
			v := it.VoiceItem
			if v == nil || v.Media == nil {
				continue
			}
			if strings.TrimSpace(v.Text) != "" {
				// WeChat ASR text is enough when present; avoid STT path / duplicate handling.
				continue
			}
			enc := strings.TrimSpace(v.Media.EncryptQueryParam)
			keyB64 := strings.TrimSpace(v.Media.AESKey)
			if enc == "" || keyB64 == "" {
				continue
			}
			buf, err := downloadAndDecryptCDN(dlCtx, client, base, enc, keyB64, "weixin inbound voice")
			if err != nil {
				slog.Warn("weixin: inbound voice CDN failed", "error", err)
				continue
			}
			a := &core.AudioAttachment{
				MimeType: "audio/silk",
				Data:     buf,
				Format:   "silk",
			}
			if audio == nil {
				audio = a
			}
		}
	}
	return images, files, audio
}
