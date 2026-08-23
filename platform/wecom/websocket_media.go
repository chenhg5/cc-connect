package wecom

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	// Max download size for WeCom WS image/file payloads (matches OpenClaw default).
	wecomWSMediaMaxBytes = 20 << 20

	wecomWSMediaDownloadFailureMessage = "Attachment download failed. The message was not submitted; please resend it."
)

// wsMediaRef is an encrypted download URL plus optional AES key (base64) from the WS protocol.
type wsMediaRef struct {
	URL    string
	Aeskey string
}

type wsTextContent struct {
	Content string `json:"content"`
}

// wsVoiceContent follows the official AI Bot payload. WebSocket voice callbacks
// contain only transcription text; they do not contain downloadable audio bytes.
type wsVoiceContent struct {
	Content string `json:"content"`
}

type wsMediaContent struct {
	URL    string `json:"url"`
	Aeskey string `json:"aeskey"`
}

type wsMixedItem struct {
	MsgType string          `json:"msgtype"`
	Text    *wsTextContent  `json:"text,omitempty"`
	Image   *wsMediaContent `json:"image,omitempty"`
	File    *wsMediaContent `json:"file,omitempty"`
}

type wsMixedBlock struct {
	MsgItem []wsMixedItem `json:"msg_item"`
}

type wsQuoteBlock struct {
	MsgType string          `json:"msgtype"`
	Text    *wsTextContent  `json:"text,omitempty"`
	Voice   *wsVoiceContent `json:"voice,omitempty"`
	Image   *wsMediaContent `json:"image,omitempty"`
	File    *wsMediaContent `json:"file,omitempty"`
	Mixed   *wsMixedBlock   `json:"mixed,omitempty"`
}

// wsInboundParts keeps the current message separate from its quoted context.
// content contains readable text and, for quotes, attachment markers in display order.
type wsInboundParts struct {
	content []string
	images  []wsMediaRef
	files   []wsMediaRef
}

func (p wsInboundParts) hasMedia() bool {
	return len(p.images) > 0 || len(p.files) > 0
}

func formatWSQuotedContent(parts wsInboundParts) string {
	if len(parts.content) == 0 {
		return ""
	}
	return "[Quoted message]:\n" + strings.Join(parts.content, "\n") + "\n\n"
}

// wsCollectInboundParts separates main-message and quote content so the engine can
// deliver the latter as explicit context instead of treating it as a new instruction.
// It does not include the top-level voice transcription, which is read from voice.content.
func wsCollectInboundParts(body *wsMsgCallbackBody) (current, quoted wsInboundParts) {
	appendText := func(parts *wsInboundParts, s string) {
		s = strings.TrimSpace(s)
		if s != "" {
			parts.content = append(parts.content, s)
		}
	}
	appendImage := func(parts *wsInboundParts, url, aeskey string, marker bool) {
		parts.images = append(parts.images, wsMediaRef{URL: url, Aeskey: aeskey})
		if marker {
			parts.content = append(parts.content, "[image]")
		}
	}
	appendFile := func(parts *wsInboundParts, url, aeskey string, marker bool) {
		parts.files = append(parts.files, wsMediaRef{URL: url, Aeskey: aeskey})
		if marker {
			parts.content = append(parts.content, "[file]")
		}
	}
	walkMixed := func(parts *wsInboundParts, m *wsMixedBlock, markers bool) {
		if m == nil {
			return
		}
		for _, item := range m.MsgItem {
			switch item.MsgType {
			case "text":
				if item.Text != nil {
					appendText(parts, item.Text.Content)
				}
			case "image":
				if item.Image != nil {
					appendImage(parts, item.Image.URL, item.Image.Aeskey, markers)
				} else {
					appendImage(parts, "", "", markers)
				}
			case "file":
				if item.File != nil {
					appendFile(parts, item.File.URL, item.File.Aeskey, markers)
				} else {
					appendFile(parts, "", "", markers)
				}
			}
		}
	}
	walkQuote := func(q *wsQuoteBlock) {
		if q == nil {
			return
		}
		switch q.MsgType {
		case "text":
			if q.Text != nil {
				appendText(&quoted, q.Text.Content)
			}
		case "voice":
			if q.Voice != nil {
				appendText(&quoted, q.Voice.Content)
			}
			if len(quoted.content) == 0 {
				quoted.content = append(quoted.content, "[voice]")
			}
		case "image":
			if q.Image != nil {
				appendImage(&quoted, q.Image.URL, q.Image.Aeskey, true)
			} else {
				appendImage(&quoted, "", "", true)
			}
		case "file":
			if q.File != nil {
				appendFile(&quoted, q.File.URL, q.File.Aeskey, true)
			} else {
				appendFile(&quoted, "", "", true)
			}
		case "mixed":
			walkMixed(&quoted, q.Mixed, true)
		}
	}

	if body.Mixed != nil && len(body.Mixed.MsgItem) > 0 {
		walkMixed(&current, body.Mixed, false)
	} else {
		if body.MsgType != "voice" {
			appendText(&current, body.Text.Content)
		}
		if body.Image != nil {
			appendImage(&current, body.Image.URL, body.Image.Aeskey, false)
		} else if body.MsgType == "image" {
			appendImage(&current, "", "", false)
		}
		if body.MsgType == "file" {
			if body.File != nil {
				appendFile(&current, body.File.URL, body.File.Aeskey, false)
			} else {
				appendFile(&current, "", "", false)
			}
		}
	}
	// WeCom may send msgtype=file (or image) together with a non-empty mixed block; the real
	// download url is then only on the top-level file/image object. Merge those here.
	if body.Mixed != nil && len(body.Mixed.MsgItem) > 0 {
		if body.MsgType == "file" {
			if body.File != nil {
				appendFile(&current, body.File.URL, body.File.Aeskey, false)
			} else {
				appendFile(&current, "", "", false)
			}
		}
		if body.MsgType == "image" {
			if body.Image != nil {
				appendImage(&current, body.Image.URL, body.Image.Aeskey, false)
			} else {
				appendImage(&current, "", "", false)
			}
		}
	}
	walkQuote(body.Quote)
	return current, quoted
}

// decodeWeComAESKey normalizes and decodes the aeskey from WeCom WS callbacks.
// The server may send standard Base64, URL-safe Base64 (- _), omit padding, insert
// whitespace, or (rarely) a 64-char hex string. Node's Buffer.from(s, 'base64') is more
// permissive than Go's StdEncoding; we mirror common cases so decryption matches the SDK.
func decodeWeComAESKey(aesKey string) ([]byte, error) {
	s := strings.TrimSpace(aesKey)
	if s == "" {
		return nil, fmt.Errorf("wecom-ws: empty aeskey")
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n', '\r', ' ', '\t':
			continue
		default:
			b.WriteByte(s[i])
		}
	}
	s = b.String()

	if len(s) == 64 && isHexString(s) {
		key, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("wecom-ws: decode aeskey hex: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("wecom-ws: aeskey hex length %d, want 32 bytes", len(key))
		}
		return key, nil
	}

	// URL-safe alphabet → standard (RFC 4648 §5)
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")

	switch len(s) % 4 {
	case 0:
	case 2:
		s += "=="
	case 3:
		s += "="
	default:
		return nil, fmt.Errorf("wecom-ws: invalid aeskey base64 length")
	}

	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("wecom-ws: decode aeskey: %w", err)
	}
	if len(key) < 32 {
		return nil, fmt.Errorf("wecom-ws: aeskey decoded length %d, need >= 32", len(key))
	}
	return key, nil
}

func isHexString(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// wecomDecryptFile decrypts payload from WeCom WS media URLs (AES-256-CBC, IV = first 16 key bytes).
// Same algorithm as @wecom/aibot-node-sdk decryptFile.
func wecomDecryptFile(ciphertext []byte, aesKeyB64 string) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, fmt.Errorf("wecom-ws: empty ciphertext")
	}
	key, err := decodeWeComAESKey(aesKeyB64)
	if err != nil {
		return nil, err
	}
	key32 := key[:32]
	iv := key32[:16]

	block, err := aes.NewCipher(key32)
	if err != nil {
		return nil, err
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("wecom-ws: ciphertext not multiple of block size")
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)
	return pkcs7UnpadWeCom(plain)
}

func pkcs7UnpadWeCom(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("wecom-ws: empty padded data")
	}
	padLen := int(data[len(data)-1])
	if padLen < 1 || padLen > 32 || padLen > len(data) {
		return nil, fmt.Errorf("wecom-ws: invalid pkcs7 pad length %d", padLen)
	}
	for i := len(data) - padLen; i < len(data); i++ {
		if int(data[i]) != padLen {
			return nil, fmt.Errorf("wecom-ws: invalid pkcs7 padding")
		}
	}
	return data[:len(data)-padLen], nil
}

func parseContentDispositionFilename(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	lower := strings.ToLower(h)
	// RFC 5987: filename*=UTF-8''percent-encoded
	if idx := strings.Index(lower, "filename*="); idx >= 0 {
		val := strings.TrimSpace(h[idx+len("filename*="):])
		val = strings.TrimSuffix(strings.TrimSpace(val), ";")
		if after, ok := strings.CutPrefix(val, "UTF-8''"); ok {
			if dec, err := url.QueryUnescape(after); err == nil {
				return filepath.Base(dec)
			}
			return filepath.Base(after)
		}
	}
	if idx := strings.Index(lower, "filename="); idx >= 0 {
		val := strings.TrimSpace(h[idx+len("filename="):])
		val = strings.TrimSuffix(val, ";")
		val = strings.Trim(val, `"`)
		if dec, err := url.QueryUnescape(val); err == nil {
			return filepath.Base(dec)
		}
		return filepath.Base(val)
	}
	return ""
}

func downloadWeComWSMedia(ctx context.Context, urlStr, aesKey string) (data []byte, fileName string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, "", err
	}
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("wecom-ws: download HTTP %s", resp.Status)
	}
	fileName = parseContentDispositionFilename(resp.Header.Get("Content-Disposition"))
	lim := io.LimitReader(resp.Body, wecomWSMediaMaxBytes+1)
	raw, err := io.ReadAll(lim)
	if err != nil {
		return nil, "", err
	}
	if len(raw) > wecomWSMediaMaxBytes {
		return nil, "", fmt.Errorf("wecom-ws: media larger than %d bytes", wecomWSMediaMaxBytes)
	}
	if aesKey != "" {
		raw, err = wecomDecryptFile(raw, aesKey)
		if err != nil {
			return nil, "", err
		}
	}
	return raw, fileName, nil
}

func safeWSMediaDownloadError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("wecom-ws: media request %s failed: %w", urlErr.Op, urlErr.Err)
	}
	return err
}

// deliverWSMediaInbound downloads media and forwards one core.Message. Quoted media
// is downloaded first so attachment order mirrors the quoted-context prompt.
func (p *WSPlatform) deliverWSMediaInbound(body *wsMsgCallbackBody, sessionKey, chatName string, rctx wsReplyContext, current, quoted wsInboundParts, fromVoice bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var images []core.ImageAttachment
	var fileAtts []core.FileAttachment

	downloadImages := func(refs []wsMediaRef) error {
		for _, im := range refs {
			buf, fn, err := downloadWeComWSMedia(ctx, im.URL, im.Aeskey)
			if err != nil {
				return fmt.Errorf("download image: %w", err)
			}
			base := filepath.Base(strings.TrimSpace(fn))
			if base == "" || base == "." {
				base = "image.bin"
			}
			mt := wecomInboundFileMime(base, buf)
			if !strings.HasPrefix(mt, "image/") {
				mt = http.DetectContentType(buf)
				if !strings.HasPrefix(mt, "image/") {
					mt = "image/jpeg"
				}
			}
			images = append(images, core.ImageAttachment{MimeType: mt, Data: buf, FileName: base})
			slog.Info("wecom-ws: image downloaded", "bytes", len(buf), "mime", mt, "name", base)
		}
		return nil
	}

	downloadFiles := func(refs []wsMediaRef) error {
		for _, f := range refs {
			buf, fn, err := downloadWeComWSMedia(ctx, f.URL, f.Aeskey)
			if err != nil {
				return fmt.Errorf("download file: %w", err)
			}
			base := filepath.Base(strings.TrimSpace(fn))
			if base == "" || base == "." {
				base = "attachment"
			}
			mt := wecomInboundFileMime(base, buf)
			fileAtts = append(fileAtts, core.FileAttachment{MimeType: mt, Data: buf, FileName: base})
			slog.Info("wecom-ws: file downloaded", "bytes", len(buf), "mime", mt, "name", base)
		}
		return nil
	}

	for _, download := range []func() error{
		func() error { return downloadImages(quoted.images) },
		func() error { return downloadImages(current.images) },
		func() error { return downloadFiles(quoted.files) },
		func() error { return downloadFiles(current.files) },
	} {
		if err := download(); err != nil {
			slog.Error("wecom-ws: media download failed; message not delivered", "error", safeWSMediaDownloadError(err))
			if replyErr := p.Reply(context.Background(), rctx, wecomWSMediaDownloadFailureMessage); replyErr != nil {
				slog.Error("wecom-ws: media download failure reply failed", "error", replyErr)
			}
			return
		}
	}

	content := strings.Join(current.content, "\n")
	content = stripWeComAtMentions(content, p.botID, body.AibotID)
	quotedContent := formatWSQuotedContent(quoted)

	if content == "" && quotedContent == "" && len(images) == 0 && len(fileAtts) == 0 {
		slog.Warn("wecom-ws: media inbound empty after downloads", "msg_id", body.MsgID)
		return
	}

	p.handler(p, &core.Message{
		SessionKey: sessionKey, Platform: "wecom",
		Scope:     conversationScopeForWeCom(body.ChatType),
		MessageID: body.MsgID,
		UserID:    body.From.UserID, UserName: body.From.UserID,
		ChatName:     chatName,
		Content:      content,
		ExtraContent: quotedContent,
		Images:       images,
		Files:        fileAtts,
		ReplyCtx:     rctx, FromVoice: fromVoice,
	})
}

func conversationScopeForWeCom(chatType string) core.ConversationScope {
	if strings.EqualFold(strings.TrimSpace(chatType), "group") {
		return core.ConversationScopeGroup
	}
	return core.ConversationScopeDirect
}
